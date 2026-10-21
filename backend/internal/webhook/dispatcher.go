package webhook

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"bot-infominas/internal/alerta"
	"bot-infominas/internal/engine"
	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
)

// prazoJob limita o processamento de um evento (a engine só enfileira o turno
// e o cérebro roda no worker da conversa; aqui ficam store e limpeza).
const prazoJob = 30 * time.Second

// Start sobe o pool de workers e bloqueia até ctx ser cancelado.
func (s *Server) Start(ctx context.Context) {
	s.baseCtx = ctx
	var wg sync.WaitGroup
	for i := 0; i < s.opt.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.worker(ctx)
		}()
	}
	wg.Wait()
}

func (s *Server) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-s.jobs:
			s.processarJob(ctx, j)
		}
	}
}

func (s *Server) processarJob(base context.Context, j job) {
	ctx, cancel := context.WithTimeout(base, prazoJob)
	defer cancel()
	start := time.Now()
	log := s.log.With("event", j.ev.Event, "event_id", j.ev.ID, "seq", j.ev.Seq)
	defer func() {
		if rec := recover(); rec != nil {
			log.Error("panic no processamento do webhook", "recover", rec)
		}
		log.Info("job finalizado", "duracao_ms", time.Since(start).Milliseconds())
	}()
	s.Processar(ctx, j.ev)
}

// Processar trata UM evento (webhook ou feed): atualiza o estado local da
// conversa, decide se há turno do cliente e aplica os eventos de ticket. É
// síncrono e não deduplica — quem chama já passou por Receber. Ao final
// avança `eventos_cursor` para o maior seq visto.
func (s *Server) Processar(ctx context.Context, ev infodesk.Evento) {
	log := s.log.With("event", ev.Event, "event_id", ev.ID, "seq", ev.Seq)
	defer func() {
		if ev.Seq > 0 {
			if err := s.store.AvancarCursor(ctx, store.NomeCursorInfodesk, ev.Seq); err != nil {
				log.Error("avançar eventos_cursor", "err", err)
			}
		}
	}()

	if ev.Data.Erro != nil {
		// Objeto presente mas ilegível ≠ evento sem conversa: sem este alerta a
		// mensagem do cliente sumiria em silêncio (aviso do revisor da E1.2).
		log.Error("evento com campo ilegível; não decodificado por inteiro", "err", ev.Data.Erro)
		s.vigia.Aviso(alerta.TipoEventoIlegivel, "Evento do Infodesk com campo ilegível",
			"O evento "+ev.Event+" ("+ev.ID+") trouxe conversation/contact/message que não pôde ser lido ("+ev.Data.Erro.Error()+
				"). Se for message.received, o cliente pode estar sem resposta. Confira se o formato da API mudou.", ev.Data.ConversaID())
	}

	switch ev.Event {
	case infodesk.EventoPing:
		return
	case infodesk.EventoMensagemRecebida:
		s.mensagemRecebida(ctx, ev, log)
	case infodesk.EventoMensagemEnviada:
		s.mensagemEnviada(ctx, ev, log)
	case infodesk.EventoConversaIniciada:
		// Só log: a saudação vem na 1ª message.received. O handledBy ainda é registrado.
		log.Info("conversa iniciada no canal", "conversation_id", ev.Data.ConversaID())
		s.estadoDaConversa(ctx, ev, log, func(*engine.ConversaVista) {})
	case infodesk.EventoConversaAtribuida:
		s.estadoDaConversa(ctx, ev, log, func(v *engine.ConversaVista) { v.Atribuida = true })
	case infodesk.EventoConversaHandoff:
		s.estadoDaConversa(ctx, ev, log, func(v *engine.ConversaVista) { v.Handoff = true })
	case infodesk.EventoConversaDepartamento:
		s.estadoDaConversa(ctx, ev, log, func(*engine.ConversaVista) {})
	case infodesk.EventoConversaEncerrada:
		s.conversaEncerrada(ctx, ev, log)
	case infodesk.EventoTicketCriado, infodesk.EventoTicketAtualizado, infodesk.EventoTicketFechado,
		infodesk.EventoTicketReaberto, infodesk.EventoTicketCancelado:
		s.ticket(ctx, ev, log)
	default:
		// message.status, ticket.replied, ticket.note_added…: sem efeito no gateway.
		log.Debug("evento sem handler, ignorado")
	}
}

// vista monta a ConversaVista de um evento que traz `conversation`; false se
// o evento não a trouxe (ou veio ilegível).
func vista(ev infodesk.Evento) (engine.ConversaVista, bool) {
	c := ev.Data.Conversation
	if c == nil || c.ID == "" {
		return engine.ConversaVista{}, false
	}
	tel := c.Contact.Phone
	if tel == "" && ev.Data.Contact != nil {
		tel = ev.Data.Contact.Phone
	}
	return engine.ConversaVista{
		ID: c.ID, Telefone: tel, HandledBy: c.HandledBy, Evento: ev.Event,
		Quando: ev.CreatedAt, SessaoIniciadaEm: c.SessionStartedAt, TicketNumber: c.TicketNumber,
	}, true
}

// estadoDaConversa aplica um evento de conversa ao estado local; ajusta
// configura as flags do evento (assigned, handoff…).
func (s *Server) estadoDaConversa(ctx context.Context, ev infodesk.Evento, log *slog.Logger, ajusta func(*engine.ConversaVista)) (engine.Resultado, bool) {
	v, ok := vista(ev)
	if !ok {
		log.Info("evento de conversa sem conversation legível; ignorado")
		return engine.Resultado{}, false
	}
	ajusta(&v)
	res, err := s.engine.AplicarConversa(ctx, v)
	if err != nil {
		log.Error("aplicar estado da conversa", "conversation_id", v.ID, "err", err)
		return engine.Resultado{}, false
	}
	log.Info("estado da conversa", "conversation_id", v.ID, "handled_by", v.HandledBy,
		"estado", res.Estado, "aplicado", res.Aplicado)
	return res, true
}

// conversaEncerrada: `conversation.finished` fecha o episódio — estado local
// `encerrada` e limpeza no cérebro. Idempotente: a reentrega do mesmo evento
// já morreu no dedup, e um `finished` mais velho que o estado local (W9) não
// fecha a sessão nova.
func (s *Server) conversaEncerrada(ctx context.Context, ev infodesk.Evento, log *slog.Logger) {
	res, ok := s.estadoDaConversa(ctx, ev, log, func(v *engine.ConversaVista) { v.Encerrada = true })
	if !ok || !res.Aplicado {
		return
	}
	s.engine.EncerrarEpisodio(ctx, ev.Data.ConversaID())
}

// naoETurno diz por que uma message.received NÃO vai ao cérebro ("" = é
// turno): só mensagem do cliente, numa conversa de WhatsApp individual que o
// bot externo conduz (docs/infodesk/07 N5).
func naoETurno(c *infodesk.Conversa, m *infodesk.Mensagem) string {
	switch {
	case !m.DoCliente():
		return "remetente " + m.Sender + " não é o cliente"
	case m.Internal:
		return "nota interna"
	case c.HandledBy != infodesk.ConduzidaPorBotExterno:
		return "conversa conduzida por " + c.HandledBy
	case c.Channel != infodesk.CanalWhatsApp:
		return "canal " + c.Channel + " não passa pelo bot"
	case c.Contact.GroupJid != "":
		return "grupo de WhatsApp não passa pelo bot"
	}
	return ""
}

// mensagemRecebida: atualiza o estado da conversa e, se for turno do
// cliente numa conversa que ainda é da Sofia, entrega a mensagem à engine.
func (s *Server) mensagemRecebida(ctx context.Context, ev infodesk.Evento, log *slog.Logger) {
	c, m := ev.Data.Conversation, ev.Data.Message
	if c == nil || m == nil || c.ID == "" {
		log.Warn("message.received sem conversation/message legíveis; ignorada")
		return
	}
	v, _ := vista(ev)
	criada := m.CreatedAt
	if criada.IsZero() {
		criada = ev.CreatedAt
	}
	v.DoCliente, v.MsgID, v.MsgEm = m.DoCliente(), m.ID, criada
	res, err := s.engine.AplicarConversa(ctx, v)
	if err != nil {
		log.Error("aplicar estado da conversa", "conversation_id", c.ID, "err", err)
		return
	}
	if motivo := naoETurno(c, m); motivo != "" {
		log.Info("message.received não é turno do cliente", "conversation_id", c.ID, "motivo", motivo,
			"estado", res.Estado)
		return
	}
	if res.Estado != store.EstadoBot {
		log.Info("conversa muda: mensagem do cliente não vai ao cérebro",
			"conversation_id", c.ID, "estado", res.Estado)
		return
	}
	nome, tel := c.Contact.Name, c.Contact.Phone
	if ev.Data.Contact != nil {
		if nome == "" {
			nome = ev.Data.Contact.Name
		}
		if tel == "" {
			tel = ev.Data.Contact.Phone
		}
	}
	s.engine.HandleMensagem(ctx, engine.Inbound{
		ConversationID: c.ID,
		Telefone:       tel,
		Nome:           nome,
		Texto:          m.Body,
		MsgID:          m.ID,
		TemMidia:       m.TemMidia(),
		CriadoEm:       criada,
	})
}

// mensagemEnviada: `message.sent` com o nome da NOSSA chave é o eco das
// nossas mensagens (07 W8) e é ignorado; de atendente só atualiza o estado
// (se a conversa mostra um humano conduzindo, o bot cala).
func (s *Server) mensagemEnviada(ctx context.Context, ev infodesk.Evento, log *slog.Logger) {
	if m := ev.Data.Message; m != nil && s.opt.NomeChave != "" && m.SenderName == s.opt.NomeChave {
		log.Debug("eco da própria chave ignorado", "message_id", m.ID)
		return
	}
	s.estadoDaConversa(ctx, ev, log, func(*engine.ConversaVista) {})
}

// ticket aplica ticket.* a `tickets.status_infodesk`/`encerrado`. Só atualiza
// ticket que o gateway abriu (número conhecido); os demais são ignorados.
func (s *Server) ticket(ctx context.Context, ev infodesk.Evento, log *slog.Logger) {
	var d struct {
		Ticket json.RawMessage `json:"ticket"`
	}
	if err := json.Unmarshal(ev.Data.Bruto, &d); err != nil || len(d.Ticket) == 0 {
		log.Warn("evento de ticket sem objeto ticket legível")
		return
	}
	var t infodesk.Ticket
	if err := json.Unmarshal(d.Ticket, &t); err != nil || t.Number <= 0 {
		log.Warn("evento de ticket sem número legível")
		return
	}
	encerrado := t.IsClosed || t.Status.IsClose || t.Status.IsCanceled
	switch ev.Event {
	case infodesk.EventoTicketFechado, infodesk.EventoTicketCancelado:
		encerrado = true
	case infodesk.EventoTicketReaberto:
		encerrado = false
	}
	ok, err := s.store.AtualizarStatusTicket(ctx, t.Number, t.Status.Name, encerrado, ev.CreatedAt)
	if err != nil {
		log.Error("atualizar status do ticket", "ticket", t.Number, "err", err)
		return
	}
	log.Info("ticket atualizado", "ticket", t.Number, "nosso", ok, "encerrado", encerrado, "status", t.Status.Name)
}
