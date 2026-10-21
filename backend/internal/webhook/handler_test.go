package webhook

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bot-infominas/internal/alerta"
	"bot-infominas/internal/engine"
	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
)

// engineNoop satisfaz Engine sem processar nada: os testes abaixo olham só a
// borda HTTP (autenticação, janela, dedup e enfileiramento).
type engineNoop struct{}

func (engineNoop) HandleMensagem(context.Context, engine.Inbound) {}
func (engineNoop) EncerrarEpisodio(context.Context, string)       {}
func (engineNoop) AplicarConversa(context.Context, engine.ConversaVista) (engine.Resultado, error) {
	return engine.Resultado{Estado: store.EstadoBot, Aplicado: true}, nil
}

func servidorSimples(t *testing.T, o Options) (*Server, *notificadorFalso) {
	t.Helper()
	st, err := store.NewSQLite(filepath.Join(t.TempDir(), "webhook.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	n := &notificadorFalso{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewServer(o, st, engineNoop{}, alerta.NovaVigia(n, func() time.Time { return t0 }), log), n
}

func postar(s *Server, url string, corpo []byte, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(string(corpo)))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func corpoSimples(id string, seq int64) []byte {
	return evt(id, seq, "conversation.started", t0, map[string]any{
		"conversation": convJSON("c1", "EXTERNAL_BOT", t0),
	})
}

// HMAC inválido = 401 e nada entra na fila.
func TestHMACInvalidoRecusa401(t *testing.T) {
	a := novoAmbiente(t)
	corpo := a.recebida("c-1", "m-1", "oi", t0)
	stamp := fmt.Sprint(t0.Unix())
	casos := map[string]map[string]string{
		"sem assinatura":               {infodesk.HeaderTimestamp: stamp},
		"sem timestamp":                {infodesk.HeaderAssinatura: infodesk.Assinar(segredoTeste, stamp, corpo)},
		"assinatura com outro segredo": {infodesk.HeaderTimestamp: stamp, infodesk.HeaderAssinatura: infodesk.Assinar("outro", stamp, corpo)},
		"assinatura de outro corpo":    {infodesk.HeaderTimestamp: stamp, infodesk.HeaderAssinatura: infodesk.Assinar(segredoTeste, stamp, []byte("outro"))},
		"assinatura sem o timestamp":   {infodesk.HeaderTimestamp: stamp, infodesk.HeaderAssinatura: "sha256=deadbeef"},
		"timestamp trocado":            {infodesk.HeaderTimestamp: fmt.Sprint(t0.Unix() + 1), infodesk.HeaderAssinatura: infodesk.Assinar(segredoTeste, stamp, corpo)},
	}
	for nome, headers := range casos {
		if rec := postar(a.srv, "/webhook", corpo, headers); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: esperava 401, veio %d", nome, rec.Code)
		}
	}
	if n := len(a.srv.jobs); n != 0 {
		t.Fatalf("recusadas não podem enfileirar: %d na fila", n)
	}
	if len(a.alertas.doTipo(alerta.TipoWebhookTimestamp)) != 0 {
		t.Fatal("assinatura inválida não é replay: não alerta timestamp")
	}
	// Positivo: a mesma entrega bem assinada passa.
	if rec := a.entregar(corpo, "d1"); rec.Code != http.StatusOK {
		t.Fatalf("assinatura válida: esperava 200, veio %d", rec.Code)
	}
}

// Timestamp a mais de 5 min = 401 (passado ou futuro) e alerta
// webhook_timestamp_fora_da_janela, mesmo com a assinatura correta.
func TestTimestampForaDaJanelaRecusa401EAlerta(t *testing.T) {
	a := novoAmbiente(t)
	corpo := a.recebida("c-2", "m-2", "oi", t0)
	for nome, quando := range map[string]time.Time{
		"6 min no passado": t0.Add(-6 * time.Minute),
		"6 min no futuro":  t0.Add(6 * time.Minute),
	} {
		if rec := a.entregarComTimestamp(corpo, "d", quando); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: esperava 401, veio %d", nome, rec.Code)
		}
	}
	if n := len(a.alertas.doTipo(alerta.TipoWebhookTimestamp)); n != 1 {
		t.Fatalf("esperava 1 alerta webhook_timestamp_fora_da_janela (silenciado na repetição), veio %d", n)
	}
	if len(a.srv.jobs) != 0 {
		t.Fatal("entrega fora da janela não pode enfileirar")
	}
	// Dentro da janela (4 min) passa — e WEBHOOK_JANELA é respeitada.
	if rec := a.entregarComTimestamp(corpo, "d", t0.Add(-4*time.Minute)); rec.Code != http.StatusOK {
		t.Fatalf("4 min está dentro da janela de 5: veio %d", rec.Code)
	}
	curto, _ := servidorSimples(t, Options{Secret: segredoTeste, Janela: time.Minute, Now: func() time.Time { return t0 }})
	stamp := fmt.Sprint(t0.Add(-3 * time.Minute).Unix())
	h := map[string]string{infodesk.HeaderTimestamp: stamp, infodesk.HeaderAssinatura: infodesk.Assinar(segredoTeste, stamp, corpo)}
	if rec := postar(curto, "/webhook", corpo, h); rec.Code != http.StatusUnauthorized {
		t.Fatalf("janela de 1 min: 3 min devia ser recusado, veio %d", rec.Code)
	}
}

// O mesmo `id` de evento com um X-Infominas-Delivery NOVO (reentrega) é no-op:
// responde 200, não enfileira e não processa de novo.
func TestMesmoIDComDeliveryNovoENoop(t *testing.T) {
	a := novoAmbiente(t)
	corpo := a.recebida("c-3", "m-3", "oi", t0)
	if rec := a.entregar(corpo, "entrega-1"); rec.Code != http.StatusOK {
		t.Fatalf("1ª entrega: %d", rec.Code)
	}
	if rec := a.entregar(corpo, "entrega-2-reentrega"); rec.Code != http.StatusOK {
		t.Fatalf("reentrega: esperava 200, veio %d", rec.Code)
	}
	if n := len(a.srv.jobs); n != 1 {
		t.Fatalf("a reentrega do mesmo evento não enfileira: %d na fila", n)
	}
	a.drenar()
	esperar(t, "cérebro chamado", func() bool { return a.br.total() >= 1 })
	// Reentrega DEPOIS de processado também é no-op.
	a.entregar(corpo, "entrega-3-tardia")
	a.drenar()
	silencio()
	if a.br.total() != 1 {
		t.Fatalf("reentrega tardia gerou novo turno: %d chamadas ao cérebro", a.br.total())
	}
}

// Dedup por data.message.id: o MESMO message.id em eventos diferentes (webhook
// + feed) vira um turno só.
func TestMesmaMensagemEmEventosDiferentesViraUmTurno(t *testing.T) {
	a := novoAmbiente(t)
	a.enviar(a.recebida("c-4", "m-4", "oi", t0))
	a.enviar(a.recebida("c-4", "m-4", "oi", t0)) // outro evt_ id, mesma message.id
	esperar(t, "cérebro", func() bool { return a.br.total() >= 1 })
	silencio()
	if a.br.total() != 1 {
		t.Fatalf("a mesma mensagem não vira dois turnos: %d", a.br.total())
	}
}

// ping: 200 sem parse (data não documentado).
func TestPing200SemParse(t *testing.T) {
	s, _ := servidorSimples(t, Options{})
	rec := postar(s, "/webhook", []byte("isto nem é json"), map[string]string{infodesk.HeaderEvento: "ping"})
	if rec.Code != http.StatusOK {
		t.Fatalf("ping: esperava 200, veio %d", rec.Code)
	}
	if len(s.jobs) != 0 {
		t.Fatal("ping não enfileira")
	}
}

// organizationId diferente do de GET /me é descartado com 200 (não adianta
// reentregar) e alerta.
func TestOrganizacaoDivergenteDescartaEAlerta(t *testing.T) {
	a := novoAmbiente(t)
	id, seq := a.proximo()
	corpo := evt(id, seq, "conversation.started", t0, map[string]any{"conversation": convJSON("c-5", "EXTERNAL_BOT", t0)})
	a.srv.opt.OrganizationID = "outra-org"
	if rec := a.entregar(corpo, "d"); rec.Code != http.StatusOK {
		t.Fatalf("esperava 200, veio %d", rec.Code)
	}
	if len(a.srv.jobs) != 0 {
		t.Fatal("evento de outra organização não pode ser processado")
	}
	if len(a.alertas.doTipo(alerta.TipoOrganizacaoDivergente)) != 1 {
		t.Fatalf("esperava alerta organizacao_divergente: %+v", a.alertas.alertas)
	}
	if c := a.conversa("c-5"); c != nil {
		t.Fatalf("nada podia ser gravado: %+v", c)
	}
}

// Sem segredo a assinatura não é exigida (dev); token na URL vale sozinho ou
// junto com o HMAC; corpo inválido = 400; acima do teto = 413.
func TestSemSegredoTokenECorpoInvalido(t *testing.T) {
	s, _ := servidorSimples(t, Options{})
	if rec := postar(s, "/webhook", corpoSimples("evt_000000000901", 901), nil); rec.Code != http.StatusOK {
		t.Fatalf("sem segredo: esperava 200, veio %d", rec.Code)
	}
	if rec := postar(s, "/webhook", []byte("não é json"), nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("json inválido: esperava 400, veio %d", rec.Code)
	}
	if rec := postar(s, "/webhook", []byte(`{"id":"","event":""}`), nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("sem id/event: esperava 400, veio %d", rec.Code)
	}
	grande := []byte(`{"id":"g","event":"x","pad":"` + strings.Repeat("x", maxBodyBytes) + `"}`)
	if rec := postar(s, "/webhook", grande, nil); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("corpo acima do teto: esperava 413, veio %d", rec.Code)
	}

	tok, _ := servidorSimples(t, Options{Token: "tok-teste"})
	if rec := postar(tok, "/webhook", corpoSimples("evt_000000000902", 902), nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("sem token: esperava 401, veio %d", rec.Code)
	}
	if rec := postar(tok, "/webhook?token=errado", corpoSimples("evt_000000000903", 903), nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token errado: esperava 401, veio %d", rec.Code)
	}
	if rec := postar(tok, "/webhook?token=tok-teste", corpoSimples("evt_000000000904", 904), nil); rec.Code != http.StatusOK {
		t.Fatalf("token certo: esperava 200, veio %d", rec.Code)
	}
	if rec := postar(tok, "/webhook", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("sem token e sem corpo: %d", rec.Code)
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/webhook", nil)
	rec := httptest.NewRecorder()
	tok.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: esperava 405, veio %d", rec.Code)
	}
}

// Fila cheia: o evento volta com 503 e NÃO fica marcado como visto — a
// reentrega do Infodesk o aceita depois.
func TestFilaCheiaDevolve503ESemMarcarComoVisto(t *testing.T) {
	s, _ := servidorSimples(t, Options{QueueSize: 1})
	if rec := postar(s, "/webhook", corpoSimples("evt_000000000911", 911), nil); rec.Code != http.StatusOK {
		t.Fatalf("1º: %d", rec.Code)
	}
	segundo := corpoSimples("evt_000000000912", 912)
	if rec := postar(s, "/webhook", segundo, nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("fila cheia: esperava 503, veio %d", rec.Code)
	}
	<-s.jobs // libera espaço
	if rec := postar(s, "/webhook", segundo, nil); rec.Code != http.StatusOK || len(s.jobs) != 1 {
		t.Fatalf("a reentrega devia ser aceita: %d, fila=%d", rec.Code, len(s.jobs))
	}
}
