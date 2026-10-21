package reconciliador

import (
	"context"
	"fmt"

	"bot-infominas/internal/engine"
	"bot-infominas/internal/infodesk"
)

// vigiaSemResposta (b) é o vigia BOT_TIMEOUT_HANDOFF (docs/infodesk/07, N1):
// conversa que a Sofia conduz (estado `bot`, handled_by EXTERNAL_BOT), cuja
// última mensagem do cliente tem mais de BotTimeoutHandoff e nenhuma resposta
// nossa depois dela → handoff pelo caminho único da engine (ADR 0007: aviso ao
// cliente, ocorrência, nota por código, estado `transferida`) + alerta.
//
// Uma vez por episódio: a transferência tira a conversa do estado `bot`, que
// é o que o store consulta; o episódio seguinte começa com uma mensagem nova.
// Antes de transferir confere no Infodesk que o bot ainda conduz (um humano
// pode ter assumido sem o evento ter chegado).
//
// TODO(P3): se o handoff falhar, a engine deixa a conversa `transferida`
// localmente e este vigia não a repete (precisa de uma marca de "handoff não
// confirmado" na engine/store).
func (r *Reconciliador) vigiaSemResposta(ctx context.Context) error {
	antes := r.opt.Now().Add(-r.opt.BotTimeoutHandoff)
	convs, err := r.st.ConversasSemResposta(ctx, antes)
	if err != nil {
		return fmt.Errorf("listar conversas sem resposta: %w", err)
	}
	for _, c := range convs {
		if ctx.Err() != nil {
			return nil
		}
		conv, err := r.idk.Conversa(ctx, c.ID)
		if err != nil {
			if lim := limitar(err); isRateLimited(lim) {
				return lim
			}
			r.log.Warn("vigia: não consegui conferir a conversa no Infodesk", "conversation_id", c.ID, "err", classeDoErro(err))
			continue
		}
		if conv.HandledBy != infodesk.ConduzidaPorBotExterno {
			r.log.Info("vigia: a conversa já não é do bot; o evento do Infodesk acerta o estado local",
				"conversation_id", c.ID, "handled_by", conv.HandledBy)
			continue
		}
		parado := r.opt.Now().Sub(c.UltimaMsgEm).Round(1e9)
		id := r.transf.TransferirParaHumano(ctx, c.ID, engine.Transferencia{
			Origem:  OrigemVigia,
			Causa:   fmt.Sprintf("a Sofia não respondeu o cliente em %s", r.opt.BotTimeoutHandoff),
			Detalhe: "BOT_TIMEOUT_HANDOFF: última mensagem do cliente há " + parado.String() + " sem resposta",
		})
		if id == "" {
			continue // já não era do bot (outro caminho transferiu primeiro)
		}
		r.log.Error("vigia: conversa sem resposta transferida para atendimento humano",
			"id_ocorrencia", id, "conversation_id", c.ID, "parado", parado.String())
		r.avisar(TipoBotSemResposta, "Conversa sem resposta da Sofia foi para um atendente",
			"O cliente escreveu e a Sofia não respondeu em "+r.opt.BotTimeoutHandoff.String()+
				" (ocorrência "+id+"). A conversa foi transferida para atendimento humano. Veja o log sofia_falha.", c.ID)
	}
	return nil
}
