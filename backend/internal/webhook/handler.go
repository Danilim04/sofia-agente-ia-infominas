// Package webhook recebe os eventos do Infodesk v1 (docs/infodesk/05):
// autentica a origem (token opcional na URL + HMAC com timestamp e janela de
// 5 min), confere a organização, deduplica pelo `id` do EVENTO e responde 200
// em milissegundos, despachando o processamento para um pool de workers
// (dispatcher.go). O mesmo caminho (Receber) serve ao reconciliador do feed
// GET /events (E3.3): webhook e feed entregam o mesmo envelope.
package webhook

import (
	"context"
	"crypto/hmac"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"bot-infominas/internal/alerta"
	"bot-infominas/internal/engine"
	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
)

// Engine é a interface do motor consumida pela camada HTTP.
type Engine interface {
	HandleMensagem(ctx context.Context, m engine.Inbound)
	EncerrarEpisodio(ctx context.Context, convID string)
	AplicarConversa(ctx context.Context, v engine.ConversaVista) (engine.Resultado, error)
}

// Options configura o receptor.
type Options struct {
	// Secret habilita a verificação HMAC (INFODESK_WEBHOOK_SECRET); vazio só chega
	// aqui com WEBHOOK_SEM_AUTENTICACAO=true (config.Load recusa o boot sem ele) =
	// sem verificação (só dev — main.go avisa no boot).
	Secret string
	// Token exige um segredo na query da URL (?token=...), além do HMAC.
	Token string
	// Janela é a tolerância de relógio do timestamp (WEBHOOK_JANELA; 0 = 5 min).
	Janela time.Duration
	// Now é o relógio da verificação (injetável nos testes; nil = time.Now).
	Now func() time.Time
	// OrganizationID é o `organizationId` esperado (GET /me do boot); vazio =
	// não confere.
	OrganizationID string
	// NomeChave é o nome da nossa chave de API: `message.sent` com este
	// `senderName` é o eco das nossas mensagens (07 W8) e é ignorado.
	NomeChave string

	Workers   int
	QueueSize int
}

// Server recebe webhooks e os enfileira para processamento assíncrono.
type Server struct {
	opt    Options
	ver    infodesk.Verificador
	store  store.Store
	engine Engine
	vigia  *alerta.Vigia
	log    *slog.Logger
	jobs   chan job

	baseCtx context.Context
}

type job struct {
	ev      infodesk.Evento
	entrega string // X-Infominas-Delivery: só para o log
}

const maxBodyBytes = 2 << 20 // 2 MiB

// NewServer cria o servidor de webhooks. vigia pode ser nil (sem alertas).
func NewServer(o Options, st store.Store, eng Engine, vigia *alerta.Vigia, log *slog.Logger) *Server {
	if o.Workers <= 0 {
		o.Workers = 4
	}
	if o.QueueSize <= 0 {
		o.QueueSize = 256
	}
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		opt:     o,
		ver:     infodesk.Verificador{Secret: o.Secret, Janela: o.Janela, Now: o.Now},
		store:   st,
		engine:  eng,
		vigia:   vigia,
		log:     log,
		jobs:    make(chan job, o.QueueSize),
		baseCtx: context.Background(),
	}
}

// ServeHTTP valida, deduplica e enfileira o evento, respondendo 200 rápido.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.opt.Token != "" {
		provided := r.URL.Query().Get("token")
		if !hmac.Equal([]byte(provided), []byte(s.opt.Token)) {
			s.log.Warn("token de webhook inválido", "remote", r.RemoteAddr)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	// Corpo além do teto é recusado com 413 (não truncado em silêncio, que
	// cairia em 401 por HMAC errado e esconderia a causa).
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var grande *http.MaxBytesError
		if errors.As(err, &grande) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}

	// HMAC sobre "<timestamp>.<corpo bruto>" + janela, ANTES de qualquer parse.
	if s.opt.Secret != "" {
		if err := s.ver.Verificar(r.Header.Get(infodesk.HeaderTimestamp), body, r.Header.Get(infodesk.HeaderAssinatura)); err != nil {
			s.recusar(w, r, err)
			return
		}
	}

	// `ping` ("Enviar teste"): 200 sem parse (o `data` não é documentado).
	if strings.EqualFold(strings.TrimSpace(r.Header.Get(infodesk.HeaderEvento)), infodesk.EventoPing) {
		s.log.Info("webhook ping", "delivery", r.Header.Get(infodesk.HeaderEntrega))
		w.WriteHeader(http.StatusOK)
		return
	}

	ev, err := infodesk.DecodificarEvento(body)
	if err != nil {
		s.log.Warn("webhook com corpo inválido", "err", err, "bytes", len(body))
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if ev.Event == "" {
		ev.Event = r.Header.Get(infodesk.HeaderEvento)
	}

	switch s.Receber(r.Context(), ev, r.Header.Get(infodesk.HeaderEntrega)) {
	case FilaCheia:
		// 503: o Infodesk reentrega (30 s, 2 min…); o evento foi desmarcado.
		http.Error(w, "busy", http.StatusServiceUnavailable)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

// recusar responde 401 e, para uma entrega AUTÊNTICA fora da janela de
// timestamp, dispara o alerta de possível replay/relógio errado.
func (s *Server) recusar(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, infodesk.ErrTimestampForaDaJanela) {
		s.log.Warn("webhook com timestamp fora da janela", "remote", r.RemoteAddr,
			"delivery", r.Header.Get(infodesk.HeaderEntrega))
		s.vigia.Aviso(alerta.TipoWebhookTimestamp, "Webhook do Infodesk com timestamp fora da janela",
			"Uma entrega com assinatura válida chegou com X-Infominas-Timestamp fora da janela de tolerância: possível replay ou relógio do servidor errado.", "")
	} else {
		s.log.Warn("assinatura inválida", "remote", r.RemoteAddr, "motivo", err.Error())
	}
	http.Error(w, "invalid signature", http.StatusUnauthorized)
}

// Recebimento é o destino de um evento entregue a Receber.
type Recebimento int

const (
	Enfileirado Recebimento = iota
	Duplicado               // o `id` do evento já foi visto (reentrega, feed)
	Descartado              // organizationId errado
	FilaCheia               // sem espaço: o evento NÃO ficou marcado como visto
)

// Receber é a entrada comum de webhook e reconciliador: confere a
// organização, deduplica pelo `id` do EVENTO (estável entre reentregas;
// `X-Infominas-Delivery` muda a cada uma e só vai ao log) e enfileira.
func (s *Server) Receber(ctx context.Context, ev infodesk.Evento, entrega string) Recebimento {
	log := s.log.With("event", ev.Event, "event_id", ev.ID, "seq", ev.Seq, "delivery", entrega)
	if esperada := s.opt.OrganizationID; esperada != "" && ev.OrganizationID != esperada {
		log.Error("evento de outra organização descartado")
		s.vigia.Aviso(alerta.TipoOrganizacaoDivergente, "Webhook de outra organização",
			"Chegou um evento assinado cujo organizationId não é o da chave (GET /me). Descartado. Confira se o segredo do webhook pertence a esta organização.", "")
		return Descartado
	}
	fresh, err := s.store.MarkEvent(ctx, ev.ID, ev.Seq)
	if err != nil {
		// Sem dedup não dá para garantir "uma vez": processa mesmo assim — o
		// dedup por msg_id da engine ainda protege o turno.
		log.Error("dedup do evento falhou; processando mesmo assim", "err", err)
	} else if !fresh {
		log.Info("evento duplicado ignorado")
		return Duplicado
	}
	select {
	case s.jobs <- job{ev: ev, entrega: entrega}:
		log.Info("evento enfileirado", "fila_pendente", len(s.jobs))
		return Enfileirado
	default:
		log.Error("fila de jobs cheia; evento devolvido ao Infodesk")
		if err == nil {
			if derr := s.store.DesmarcarEvento(ctx, ev.ID); derr != nil {
				log.Error("desmarcar evento da fila cheia", "err", derr)
			}
		}
		return FilaCheia
	}
}
