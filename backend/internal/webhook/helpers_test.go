package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bot-infominas/internal/alerta"
	"bot-infominas/internal/brain"
	"bot-infominas/internal/config"
	"bot-infominas/internal/engine"
	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
)

// ---------------------------------------------------------------------------
// Fakes: o Infodesk (Transporte) e o cérebro (BrainAPI). A engine é a REAL.
// ---------------------------------------------------------------------------

type handoffFeito struct{ conv, dept, note string }

// fakeInfodesk satisfaz engine.Transporte sem rede e registra o que a engine
// pediu ao Infodesk.
type fakeInfodesk struct {
	mu         sync.Mutex
	msgs       map[string][]string
	handoffs   []handoffFeito
	handoffErr error
	lookups    map[string]infodesk.Lookup
	nLookups   int
}

func novoFakeInfodesk() *fakeInfodesk {
	return &fakeInfodesk{msgs: map[string][]string{}, lookups: map[string]infodesk.Lookup{}}
}

func (f *fakeInfodesk) EnviarMensagem(_ context.Context, convID, texto string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs[convID] = append(f.msgs[convID], texto)
	return nil
}

func (f *fakeInfodesk) Handoff(_ context.Context, convID, deptID, note string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.handoffErr != nil {
		return f.handoffErr
	}
	f.handoffs = append(f.handoffs, handoffFeito{convID, deptID, note})
	return nil
}

func (f *fakeInfodesk) LerConversa(_ context.Context, convID string) (map[string]any, error) {
	return map[string]any{"id": convID}, nil
}

func (f *fakeInfodesk) Lookup(_ context.Context, telefone string) (infodesk.Lookup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nLookups++
	return f.lookups[telefone], nil
}

func (f *fakeInfodesk) mensagens(conv string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.msgs[conv]...)
}

func (f *fakeInfodesk) handoffsDe(conv string) []handoffFeito {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []handoffFeito
	for _, h := range f.handoffs {
		if h.conv == conv {
			out = append(out, h)
		}
	}
	return out
}

func (f *fakeInfodesk) lookupsFeitos() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nLookups
}

type fakeBrain struct {
	mu         sync.Mutex
	pedidos    []brain.ChatRequest
	resposta   func(brain.ChatRequest) (*brain.ChatResponse, error)
	encerrados []string
}

func (b *fakeBrain) Chat(_ context.Context, req brain.ChatRequest) (*brain.ChatResponse, error) {
	b.mu.Lock()
	b.pedidos = append(b.pedidos, req)
	fn := b.resposta
	b.mu.Unlock()
	if fn != nil {
		return fn(req)
	}
	return &brain.ChatResponse{Reply: "Oi! Como posso ajudar?"}, nil
}

func (b *fakeBrain) EncerrarEpisodio(_ context.Context, convID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.encerrados = append(b.encerrados, convID)
	return nil
}

func (b *fakeBrain) total() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pedidos)
}

func (b *fakeBrain) ultimo() brain.ChatRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pedidos) == 0 {
		return brain.ChatRequest{}
	}
	return b.pedidos[len(b.pedidos)-1]
}

func (b *fakeBrain) fechados() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string{}, b.encerrados...)
}

// notificadorFalso captura os alertas da vigia.
type notificadorFalso struct {
	mu      sync.Mutex
	alertas []alerta.Alerta
}

func (n *notificadorFalso) Notificar(_ context.Context, a alerta.Alerta) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.alertas = append(n.alertas, a)
}

func (n *notificadorFalso) doTipo(tipo string) []alerta.Alerta {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []alerta.Alerta
	for _, a := range n.alertas {
		if a.Tipo == tipo {
			out = append(out, a)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Ambiente: servidor de webhook + engine real + fakes + store SQLite
// ---------------------------------------------------------------------------

const (
	segredoTeste    = "whsec_segredo-de-teste"
	orgTeste        = "org-infominas-1"
	nomeChaveTeste  = "Bot de atendimento"
	telefoneTeste   = "+5531990000001"
	deptHumanoTeste = "dept-humano-1"
)

// t0 é o "agora" dos testes: relógio do verificador de assinatura e base dos
// createdAt dos eventos.
var t0 = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

type ambiente struct {
	t       *testing.T
	srv     *Server
	eng     *engine.Engine
	st      store.Store
	idk     *fakeInfodesk
	br      *fakeBrain
	alertas *notificadorFalso
	seq     int64

	mu   sync.Mutex
	hora time.Time // relógio do ambiente: assinatura, engine e vigia
}

func (a *ambiente) now() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hora
}

type opcaoAmb func(*config.Config)

func novoAmbiente(t *testing.T, opcoes ...opcaoAmb) *ambiente {
	t.Helper()
	st, err := store.NewSQLite(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.Config{
		DepartamentoHumanoPadrao: deptHumanoTeste,
		DebounceJanela:           30 * time.Millisecond,
		DebounceTeto:             300 * time.Millisecond,
		TurnoTeto:                5 * time.Second,
		ReplyMaxChars:            900,
		IdentTTL:                 24 * time.Hour,
	}
	for _, o := range opcoes {
		o(cfg)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := &ambiente{t: t, st: st, idk: novoFakeInfodesk(), br: &fakeBrain{}, alertas: &notificadorFalso{}, hora: t0}
	a.eng = engine.New(cfg, a.idk, a.br, st, log)
	a.eng.UsarRelogio(a.now)
	a.eng.UsarVigia(alerta.NovaVigia(a.alertas, a.now))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a.eng.Start(ctx)
	a.srv = NewServer(Options{
		Secret: segredoTeste, Now: a.now,
		OrganizationID: orgTeste, NomeChave: nomeChaveTeste, Workers: 1, QueueSize: 16,
	}, st, a.eng, a.eng.Vigia(), log)
	a.srv.baseCtx = ctx
	return a
}

// ---------------------------------------------------------------------------
// Construção de eventos no formato do Infodesk v1 (docs/infodesk/05)
// ---------------------------------------------------------------------------

func ts(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// convJSON é o objeto `conversation` de um evento, conduzido por handledBy.
func convJSON(id, handledBy string, sessao time.Time) map[string]any {
	return map[string]any{
		"id": id, "channel": "WHATSAPP", "status": "PENDING", "subject": nil,
		"botActive": handledBy == "EXTERNAL_BOT", "handledBy": handledBy,
		"assignedUserId": nil, "departmentId": nil, "clientId": nil, "requesterId": nil, "resourceId": nil, "ticketNumber": nil,
		"contact":  map[string]any{"name": "Maria", "phone": telefoneTeste, "email": nil, "groupJid": nil},
		"provider": "GUPSHUP", "sessionStartedAt": ts(sessao), "createdAt": ts(sessao), "updatedAt": ts(sessao),
	}
}

func msgJSON(id, conv, sender, tipo, corpo string, quando time.Time, anexo bool) map[string]any {
	m := map[string]any{
		"id": id, "conversationId": conv, "sender": sender, "senderName": nil, "type": tipo, "status": nil,
		"body": corpo, "internal": false, "deleted": false, "replyToId": nil, "createdAt": ts(quando), "attachment": nil,
	}
	if anexo {
		m["attachment"] = map[string]any{"url": "https://desk.exemplo/attachments/a1"}
	}
	return m
}

// evt monta o envelope {id, seq, event, createdAt, organizationId, data}.
func evt(id string, seq int64, event string, quando time.Time, data map[string]any) []byte {
	b, err := json.Marshal(map[string]any{
		"id": id, "seq": seq, "event": event, "createdAt": ts(quando), "organizationId": orgTeste, "data": data,
	})
	if err != nil {
		panic(err)
	}
	return b
}

// proximo devolve (id, seq) novos para um evento.
func (a *ambiente) proximo() (string, int64) {
	a.seq++
	return fmt.Sprintf("evt_%012d", a.seq), a.seq
}

// recebida monta uma message.received do cliente na conversa.
func (a *ambiente) recebida(conv, msgID, texto string, quando time.Time) []byte {
	id, seq := a.proximo()
	return evt(id, seq, "message.received", quando, map[string]any{
		"conversation": convJSON(conv, "EXTERNAL_BOT", t0.Add(-time.Hour)),
		"contact":      map[string]any{"name": "Maria", "phone": telefoneTeste},
		"message":      msgJSON(msgID, conv, "CLIENT", "TEXT", texto, quando, false),
	})
}

// recebidaDe monta a message.received com remetente/handledBy/tipo à escolha.
func (a *ambiente) recebidaDe(conv, msgID, texto, handledBy, tipo string, anexo bool, quando time.Time) []byte {
	id, seq := a.proximo()
	return evt(id, seq, "message.received", quando, map[string]any{
		"conversation": convJSON(conv, handledBy, t0.Add(-time.Hour)),
		"contact":      map[string]any{"name": "Maria", "phone": telefoneTeste},
		"message":      msgJSON(msgID, conv, "CLIENT", tipo, texto, quando, anexo),
	})
}

// deConversa monta um evento de conversa (assigned, handoff, finished…).
func (a *ambiente) deConversa(event, conv, handledBy string, quando time.Time) []byte {
	id, seq := a.proximo()
	return evt(id, seq, event, quando, map[string]any{
		"conversation": convJSON(conv, handledBy, t0.Add(-time.Hour)),
		"contact":      map[string]any{"name": "Maria", "phone": telefoneTeste},
	})
}

// ---------------------------------------------------------------------------
// Entrega HTTP assinada e execução dos jobs
// ---------------------------------------------------------------------------

// entregar faz o POST assinado (timestamp = agora do ambiente) e devolve a resposta.
func (a *ambiente) entregar(corpo []byte, entrega string) *httptest.ResponseRecorder {
	return a.entregarComTimestamp(corpo, entrega, a.now())
}

func (a *ambiente) entregarComTimestamp(corpo []byte, entrega string, quando time.Time) *httptest.ResponseRecorder {
	stamp := fmt.Sprint(quando.Unix())
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/webhook", strings.NewReader(string(corpo)))
	req.Header.Set(infodesk.HeaderTimestamp, stamp)
	req.Header.Set(infodesk.HeaderAssinatura, infodesk.Assinar(segredoTeste, stamp, corpo))
	req.Header.Set(infodesk.HeaderEntrega, entrega)
	rec := httptest.NewRecorder()
	a.srv.ServeHTTP(rec, req)
	return rec
}

// drenar processa, no teste, todos os jobs que a borda enfileirou.
func (a *ambiente) drenar() {
	for {
		select {
		case j := <-a.srv.jobs:
			a.srv.processarJob(context.Background(), j)
		default:
			return
		}
	}
}

// enviar entrega o evento (200 esperado) e o processa.
func (a *ambiente) enviar(corpo []byte) {
	a.t.Helper()
	if rec := a.entregar(corpo, "ent-"+fmt.Sprint(time.Now().UnixNano())); rec.Code != http.StatusOK {
		a.t.Fatalf("entrega: esperava 200, veio %d (%s)", rec.Code, rec.Body.String())
	}
	a.drenar()
}

// esperar aguarda (até 3 s) a condição — o turno roda no worker da conversa.
func esperar(t *testing.T, descricao string, cond func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("tempo esgotado esperando: %s", descricao)
}

// silencio garante que a condição NÃO passa a valer: espera um pouco (a
// debounce é de 30 ms) e então confere.
func silencio() { time.Sleep(200 * time.Millisecond) }

func (a *ambiente) conversa(id string) *store.Conversa {
	a.t.Helper()
	c, err := a.st.ObterConversa(context.Background(), id)
	if err != nil {
		a.t.Fatalf("ler conversa: %v", err)
	}
	return c
}
