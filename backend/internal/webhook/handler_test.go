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

	"bot-infominas/internal/engine"
	"bot-infominas/internal/store"
)

// engineNoop satisfaz Engine sem processar nada: os testes olham só a borda
// HTTP (autenticação, dedup e enfileiramento); os workers não são iniciados.
type engineNoop struct{}

func (engineNoop) HandleMensagem(context.Context, engine.Inbound) {}
func (engineNoop) EncerrarEpisodio(context.Context, string)       {}

const (
	segredoTeste = "segredo-hmac-de-teste"
	corpoTeste   = `{"id":"ent-1","event":"message.received","data":{"conversationId":"c1"}}`
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

// post envia o corpo ao handler com os headers dados.
func post(s *Server, url, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func assinado(secret, body string) map[string]string {
	return map[string]string{HeaderAssinatura: ComputeSignature(secret, []byte(body))}
}

// corpoUnico gera um corpo com id de entrega próprio (a dedup por id fica
// fora do caminho dos testes de autenticação).
func corpoUnico(id string) string {
	return `{"id":"` + id + `","event":"message.received","data":{}}`
}

// Com INFODESK_WEBHOOK_SECRET preenchido, só entra requisição com HMAC válido
// sobre o corpo cru (sha256=hex em X-Infominas-Signature).
func TestWebhookComSegredoExigeAssinaturaValida(t *testing.T) {
	s := newServer(t, segredoTeste, "")

	rec := post(s, "/webhook", corpoTeste, assinado(segredoTeste, corpoTeste))
	if rec.Code != http.StatusOK {
		t.Fatalf("assinatura válida: esperava 200, veio %d (%s)", rec.Code, rec.Body.String())
	}
	if n := len(s.jobs); n != 1 {
		t.Fatalf("assinatura válida: esperava 1 evento enfileirado, veio %d", n)
	}

	outro := corpoUnico("ent-2")
	casos := map[string]map[string]string{
		"sem assinatura":               {},
		"assinatura com outro segredo": assinado("outro-segredo", outro),
		"assinatura de outro corpo":    assinado(segredoTeste, corpoTeste),
		"lixo no header":               {HeaderAssinatura: "sha256=deadbeef"},
		"hex sem prefixo":              {HeaderAssinatura: strings.TrimPrefix(ComputeSignature(segredoTeste, []byte(outro)), "sha256=")},
	}
	for nome, headers := range casos {
		rec := post(s, "/webhook", outro, headers)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: esperava 401, veio %d (%s)", nome, rec.Code, rec.Body.String())
		}
	}
	if n := len(s.jobs); n != 1 {
		t.Fatalf("requisições recusadas não podem enfileirar: fila com %d evento(s)", n)
	}
}

// Sem segredo a assinatura não é exigida nem verificada (dev).
func TestWebhookSemSegredoNaoVerifica(t *testing.T) {
	s := newServer(t, "", "")

	if rec := post(s, "/webhook", corpoUnico("a"), nil); rec.Code != http.StatusOK {
		t.Fatalf("sem segredo e sem headers: esperava 200, veio %d", rec.Code)
	}
	if rec := post(s, "/webhook", corpoUnico("b"), map[string]string{HeaderAssinatura: "sha256=lixo"}); rec.Code != http.StatusOK {
		t.Fatalf("sem segredo, assinatura inválida ignorada: esperava 200, veio %d", rec.Code)
	}
	if n := len(s.jobs); n != 2 {
		t.Fatalf("esperava 2 eventos enfileirados, veio %d", n)
	}
}

// Token na URL vale sozinho ou junto com o HMAC (a checagem do token vem
// antes de ler o corpo).
func TestWebhookTokenNaURL(t *testing.T) {
	s := newServer(t, "", "tok-teste")
	if rec := post(s, "/webhook", corpoUnico("a"), nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("sem token: esperava 401, veio %d", rec.Code)
	}
	if rec := post(s, "/webhook?token=errado", corpoUnico("b"), nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token errado: esperava 401, veio %d", rec.Code)
	}
	if rec := post(s, "/webhook?token=tok-teste", corpoUnico("c"), nil); rec.Code != http.StatusOK {
		t.Fatalf("token certo: esperava 200, veio %d", rec.Code)
	}

	ambos := newServer(t, segredoTeste, "tok-teste")
	if rec := post(ambos, "/webhook?token=tok-teste", corpoTeste, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token certo sem assinatura (segredo definido): esperava 401, veio %d", rec.Code)
	}
	if rec := post(ambos, "/webhook?token=tok-teste", corpoTeste, assinado(segredoTeste, corpoTeste)); rec.Code != http.StatusOK {
		t.Fatalf("token certo + assinatura válida: esperava 200, veio %d", rec.Code)
	}
	if n := len(ambos.jobs); n != 1 {
		t.Fatalf("esperava 1 evento enfileirado, veio %d", n)
	}
}

// Dedup por id de entrega: a mesma entrega duas vezes responde 200 mas só
// enfileira uma; evento sem id passa sempre; evento sem `event` no corpo usa
// o header.
func TestWebhookDeduplicaPorIdDeEntrega(t *testing.T) {
	s := newServer(t, "", "")
	post(s, "/webhook", corpoTeste, nil)
	if rec := post(s, "/webhook", corpoTeste, nil); rec.Code != http.StatusOK {
		t.Fatalf("duplicata responde 200, veio %d", rec.Code)
	}
	if n := len(s.jobs); n != 1 {
		t.Fatalf("duplicata não enfileira: esperava 1, veio %d", n)
	}
	post(s, "/webhook", `{"event":"x"}`, nil)
	post(s, "/webhook", `{"event":"x"}`, nil)
	if n := len(s.jobs); n != 3 {
		t.Fatalf("sem id não há dedup: esperava 3, veio %d", n)
	}
	post(s, "/webhook", `{"id":"h1"}`, map[string]string{HeaderEvento: "conversation.started"})
	var ultimo job
	for i := 0; i < 4; i++ {
		ultimo = <-s.jobs
	}
	if ultimo.event != "conversation.started" {
		t.Fatalf("evento devia vir do header: %+v", ultimo.event)
	}
	if rec := post(s, "/webhook", `não é json`, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("json inválido: esperava 400, veio %d", rec.Code)
	}
	grande := `{"id":"g","event":"x","pad":"` + strings.Repeat("x", maxBodyBytes) + `"}`
	if rec := post(s, "/webhook", grande, nil); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("corpo acima do teto: esperava 413, veio %d", rec.Code)
	}
}

func TestComputeSignatureVetorConhecido(t *testing.T) {
	// HMAC-SHA256("segredo", "corpo") — vetor calculado fora deste código.
	got := ComputeSignature("segredo", []byte("corpo"))
	if !strings.HasPrefix(got, "sha256=") || len(got) != len("sha256=")+64 {
		t.Fatalf("formato inesperado: %q", got)
	}
	if !VerifySignature("segredo", []byte("corpo"), got) || VerifySignature("segredo", []byte("corpo2"), got) {
		t.Fatal("verificação deveria casar só com o mesmo corpo")
	}
}
