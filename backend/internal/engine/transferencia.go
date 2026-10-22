package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"time"

	"bot-infominas/internal/alerta"
	"bot-infominas/internal/brain"
	"bot-infominas/internal/config"
	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
)

// Origens de uma transferência para atendimento humano (campo `origem` da
// tabela `falhas` e do log `sofia_falha`).
const (
	OrigemCerebro   = "cerebro"   // o cérebro Python falhou (classe transitória/definitiva)
	OrigemDisjuntor = "disjuntor" // disjuntor aberto: o cérebro nem foi chamado
	OrigemCliente   = "cliente"   // o cliente pediu um atendente
	OrigemAnexo     = "anexo"     // o cliente mandou áudio/imagem/arquivo, que o bot não lê

	// Origens vindas das `acoes` do ChatResponse (docs/plano/01, "Contrato A").
	OrigemFrustracao = "frustracao" // frustração alta detectada pelo cérebro
	OrigemConfianca  = "confianca"  // classificação com confiança baixa demais
	OrigemAcao       = "acao"       // ação `transferir` com origem que o gateway não conhece

	// Origens de quem chama a engine de fora dela (reconciliador, toolsapi).
	OrigemVigia          = "vigia"           // vigia BOT_TIMEOUT_HANDOFF: a Sofia não respondeu o cliente
	OrigemTicketPendente = "ticket_pendente" // POST do chamado sem resposta / Infodesk recusou: o chamado pode existir
	OrigemTicketAberto   = "ticket_aberto"   // handoff depois do chamado aberto (TRANSFERIR_APOS_TICKET)
	OrigemForaDoPiloto   = "fora_do_piloto"  // lista de testadores ligada e o número não está nela (E3.9)
)

// Transferencia descreve por que a conversa vai para um atendente.
type Transferencia struct {
	Origem   string // OrigemCerebro, OrigemCliente, ... (obrigatório)
	Classe   string // brain.ClasseTransitoria | brain.ClasseDefinitiva | "" (não se aplica)
	Causa    string // linguagem simples: alerta, log, tabela
	Detalhe  string // técnico, sem dado pessoal: só log/tabela
	ID       string // ID de ocorrência já existente (ex.: vindo do cérebro); vazio = gerar
	Mensagem string // mensagem ao cliente; vazia = MsgTransferencia
	// SemAviso: o cliente JÁ foi avisado (a resposta do cérebro anunciou a
	// transferência) — não manda a mensagem de aviso de novo.
	SemAviso bool
	// Espera: há quanto tempo o cliente esperava (origem `vigia`; entra na nota
	// como "sem resposta em N min"). Zero = a nota não cita o tempo.
	Espera time.Duration

	// foraDoHorario é preenchido por TransferirParaHumano (nunca por quem chama):
	// a transferência aconteceu fora do horário humano (HORARIO_HUMANO). Vai na
	// nota interna e no log `transferencia`; com FORA_DO_HORARIO_MODO=avisar troca
	// a mensagem ao cliente.
	foraDoHorario bool
}

// transferenciaPorErroDoCerebro monta a transferência a partir do erro da
// chamada ao cérebro: reaproveita classe, causa e ID quando o cérebro os
// mandou (Contrato A), para o mesmo ID aparecer nos dois lados.
func transferenciaPorErroDoCerebro(err error) Transferencia {
	t := Transferencia{Origem: OrigemCerebro, Detalhe: err.Error()}
	if be, ok := brain.ComoErro(err); ok {
		t.Classe, t.Causa, t.ID = be.Classe, be.Causa, be.IDOcorrencia
	}
	if t.Causa == "" {
		t.Causa = "falha ao processar a mensagem no cérebro"
	}
	return t
}

// prefixoOcorrencia abre todo ID de ocorrência gerado no gateway — o MESMO
// prefixo que o cérebro Python usa (src/agent/falhas.py).
const prefixoOcorrencia = "SOF-"

// NovoIDOcorrencia gera `SOF-AAAAMMDD-HHMM-NN` (horário de Brasília), legível
// no alerta e localizável no log dos dois lados.
func NovoIDOcorrencia(agora time.Time) string {
	if loc, err := time.LoadLocation("America/Sao_Paulo"); err == nil {
		agora = agora.In(loc)
	}
	return fmt.Sprintf("%s%s-%s-%02d", prefixoOcorrencia, agora.Format("20060102"), agora.Format("1504"), rand.IntN(100)) //nolint:gosec // G404: sufixo legível de ID de ocorrência, não é material criptográfico
}

// rePedeAtendente casa a palavra inteira ("desumano" não conta). Lista de
// docs/plano/01-ferramentas.md (origem `cliente`): atendente | humano |
// pessoa de verdade.
var rePedeAtendente = regexp.MustCompile(`(?i)(^|[^\p{L}])(atendente|humano|humana|pessoa de verdade)([^\p{L}]|$)`)

// reVerboDePedido: num texto longo, só transfere quando há um pedido de fato
// ("quero falar com", "me passa para", "tem algum...") — "o atendente me disse
// ontem que..." em meio a um relato não é pedido.
var reVerboDePedido = regexp.MustCompile(`(?i)\b(falar|conversar|quero|queria|preciso|chamar|chama|passar|passa|transferir|transfere|encaminhar|encaminha|coloca|colocar|tem|existe|cad[eê]|me d[aá])\b`)

// PedeAtendente detecta o pedido explícito do cliente por um atendente humano
// (palavra de escape, decidida por CÓDIGO no gateway — nunca pelo LLM).
// Mensagem curta (≤ 60 caracteres) com a palavra basta; mensagem longa exige
// também um verbo de pedido perto.
//
// Hipótese de trabalho herdada do gate: negação ("não quero atendente") e
// pergunta ("você é atendente virtual?") curtas TAMBÉM transferem — o custo
// de um falso positivo (humano atende) é menor que o de prender o cliente
// no bot. TODO(E2.4): medir nos evals de conversa e refinar se preciso.
func PedeAtendente(mensagem string) bool {
	m := strings.TrimSpace(mensagem)
	if m == "" || !rePedeAtendente.MatchString(m) {
		return false
	}
	if len([]rune(m)) <= 60 {
		return true
	}
	return reVerboDePedido.MatchString(m)
}

// transferirPorMidia avisa o cliente que a Sofia não lê anexos e passa a
// conversa a um atendente pelo caminho único.
func (e *Engine) transferirPorMidia(ctx context.Context, convID string) {
	e.log.Info("mídia recebida: avisando o cliente e transferindo", "conversation_id", convID)
	e.TransferirParaHumano(ctx, convID, Transferencia{
		Origem:   OrigemAnexo,
		Causa:    "cliente enviou áudio, imagem ou arquivo — a " + nomeAssistente + " não lê anexos",
		Mensagem: MsgMidia,
	})
}

// TransferirParaHumano é o ÚNICO caminho para o atendimento humano (ADR 0007)
// — usado pelas falhas do cérebro, pelo disjuntor, pela mídia, pelo pedido
// explícito do cliente e pelas `acoes` do cérebro. Faz, nesta ordem e sem
// interromper nas falhas parciais (cada passo loga o próprio erro):
//
//  1. conversa que já não é do bot (transferida, encerrada, humano conduz) →
//     não faz nada (idempotente: duas mídias = um handoff);
//  2. ID de ocorrência (gera se não veio), log estruturado `sofia_falha` e
//     registro na tabela `falhas` — o mesmo ID em todos;
//  3. alertas ao responsável (vigia, ZC-7);
//  4. mensagem de aviso ao cliente (nunca erro técnico) ANTES do handoff — o
//     handoff não fala com o cliente (07 C10);
//  5. `conversas.estado = transferida`: o bot cala, mesmo que o handoff falhe
//     (a marca de handoff não confirmado faz o vigia do reconciliador repetir);
//  6. `Handoff(DEPARTAMENTO_HUMANO_PADRAO, nota por código)`.
//
// Devolve o ID de ocorrência ("" quando a conversa já não era do bot).
//
// TODO(P11): escolher o departamento pela taxonomia (categoria → departamento)
// quando a E2.2/E3.1 entregarem a tabela; hoje é sempre o padrão. Sem padrão
// configurado o handoff vai SEM departamento (distribuição automática do
// Infodesk) — melhor que deixar o bot preso à conversa.
func (e *Engine) TransferirParaHumano(ctx context.Context, convID string, t Transferencia) string {
	if !e.botConduz(ctx, convID) {
		e.log.Info("transferência ignorada: a conversa já não é do bot",
			"conversation_id", convID, "origem", t.Origem)
		return ""
	}
	// Lista de testadores (E3.9): quem está fora dela nunca recebe mensagem do
	// bot, seja qual for o caminho que o transfere (o vigia N1, por exemplo, pode
	// chegar aqui para uma conversa que o filtro do lote não viu).
	if t.Origem != OrigemForaDoPiloto && e.cfg.TestadoresAtivo() {
		if c, err := e.st.ObterConversa(ctx, convID); err == nil && c != nil && foraDoPiloto(e.cfg, c.Telefone) {
			e.log.Info("fora_do_piloto: transferência de origem "+t.Origem+" vira silenciosa para número fora da lista",
				"conversation_id", convID, "telefone", mascararTelefone(c.Telefone))
			t = Transferencia{Origem: OrigemForaDoPiloto, Causa: "número fora da lista de testadores (TESTADORES_TELEFONES)", SemAviso: true}
		}
	}
	agora := e.agora()
	// Fora do horário humano (E5.5): a transferência acontece do mesmo jeito (a
	// conversa precisa cair na fila para a equipe ver no próximo expediente); muda
	// a mensagem ao cliente, a nota e o log.
	t.foraDoHorario = !e.cfg.Horario.Dentro(agora)
	id := strings.TrimSpace(t.ID)
	if id == "" {
		id = NovoIDOcorrencia(agora)
	}
	if t.Causa == "" {
		t.Causa = "transferido pela " + nomeAssistente
	}

	// Fora do piloto não é falha: não gera ocorrência, ERROR nem alerta (seriam um por
	// cliente de fora do grupo); o evento `transferencia` no fim do caminho registra.
	if t.Origem != OrigemForaDoPiloto {
		e.log.Error("sofia_falha",
			"id_ocorrencia", id, "conversation_id", convID,
			"origem", t.Origem, "classe", t.Classe, "causa", t.Causa, "detalhe", t.Detalhe)
		if err := e.st.RegistrarFalha(ctx, &store.Falha{
			ID: id, ConversationID: convID, Origem: t.Origem, Classe: t.Classe,
			Causa: t.Causa, Detalhe: t.Detalhe, CreatedAt: agora,
		}); err != nil {
			e.log.Error("registrar falha", "id_ocorrencia", id, "conversation_id", convID, "err", err)
		}
		// Alertas (ZC-7): turno do cérebro que terminou em falha conta na taxa;
		// falha definitiva alerta na hora.
		if t.Origem == OrigemCerebro || t.Origem == OrigemDisjuntor {
			e.vigia.Turno(false)
		}
		e.vigia.FalhaRegistrada(alerta.Falha{
			ID: id, ConversationID: convID, Origem: t.Origem, Classe: t.Classe, Causa: t.Causa,
		})
	}

	if !t.SemAviso {
		msg := t.Mensagem
		if msg == "" {
			msg = MsgTransferencia
		}
		if t.foraDoHorario && e.cfg.ForaDoHorarioModo != config.ForaDoHorarioIgnorar {
			msg = e.msgForaDoHorario(ctx, convID, t)
		}
		if !e.send(ctx, convID, msg) {
			// O handoff segue (o atendente recebe a nota), mas o cliente ficou sem o aviso
			// (ex.: o número do chamado, na origem ticket_aberto): vira WARN para a operação.
			e.log.Warn("aviso ao cliente antes do handoff não foi entregue",
				"id_ocorrencia", id, "conversation_id", convID, "origem", t.Origem)
		}
	}

	// Entre o aviso (pausas de bolha, rede) e o handoff um atendente pode ter
	// assumido ou a conversa ter sido encerrada: não devolve à fila o que
	// outro já conduz. O estado vai ANTES da chamada, para as mensagens que
	// chegarem durante o handoff já encontrarem a conversa muda.
	if !e.botConduz(ctx, convID) {
		e.log.Info("handoff cancelado: a conversa deixou de ser do bot durante a transferência",
			"id_ocorrencia", id, "conversation_id", convID)
		return id
	}
	nota := e.montarNota(ctx, convID, t, id)
	if !e.marcarEstado(ctx, convID, store.EstadoTransferida) {
		return id
	}

	dept := strings.TrimSpace(e.cfg.DepartamentoHumanoPadrao)
	if dept == "" {
		e.log.Warn("transferir: DEPARTAMENTO_HUMANO_PADRAO vazio — handoff sem departamento (distribuição automática)",
			"id_ocorrencia", id, "conversation_id", convID)
	}
	if err := e.handoffComRetry(ctx, convID, dept, nota); err != nil {
		e.log.Error("transferir: handoff falhou; conversa fica muda localmente e o vigia do reconciliador repete",
			"id_ocorrencia", id, "conversation_id", convID, "departamento", dept, "err", erroParaLog(err))
		texto := "Ocorrência " + id + " (origem " + t.Origem + "): o Infodesk não aceitou o handoff. A Sofia parou de responder nessa conversa; o cliente pode estar sem ninguém. O reconciliador vai repetir o handoff. Veja o log do gateway (a ocorrência está nos logs sofia_falha ou transferencia)."
		if !e.marcarHandoffPendente(ctx, convID, t.Origem, id, dept, nota) {
			texto = "Ocorrência " + id + " (origem " + t.Origem + "): o Infodesk não aceitou o handoff e a marca para repeti-lo não foi gravada. A Sofia parou de responder nessa conversa e NINGUÉM vai repetir o handoff sozinho: transfira à mão. Veja o log do gateway."
		}
		e.vigia.Aviso(alerta.TipoHandoffFalhou, "Handoff para o atendente falhou", texto, convID)
	}
	alerta.LogTransferencia(e.log, alerta.Transferencia{
		IDOcorrencia: id, ConversationID: convID, Origem: t.Origem, Departamento: dept, ForaDoHorario: t.foraDoHorario,
	})
	return id
}

// marcarHandoffPendente grava a marca de "handoff não confirmado" (a conversa
// já está `transferida` e muda, mas nenhum atendente foi avisado) para o vigia
// do reconciliador repetir. Devolve false se não conseguiu gravar. Não usa o
// cancelamento do ctx do turno: é justamente nele que o handoff pode ter falhado.
func (e *Engine) marcarHandoffPendente(ctx context.Context, convID, origem, idOcorrencia, dept, nota string) bool {
	agora := e.agora()
	err := e.st.MarcarHandoffPendente(context.WithoutCancel(ctx), &store.HandoffPendente{
		ConversationID: convID, Origem: origem, IDOcorrencia: idOcorrencia,
		DepartamentoID: dept, Nota: nota, CreatedAt: agora, ProximaEm: agora,
	})
	if err != nil {
		e.log.Error("gravar a marca de handoff não confirmado", "conversation_id", convID, "id_ocorrencia", idOcorrencia, "err", err)
		return false
	}
	return true
}

// ReenviarHandoff repete UMA vez o handoff de uma marca (o backoff e o alerta
// são do vigia do reconciliador). Sucesso apaga a marca. O handoff é
// idempotente — ao contrário de POST /tickets. Não reconfere o estado local: há uma janela
// minúscula entre a conferência do vigia e esta chamada em que um humano pode
// assumir e devolver a conversa à fila; o custo é um handoff a mais, sem efeito.
func (e *Engine) ReenviarHandoff(ctx context.Context, h store.HandoffPendente) error {
	if err := e.tr.Handoff(ctx, h.ConversationID, h.DepartamentoID, h.Nota); err != nil {
		return err
	}
	if _, err := e.st.LimparHandoffPendente(context.WithoutCancel(ctx), h.ConversationID); err != nil {
		e.log.Error("handoff repetido confirmado, mas não consegui apagar a marca (o vigia repete uma vez a mais, sem efeito)",
			"conversation_id", h.ConversationID, "id_ocorrencia", h.IDOcorrencia, "err", erroParaLog(err))
	}
	e.log.Info("handoff repetido e confirmado", "conversation_id", h.ConversationID, "id_ocorrencia", h.IDOcorrencia, "tentativas_antes", h.Tentativas)
	return nil
}

// handoffComRetry repete o handoff só em falha transitória (5xx, rede, 429):
// ele é idempotente — ao contrário de POST /tickets. Se falhar de vez, a
// engine grava a marca de handoff não confirmado e o vigia do reconciliador
// o repete com backoff (E3.6).
func (e *Engine) handoffComRetry(ctx context.Context, convID, dept, nota string) error {
	var err error
	for tentativa := 0; ; tentativa++ {
		if err = e.tr.Handoff(ctx, convID, dept, nota); err == nil {
			return nil
		}
		if !infodesk.Transitorio(err) || tentativa >= len(e.esperasEnvio) {
			return err
		}
		e.log.Warn("handoff transitório falhou; nova tentativa",
			"conversation_id", convID, "tentativa", tentativa+1, "err", erroParaLog(err))
		if !e.dormir(ctx, e.esperasEnvio[tentativa]) {
			return err
		}
	}
}

// motivosDaNota traduz a origem da transferência para o texto fixo da nota
// interna (nunca texto livre do LLM nem do cliente).
var motivosDaNota = map[string]string{
	OrigemCliente:    "pedido do cliente",
	OrigemAnexo:      "cliente enviou áudio, imagem ou arquivo",
	OrigemCerebro:    "falha ao processar a mensagem",
	OrigemDisjuntor:  "cérebro em pausa por falhas repetidas",
	OrigemFrustracao: "frustração do cliente",
	OrigemConfianca:  "classificação com baixa confiança",
	OrigemAcao:       "ação do cérebro",

	// Origens de fora da engine. vigia e ticket_aberto têm texto montado em
	// motivoDaNota (levam número); este é o fixo das demais.
	OrigemTicketPendente: "chamado possivelmente criado, confira no Desk antes de abrir outro",
	OrigemForaDoPiloto:   "fora do grupo de teste da Sofia",
}

// motivoDaNota é o texto do motivo da nota interna: fixo por origem, com só
// dois números que o CÓDIGO conhece (nº do chamado e minutos de espera).
func motivoDaNota(t Transferencia, ticket int) string {
	switch t.Origem {
	case OrigemVigia:
		if t.Espera >= time.Minute {
			return fmt.Sprintf("sem resposta da %s em %d min", nomeAssistente, int(t.Espera.Minutes()))
		}
		return "sem resposta da " + nomeAssistente + " no prazo"
	case OrigemTicketAberto:
		if ticket > 0 {
			return fmt.Sprintf("chamado #%d aberto", ticket)
		}
		return "chamado aberto"
	}
	if m := motivosDaNota[t.Origem]; m != "" {
		return m
	}
	return "transferido pela " + nomeAssistente
}

var (
	reCategoria = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)
	reOrigemCls = regexp.MustCompile(`^(jev|llm|esclarecimento)$`)
)

const notaMaxRunas = 500

// montarNota escreve a nota interna do handoff POR CÓDIGO (ADR 0007): motivo
// (texto fixo por origem), categoria + confiança validadas, identificação sem
// PII em claro (nome do cadastro abreviado; CPF/contrato só mascarado) e o
// nº do chamado. Nada do que o cliente ou o LLM escreveu entra aqui.
func (e *Engine) montarNota(ctx context.Context, convID string, t Transferencia, idOcorrencia string) string {
	conv, err := e.st.ObterConversa(ctx, convID)
	if err != nil {
		e.log.Error("nota do handoff: ler conversa", "conversation_id", convID, "err", err)
	}
	ticket := 0
	if conv != nil {
		ticket = conv.TicketNumber
	}
	partes := []string{nomeAssistente + " → humano", "motivo: " + motivoDaNota(t, ticket)}
	if t.foraDoHorario {
		partes = append(partes, notaForaDoHorario)
	}
	if idOcorrencia != "" && (t.Origem == OrigemCerebro || t.Origem == OrigemDisjuntor) {
		partes = append(partes, "ocorrência: "+idOcorrencia)
	}

	if conv != nil {
		if cat := categoriaDaClassificacao(conv.ClassificacaoJSON); cat != "" {
			partes = append(partes, "categoria: "+cat)
		}
		if conv.Telefone != "" {
			if c, _ := e.st.ObterContato(ctx, conv.Telefone, e.agora()); c != nil {
				switch c.Forma {
				case store.FormaTelefone:
					if c.ValorMascarado != "" {
						partes = append(partes, "solicitante: "+c.ValorMascarado)
					}
				case store.FormaCPF, store.FormaContrato:
					if c.ValorMascarado != "" {
						partes = append(partes, c.Forma+": "+c.ValorMascarado)
					}
				}
			}
		}
		switch {
		case t.Origem == OrigemTicketAberto && conv.TicketNumber > 0:
			// o motivo já diz "chamado #N aberto"
		case conv.TicketNumber > 0:
			partes = append(partes, fmt.Sprintf("chamado #%d", conv.TicketNumber))
		case t.Origem == OrigemForaDoPiloto:
			// o motivo basta: a conversa nem começou
		case t.Origem == OrigemTicketPendente:
			// o motivo já avisa que o chamado pode existir: "sem chamado" seria falso
		default:
			partes = append(partes, "sem chamado")
		}
	}
	nota := strings.Join(partes, " · ")
	if r := []rune(nota); len(r) > notaMaxRunas {
		nota = string(r[:notaMaxRunas])
	}
	return nota
}

// categoriaDaClassificacao lê categoria e confiança da última classificação
// gravada, só se estiverem no formato esperado ("sem_conexao (jev 0.88)").
func categoriaDaClassificacao(j string) string {
	if j == "" {
		return ""
	}
	var c struct {
		Categoria string  `json:"categoria"`
		Confianca float64 `json:"confianca"`
		Origem    string  `json:"origem"`
	}
	if json.Unmarshal([]byte(j), &c) != nil || !reCategoria.MatchString(c.Categoria) {
		return ""
	}
	if reOrigemCls.MatchString(c.Origem) && c.Confianca > 0 && c.Confianca <= 1 {
		return fmt.Sprintf("%s (%s %.2f)", c.Categoria, c.Origem, c.Confianca)
	}
	return c.Categoria
}
