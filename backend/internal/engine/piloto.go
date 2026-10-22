package engine

import (
	"context"

	"bot-infominas/internal/config"
)

// Lista de testadores (E3.9; TESTADORES_TELEFONES): homologação em produção sem
// atingir clientes reais. A flag "Conduz o atendimento" da chave do Infodesk vale
// para a organização inteira (docs/infodesk/08 e 10 §4): ligada, toda conversa
// nova de WhatsApp nasce com a Sofia e os atendentes não são avisados. Com a lista
// ligada, quem não está nela é passado a um atendente NA HORA, pelo caminho único
// (ADR 0007) e sem mensagem ao cliente — para ele, é como se a Sofia não existisse.
// Lista vazia = desligada: todo mundo fala com a Sofia.

// foraDoPiloto diz se a lista está ligada e o telefone não consta nela. Telefone
// vazio com a lista ligada conta como fora: na dúvida o cliente vai a um humano,
// nunca à Sofia.
func foraDoPiloto(cfg *config.Config, telefone string) bool {
	testadores := cfg.Testadores()
	if len(testadores) == 0 {
		return false
	}
	chave, ok := config.ChaveTelefone(telefone)
	if !ok {
		return true
	}
	_, dentro := testadores[chave]
	return !dentro
}

// telefoneDoLote devolve o telefone do cliente: o do evento ou, se o evento não o
// trouxe, o da conversa no store.
func (e *Engine) telefoneDoLote(ctx context.Context, lote []Inbound) string {
	for _, m := range lote {
		if m.Telefone != "" {
			return m.Telefone
		}
	}
	if c, err := e.st.ObterConversa(ctx, lote[0].ConversationID); err == nil && c != nil {
		return c.Telefone
	}
	return ""
}

// transferirForaDoPiloto passa a conversa de quem está fora da lista a um
// atendente: Handoff imediato (DEPARTAMENTO_HUMANO_PADRAO), nota por código, SEM
// mensagem ao cliente e sem ocorrência/alerta de falha. A conversa fica
// `transferida`: as mensagens seguintes do episódio nem chegam aqui (o webhook as
// descarta como "conversa muda") e a rajada de agora é um lote só. Handoff que
// falha ganha a marca de não confirmado e o vigia da E3.6 o repete.
func (e *Engine) transferirForaDoPiloto(ctx context.Context, convID, telefone string, mensagens int) {
	e.log.Info("fora_do_piloto: número fora da lista de testadores; handoff imediato, sem resposta ao cliente",
		"conversation_id", convID, "telefone", mascararTelefone(telefone), "mensagens", mensagens)
	e.TransferirParaHumano(ctx, convID, Transferencia{
		Origem:   OrigemForaDoPiloto,
		Causa:    "número fora da lista de testadores (TESTADORES_TELEFONES)",
		SemAviso: true,
	})
}
