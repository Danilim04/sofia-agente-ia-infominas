package reconciliador

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"bot-infominas/internal/alerta"
	"bot-infominas/internal/engine"
	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
	"bot-infominas/internal/webhook"
)

// agora é o relógio fixo dos testes.
var agora = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

const telefone = "+5531990000001"

// ---------------------------------------------------------------------------
// Eventos no formato do Infodesk v1 (docs/infodesk/05)
// ---------------------------------------------------------------------------

func ts(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func conversaJSON(id, handledBy string) map[string]any {
	return map[string]any{
		"id": id, "channel": "WHATSAPP", "status": "PENDING", "botActive": handledBy == "EXTERNAL_BOT",
		"handledBy": handledBy, "ticketNumber": nil,
		"contact":          map[string]any{"name": "Maria", "phone": telefone, "email": nil, "groupJid": nil},
		"sessionStartedAt": ts(agora.Add(-2 * time.Hour)),
	}
}

// eventoMsg monta uma message.received do cliente.
func eventoMsg(t *testing.T, seq int64, conv, texto string, quando time.Time) infodesk.Evento {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"id": "evt_" + texto, "seq": seq, "event": "message.received", "createdAt": ts(quando), "organizationId": "org-1",
		"data": map[string]any{
			"conversation": conversaJSON(conv, "EXTERNAL_BOT"),
			"message": map[string]any{
				"id": "msg_" + texto, "conversationId": conv, "sender": "CLIENT", "type": "TEXT", "body": texto,
				"internal": false, "createdAt": ts(quando),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := infodesk.DecodificarEvento(raw)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

// ---------------------------------------------------------------------------
// Fake do Infodesk: feed paginado, tickets e conversas
// ---------------------------------------------------------------------------

type fakeInfodesk struct {
	mu sync.Mutex

	eventos     []infodesk.Evento // ordenados por seq
	errosFeed   []error           // consumidos um por chamada a Eventos (nil = responde)
	afters      []int64           // `after` de cada chamada a Eventos
	chamadasFd  int
	tickets     map[string][]infodesk.Ticket // por telefone
	errTickets  error
	listagens   int
	conversas   map[string]infodesk.Conversa
	errConversa error
}

func novoFake() *fakeInfodesk {
	return &fakeInfodesk{tickets: map[string][]infodesk.Ticket{}, conversas: map[string]infodesk.Conversa{}}
}

func (f *fakeInfodesk) Eventos(_ context.Context, after int64, limit int) (infodesk.FeedEventos, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.afters = append(f.afters, after)
	f.chamadasFd++
	if len(f.errosFeed) > 0 {
		err := f.errosFeed[0]
		f.errosFeed = f.errosFeed[1:]
		if err != nil {
			return infodesk.FeedEventos{}, err
		}
	}
	var pag []infodesk.Evento
	for _, ev := range f.eventos {
		if ev.Seq > after {
			pag = append(pag, ev)
		}
	}
	out := infodesk.FeedEventos{NextAfter: after}
	if len(pag) > limit {
		pag, out.HasMore = pag[:limit], true
	}
	if len(pag) > 0 {
		out.NextAfter = pag[len(pag)-1].Seq
	}
	out.Data = pag
	return out, nil
}

func (f *fakeInfodesk) ListarTickets(_ context.Context, fl infodesk.FiltroTickets) (infodesk.Pagina[infodesk.Ticket], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listagens++
	if f.errTickets != nil {
		return infodesk.Pagina[infodesk.Ticket]{}, f.errTickets
	}
	return infodesk.Pagina[infodesk.Ticket]{Data: f.tickets[fl.Phone]}, nil
}

func (f *fakeInfodesk) Conversa(_ context.Context, id string) (infodesk.Conversa, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errConversa != nil {
		return infodesk.Conversa{}, f.errConversa
	}
	c, ok := f.conversas[id]
	if !ok {
		c = infodesk.Conversa{ID: id, HandledBy: infodesk.ConduzidaPorBotExterno}
	}
	return c, nil
}

func (f *fakeInfodesk) chamadasAoFeed() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64{}, f.afters...)
}

// ---------------------------------------------------------------------------
// Fake da engine: satisfaz webhook.Engine (o handler REAL do webhook roda por
// cima) e o Transferidor do vigia.
// ---------------------------------------------------------------------------

type fakeEngine struct {
	mu         sync.Mutex
	st         store.Store
	turnos     []engine.Inbound
	transfer   []string
	pedidos    []engine.Transferencia
	transferID string
	errReenvio error    // erro de ReenviarHandoff (nil = confirma)
	reenvios   []string // conversas cujo handoff foi repetido
}

func (e *fakeEngine) HandleMensagem(_ context.Context, m engine.Inbound) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.turnos = append(e.turnos, m)
}

func (e *fakeEngine) EncerrarEpisodio(context.Context, string) {}

func (e *fakeEngine) AplicarConversa(_ context.Context, v engine.ConversaVista) (engine.Resultado, error) {
	return engine.Resultado{Estado: store.EstadoBot, Aplicado: true}, nil
}

// TransferirParaHumano imita a engine: tira a conversa do estado bot.
func (e *fakeEngine) TransferirParaHumano(ctx context.Context, convID string, t engine.Transferencia) string {
	e.mu.Lock()
	e.pedidos = append(e.pedidos, t)
	e.transfer = append(e.transfer, convID)
	id := e.transferID
	e.mu.Unlock()
	_, _ = e.st.AtualizarConversa(ctx, convID, func(c *store.Conversa, _ bool) bool {
		c.Estado = store.EstadoTransferida
		return true
	})
	return id
}

func (e *fakeEngine) textos() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, m := range e.turnos {
		out = append(out, m.Texto)
	}
	return out
}

func (e *fakeEngine) transferidas() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string{}, e.transfer...)
}

// ---------------------------------------------------------------------------
// Ambiente: store SQLite + webhook.Server real + fakes
// ---------------------------------------------------------------------------

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

type ambiente struct {
	t       *testing.T
	st      store.Store
	idk     *fakeInfodesk
	eng     *fakeEngine
	srv     *webhook.Server
	rec     *Reconciliador
	alertas *notificadorFalso
	cancel  context.CancelFunc
}

// opcoesDeTeste: relógio fixo e Atraso desprezível (só os testes do Atraso o
// aumentam).
func opcoesDeTeste() Options {
	return Options{
		Ativo: true, Intervalo: time.Minute, Retroativo: time.Hour, Atraso: -1,
		TicketPendenteTTL: 10 * time.Minute, BotTimeoutHandoff: 5 * time.Minute, BackoffMax: 5 * time.Minute,
		Limite: 500, Now: func() time.Time { return agora },
		Dormir: func(context.Context, time.Duration) bool { return false },
	}
}

func novoStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.NewSQLite(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// novoAmbiente monta o ambiente com o store dado (nil = novo). workers=true
// sobe os workers do webhook; queue é o tamanho da fila em memória.
func novoAmbiente(t *testing.T, st store.Store, o Options, workers bool, queue int) *ambiente {
	t.Helper()
	if st == nil {
		st = novoStore(t)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := &ambiente{t: t, st: st, idk: novoFake(), alertas: &notificadorFalso{}}
	a.eng = &fakeEngine{st: st, transferID: "SOF-20261021-1200-01"}
	vigia := alerta.NovaVigia(a.alertas, func() time.Time { return agora })
	a.srv = webhook.NewServer(webhook.Options{Workers: 1, QueueSize: queue}, st, a.eng, vigia, log)
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	t.Cleanup(cancel)
	if workers {
		go a.srv.Start(ctx)
	}
	a.rec = New(o, a.idk, a.srv, a.eng, st, vigia, log)
	return a
}

// espera consulta cond até 3 s.
func espera(t *testing.T, descricao string, cond func() bool) {
	t.Helper()
	limite := time.Now().Add(3 * time.Second)
	for time.Now().Before(limite) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout esperando: %s", descricao)
}

func cursor(t *testing.T, st store.Store, nome string) int64 {
	t.Helper()
	seq, err := st.Cursor(context.Background(), nome)
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

// conversaDoBot grava uma conversa conduzida pela Sofia com a última mensagem
// do cliente em `msgEm` (e, se respEm não for zero, resposta nossa).
func conversaDoBot(t *testing.T, st store.Store, id string, msgEm, respEm time.Time) {
	t.Helper()
	_, err := st.AtualizarConversa(context.Background(), id, func(c *store.Conversa, _ bool) bool {
		c.Telefone, c.Estado, c.HandledBy = telefone, store.EstadoBot, store.HandledByBotExterno
		c.UltimaMsgID, c.UltimaMsgEm, c.UltimaRespostaEm = "msg-"+id, msgEm, respEm
		c.UpdatedAt = msgEm
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
}

// ReenviarHandoff imita a engine: registra a chamada e, se confirmou, apaga a marca.
func (e *fakeEngine) ReenviarHandoff(ctx context.Context, h store.HandoffPendente) error {
	e.mu.Lock()
	e.reenvios = append(e.reenvios, h.ConversationID)
	err := e.errReenvio
	e.mu.Unlock()
	if err != nil {
		return err
	}
	_, _ = e.st.LimparHandoffPendente(ctx, h.ConversationID)
	return nil
}

func (e *fakeEngine) reenviadas() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string{}, e.reenvios...)
}
