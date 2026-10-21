package reconciliador

import (
	"context"
	"fmt"
	"strconv"

	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
)

// ticketsPendentes (a) reconcilia `tickets.status == pendente` (um POST de
// ticket sem resposta; ADR 0005, revisão 2026-10-20) por listagem:
// GET /tickets?phone=&status=open, casando por `conversationId`. Achou →
// `aberto` com o número. Não achou até TICKET_PENDENTE_TTL → `falha` + alerta.
// Nunca repete o POST. Se a listagem não respondeu e o prazo venceu, o ticket
// fica `pendente` (pode existir no Infodesk) com alerta próprio — marcar
// `falha` sem ter conseguido olhar esconderia um chamado real.
func (r *Reconciliador) ticketsPendentes(ctx context.Context) error {
	pend, err := r.st.TicketsPendentes(ctx)
	if err != nil {
		return fmt.Errorf("listar tickets pendentes: %w", err)
	}
	for _, t := range pend {
		if ctx.Err() != nil {
			return nil
		}
		vencido := !t.CreatedAt.IsZero() && r.opt.Now().Sub(t.CreatedAt) >= r.opt.TicketPendenteTTL
		numero, listou, err := r.procurarTicket(ctx, t)
		if err != nil {
			if lim := limitar(err); isRateLimited(lim) {
				return lim
			}
			r.log.Warn("reconciliar ticket pendente: listagem falhou", "chave", t.Chave, "conversation_id", t.ConversationID, "err", classeDoErro(err))
			if vencido {
				r.avisar(TipoTicketNaoChecado, "Ticket pendente além do prazo e não consegui conferir no Infodesk",
					"Um chamado ficou pendente além de TICKET_PENDENTE_TTL e a listagem de tickets do Infodesk falhou ("+classeDoErro(err)+
						"). Ele continua pendente até a listagem responder; confira se o chamado existe no painel. Chave "+t.Chave+".", t.ConversationID)
			}
			continue
		}
		if numero > 0 {
			raw := `{"number":` + strconv.Itoa(numero) + `,"origem":"reconciliador"}`
			if err := r.st.ConcluirTicket(ctx, t.Chave, numero, raw, true); err != nil {
				r.log.Error("concluir ticket reconciliado", "chave", t.Chave, "ticket", numero, "err", err)
				continue
			}
			r.log.Info("ticket pendente reconciliado", "chave", t.Chave, "ticket", numero, "conversation_id", t.ConversationID, "reconciliado", true)
			continue
		}
		if !vencido || !listou {
			continue
		}
		ok, err := r.st.FalharTicket(ctx, t.Chave)
		if err != nil {
			r.log.Error("marcar ticket como falha", "chave", t.Chave, "err", err)
			continue
		}
		if ok {
			r.log.Error("ticket pendente não apareceu no Infodesk dentro do prazo; marcado como falha", "chave", t.Chave, "conversation_id", t.ConversationID)
			r.avisar(TipoTicketPendenteFalh, "Chamado não foi aberto no Infodesk",
				"O POST do chamado ficou sem resposta e ele não apareceu na listagem do Infodesk em "+r.opt.TicketPendenteTTL.String()+
					". Marcado como falha (chave "+t.Chave+"). O cliente pode estar esperando um chamado que não existe.", t.ConversationID)
		}
	}
	return nil
}

// procurarTicket lista os tickets abertos do telefone e devolve o número do
// que pertence à conversa do ticket pendente (0 = não achou). listou=true
// quando a listagem inteira respondeu (a ausência é confiável).
func (r *Reconciliador) procurarTicket(ctx context.Context, t store.Ticket) (numero int, listou bool, err error) {
	if t.Telefone == "" || t.ConversationID == "" {
		// Sem telefone ou conversa não há como casar; deixa o prazo correr.
		return 0, false, nil
	}
	cursor := ""
	for pagina := 0; pagina < maxPaginasListaTickets; pagina++ {
		pag, err := r.idk.ListarTickets(ctx, infodesk.FiltroTickets{
			Phone: t.Telefone, Status: "open", Limit: limiteListaTickets, Cursor: cursor,
		})
		if err != nil {
			return 0, false, err
		}
		for _, tk := range pag.Data {
			if tk.ConversationID == t.ConversationID && tk.Number > 0 {
				return tk.Number, true, nil
			}
		}
		if pag.NextCursor == "" {
			return 0, true, nil
		}
		cursor = pag.NextCursor
	}
	return 0, false, nil
}
