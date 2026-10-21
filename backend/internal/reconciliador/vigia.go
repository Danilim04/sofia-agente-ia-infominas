package reconciliador

import (
	"context"
	"fmt"
	"time"

	"bot-infominas/internal/engine"
	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
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
func (r *Reconciliador) vigiaSemResposta(ctx context.Context) error {
	antes := r.opt.Now().Add(-r.opt.BotTimeoutHandoff)
	convs, err := r.st.ConversasSemResposta(ctx, antes, r.opt.Skew)
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
		if conv.Status == "FINISHED" {
			r.log.Info("vigia: a conversa já foi encerrada no Infodesk; o evento finished acerta o estado local", "conversation_id", c.ID)
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
			Espera:  r.opt.BotTimeoutHandoff,
			Causa:   fmt.Sprintf("a Sofia não respondeu o cliente em %s", minutos(r.opt.BotTimeoutHandoff)),
			Detalhe: "BOT_TIMEOUT_HANDOFF: última mensagem do cliente há " + parado.String() + " sem resposta",
		})
		if id == "" {
			continue // já não era do bot (outro caminho transferiu primeiro)
		}
		r.log.Error("vigia: conversa sem resposta transferida para atendimento humano",
			"id_ocorrencia", id, "conversation_id", c.ID, "parado", parado.String())
		r.avisar(TipoBotSemResposta, "Conversa sem resposta da Sofia foi para um atendente",
			"O cliente escreveu e a Sofia não respondeu em "+minutos(r.opt.BotTimeoutHandoff)+
				" (ocorrência "+id+"). A conversa foi transferida para atendimento humano. Veja o log sofia_falha.", c.ID)
	}
	return nil
}

// minutos escreve a duração em minutos inteiros ("5 min") para texto de
// alerta; abaixo de 1 min cai em segundos.
func minutos(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d s", int(d.Seconds()))
	}
	return fmt.Sprintf("%d min", int(d.Minutes()))
}

// handoffBackoffBase é a espera após a 1ª repetição que falhou; dobra a cada
// falha até BackoffMax. Hipótese de trabalho (sem fonte no Infodesk).
const handoffBackoffBase = 30 * time.Second

// esperaDoHandoff é o backoff exponencial depois de `tentativas` repetições
// que falharam (1 → 30 s, 2 → 60 s, …), limitado a BackoffMax.
func (r *Reconciliador) esperaDoHandoff(tentativas int) time.Duration {
	espera := handoffBackoffBase
	for i := 1; i < tentativas && espera < r.opt.BackoffMax; i++ {
		espera *= 2
	}
	if espera > r.opt.BackoffMax {
		espera = r.opt.BackoffMax
	}
	return espera
}

// handoffsPendentes (c) repete o handoff que a engine não conseguiu confirmar:
// a conversa já está `transferida` (o bot calou), mas nenhum atendente foi
// avisado (docs/plano/01, seção B). Cada marca vencida:
//
//   - some se a conversa deixou de precisar dele (já não está `transferida`
//     aqui, foi encerrada ou um humano/fila já conduz no Infodesk);
//   - senão repete o handoff UMA vez (idempotente) — sucesso apaga a marca (na
//     engine); falha adia com backoff exponencial e alerta (o Vigia limita a um
//     alerta por conversa a cada 5 min). Não há limite de repetições: parar
//     deixaria o cliente sem ninguém; o teto é o BackoffMax.
//
// A marca também é apagada pela engine ao ver `conversation.handoff`,
// `finished` ou humano conduzindo.
func (r *Reconciliador) handoffsPendentes(ctx context.Context) error {
	hs, err := r.st.HandoffsPendentes(ctx, r.opt.Now())
	if err != nil {
		return fmt.Errorf("listar handoffs não confirmados: %w", err)
	}
	for _, h := range hs {
		if ctx.Err() != nil {
			return nil
		}
		if r.handoffDispensado(ctx, h) {
			continue
		}
		conv, err := r.idk.Conversa(ctx, h.ConversationID)
		if err != nil {
			if lim := limitar(err); isRateLimited(lim) {
				return lim
			}
			r.log.Warn("handoff pendente: não consegui conferir a conversa no Infodesk; tento no próximo ciclo",
				"conversation_id", h.ConversationID, "err", classeDoErro(err))
			continue
		}
		if conv.Status == "FINISHED" || conv.HandledBy != infodesk.ConduzidaPorBotExterno {
			r.limparHandoff(ctx, h, "a conversa já não é do bot no Infodesk (handled_by="+conv.HandledBy+", status="+conv.Status+")")
			continue
		}
		if err := r.transf.ReenviarHandoff(ctx, h); err != nil {
			if lim := limitar(err); isRateLimited(lim) {
				return lim
			}
			tentativas := h.Tentativas + 1
			espera := r.esperaDoHandoff(tentativas)
			if errAdiar := r.st.AdiarHandoffPendente(ctx, h.ConversationID, tentativas, r.opt.Now().Add(espera)); errAdiar != nil {
				r.log.Error("handoff pendente: gravar o adiamento", "conversation_id", h.ConversationID, "err", errAdiar)
			}
			r.log.Error("handoff pendente: o Infodesk continua sem aceitar o handoff",
				"id_ocorrencia", h.IDOcorrencia, "conversation_id", h.ConversationID, "tentativas", tentativas,
				"proxima_em", espera.String(), "err", classeDoErro(err))
			r.avisar(TipoHandoffNaoConfirmado, "Handoff para o atendente continua sem confirmação",
				fmt.Sprintf("Ocorrência %s: repeti o handoff %d vez(es) e o Infodesk não aceitou (%s). O cliente segue sem atendente; próxima tentativa em %s. Veja o log do gateway.",
					h.IDOcorrencia, tentativas, classeDoErro(err), espera.Round(time.Second)), h.ConversationID)
		}
	}
	return nil
}

// handoffDispensado apaga a marca se a conversa local já não está
// `transferida` (episódio novo, encerrada) e diz se a apagou.
func (r *Reconciliador) handoffDispensado(ctx context.Context, h store.HandoffPendente) bool {
	c, err := r.st.ObterConversa(ctx, h.ConversationID)
	if err != nil {
		r.log.Error("handoff pendente: ler conversa", "conversation_id", h.ConversationID, "err", err)
		return false
	}
	if c != nil && c.Estado == store.EstadoTransferida {
		return false
	}
	r.limparHandoff(ctx, h, "a conversa já não está transferida localmente")
	return true
}

func (r *Reconciliador) limparHandoff(ctx context.Context, h store.HandoffPendente, motivo string) {
	if _, err := r.st.LimparHandoffPendente(ctx, h.ConversationID); err != nil {
		r.log.Error("handoff pendente: apagar a marca", "conversation_id", h.ConversationID, "err", err)
		return
	}
	r.log.Info("handoff pendente: marca apagada sem repetir o handoff", "conversation_id", h.ConversationID,
		"id_ocorrencia", h.IDOcorrencia, "motivo", motivo)
}
