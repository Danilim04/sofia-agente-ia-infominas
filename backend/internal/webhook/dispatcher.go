package webhook

import (
	"context"
	"sync"
	"time"
)

// Eventos do Infodesk (docs/infodesk/05-webhooks.md).
const (
	EventoMensagemRecebida   = "message.received"
	EventoConversaIniciada   = "conversation.started"
	EventoConversaFinalizada = "conversation.finished"
)

// Start sobe o pool de workers e bloqueia até ctx ser cancelado.
func (s *Server) Start(ctx context.Context) {
	var wg sync.WaitGroup
	for i := 0; i < s.workers; i++ {
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
			s.process(j)
		}
	}
}

// process roteia o evento. O payload do Infodesk ainda não é decodificado:
// TODO(E2.1) montar engine.Inbound a partir de `data` e chamar
// s.engine.HandleMensagem / s.engine.EncerrarEpisodio (com timeout por job).
// Até lá os eventos só são logados — o gateway sobe, autentica e deduplica,
// mas não responde.
func (s *Server) process(j job) {
	start := time.Now()
	defer func() {
		if rec := recover(); rec != nil {
			s.log.Error("panic no processamento do webhook", "event", j.event, "recover", rec)
		}
		s.log.Info("job finalizado", "event", j.event, "duracao_ms", time.Since(start).Milliseconds())
	}()

	switch j.event {
	case EventoMensagemRecebida, EventoConversaFinalizada:
		s.log.Warn("evento recebido sem tratamento (entra em E2.1)",
			"event", j.event, "bytes", len(j.body), "engine_ligada", s.engine != nil)
	case EventoConversaIniciada:
		s.log.Info("conversa iniciada no canal", "event", j.event)
	default:
		s.log.Debug("evento sem handler, ignorado", "event", j.event)
	}
}
