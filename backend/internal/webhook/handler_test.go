package webhook

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"bot-azapfy/internal/chatwoot"
	"bot-azapfy/internal/store"
)

// engineNoop satisfaz Engine sem processar nada: os testes olham só a borda
// HTTP (autenticação e enfileiramento); os workers não são iniciados.
type engineNoop struct{}

func (engineNoop) HandleMessageCreated(context.Context, *chatwoot.MessageCreated)           {}
func (engineNoop) HandleConversationUpdated(context.Context, *chatwoot.ConversationUpdated) {}
func (engineNoop) HandleConversationCreated(context.Context, *chatwoot.ConversationCreated) {}
func (engineNoop) HandleConversationStatusChanged(context.Context, *chatwoot.ConversationStatusChanged) {
}

const (
	segredoTeste = "segredo-hmac-de-teste"
	corpoTeste   = `{"event":"message_created","id":1,"content":"oi"}`
	tsTeste      = "1759700000"
)

func newServer(t *testing.T, secret, token string) *Server {
	t.Helper()
	st, err := store.NewSQLite(filepath.Join(t.TempDir(), "webhook.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewServer(secret, token, st, engineNoop{}, log, 1, 8)
}

// post envia o corpo ao handler com os headers dados (sem X-Chatwoot-Delivery:
// a dedup fica fora do caminho e cada chamada conta como evento novo).
func post(s *Server, url, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func assinado(secret, ts, body string) map[string]string {
	return map[string]string{
		"X-Chatwoot-Timestamp": ts,
		"X-Chatwoot-Signature": chatwoot.ComputeSignature(secret, ts, []byte(body)),
	}
}

// ZG-3: com WEBHOOK_SECRET preenchido, só entra requisição com assinatura
// HMAC válida (X-Chatwoot-Signature sobre "{timestamp}.{corpo}").
func TestWebhookComSegredoExigeAssinaturaValida(t *testing.T) {
	s := newServer(t, segredoTeste, "")

	rec := post(s, "/webhook", corpoTeste, assinado(segredoTeste, tsTeste, corpoTeste))
	if rec.Code != http.StatusOK {
		t.Fatalf("assinatura válida: esperava 200, veio %d (%s)", rec.Code, rec.Body.String())
	}
	if n := len(s.jobs); n != 1 {
		t.Fatalf("assinatura válida: esperava 1 evento enfileirado, veio %d", n)
	}

	casos := map[string]map[string]string{
		"sem assinatura":                {},
		"só timestamp":                  {"X-Chatwoot-Timestamp": tsTeste},
		"assinatura com outro segredo":  assinado("outro-segredo", tsTeste, corpoTeste),
		"assinatura de outro timestamp": {"X-Chatwoot-Timestamp": tsTeste, "X-Chatwoot-Signature": chatwoot.ComputeSignature(segredoTeste, "1759700999", []byte(corpoTeste))},
		"assinatura de outro corpo":     assinado(segredoTeste, tsTeste, `{"event":"message_created","id":2}`),
		"lixo no header":                {"X-Chatwoot-Timestamp": tsTeste, "X-Chatwoot-Signature": "sha256=deadbeef"},
	}
	for nome, headers := range casos {
		rec := post(s, "/webhook", corpoTeste, headers)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: esperava 401, veio %d (%s)", nome, rec.Code, rec.Body.String())
		}
	}
	if n := len(s.jobs); n != 1 {
		t.Fatalf("requisições recusadas não podem enfileirar: fila com %d evento(s)", n)
	}
}

// ZG-3 (compatibilidade): com WEBHOOK_SECRET vazio o comportamento atual
// continua — a assinatura não é exigida nem verificada (o Chatwoot em uso não
// assina). Sem isto o deploy derrubaria o bot até alguém configurar o segredo.
func TestWebhookSemSegredoMantemComportamentoAtual(t *testing.T) {
	s := newServer(t, "", "")

	if rec := post(s, "/webhook", corpoTeste, nil); rec.Code != http.StatusOK {
		t.Fatalf("sem segredo e sem headers: esperava 200, veio %d", rec.Code)
	}
	if rec := post(s, "/webhook", corpoTeste, map[string]string{"X-Chatwoot-Signature": "sha256=lixo"}); rec.Code != http.StatusOK {
		t.Fatalf("sem segredo, assinatura inválida ignorada: esperava 200, veio %d", rec.Code)
	}
	if n := len(s.jobs); n != 2 {
		t.Fatalf("esperava 2 eventos enfileirados, veio %d", n)
	}
}

// Token na URL continua valendo, sozinho ou junto com o HMAC (a checagem do
// token vem antes de ler o corpo).
func TestWebhookTokenNaURL(t *testing.T) {
	s := newServer(t, "", "tok-teste")
	if rec := post(s, "/webhook", corpoTeste, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("sem token: esperava 401, veio %d", rec.Code)
	}
	if rec := post(s, "/webhook?token=errado", corpoTeste, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token errado: esperava 401, veio %d", rec.Code)
	}
	if rec := post(s, "/webhook?token=tok-teste", corpoTeste, nil); rec.Code != http.StatusOK {
		t.Fatalf("token certo: esperava 200, veio %d", rec.Code)
	}

	ambos := newServer(t, segredoTeste, "tok-teste")
	if rec := post(ambos, "/webhook?token=tok-teste", corpoTeste, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token certo sem assinatura (segredo definido): esperava 401, veio %d", rec.Code)
	}
	if rec := post(ambos, "/webhook?token=tok-teste", corpoTeste, assinado(segredoTeste, tsTeste, corpoTeste)); rec.Code != http.StatusOK {
		t.Fatalf("token certo + assinatura válida: esperava 200, veio %d", rec.Code)
	}
	if n := len(ambos.jobs); n != 1 {
		t.Fatalf("esperava 1 evento enfileirado, veio %d", n)
	}
}
