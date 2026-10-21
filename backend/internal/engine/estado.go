package engine

import (
	"context"
	"time"

	"bot-infominas/internal/alerta"
	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
)

// ConversaVista é o que um evento do Infodesk diz sobre a conversa, já
// neutro: quem recebe o webhook (ou o reconciliador) monta uma e chama
// Engine.AplicarConversa. A engine não conhece o envelope da API.
type ConversaVista struct {
	ID        string
	Telefone  string
	HandledBy string // EXTERNAL_BOT | INTERNAL_BOT | AGENT | QUEUE | "" (desconhecido)
	Evento    string // nome do evento, só para o log

	// Quando é o `createdAt` do evento. Um evento mais velho que o último fato
	// aplicado à conversa NÃO muda o estado (reentrega de até 6 h; W9).
	Quando           time.Time
	SessaoIniciadaEm time.Time
	TicketNumber     int // conversation.ticketNumber; 0 = sem chamado vinculado

	DoCliente bool      // message.received de CLIENT: abre (ou reabre) o episódio
	MsgID     string    // id da mensagem do cliente
	MsgEm     time.Time // quando o cliente mandou

	Atribuida bool // conversation.assigned
	Handoff   bool // conversation.handoff (confirma a transferência)
	Encerrada bool // conversation.finished
}

// Resultado é o que ficou de uma ConversaVista aplicada.
type Resultado struct {
	// Estado da conversa DEPOIS de aplicar (store.EstadoBot, …).
	Estado string
	// Aplicado é false quando o evento era mais velho que o estado local e
	// foi ignorado (W9).
	Aplicado bool
	// HumanoAssumiu é true quando esta chamada levou a conversa de bot para
	// transferida sem handoff nosso (atendente assumiu): métrica
	// `humano_assumiu_sem_handoff`.
	HumanoAssumiu bool
}

// humanoConduz: o evento diz que quem conduz NÃO é o bot externo.
func (v ConversaVista) humanoConduz() bool {
	return v.Atribuida || (v.HandledBy != "" && v.HandledBy != infodesk.ConduzidaPorBotExterno)
}

// AplicarConversa aplica o que um evento diz sobre a conversa ao estado local
// (`conversas`), na ordem de precedência:
//
//  1. evento mais velho que `updated_at` → ignorado (W9: reentrega não rebaixa);
//  2. `conversation.finished` → encerrada (episódio zerado);
//  3. `conversation.handoff` → transferida (confirma o nosso handoff);
//  4. humano conduz (assigned ou handledBy ≠ EXTERNAL_BOT) → transferida, e
//     conta `humano_assumiu_sem_handoff` se o estado era bot;
//  5. mensagem do cliente com handledBy == EXTERNAL_BOT → bot (conversa nova,
//     reaberta depois de encerrada, ou nova sessão com o `finished` perdido).
//
// `transferida` só sai por `finished`/nova sessão: o bot cala até lá.
func (e *Engine) AplicarConversa(ctx context.Context, v ConversaVista) (Resultado, error) {
	var res Resultado
	var anterior string // estado antes do evento
	quando := v.Quando
	if quando.IsZero() {
		quando = e.agora()
	}
	c, err := e.st.AtualizarConversa(ctx, v.ID, func(c *store.Conversa, existia bool) bool {
		if existia && !c.UpdatedAt.IsZero() && quando.Before(c.UpdatedAt) {
			return false // reentrega mais velha que o estado local
		}
		res.Aplicado = true
		anterior = c.Estado

		if c.Telefone == "" {
			c.Telefone = v.Telefone
		}
		if v.HandledBy != "" {
			c.HandledBy = v.HandledBy
		}
		novaSessao := !v.SessaoIniciadaEm.IsZero() && !c.SessaoIniciadaEm.IsZero() && v.SessaoIniciadaEm.After(c.SessaoIniciadaEm)
		switch {
		case v.Encerrada:
			c.Estado = store.EstadoEncerrada
			zerarEpisodio(c)
		case v.Handoff:
			c.Estado = store.EstadoTransferida
		case v.humanoConduz():
			// INTERNAL_BOT também cala a Sofia, mas não é "humano assumiu".
			if c.Estado == store.EstadoBot && (v.Atribuida || v.HandledBy == infodesk.ConduzidaPorAtendente || v.HandledBy == infodesk.ConduzidaPorFila) {
				res.HumanoAssumiu = true
			}
			c.Estado = store.EstadoTransferida
		case v.HandledBy == infodesk.ConduzidaPorBotExterno:
			switch c.Estado {
			case "":
				c.Estado = store.EstadoBot
			case store.EstadoEncerrada:
				if v.DoCliente {
					c.Estado = store.EstadoBot
					zerarEpisodio(c)
				}
			case store.EstadoTransferida:
				// `finished` perdido: uma sessão nova com o bot conduzindo é outro episódio.
				if v.DoCliente && novaSessao {
					c.Estado = store.EstadoBot
					zerarEpisodio(c)
				}
			}
		}
		if c.Estado == "" {
			c.Estado = store.EstadoBot
		}
		if !v.SessaoIniciadaEm.IsZero() && (c.SessaoIniciadaEm.IsZero() || v.SessaoIniciadaEm.After(c.SessaoIniciadaEm)) {
			c.SessaoIniciadaEm = v.SessaoIniciadaEm
		}
		if v.TicketNumber > 0 && !v.Encerrada {
			c.TicketNumber = v.TicketNumber
		}
		if v.DoCliente && v.MsgID != "" {
			c.UltimaMsgID = v.MsgID
			c.UltimaMsgEm = v.MsgEm
		}
		c.UpdatedAt = quando
		return true
	})
	if err != nil {
		return Resultado{}, err
	}
	res.Estado = c.Estado
	// Handoff não confirmado (E3.6): o evento conversation.handoff o confirma;
	// humano que assumiu, conversa encerrada ou episódio novo o tornam
	// desnecessário. Só vale evento aplicado: uma reentrega velha de outro
	// episódio não pode apagar a marca do atual. CUIDADO: apagar a mais deixa o
	// cliente sem atendente e sem ninguém repetindo (o vigia só confere o
	// Infodesk antes de repetir, não antes de desistir); por isso o chatbot
	// interno do Desk (INTERNAL_BOT sem atribuição) não conta como humano.
	humano := v.humanoConduz() && (v.Atribuida || v.HandledBy != infodesk.ConduzidaPorBotInterno)
	if res.Aplicado && (v.Handoff || humano || v.Encerrada || (anterior != "" && anterior != store.EstadoBot && res.Estado == store.EstadoBot)) {
		if ok, err := e.st.LimparHandoffPendente(ctx, v.ID); err != nil {
			e.log.Error("limpar a marca de handoff não confirmado", "conversation_id", v.ID, "evento", v.Evento, "err", err)
		} else if ok {
			e.log.Info("marca de handoff não confirmado limpa", "conversation_id", v.ID, "evento", v.Evento, "estado", res.Estado)
		}
	}
	if res.HumanoAssumiu {
		n := e.vigia.Contar(alerta.MetricaHumanoAssumiuSemHandoff)
		e.log.Warn("sofia_metrica", "metrica", alerta.MetricaHumanoAssumiuSemHandoff, "total", n,
			"conversation_id", v.ID, "evento", v.Evento, "handled_by", v.HandledBy)
	}
	if !res.Aplicado {
		e.log.Info("evento mais velho que o estado local: estado não rebaixado",
			"conversation_id", v.ID, "evento", v.Evento, "estado", c.Estado)
	}
	return res, nil
}

// zerarEpisodio limpa o que pertence ao episódio que acabou: a próxima
// mensagem é "primeiro turno" (lookup de novo), sem classificação nem
// chamado vinculados. O cache de identificação (`contatos`) tem TTL próprio.
func zerarEpisodio(c *store.Conversa) {
	c.UltimaRespostaEm = time.Time{}
	c.ClassificacaoJSON = ""
	c.TicketNumber = 0
}

// botConduz informa se a Sofia ainda pode falar na conversa: estado bot e o
// Infodesk não diz que um humano conduz. Sem linha = bot (conversa nova ou
// teste direto da engine). Erro do store = bot (melhor responder que calar).
func (e *Engine) botConduz(ctx context.Context, convID string) bool {
	c, err := e.st.ObterConversa(ctx, convID)
	if err != nil {
		e.log.Error("ler estado da conversa", "conversation_id", convID, "err", err)
		return true
	}
	if c == nil {
		return true
	}
	if c.Estado != store.EstadoBot && c.Estado != "" {
		return false
	}
	return c.HandledBy == "" || c.HandledBy == infodesk.ConduzidaPorBotExterno
}

// marcarEstado grava uma mudança de estado NOSSA (handoff feito, janela do
// WhatsApp fechada) e diz se gravou. Só transita a partir de `bot`: se um
// evento concorrente (finished, assigned) já levou a conversa a outro estado,
// ele vence e nada é regravado. Usa o relógio local; eventos mais velhos que
// ele não rebaixam o estado.
func (e *Engine) marcarEstado(ctx context.Context, convID, estado string) bool {
	agora := e.agora()
	gravou := false
	_, err := e.st.AtualizarConversa(ctx, convID, func(c *store.Conversa, _ bool) bool {
		if c.Estado != store.EstadoBot && c.Estado != "" {
			return false
		}
		gravou = true
		c.Estado = estado
		if estado == store.EstadoEncerrada {
			zerarEpisodio(c)
		}
		c.UpdatedAt = agora
		return true
	})
	if err != nil {
		e.log.Error("gravar estado da conversa", "conversation_id", convID, "estado", estado, "err", err)
		return false
	}
	if !gravou {
		e.log.Info("estado da conversa não regravado: outro evento já a tirou do bot",
			"conversation_id", convID, "estado_pedido", estado)
	}
	return gravou
}

// registrarResposta guarda quando a Sofia respondeu (`ultima_resposta_em`):
// marca o fim do primeiro turno do episódio e alimenta o vigia N1 (E3.3).
func (e *Engine) registrarResposta(ctx context.Context, convID string) {
	agora := e.agora()
	_, err := e.st.AtualizarConversa(ctx, convID, func(c *store.Conversa, existia bool) bool {
		if !existia {
			c.Estado = store.EstadoBot
		}
		c.UltimaRespostaEm = agora
		return true
	})
	if err != nil {
		e.log.Error("gravar ultima_resposta_em", "conversation_id", convID, "err", err)
	}
}

// Vigia expõe a vigia de alertas da engine, para o receptor de webhook e o
// reconciliador alertarem pelo mesmo canal.
func (e *Engine) Vigia() *alerta.Vigia { return e.vigia }

// UsarVigia troca a vigia de alertas da engine (main.go e testes de
// integração que precisam capturar os alertas).
func (e *Engine) UsarVigia(v *alerta.Vigia) { e.ligarVigia(v) }

// UsarRelogio troca o relógio da engine (IDs de ocorrência, `updated_at` das
// mudanças locais, disjuntor): os testes de integração o alinham com o
// createdAt dos eventos.
func (e *Engine) UsarRelogio(agora func() time.Time) {
	e.agora = agora
	if e.vigia != nil {
		e.vigia.Agora = agora
	}
}
