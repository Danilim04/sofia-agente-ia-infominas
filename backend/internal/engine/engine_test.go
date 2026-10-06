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

	"bot-azapfy/internal/alerta"
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
	typing  map[int64]int // vezes que "digitando" foi ligado
	// estado "ao vivo" devolvido por GetConversation; sem entrada → conversa
	// na fila do bot. getErr simula Chatwoot fora.
	aoVivo map[int64]*chatwoot.Conversation
	getErr error
	gets   int
}

func novoFakeChatwoot() *fakeChatwoot {
	return &fakeChatwoot{
		labels: map[int64][]string{}, status: map[int64]string{}, equipes: map[int64]int64{},
		typing: map[int64]int{}, aoVivo: map[int64]*chatwoot.Conversation{},
	}
}

func (f *fakeChatwoot) GetConversation(_ context.Context, convID int64) (*chatwoot.Conversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	if f.getErr != nil {
		return nil, f.getErr
	}
	if c, ok := f.aoVivo[convID]; ok {
		return c, nil
	}
	c := conversaDoBot(convID, chatwoot.StatusOpen)
	return &c, nil
}

func (f *fakeChatwoot) ToggleTyping(_ context.Context, convID int64, on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if on {
		f.typing[convID]++
	}
	return nil
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

// relogioFalso injeta tempo e espera na engine: dormir avança o relógio em vez
// de dormir de verdade (teste não gasta segundos reais).
type relogioFalso struct {
	mu      sync.Mutex
	agora   time.Time
	esperas []time.Duration
}

func (r *relogioFalso) now() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.agora
}

func (r *relogioFalso) avancar(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agora = r.agora.Add(d)
}

func (r *relogioFalso) dormir(_ context.Context, d time.Duration) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.esperas = append(r.esperas, d)
	r.agora = r.agora.Add(d)
	return true
}

func comRelogioFalso(e *Engine) *relogioFalso {
	r := &relogioFalso{agora: time.Date(2026, 10, 2, 9, 11, 0, 0, time.UTC)}
	e.agora, e.dormir = r.now, r.dormir
	if e.vigia != nil {
		e.vigia.Agora = r.now
	}
	return r
}

// notificadorFalso captura os alertas da vigia (ZC-7/ZC-8).
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

func comNotificadorFalso(e *Engine) *notificadorFalso {
	n := &notificadorFalso{}
	e.ligarVigia(alerta.NovaVigia(n, e.agora))
	return n
}

func erroTransitorio() *brain.Erro {
	return &brain.Erro{Classe: brain.ClasseTransitoria, Causa: "cérebro não respondeu a tempo (timeout)"}
}

func erro402() *brain.Erro {
	return &brain.Erro{Classe: brain.ClasseDefinitiva, Causa: "saldo do OpenRouter insuficiente (402)", Status: 500}
}

// turno roda um lote de uma mensagem do telefone identificado na conversa.
func turno(e *Engine, conv int64, recebido time.Time) {
	e.processarLote(context.Background(), []inbound{{
		conv: conversaDoBot(conv, chatwoot.StatusOpen), phone: telefoneIdentificado,
		content: "oi", recebidoEm: recebido,
	}})
}

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

// ---------------------------------------------------------------------------
// ZC-5: nova tentativa só para falha transitória + teto do turno + disjuntor
// ---------------------------------------------------------------------------

// TestTransitoriaRepeteEDepoisTransfere: 3 chamadas (1 + 2 novas tentativas,
// esperas de 2 s e 5 s com "digitando"), então transferência silenciosa.
func TestTransitoriaRepeteEDepoisTransfere(t *testing.T) {
	e, st, cw, br := engineCompleta(t)
	rel := comRelogioFalso(e)
	br.respostas = []func() (*brain.ChatResponse, error){sempre(erroTransitorio())}

	turno(e, 70, rel.now())

	if br.total() != 3 {
		t.Fatalf("esperava 3 chamadas ao cérebro (1 + 2 novas tentativas), veio %d", br.total())
	}
	if len(rel.esperas) != 2 || rel.esperas[0] != 2*time.Second || rel.esperas[1] != 5*time.Second {
		t.Fatalf("esperas: queria [2s 5s], veio %v", rel.esperas)
	}
	if cw.typing[70] != 2 {
		t.Fatalf("esperava 'digitando' ligado antes de cada nova tentativa (2), veio %d", cw.typing[70])
	}
	if got := cw.mensagens(70, false); len(got) != 1 || got[0] != FormatWhatsApp(MsgTransferencia) {
		t.Fatalf("esperava só a mensagem neutra, veio %q", got)
	}
	f, _ := st.UltimaFalha(context.Background(), 70)
	if f == nil || f.Classe != brain.ClasseTransitoria || !strings.Contains(f.Causa, "3 tentativas") {
		t.Fatalf("registro da transferência inesperado: %+v", f)
	}
}

// TestTransitoriaRecuperaNaNovaTentativa: a 2ª tentativa dá certo → resposta
// entregue, sem transferência.
func TestTransitoriaRecuperaNaNovaTentativa(t *testing.T) {
	e, st, cw, br := engineCompleta(t)
	comRelogioFalso(e)
	br.respostas = []func() (*brain.ChatResponse, error){sempre(erroTransitorio()), responde("tudo certo!")}

	turno(e, 71, e.agora())

	if br.total() != 2 {
		t.Fatalf("esperava 2 chamadas, veio %d", br.total())
	}
	if got := cw.mensagens(71, false); len(got) != 1 || got[0] != "tudo certo!" {
		t.Fatalf("resposta da nova tentativa não chegou: %q", got)
	}
	if f, _ := st.UltimaFalha(context.Background(), 71); f != nil {
		t.Fatalf("recuperou: não devia registrar falha: %+v", f)
	}
}

// TestDefinitivaNaoRepete: 402 → uma única chamada e transferência.
func TestDefinitivaNaoRepete(t *testing.T) {
	e, _, cw, br := engineCompleta(t)
	rel := comRelogioFalso(e)
	br.respostas = []func() (*brain.ChatResponse, error){sempre(erro402())}

	turno(e, 72, rel.now())

	if br.total() != 1 || len(rel.esperas) != 0 || cw.typing[72] != 0 {
		t.Fatalf("definitiva não repete: chamadas=%d esperas=%v typing=%d", br.total(), rel.esperas, cw.typing[72])
	}
	if labels := cw.labels[72]; len(labels) != 1 || labels[0] != "fila-humano" {
		t.Fatalf("esperava fila-humano, veio %v", labels)
	}
}

// TestTetoDoTurnoImpedeNovaTentativa: mensagem recebida há 44 s (rajada +
// cérebro lento) → sem tempo para esperar 2 s dentro do teto de 45 s: não
// repete, transfere.
func TestTetoDoTurnoImpedeNovaTentativa(t *testing.T) {
	e, st, _, br := engineCompleta(t)
	rel := comRelogioFalso(e)
	br.respostas = []func() (*brain.ChatResponse, error){sempre(erroTransitorio())}

	turno(e, 73, rel.now().Add(-44*time.Second))

	if br.total() != 1 || len(rel.esperas) != 0 {
		t.Fatalf("fora do teto não repete: chamadas=%d esperas=%v", br.total(), rel.esperas)
	}
	if f, _ := st.UltimaFalha(context.Background(), 73); f == nil || !strings.Contains(f.Causa, "teto") {
		t.Fatalf("causa devia citar o teto: %+v", f)
	}
}

// TestDisjuntorAbreNaTerceiraEFechaAposTeste: 3 falhas definitivas iguais em
// 5 min abrem o disjuntor (10 min sem chamar o cérebro, transferência direta);
// vencido o prazo, UMA chamada de teste bem-sucedida fecha.
func TestDisjuntorAbreNaTerceiraEFechaAposTeste(t *testing.T) {
	e, st, cw, br := engineCompleta(t)
	rel := comRelogioFalso(e)
	br.respostas = []func() (*brain.ChatResponse, error){sempre(erro402())}

	for conv := int64(80); conv < 83; conv++ {
		turno(e, conv, rel.now())
		rel.avancar(30 * time.Second)
	}
	if br.total() != 3 {
		t.Fatalf("3 falhas → 3 chamadas, veio %d", br.total())
	}
	if aberto, _ := e.disjuntor.estado(); !aberto {
		t.Fatal("3ª falha definitiva igual devia abrir o disjuntor")
	}

	// Aberto: a 4ª conversa nem chama o cérebro e é transferida direto.
	turno(e, 83, rel.now())
	if br.total() != 3 {
		t.Fatalf("disjuntor aberto não pode chamar o cérebro; chamadas=%d", br.total())
	}
	f, _ := st.UltimaFalha(context.Background(), 83)
	if f == nil || f.Origem != OrigemDisjuntor || !strings.Contains(f.Causa, "402") {
		t.Fatalf("transferência pelo disjuntor inesperada: %+v", f)
	}
	if got := cw.mensagens(83, false); len(got) != 1 || got[0] != FormatWhatsApp(MsgTransferencia) {
		t.Fatalf("cliente devia receber só a mensagem neutra: %q", got)
	}

	// Ainda dentro dos 10 min: continua fechado para o cérebro.
	rel.avancar(9 * time.Minute)
	turno(e, 84, rel.now())
	if br.total() != 3 {
		t.Fatalf("antes de 10 min o disjuntor segue aberto; chamadas=%d", br.total())
	}

	// Passaram 10 min e o cérebro voltou: a chamada de teste passa e fecha.
	rel.avancar(2 * time.Minute)
	br.respostas = []func() (*brain.ChatResponse, error){responde("voltei")}
	turno(e, 85, rel.now())
	if br.total() != 4 {
		t.Fatalf("após 10 min devia liberar a chamada de teste; chamadas=%d", br.total())
	}
	if got := cw.mensagens(85, false); len(got) != 1 || got[0] != "voltei" {
		t.Fatalf("resposta da chamada de teste não chegou: %q", got)
	}
	if aberto, _ := e.disjuntor.estado(); aberto {
		t.Fatal("chamada de teste bem-sucedida devia fechar o disjuntor")
	}
	turno(e, 86, rel.now())
	if br.total() != 5 {
		t.Fatalf("fechado: toda conversa volta a chamar o cérebro; chamadas=%d", br.total())
	}
}

// TestDisjuntorReabreSeTesteFalha: a chamada de teste falha definitiva → mais
// 10 min fechado, sem chamar o cérebro.
func TestDisjuntorReabreSeTesteFalha(t *testing.T) {
	e, _, _, br := engineCompleta(t)
	rel := comRelogioFalso(e)
	br.respostas = []func() (*brain.ChatResponse, error){sempre(erro402())}
	for conv := int64(90); conv < 93; conv++ {
		turno(e, conv, rel.now())
	}
	rel.avancar(11 * time.Minute)
	turno(e, 93, rel.now()) // chamada de teste: falha de novo
	if br.total() != 4 {
		t.Fatalf("esperava a chamada de teste (4ª), veio %d", br.total())
	}
	turno(e, 94, rel.now())
	if br.total() != 4 {
		t.Fatalf("teste falhou: disjuntor reaberto, não chama; chamadas=%d", br.total())
	}
}

// TestFalhasDefinitivasDiferentesNaoAbremDisjuntor: 2 de um tipo + 1 de outro
// em 5 min não são "3 falhas iguais".
func TestFalhasDefinitivasDiferentesNaoAbremDisjuntor(t *testing.T) {
	e, _, _, br := engineCompleta(t)
	rel := comRelogioFalso(e)
	outra := &brain.Erro{Classe: brain.ClasseDefinitiva, Causa: "chave do OpenRouter inválida (401)", Status: 500}
	br.respostas = []func() (*brain.ChatResponse, error){sempre(erro402()), sempre(erro402()), sempre(outra)}
	for conv := int64(95); conv < 98; conv++ {
		turno(e, conv, rel.now())
	}
	if aberto, _ := e.disjuntor.estado(); aberto {
		t.Fatal("falhas de chaves diferentes não abrem o disjuntor")
	}
}

// TestFalhasForaDaJanelaNaoAbremDisjuntor: 3 falhas iguais espalhadas por mais
// de 5 min não abrem.
func TestFalhasForaDaJanelaNaoAbremDisjuntor(t *testing.T) {
	e, _, _, br := engineCompleta(t)
	rel := comRelogioFalso(e)
	br.respostas = []func() (*brain.ChatResponse, error){sempre(erro402())}
	for conv := int64(100); conv < 103; conv++ {
		turno(e, conv, rel.now())
		rel.avancar(3 * time.Minute)
	}
	if aberto, _ := e.disjuntor.estado(); aberto {
		t.Fatal("3 falhas em 6 min (janela de 5) não abrem o disjuntor")
	}
}

// ---------------------------------------------------------------------------
// ZC-6: reconferir etiqueta imediatamente antes de enviar
// ---------------------------------------------------------------------------

// TestRespostaNaoSaiSeConversaPassouParaHumano: entre a chamada ao cérebro e
// o envio, a conversa ganhou fila-humano (atendente assumiu) → a resposta do
// bot é descartada; nada de transferência nem nota.
func TestRespostaNaoSaiSeConversaPassouParaHumano(t *testing.T) {
	e, st, cw, br := engineCompleta(t)
	comRelogioFalso(e)
	br.respostas = []func() (*brain.ChatResponse, error){func() (*brain.ChatResponse, error) {
		// O atendente assume ENQUANTO o cérebro responde.
		c := chatwoot.Conversation{ID: 110, Labels: []string{"fila-humano"}, Status: chatwoot.StatusOpen}
		cw.mu.Lock()
		cw.aoVivo[110] = &c
		cw.mu.Unlock()
		return &brain.ChatResponse{Reply: "resposta atrasada do bot"}, nil
	}}

	turno(e, 110, e.agora())

	if got := cw.mensagens(110, false); len(got) != 0 {
		t.Fatalf("conversa já com atendente não pode receber resposta do bot: %q", got)
	}
	if got := cw.mensagens(110, true); len(got) != 0 {
		t.Fatalf("não é falha: sem nota privada: %q", got)
	}
	if f, _ := st.UltimaFalha(context.Background(), 110); f != nil {
		t.Fatalf("não é falha: sem registro: %+v", f)
	}
	if cw.gets == 0 {
		t.Fatal("esperava a reconferência (GetConversation) antes de enviar")
	}
}

// TestRespostaNaoSaiSeConversaSaiuDaFilaDoBot: perdeu fila-bot (sem ganhar
// fila-humano) → também não envia.
func TestRespostaNaoSaiSeConversaSaiuDaFilaDoBot(t *testing.T) {
	e, _, cw, br := engineCompleta(t)
	comRelogioFalso(e)
	br.respostas = []func() (*brain.ChatResponse, error){responde("oi")}
	c := chatwoot.Conversation{ID: 111, Labels: []string{"vip"}, Status: chatwoot.StatusOpen}
	cw.aoVivo[111] = &c

	turno(e, 111, e.agora())

	if got := cw.mensagens(111, false); len(got) != 0 {
		t.Fatalf("conversa fora da fila do bot não recebe resposta: %q", got)
	}
}

// TestEnvioSegueQuandoReconferenciaFalha: Chatwoot fora na consulta → fail-open
// (a resposta sai; um Chatwoot instável não cala o bot).
func TestEnvioSegueQuandoReconferenciaFalha(t *testing.T) {
	e, _, cw, br := engineCompleta(t)
	comRelogioFalso(e)
	br.respostas = []func() (*brain.ChatResponse, error){responde("oi")}
	cw.getErr = context.DeadlineExceeded

	turno(e, 112, e.agora())

	if got := cw.mensagens(112, false); len(got) != 1 || got[0] != "oi" {
		t.Fatalf("fail-open: resposta devia sair, veio %q", got)
	}
}

// ---------------------------------------------------------------------------
// ZC-7: alertas ao responsável
// ---------------------------------------------------------------------------

// TestFalhaDefinitivaDoCerebroAlerta: 402 → um alerta `falha_definitiva` com o
// ID da ocorrência e o link da conversa; nada de alerta no caminho feliz.
func TestFalhaDefinitivaDoCerebroAlerta(t *testing.T) {
	e, _, _, br := engineCompleta(t)
	e.cfg.ChatwootBaseURL, e.cfg.ChatwootAccountID = "http://chatwoot:3000", "1"
	e.cfg.ChatwootPublicURL = "https://painel.exemplo"
	comRelogioFalso(e)
	n := comNotificadorFalso(e)

	br.respostas = []func() (*brain.ChatResponse, error){responde("ok")}
	turno(e, 120, e.agora())
	if len(n.alertas) != 0 {
		t.Fatalf("caminho feliz não alerta: %+v", n.alertas)
	}

	br.respostas = []func() (*brain.ChatResponse, error){sempre(&brain.Erro{
		Classe: brain.ClasseDefinitiva, IDOcorrencia: "ZPF-20261002-0911-55", Causa: "saldo do OpenRouter insuficiente (402)", Status: 500,
	})}
	turno(e, 121, e.agora())
	al := n.doTipo(alerta.TipoFalhaDefinitiva)
	if len(al) != 1 || al[0].IDOcorrencia != "ZPF-20261002-0911-55" || al[0].ConversationID != 121 {
		t.Fatalf("esperava 1 alerta de falha definitiva com o ID do cérebro: %+v", n.alertas)
	}
	if al[0].Link != "https://painel.exemplo/app/accounts/1/conversations/121" {
		t.Fatalf("link da conversa (URL pública) inesperado: %q", al[0].Link)
	}
}

// TestTransitoriaEsgotadaNaoAlertaDefinitivaMasContaNaTaxa: transitória não
// gera alerta de definitiva; 3 turnos com falha em 15 min geram o de taxa.
func TestTransitoriaEsgotadaContaNaTaxa(t *testing.T) {
	e, _, _, br := engineCompleta(t)
	comRelogioFalso(e)
	n := comNotificadorFalso(e)
	br.respostas = []func() (*brain.ChatResponse, error){sempre(erroTransitorio())}
	for conv := int64(130); conv < 133; conv++ {
		turno(e, conv, e.agora())
	}
	if len(n.doTipo(alerta.TipoFalhaDefinitiva)) != 0 {
		t.Fatalf("transitória não é falha definitiva: %+v", n.alertas)
	}
	if len(n.doTipo(alerta.TipoTaxaDeFalhas)) != 1 {
		t.Fatalf("3 turnos falhos em 15 min deviam gerar 1 alerta de taxa: %+v", n.alertas)
	}
}

// TestDisjuntorAbertoAlertaUmaVez: a 3ª falha igual abre o disjuntor e alerta.
func TestDisjuntorAbertoAlertaUmaVez(t *testing.T) {
	e, _, _, br := engineCompleta(t)
	comRelogioFalso(e)
	n := comNotificadorFalso(e)
	br.respostas = []func() (*brain.ChatResponse, error){sempre(erro402())}
	for conv := int64(140); conv < 144; conv++ { // a 4ª já cai no disjuntor
		turno(e, conv, e.agora())
	}
	if al := n.doTipo(alerta.TipoDisjuntorAberto); len(al) != 1 || !strings.Contains(al[0].Texto, "402") {
		t.Fatalf("esperava exatamente 1 alerta de disjuntor aberto: %+v", n.alertas)
	}
}
