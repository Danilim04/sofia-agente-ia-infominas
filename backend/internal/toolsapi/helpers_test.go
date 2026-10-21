package toolsapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bot-infominas/internal/engine"
	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
	"bot-infominas/internal/taxonomia"
)

const (
	tokenTeste = "segredo-tools" //nolint:gosec // G101: valor de teste
	convTeste  = "6f1c2a3e-9b4d-4e7a-8c21-5d3f0a9b7e10"
	telTeste   = "+5531999990001"

	idMesaSuporte    = "9c1f4e2a-7b3d-4a8e-9f10-2d5c6b7a8e01"
	idMesaFinanceiro = "7a2d9b3c-5e4f-4c1a-8b60-1f2e3d4c5b02"
	idMesaTriagem    = "3b8c1d5e-2f7a-4e9c-a410-6d7e8f9a0b03"
	idDeptSuporte    = "4d7e2b9f-1a6c-4f3b-8e57-0c9a1b2d3e02"
	idDeptFinanceiro = "5e8f3c0a-2b7d-4a4c-9f68-1d0b2c3e4f04"
	idDeptAtend      = "6f9a4d1b-3c8e-4b5d-a079-2e1c3d4f5a05"
	idPrioAlta       = "11111111-aaaa-4bbb-8ccc-000000000003"
)

// taxonomiaTeste é o config/taxonomia.yaml dos testes: nomes que o fakeIdk conhece.
const taxonomiaTeste = `
versao: "2026-10-21"
mesa_triagem: "Triagem"
departamento_humano_padrao: "Atendimento"
categorias:
  sem_conexao:
    mesa: "Suporte Técnico"
    catalogo: "Serviços de Suporte"
    departamento: "Suporte"
    prioridade_minima: alta
  lentidao:
    mesa: "Suporte Técnico"
    departamento: "Suporte"
  cobranca:
    mesa: "Financeiro"
    departamento: "Financeiro"
  indefinida:
    mesa: "Triagem"
    departamento: "Atendimento"
prioridades:
  baixa: "Baixa"
  media: "Média"
  alta: "Alta"
  urgente: "Urgente"
`

// fakeIdk é o Infodesk falso: serve de InfodeskAPI e de taxonomia.Fonte.
type fakeIdk struct {
	mu sync.Mutex

	mesas     []infodesk.Referencia
	mesaPorID map[string]infodesk.Mesa
	depts     []infodesk.Referencia
	errRef    error // erro de Mesas/Departamentos

	postErr  error            // erro do POST do ticket
	postResp *infodesk.Ticket // resposta do POST quando não há erro
	posts    []infodesk.NovoTicketDaConversa
	postConv []string
	ordem    []string // "vincular" / "post"

	listados  []infodesk.Ticket
	listErr   error
	listCalls int

	tickets   map[int]infodesk.Ticket
	ticketErr error

	vinculos      []string
	vincularErr   error
	invalidacoes  int
	recargasMesas int
}

func ref(id, nome string) infodesk.Referencia { return infodesk.Referencia{ID: id, Nome: nome} }

func novoFakeIdk() *fakeIdk {
	prios := func(nomes ...string) []infodesk.Referencia {
		var out []infodesk.Referencia
		ids := map[string]string{"Baixa": "11111111-aaaa-4bbb-8ccc-000000000001", "Média": "11111111-aaaa-4bbb-8ccc-000000000002", "Alta": idPrioAlta, "Urgente": "11111111-aaaa-4bbb-8ccc-000000000004"}
		for _, n := range nomes {
			out = append(out, ref(ids[n], n))
		}
		return out
	}
	return &fakeIdk{
		mesas: []infodesk.Referencia{ref(idMesaSuporte, "Suporte Técnico"), ref(idMesaFinanceiro, "Financeiro"), ref(idMesaTriagem, "Triagem")},
		mesaPorID: map[string]infodesk.Mesa{
			idMesaSuporte: {
				ID: idMesaSuporte, Nome: "Suporte Técnico", Priorities: prios("Baixa", "Média", "Alta", "Urgente"),
				CatalogItems: []infodesk.Referencia{ref("22222222-aaaa-4bbb-8ccc-000000000001", "Serviços de Suporte")},
			},
			// Financeiro NÃO tem "Alta" nem "Urgente": o priorityId some e vira alerta.
			idMesaFinanceiro: {ID: idMesaFinanceiro, Nome: "Financeiro", Priorities: prios("Baixa", "Média")},
			idMesaTriagem:    {ID: idMesaTriagem, Nome: "Triagem", Priorities: prios("Baixa", "Média", "Alta", "Urgente")},
		},
		depts:   []infodesk.Referencia{ref(idDeptSuporte, "Suporte"), ref(idDeptFinanceiro, "Financeiro"), ref(idDeptAtend, "Atendimento")},
		tickets: map[int]infodesk.Ticket{},
	}
}

func (f *fakeIdk) Mesas(context.Context) ([]infodesk.Referencia, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recargasMesas++
	return f.mesas, f.errRef
}

func (f *fakeIdk) Mesa(_ context.Context, id string) (infodesk.Mesa, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mesaPorID[id], f.errRef
}

func (f *fakeIdk) Catalogo(context.Context, string) ([]infodesk.Referencia, error) { return nil, nil }

func (f *fakeIdk) Departamentos(context.Context) ([]infodesk.Referencia, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.depts, f.errRef
}

func (f *fakeIdk) InvalidarCache() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalidacoes++
}

func (f *fakeIdk) CriarTicketDaConversa(_ context.Context, convID string, t infodesk.NovoTicketDaConversa) (infodesk.Ticket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts = append(f.posts, t)
	f.postConv = append(f.postConv, convID)
	f.ordem = append(f.ordem, "post")
	if f.postErr != nil {
		return infodesk.Ticket{}, f.postErr
	}
	if f.postResp != nil {
		return *f.postResp, nil
	}
	return ticketDoInfodesk(48213, convID, t.Title), nil
}

func (f *fakeIdk) ListarTickets(_ context.Context, fl infodesk.FiltroTickets) (infodesk.Pagina[infodesk.Ticket], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	if f.listErr != nil {
		return infodesk.Pagina[infodesk.Ticket]{}, f.listErr
	}
	return infodesk.Pagina[infodesk.Ticket]{Data: append([]infodesk.Ticket(nil), f.listados...)}, nil
}

func (f *fakeIdk) Ticket(_ context.Context, n int) (infodesk.Ticket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ticketErr != nil {
		return infodesk.Ticket{}, f.ticketErr
	}
	tk, ok := f.tickets[n]
	if !ok {
		return infodesk.Ticket{}, &infodesk.HTTPError{Status: 404, Code: infodesk.CodeNotFound, Rota: "/tickets/n"}
	}
	return tk, nil
}

func (f *fakeIdk) VincularSolicitante(_ context.Context, convID, requesterID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ordem = append(f.ordem, "vincular")
	f.vinculos = append(f.vinculos, convID+"|"+requesterID)
	return f.vincularErr
}

func (f *fakeIdk) totalPosts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.posts)
}

// ticketDoInfodesk monta um ticket como o Infodesk devolve (via UnmarshalJSON real).
func ticketDoInfodesk(numero int, convID, titulo string) infodesk.Ticket {
	return ticketJSON(map[string]any{
		"number": numero, "title": titulo, "conversationId": convID,
		"createdAt": time.Now().UTC().Format(time.RFC3339),
		"updatedAt": time.Now().UTC().Format(time.RFC3339),
		"status":    map[string]any{"id": "s1", "name": "Aberto", "isOpen": true},
		"desk":      map[string]any{"id": idMesaSuporte, "name": "Suporte Técnico"},
	})
}

func ticketJSON(m map[string]any) infodesk.Ticket {
	b, _ := json.Marshal(m)
	var tk infodesk.Ticket
	if err := json.Unmarshal(b, &tk); err != nil {
		panic(err)
	}
	return tk
}

// fakeTransf registra as transferências pedidas (no lugar da engine).
type fakeTransf struct {
	mu       sync.Mutex
	chamadas []engine.Transferencia
	convs    []string
	ja       bool // simula "a conversa já não era do bot"
}

func (f *fakeTransf) TransferirParaHumano(_ context.Context, conv string, t engine.Transferencia) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chamadas = append(f.chamadas, t)
	f.convs = append(f.convs, conv)
	if f.ja {
		return ""
	}
	return "SOF-20261021-1000-01"
}

func (f *fakeTransf) origens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var o []string
	for _, c := range f.chamadas {
		o = append(o, c.Origem)
	}
	return o
}

// fakeAv registra os alertas.
type fakeAv struct {
	mu     sync.Mutex
	tipos  []string
	textos []string
}

func (f *fakeAv) Aviso(tipo, _, texto, _ string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tipos = append(f.tipos, tipo)
	f.textos = append(f.textos, texto)
	return true
}

func (f *fakeAv) teve(tipo string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.tipos {
		if t == tipo {
			return true
		}
	}
	return false
}

// amb é o ambiente completo de um teste: store SQLite real em t.TempDir(),
// Infodesk falso, taxonomia real sobre o falso, transferidor e alertas gravando.
type amb struct {
	t    *testing.T
	st   store.Store
	idk  *fakeIdk
	tx   *taxonomia.Taxonomia
	tr   *fakeTransf
	av   *fakeAv
	h    *Handler
	logs *bytes.Buffer
}

func novoAmb(t *testing.T, mods ...func(*Deps)) *amb {
	t.Helper()
	st, err := store.NewSQLite(filepath.Join(t.TempDir(), "gw.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	logs := &bytes.Buffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	idk := novoFakeIdk()
	def, err := taxonomia.Parse([]byte(taxonomiaTeste))
	if err != nil {
		t.Fatalf("taxonomia: %v", err)
	}
	tx := taxonomia.Nova(def, idk, taxonomia.Opcoes{Log: log})
	if _, err := tx.Carregar(context.Background()); err != nil {
		t.Fatalf("carregar taxonomia: %v", err)
	}
	a := &amb{t: t, st: st, idk: idk, tx: tx, tr: &fakeTransf{}, av: &fakeAv{}, logs: logs}
	d := Deps{
		Token: tokenTeste, Log: log, Store: st, Infodesk: idk, Taxonomia: tx, Transferidor: a.tr, Alertas: a.av,
		Opcoes: Opcoes{DepartamentoHumanoPadrao: idDeptAtend},
	}
	for _, m := range mods {
		m(&d)
	}
	a.h = New(d)
	return a
}

// semeia a conversa (bot conduz) e, opcionalmente, o contato identificado.
func (a *amb) conversa(conv, tel string) {
	a.t.Helper()
	_, err := a.st.AtualizarConversa(context.Background(), conv, func(c *store.Conversa, _ bool) bool {
		c.Telefone, c.Estado, c.HandledBy = tel, store.EstadoBot, infodesk.ConduzidaPorBotExterno
		c.UpdatedAt = time.Now()
		return true
	})
	if err != nil {
		a.t.Fatalf("semear conversa: %v", err)
	}
}

func (a *amb) contato(c store.Contato) {
	a.t.Helper()
	if c.ExpiresAt.IsZero() {
		c.ExpiresAt = time.Now().Add(time.Hour)
	}
	if err := a.st.SalvarContato(context.Background(), &c); err != nil {
		a.t.Fatalf("semear contato: %v", err)
	}
}

// identificadoPorCPF semeia a identificação que o contrato usa (***.***.789-**).
func (a *amb) identificadoPorCPF() {
	a.contato(store.Contato{Telefone: telTeste, Origem: store.OrigemCPF, Forma: store.FormaCPF, ValorMascarado: "***.***.789-**", ValorHash: "h"})
}

// post chama a rota com token e devolve (HTTP, corpo decodificado).
func (a *amb) post(path string, body any) (int, map[string]any) {
	a.t.Helper()
	var raw []byte
	switch v := body.(type) {
	case string:
		raw = []byte(v)
	default:
		raw, _ = json.Marshal(v)
	}
	mux := http.NewServeMux()
	a.h.Register(mux)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/tools/infodesk/"+path, strings.NewReader(string(raw)))
	req.Header.Set("X-Tools-Token", tokenTeste)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// propostaTeste é a proposta carimbada do contrato, com a chave real.
func propostaTeste(categoria string) (map[string]any, string) {
	const titulo, criada = "Internet sem conexão desde ontem à noite", "2026-10-20T14:32:05-03:00"
	p := map[string]any{
		"titulo": titulo, "descricao": "Cliente relata que a internet está sem conexão desde a noite de 19/10.",
		"categoria": categoria, "prioridade": map[string]any{"nome": "alta", "id": nil}, "criada_em": criada,
	}
	return p, chaveIdempotencia(convTeste, titulo, categoria, criada)
}

func (a *amb) abrir(categoria string) (map[string]any, string) {
	a.t.Helper()
	p, chave := propostaTeste(categoria)
	_, out := a.post("ticket/abrir", map[string]any{
		"telefone": telTeste, "conversation_id": convTeste, "proposta": p, "chave_idempotencia": chave,
	})
	return out, chave
}

func (a *amb) log() string { return a.logs.String() }

// novaTaxonomiaSemCarga é uma taxonomia que ainda não carregou (boot com o
// Infodesk fora do ar): Resolver tenta carregar sob demanda.
func novaTaxonomiaSemCarga(t *testing.T, idk *fakeIdk) *taxonomia.Taxonomia {
	t.Helper()
	def, err := taxonomia.Parse([]byte(taxonomiaTeste))
	if err != nil {
		t.Fatal(err)
	}
	return taxonomia.Nova(def, idk, taxonomia.Opcoes{})
}
