package engine

import (
	"context"
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
)

// Transferencia descreve por que a conversa vai para um atendente.
type Transferencia struct {
	Origem   string // OrigemCerebro, OrigemCliente, ... (obrigatório)
	Classe   string // brain.ClasseTransitoria | brain.ClasseDefinitiva | "" (não se aplica)
	Causa    string // linguagem simples: alerta, log, tabela
	Detalhe  string // técnico, sem dado pessoal: só log/tabela
	ID       string // ID de ocorrência já existente (ex.: vindo do cérebro); vazio = gerar
	Mensagem string // mensagem ao cliente; vazia = MsgTransferencia
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

// TransferirParaHumano é o ÚNICO caminho para o atendimento humano — usado
// pelas falhas do cérebro, pelo disjuntor, pela mídia e pelo pedido explícito
// do cliente (e, a partir de E2.1, pelas `acoes` do cérebro). Faz, nesta
// ordem e sem interromper nas falhas parciais (cada passo loga o próprio
// erro):
//
//  1. ID de ocorrência (gera se não veio), log estruturado `sofia_falha` e
//     registro na tabela `falhas` — o mesmo ID em todos;
//  2. alertas ao responsável (vigia, ZC-7);
//  3. mensagem neutra ao cliente (nunca erro técnico);
//  4. transferência de departamento no transporte (DEPARTAMENTO_HUMANO_PADRAO).
//
// Devolve o ID de ocorrência.
//
// TODO(E2.1): marcar `conversas.estado = transferida` e escolher o
// departamento pela taxonomia (categoria → departamento) quando houver.
func (e *Engine) TransferirParaHumano(ctx context.Context, convID string, t Transferencia) string {
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

	msg := t.Mensagem
	if msg == "" {
		msg = MsgTransferencia
	}
	e.send(ctx, convID, msg)

	dept := strings.TrimSpace(e.cfg.DepartamentoHumanoPadrao)
	if dept == "" {
		e.log.Error("transferir: DEPARTAMENTO_HUMANO_PADRAO vazio — conversa não foi movida no canal",
			"id_ocorrencia", id, "conversation_id", convID)
	} else if err := e.tr.TransferirDepartamento(ctx, convID, dept); err != nil {
		e.log.Error("transferir: departamento", "id_ocorrencia", id, "conversation_id", convID, "departamento", dept, "err", err)
	}
	e.log.Info("conversa transferida para atendimento humano",
		"id_ocorrencia", id, "conversation_id", convID, "origem", t.Origem, "departamento", dept)
	return id
}
