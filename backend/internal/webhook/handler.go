// Package webhook recebe os eventos do canal de atendimento (Infodesk),
// autentica a origem (token opcional na URL + HMAC do corpo cru), deduplica
// por id de entrega e despacha o processamento para um pool de workers,
// respondendo 200 em milissegundos.
//
// Nesta etapa (E1.1) o pacote lê só um envelope mínimo (`id`, `event`).
// TODO(E2.1): payload completo de `message.received` (organizationId,
// data.conversationId, contact, message.sender == CLIENT, dedup por
// data.message.id, descarte por createdAt) → engine.Inbound;
// `conversation.finished` → Engine.EncerrarEpisodio; e trocar a verificação
// local pela de internal/infodesk (VerificarAssinatura).
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"bot-infominas/internal/engine"
	"bot-infominas/internal/store"
)

// Engine é a interface do motor consumida pela camada HTTP.
type Engine interface {
	HandleMensagem(ctx context.Context, m engine.Inbound)
	EncerrarEpisodio(ctx context.Context, convID string)
}

// Headers do Infodesk (docs/infodesk/05-webhooks.md).
const (
	HeaderEvento     = "X-Infominas-Event"
	HeaderAssinatura = "X-Infominas-Signature"
)

// Server recebe webhooks e os enfileira para processamento assíncrono.
type Server struct {
	secret  string
	token   string
	store   store.Store
	engine  Engine
	log     *slog.Logger
	jobs    chan job
	workers int
}

type job struct {
	event string
	body  []byte
}

// envelope é o mínimo que a borda lê do corpo: id da entrega (dedup) e nome
// do evento (roteamento). O resto é decodificado pelo dispatcher (E2.1).
type envelope struct {
	ID    string `json:"id"`
	Event string `json:"event"`
}

const maxBodyBytes = 2 << 20 // 2 MiB

// NewServer cria o servidor de webhooks. secret habilita a verificação HMAC
// (vazio = sem verificação, só dev); token exige um segredo na query da URL
// (?token=...).
func NewServer(secret, token string, st store.Store, eng Engine, log *slog.Logger, workers, queueSize int) *Server {
	if workers <= 0 {
		workers = 4
	}
	if queueSize <= 0 {
		queueSize = 256
	}
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		secret:  secret,
		token:   token,
		store:   st,
		engine:  eng,
		log:     log,
		jobs:    make(chan job, queueSize),
		workers: workers,
	}
}

// ComputeSignature reproduz a assinatura do webhook:
// "sha256=" + hex(HMAC-SHA256(secret, raw_body)).
func ComputeSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature compara, em tempo constante, a assinatura recebida com a
// esperada sobre os bytes crus do corpo. Secret vazio = verificação
// desligada (dev).
func VerifySignature(secret string, body []byte, provided string) bool {
	if secret == "" {
		return true
	}
	expected := ComputeSignature(secret, body)
	return hmac.Equal([]byte(expected), []byte(strings.TrimSpace(provided)))
}

// ServeHTTP valida e enfileira o evento, respondendo 200 rapidamente.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.token != "" {
		provided := r.URL.Query().Get("token")
		if !hmac.Equal([]byte(provided), []byte(s.token)) {
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

	if !VerifySignature(s.secret, body, r.Header.Get(HeaderAssinatura)) {
		s.log.Warn("assinatura inválida", "remote", r.RemoteAddr)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	evento := env.Event
	if evento == "" {
		evento = r.Header.Get(HeaderEvento)
	}

	s.log.Info("webhook recebido", "event", evento, "delivery", env.ID, "bytes", len(body))

	// Deduplicação por id de entrega: o canal não reentrega, mas a
	// reconciliação (E2.x) e um replay manual podem repetir o evento.
	if env.ID != "" {
		fresh, err := s.store.MarkProcessed(r.Context(), "entrega:"+env.ID)
		if err != nil {
			s.log.Error("dedup", "delivery", env.ID, "err", err)
		} else if !fresh {
			s.log.Info("evento duplicado ignorado", "event", evento, "delivery", env.ID)
			w.WriteHeader(http.StatusOK)
			return
		}
	}

	select {
	case s.jobs <- job{event: evento, body: body}:
		s.log.Info("evento enfileirado", "event", evento, "delivery", env.ID, "fila_pendente", len(s.jobs))
	default:
		s.log.Error("fila de jobs cheia, evento descartado", "event", evento, "delivery", env.ID)
	}
	w.WriteHeader(http.StatusOK)
}
