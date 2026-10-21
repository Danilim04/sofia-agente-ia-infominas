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
//     (o vigia N1 da E3.3 repete);
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
	agora := e.agora()
	id := strings.TrimSpace(t.ID)
	if id == "" {
		id = NovoIDOcorrencia(agora)
	}
	if t.Causa == "" {
		t.Causa = "transferido pela " + nomeAssistente
	}

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

	if !t.SemAviso {
		msg := t.Mensagem
		if msg == "" {
			msg = MsgTransferencia
		}
		e.send(ctx, convID, msg)
	}

	// Estado ANTES da chamada: mensagens que chegarem durante o handoff já
	// encontram a conversa muda.
	nota := e.montarNota(ctx, convID, t, id)
	e.marcarEstado(ctx, convID, store.EstadoTransferida)

	dept := strings.TrimSpace(e.cfg.DepartamentoHumanoPadrao)
	if dept == "" {
		e.log.Warn("transferir: DEPARTAMENTO_HUMANO_PADRAO vazio — handoff sem departamento (distribuição automática)",
			"id_ocorrencia", id, "conversation_id", convID)
	}
	if err := e.tr.Handoff(ctx, convID, dept, nota); err != nil {
		e.log.Error("transferir: handoff falhou; conversa fica muda localmente",
			"id_ocorrencia", id, "conversation_id", convID, "departamento", dept, "err", err)
		e.vigia.Aviso(alerta.TipoHandoffFalhou, "Handoff para o atendente falhou",
			"Ocorrência "+id+" (origem "+t.Origem+"): o Infodesk não aceitou o handoff. A Sofia parou de responder nessa conversa; o cliente pode estar sem ninguém. Veja o log sofia_falha.", convID)
	}
	e.log.Info("conversa transferida para atendimento humano",
		"id_ocorrencia", id, "conversation_id", convID, "origem", t.Origem, "departamento", dept)
	return id
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
	motivo := motivosDaNota[t.Origem]
	if motivo == "" {
		motivo = "transferido pela " + nomeAssistente
	}
	partes := []string{nomeAssistente + " → humano", "motivo: " + motivo}
	if idOcorrencia != "" && (t.Origem == OrigemCerebro || t.Origem == OrigemDisjuntor) {
		partes = append(partes, "ocorrência: "+idOcorrencia)
	}

	conv, err := e.st.ObterConversa(ctx, convID)
	if err != nil {
		e.log.Error("nota do handoff: ler conversa", "conversation_id", convID, "err", err)
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
		if conv.TicketNumber > 0 {
			partes = append(partes, fmt.Sprintf("chamado #%d", conv.TicketNumber))
		} else {
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
