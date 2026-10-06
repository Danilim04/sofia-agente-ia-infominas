package engine

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"bot-azapfy/internal/brain"
	"bot-azapfy/internal/chatwoot"
	"bot-azapfy/internal/config"
	"bot-azapfy/internal/identity"
	"bot-azapfy/internal/mongo"
	"bot-azapfy/internal/store"
)

// ---------------------------------------------------------------------------
// Fakes de Chatwoot e cérebro — a engine só enxerga as interfaces.
// ---------------------------------------------------------------------------

type msgEnviada struct {
	conv    int64
	content string
	private bool
}

type fakeChatwoot struct {
	mu      sync.Mutex
	msgs    []msgEnviada
	labels  map[int64][]string
	status  map[int64]string
	equipes map[int64]int64
}

func novoFakeChatwoot() *fakeChatwoot {
	return &fakeChatwoot{labels: map[int64][]string{}, status: map[int64]string{}, equipes: map[int64]int64{}}
}

func (f *fakeChatwoot) SetLabels(_ context.Context, convID int64, labels []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.labels[convID] = append([]string{}, labels...)
	return nil
}

func (f *fakeChatwoot) SendMessage(_ context.Context, convID int64, content string, private bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, msgEnviada{conv: convID, content: content, private: private})
	return nil
}

func (f *fakeChatwoot) ToggleStatus(_ context.Context, convID int64, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status[convID] = status
	return nil
}

func (f *fakeChatwoot) AssignTeam(_ context.Context, convID int64, teamID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.equipes[convID] = teamID
	return nil
}

// mensagens devolve o conteúdo das mensagens da conversa (públicas ou privadas).
func (f *fakeChatwoot) mensagens(convID int64, privadas bool) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.msgs {
		if m.conv == convID && m.private == privadas {
			out = append(out, m.content)
		}
	}
	return out
}

type fakeBrain struct {
	mu       sync.Mutex
	chamadas int
	// respostas em sequência; a última repete. Vazio = resposta "ok".
	respostas []func() (*brain.ChatResponse, error)
}

func (f *fakeBrain) Chat(_ context.Context, _ brain.ChatRequest) (*brain.ChatResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chamadas++
	if len(f.respostas) == 0 {
		return &brain.ChatResponse{Reply: "ok"}, nil
	}
	i := f.chamadas - 1
	if i >= len(f.respostas) {
		i = len(f.respostas) - 1
	}
	return f.respostas[i]()
}

func (f *fakeBrain) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.chamadas
}

func sempre(err error) func() (*brain.ChatResponse, error) {
	return func() (*brain.ChatResponse, error) { return nil, err }
}

func responde(reply string) func() (*brain.ChatResponse, error) {
	return func() (*brain.ChatResponse, error) { return &brain.ChatResponse{Reply: reply}, nil }
}

// repoVazio: gate sem ninguém no Mongo — a identificação resolve só pelo
// cache telefone→perfil que o teste semeia.
type repoVazio struct{}

func (repoVazio) BuscarPorLogin(context.Context, string) (mongo.UsuarioDoc, bool, error) {
	return mongo.UsuarioDoc{}, false, nil
}

const (
	telefoneIdentificado = "5511999990060"
	perfilIdentificado   = `{"encontrado":true,"login":"10596693664","nome":"Daniel","empresas":[{"grupo_empresa":"AZAPERS"}]}`
)

// engineCompleta monta a engine com Chatwoot e cérebro falsos, gate real (sobre
// SQLite) e um telefone já identificado em cache.
func engineCompleta(t *testing.T) (*Engine, store.Store, *fakeChatwoot, *fakeBrain) {
	t.Helper()
	st, err := store.NewSQLite(filepath.Join(t.TempDir(), "gate.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.PutIdentity(context.Background(), &store.CachedIdentity{
		Phone: telefoneIdentificado, Login: "10596693664", Perfil: perfilIdentificado,
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{LabelBot: "fila-bot", LabelHumano: "fila-humano", TeamHumanoID: 7}
	gate := identity.New(st, repoVazio{}, "email", 3, time.Hour, time.Hour, nil, nil)
	cw, br := novoFakeChatwoot(), &fakeBrain{}
	e := New(cfg, cw, gate, br, st, nil)
	return e, st, cw, br
}

func conversaDoBot(id int64, status string) chatwoot.Conversation {
	return chatwoot.Conversation{ID: id, Labels: []string{"fila-bot"}, Status: status}
}

var reIDOcorrencia = regexp.MustCompile(`ZPF-\d{8}-\d{4}-\d{2}`)

// engineComStore monta uma Engine só com o store (sem Chatwoot/gate/cérebro):
// os handlers de status não falam com ninguém além da persistência.
func engineComStore(t *testing.T) (*Engine, store.Store) {
	t.Helper()
	st, err := store.NewSQLite(filepath.Join(t.TempDir(), "gate.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(&config.Config{}, nil, nil, nil, st, nil), st
}

func mudancaDeStatus(convID int64, de, para string) *chatwoot.ConversationUpdated {
	ev := &chatwoot.ConversationUpdated{
		ChangedAttributes: []map[string]chatwoot.ChangeValue{{
			"status": {CurrentValue: json.RawMessage(`"` + para + `"`), PreviousValue: json.RawMessage(`"` + de + `"`)},
		}},
	}
	ev.ID = convID
	ev.Status = para
	return ev
}

func TestConversaResolvidaApagaEstadoDoGate(t *testing.T) {
	e, st := engineComStore(t)
	ctx := context.Background()
	const conv = int64(41)
	if err := st.SetGate(ctx, &store.GateState{ConversationID: conv, State: store.GateFalha}); err != nil {
		t.Fatal(err)
	}

	e.HandleConversationUpdated(ctx, mudancaDeStatus(conv, chatwoot.StatusOpen, chatwoot.StatusResolved))

	if gs, _ := st.GetGate(ctx, conv); gs != nil {
		t.Fatalf("resolvida: esperava gate apagado, ainda existe %+v", gs)
	}
}

func TestConversaResolvidaNaoMexeEmOutrasConversas(t *testing.T) {
	e, st := engineComStore(t)
	ctx := context.Background()
	_ = st.SetGate(ctx, &store.GateState{ConversationID: 42, State: store.GateIdentificado})
	_ = st.SetGate(ctx, &store.GateState{ConversationID: 43, State: store.GateFalha})

	e.HandleConversationUpdated(ctx, mudancaDeStatus(42, chatwoot.StatusOpen, chatwoot.StatusResolved))

	if gs, _ := st.GetGate(ctx, 43); gs == nil || gs.State != store.GateFalha {
		t.Fatalf("conversa 43 não podia mudar, veio %+v", gs)
	}
}

func TestConversaReabertaOuComEtiquetaNaoApagaGate(t *testing.T) {
	e, st := engineComStore(t)
	ctx := context.Background()
	const conv = int64(44)
	_ = st.SetGate(ctx, &store.GateState{ConversationID: conv, State: store.GateIdentificado, Data: `{"perfil":{}}`})

	// Reabertura (resolved → open) e mudança só de etiquetas: sem ação.
	e.HandleConversationUpdated(ctx, mudancaDeStatus(conv, chatwoot.StatusResolved, chatwoot.StatusOpen))
	soEtiquetas := &chatwoot.ConversationUpdated{
		ChangedAttributes: []map[string]chatwoot.ChangeValue{{
			"labels": {CurrentValue: json.RawMessage(`["fila-bot"]`), PreviousValue: json.RawMessage(`[]`)},
		}},
	}
	soEtiquetas.ID = conv
	soEtiquetas.Status = chatwoot.StatusResolved // status atual, mas não mudou NESTA atualização
	e.HandleConversationUpdated(ctx, soEtiquetas)

	if gs, _ := st.GetGate(ctx, conv); gs == nil || gs.State != store.GateIdentificado {
		t.Fatalf("gate não podia ser apagado, veio %+v", gs)
	}
}

func TestEventoStatusChangedResolvidoApagaGate(t *testing.T) {
	e, st := engineComStore(t)
	ctx := context.Background()
	const conv = int64(45)
	_ = st.SetGate(ctx, &store.GateState{ConversationID: conv, State: store.GateFalha})

	ev := &chatwoot.ConversationStatusChanged{}
	ev.ID = conv
	ev.Status = chatwoot.StatusResolved
	e.HandleConversationStatusChanged(ctx, ev)

	if gs, _ := st.GetGate(ctx, conv); gs != nil {
		t.Fatalf("status_changed resolved: esperava gate apagado, ainda existe %+v", gs)
	}
}

// TestMensagemDeGrupoNaoEntraNaFila: o bot não responde grupo de WhatsApp. A
// mensagem é descartada na borda — nenhum worker de conversa é criado (sem
// gate, sem cérebro, sem resposta).
func TestMensagemDeGrupoNaoEntraNaFila(t *testing.T) {
	e, _ := engineComStore(t)
	ctx := context.Background()

	msg := &chatwoot.MessageCreated{
		MessageType: chatwoot.MessageIncoming,
		Content:     "alguém sabe se o app caiu?",
		SourceID:    "waid-grupo-1",
		Sender:      chatwoot.Sender{Type: "contact", Name: "Fulano", Identifier: "120363041234567890@g.us"},
	}
	msg.Conversation.ID = 50
	msg.Conversation.Meta.Sender = chatwoot.Contact{Name: "Grupo Logística", Identifier: "120363041234567890@g.us"}

	e.HandleMessageCreated(ctx, msg)

	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.filas[50]; ok {
		t.Fatal("mensagem de grupo não pode abrir fila/worker de conversa")
	}
}

// TestConversaDeGrupoNaoEhAdotada: conversation_created de grupo na caixa do
// bot não recebe a etiqueta da fila do bot.
func TestConversaDeGrupoNaoEhAdotada(t *testing.T) {
	e, _ := engineComStore(t)
	e.cfg.LabelBot = "fila-bot"
	ctx := context.Background()

	conv := chatwoot.Conversation{ID: 51}
	conv.Meta.Sender = chatwoot.Contact{Name: "Grupo", PhoneNumber: "+120363041234567890"}
	if e.adotarConversa(ctx, &conv) {
		t.Fatal("conversa de grupo não pode ser adotada pela fila do bot")
	}
}

// TestPayloadRealDeGrupoNaoEntraNaFila repete o incidente de 2026-09-14
// (conversa 5, grupo de teste) com o webhook real: hoje a engine descarta na
// borda, mesmo com a conversa já etiquetada como fila-bot.
func TestPayloadRealDeGrupoNaoEntraNaFila(t *testing.T) {
	e, _ := engineComStore(t)
	e.cfg.LabelBot = "fila-bot"
	ctx := context.Background()

	var msg chatwoot.MessageCreated
	if err := json.Unmarshal([]byte(payloadGrupoReal), &msg); err != nil {
		t.Fatal(err)
	}
	e.HandleMessageCreated(ctx, &msg)

	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.filas[msg.Conversation.ID]; ok {
		t.Fatal("payload real de grupo não pode abrir fila/worker de conversa")
	}
}

// payloadGrupoReal: cópia do fixture de internal/chatwoot (telefone mascarado).
const payloadGrupoReal = `{
 "event": "message_created", "id": 118,
 "content": "**+55 (31) 9999-0000 - Participante Teste:**\n\nteste",
 "message_type": "incoming", "private": false, "source_id": "WAID:3EB03E00F600330A1AADC8",
 "sender": {"id": 3, "identifier": "120363428192696101@g.us", "name": " (GROUP)", "phone_number": null, "additional_attributes": {}},
 "conversation": {
  "id": 5, "inbox_id": 3, "labels": ["fila-bot"], "status": "open", "additional_attributes": {},
  "meta": {"sender": {"id": 3, "identifier": "120363428192696101@g.us", "name": " (GROUP)", "phone_number": null, "type": "contact"}, "assignee": null}
 },
 "account": {"id": 1, "name": "Omni Route"}
}`

// ---------------------------------------------------------------------------
// ZC-3: transferência única e silenciosa
// ---------------------------------------------------------------------------

// TestCerebroFalhandoTransfereSilenciosamente: falha definitiva do cérebro
// (402 do OpenRouter) → mensagem neutra ao cliente, fila-humano, equipe,
// status open, nota privada com o MESMO ID do cérebro e registro em `falhas`.
// Nenhuma mensagem de erro técnico ao cliente.
func TestCerebroFalhandoTransfereSilenciosamente(t *testing.T) {
	e, st, cw, br := engineCompleta(t)
	br.respostas = []func() (*brain.ChatResponse, error){sempre(&brain.Erro{
		Classe: brain.ClasseDefinitiva, IDOcorrencia: "ZPF-20261002-0911-55",
		Causa: "saldo do OpenRouter insuficiente (402)", Status: 500,
	})}
	ctx := context.Background()
	conv := conversaDoBot(60, chatwoot.StatusPending)

	e.processarLote(ctx, []inbound{{conv: conv, phone: telefoneIdentificado, content: "como rastreio a NF?"}})

	publicas := cw.mensagens(60, false)
	if len(publicas) != 1 || publicas[0] != FormatWhatsApp(MsgTransferencia) {
		t.Fatalf("esperava só a mensagem neutra de transferência, veio %q", publicas)
	}
	for _, m := range publicas {
		if strings.Contains(strings.ToLower(m), "problema técnico") || strings.Contains(m, "402") || strings.Contains(m, "ZPF-") {
			t.Fatalf("erro técnico vazou ao cliente: %q", m)
		}
	}
	if labels := cw.labels[60]; len(labels) != 1 || labels[0] != "fila-humano" {
		t.Fatalf("esperava etiqueta fila-humano (sem fila-bot), veio %v", labels)
	}
	if cw.equipes[60] != 7 {
		t.Fatalf("esperava equipe padrão 7, veio %d", cw.equipes[60])
	}
	if cw.status[60] != chatwoot.StatusOpen {
		t.Fatalf("conversa pendente devia voltar a open, veio %q", cw.status[60])
	}
	notas := cw.mensagens(60, true)
	if len(notas) != 1 {
		t.Fatalf("esperava 1 nota privada, veio %d: %q", len(notas), notas)
	}
	for _, quer := range []string{"ZPF-20261002-0911-55", "saldo do OpenRouter insuficiente (402)", "Quando:", "Ocorrência:"} {
		if !strings.Contains(notas[0], quer) {
			t.Fatalf("nota privada sem %q:\n%s", quer, notas[0])
		}
	}
	f, err := st.UltimaFalha(ctx, 60)
	if err != nil || f == nil {
		t.Fatalf("falha não registrada: %+v err=%v", f, err)
	}
	if f.ID != "ZPF-20261002-0911-55" || f.Origem != OrigemCerebro || f.Classe != brain.ClasseDefinitiva {
		t.Fatalf("registro em falhas inesperado: %+v", f)
	}
}

// TestTransferenciaGeraIDQuandoCerebroNaoManda: cérebro caiu sem corpo (sem
// ID) → o gateway gera o ID no mesmo formato e o usa na nota e no registro.
func TestTransferenciaGeraIDQuandoCerebroNaoManda(t *testing.T) {
	e, st, cw, br := engineCompleta(t)
	br.respostas = []func() (*brain.ChatResponse, error){sempre(&brain.Erro{
		Classe: brain.ClasseDefinitiva, Causa: "erro não tratado no cérebro (status 500)", Status: 500,
	})}
	ctx := context.Background()
	e.processarLote(ctx, []inbound{{conv: conversaDoBot(61, chatwoot.StatusOpen), phone: telefoneIdentificado, content: "oi"}})

	notas := cw.mensagens(61, true)
	if len(notas) != 1 {
		t.Fatalf("esperava 1 nota privada, veio %q", notas)
	}
	id := reIDOcorrencia.FindString(notas[0])
	if id == "" {
		t.Fatalf("nota sem ID de ocorrência:\n%s", notas[0])
	}
	if f, _ := st.UltimaFalha(ctx, 61); f == nil || f.ID != id {
		t.Fatalf("registro deve ter o mesmo ID da nota (%s): %+v", id, f)
	}
	// Conversa já aberta: não mexe no status.
	if _, ok := cw.status[61]; ok {
		t.Fatalf("conversa open não devia ter status alterado: %q", cw.status[61])
	}
}

// TestGateRoteiaHumanoPelaTransferenciaUnica: a desistência do gate passa pelo
// mesmo caminho — mensagem do gate (persona) + etiqueta + nota com ID + registro.
func TestGateRoteiaHumanoPelaTransferenciaUnica(t *testing.T) {
	e, st, cw, _ := engineCompleta(t)
	ctx := context.Background()
	conv := conversaDoBot(62, chatwoot.StatusOpen)
	const phone = "5511000000062"
	for _, m := range []string{"oi", "naoexiste", "naoexiste", "naoexiste"} {
		e.processarLote(ctx, []inbound{{conv: conv, phone: phone, content: m}})
	}
	publicas := cw.mensagens(62, false)
	if len(publicas) == 0 || !strings.Contains(publicas[len(publicas)-1], "encaminhar") {
		t.Fatalf("última mensagem devia ser a do gate avisando a transferência: %q", publicas)
	}
	if labels := cw.labels[62]; len(labels) != 1 || labels[0] != "fila-humano" {
		t.Fatalf("esperava fila-humano, veio %v", labels)
	}
	notas := cw.mensagens(62, true)
	if len(notas) != 1 || !reIDOcorrencia.MatchString(notas[0]) || !strings.Contains(notas[0], "identificar") {
		t.Fatalf("nota privada do gate inesperada: %q", notas)
	}
	if f, _ := st.UltimaFalha(ctx, 62); f == nil || f.Origem != OrigemGate || !strings.Contains(f.Causa, "identificar") {
		t.Fatalf("registro do gate inesperado: %+v", f)
	}
}

// TestRespostaNormalNaoTransfere: caminho feliz intacto — resposta do cérebro
// chega ao cliente, sem etiqueta/nota/registro.
func TestRespostaNormalNaoTransfere(t *testing.T) {
	e, st, cw, br := engineCompleta(t)
	br.respostas = []func() (*brain.ChatResponse, error){responde("Boníssimo dia! Posso ajudar?")}
	ctx := context.Background()
	e.processarLote(ctx, []inbound{{conv: conversaDoBot(63, chatwoot.StatusOpen), phone: telefoneIdentificado, content: "oi"}})
	if got := cw.mensagens(63, false); len(got) != 1 || got[0] != "Boníssimo dia! Posso ajudar?" {
		t.Fatalf("resposta do cérebro não chegou: %q", got)
	}
	if _, ok := cw.labels[63]; ok {
		t.Fatal("caminho feliz não mexe em etiqueta")
	}
	if f, _ := st.UltimaFalha(ctx, 63); f != nil {
		t.Fatalf("caminho feliz não registra falha: %+v", f)
	}
}
