package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"bot-infominas/internal/alerta"
	"bot-infominas/internal/brain"
	"bot-infominas/internal/config"
	"bot-infominas/internal/store"
)

// ---------------------------------------------------------------------------
// Fakes de transporte e cérebro — a engine só enxerga as interfaces.
// ---------------------------------------------------------------------------

type msgEnviada struct {
	conv string
	body string
}

// fakeTransporte satisfaz Transporte sem rede, registrando o que a engine
// pediu ao canal.
type fakeTransporte struct {
	mu            sync.Mutex
	msgs          []msgEnviada
	departamentos map[string][]string // conversa → departamentos pedidos, em ordem
	enviarErr     error
	transferirErr error
}

func novoFakeTransporte() *fakeTransporte {
	return &fakeTransporte{departamentos: map[string][]string{}}
}

func (f *fakeTransporte) EnviarMensagem(_ context.Context, convID, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.enviarErr != nil {
		return f.enviarErr
	}
	f.msgs = append(f.msgs, msgEnviada{conv: convID, body: body})
	return nil
}

func (f *fakeTransporte) TransferirDepartamento(_ context.Context, convID, deptID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.transferirErr != nil {
		return f.transferirErr
	}
	f.departamentos[convID] = append(f.departamentos[convID], deptID)
	return nil
}

func (f *fakeTransporte) LerConversa(_ context.Context, convID string) (map[string]any, error) {
	return map[string]any{"id": convID}, nil
}

// mensagens devolve o corpo das mensagens enviadas na conversa.
func (f *fakeTransporte) mensagens(convID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.msgs {
		if m.conv == convID {
			out = append(out, m.body)
		}
	}
	return out
}

// transferida devolve os departamentos para os quais a conversa foi movida.
func (f *fakeTransporte) transferida(convID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.departamentos[convID]...)
}

type fakeBrain struct {
	mu       sync.Mutex
	chamadas int
	// respostas em sequência; a última repete. Vazio = resposta "ok".
	respostas []func() (*brain.ChatResponse, error)
	// pedidos guarda cada ChatRequest recebido (Contrato A).
	pedidos []brain.ChatRequest
	// encerrados registra os conversation_id recebidos em EncerrarEpisodio
	// (ZB-1); encerrarErr simula o cérebro fora nessa chamada.
	encerrados  []string
	encerrarErr error
}

func (f *fakeBrain) EncerrarEpisodio(_ context.Context, conversationID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.encerrados = append(f.encerrados, conversationID)
	return f.encerrarErr
}

func (f *fakeBrain) episodiosEncerrados() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.encerrados...)
}

func (f *fakeBrain) Chat(_ context.Context, req brain.ChatRequest) (*brain.ChatResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chamadas++
	f.pedidos = append(f.pedidos, req)
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

func (f *fakeBrain) ultimoPedido() brain.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pedidos) == 0 {
		return brain.ChatRequest{}
	}
	return f.pedidos[len(f.pedidos)-1]
}

func sempre(err error) func() (*brain.ChatResponse, error) {
	return func() (*brain.ChatResponse, error) { return nil, err }
}

func responde(reply string) func() (*brain.ChatResponse, error) {
	return func() (*brain.ChatResponse, error) { return &brain.ChatResponse{Reply: reply}, nil }
}

const (
	telefoneCliente = "+5511999990060"
	deptHumano      = "dept-humano-1"
)

// engineCompleta monta a engine com transporte e cérebro falsos sobre SQLite.
func engineCompleta(t *testing.T) (*Engine, store.Store, *fakeTransporte, *fakeBrain) {
	t.Helper()
	st, err := store.NewSQLite(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.Config{DepartamentoHumanoPadrao: deptHumano}
	tr, br := novoFakeTransporte(), &fakeBrain{}
	e := New(cfg, tr, br, st, nil)
	return e, st, tr, br
}

var reIDOcorrencia = regexp.MustCompile(`SOF-\d{8}-\d{4}-\d{2}`)

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

// notificadorFalso captura os alertas da vigia (ZC-7).
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

// msg monta uma mensagem de texto do cliente na conversa dada.
func msg(conv, texto string) Inbound {
	return Inbound{ConversationID: conv, Telefone: telefoneCliente, Nome: "Daniel", Texto: texto}
}

// turno roda um lote de uma mensagem do cliente na conversa.
func turno(e *Engine, conv string, recebido time.Time) {
	m := msg(conv, "oi")
	m.CriadoEm = recebido
	e.processarLote(context.Background(), []Inbound{m})
}

// naFila informa se a conversa tem worker/fila aberta (a mensagem passou da borda).
func naFila(e *Engine, convID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.filas[convID]
	return ok
}

// esperarCerebro aguarda (até 3 s) o cérebro ser chamado pelo worker.
func esperarCerebro(br *fakeBrain, n int) bool {
	for i := 0; i < 300; i++ {
		if br.total() >= n {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return br.total() >= n
}

// esperarMensagens aguarda (até 3 s) n mensagens na conversa.
func esperarMensagens(tr *fakeTransporte, conv string, n int) []string {
	for i := 0; i < 300; i++ {
		if got := tr.mensagens(conv); len(got) >= n {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	return tr.mensagens(conv)
}

// ---------------------------------------------------------------------------
// Caminho feliz e Contrato A
// ---------------------------------------------------------------------------

// TestRespostaNormalNaoTransfere: resposta do cérebro chega ao cliente, sem
// transferência nem registro.
func TestRespostaNormalNaoTransfere(t *testing.T) {
	e, st, tr, br := engineCompleta(t)
	br.respostas = []func() (*brain.ChatResponse, error){responde("Boa tarde! Posso ajudar?")}
	ctx := context.Background()
	e.processarLote(ctx, []Inbound{msg("c-63", "oi")})
	if got := tr.mensagens("c-63"); len(got) != 1 || got[0] != "Boa tarde! Posso ajudar?" {
		t.Fatalf("resposta do cérebro não chegou: %q", got)
	}
	if got := tr.transferida("c-63"); len(got) != 0 {
		t.Fatalf("caminho feliz não transfere: %v", got)
	}
	if f, _ := st.UltimaFalha(ctx, "c-63"); f != nil {
		t.Fatalf("caminho feliz não registra falha: %+v", f)
	}
}

// TestContratoALevaConversaETelefone: o cérebro recebe conversation_id (string
// do transporte), canal whatsapp, o texto coalescido e o telefone — e nada
// de identidade resolvida pelo gateway (não há gate).
func TestContratoALevaConversaETelefone(t *testing.T) {
	e, _, _, br := engineCompleta(t)
	e.processarLote(context.Background(), []Inbound{
		msg("uuid-conv-1", "oi"), msg("uuid-conv-1", "o boleto não abre"),
	})
	req := br.ultimoPedido()
	if req.ConversationID != "uuid-conv-1" || req.Canal != "whatsapp" || req.Telefone != telefoneCliente {
		t.Fatalf("Contrato A inesperado: %+v", req)
	}
	if req.Mensagem != "oi\no boleto não abre" {
		t.Fatalf("rajada devia virar UM turno com os textos unidos por \\n: %q", req.Mensagem)
	}
}

// TestRespostaVaziaDoCerebroViraPedidoDeReformular: reply vazio não deixa o
// cliente sem resposta.
func TestRespostaVaziaDoCerebroViraPedidoDeReformular(t *testing.T) {
	e, _, tr, br := engineCompleta(t)
	br.respostas = []func() (*brain.ChatResponse, error){responde("   ")}
	e.processarLote(context.Background(), []Inbound{msg("c-64", "oi")})
	if got := tr.mensagens("c-64"); len(got) != 1 || got[0] != FormatWhatsApp(MsgSemResposta) {
		t.Fatalf("esperava a mensagem padrão de reformular, veio %q", got)
	}
}

// TestRespostaEmBolhasViraVariasMensagens: o cérebro separa bolhas com `---`;
// cada uma vira uma mensagem no transporte, formatada para o WhatsApp.
func TestRespostaEmBolhasViraVariasMensagens(t *testing.T) {
	e, _, tr, br := engineCompleta(t)
	br.respostas = []func() (*brain.ChatResponse, error){responde("Oi, Ana! 🧡\n---\nAbra *Configurações* e salve.")}
	e.processarLote(context.Background(), []Inbound{msg("c-65", "oi")})
	got := tr.mensagens("c-65")
	if len(got) != 2 || got[0] != "Oi, Ana! 🧡" || got[1] != "Abra **Configurações** e salve." {
		t.Fatalf("bolhas inesperadas: %q", got)
	}
}

// ---------------------------------------------------------------------------
// Borda: HandleMensagem (higiene, dedup, idade) e fim de episódio
// ---------------------------------------------------------------------------

// TestMensagemDeTextoEntraNaFilaEChegaAoCerebro: o caminho inteiro pelo
// worker — mensagem neutra → fila → cérebro → transporte.
func TestMensagemDeTextoEntraNaFilaEChegaAoCerebro(t *testing.T) {
	e, _, tr, br := engineCompleta(t)
	br.respostas = []func() (*brain.ChatResponse, error){responde("oi!")}
	m := msg("c-10", "bom dia")
	m.MsgID = "m-1"

	e.HandleMensagem(context.Background(), m)

	if !naFila(e, "c-10") {
		t.Fatal("mensagem de texto devia abrir a fila da conversa")
	}
	if !esperarCerebro(br, 1) {
		t.Fatal("cérebro devia ter sido chamado")
	}
	if got := esperarMensagens(tr, "c-10", 1); len(got) != 1 || got[0] != "oi!" {
		t.Fatalf("resposta não chegou ao transporte: %q", got)
	}
}

// TestMensagemVaziaSemMidiaNaoEntraNaFila: sem texto e sem mídia, nada a fazer.
func TestMensagemVaziaSemMidiaNaoEntraNaFila(t *testing.T) {
	e, _, _, _ := engineCompleta(t)
	e.HandleMensagem(context.Background(), msg("c-11", "   "))
	if naFila(e, "c-11") {
		t.Fatal("mensagem vazia sem mídia não entra na fila")
	}
	e.HandleMensagem(context.Background(), Inbound{Texto: "sem conversa"})
	if naFila(e, "") {
		t.Fatal("mensagem sem conversation_id não entra na fila")
	}
}

// TestMensagemDuplicadaPorMsgIDIgnorada (F10): a MESMA mensagem reentregue
// com outro id de entrega não vira segundo turno.
func TestMensagemDuplicadaPorMsgIDIgnorada(t *testing.T) {
	e, _, _, br := engineCompleta(t)
	m := msg("c-12", "oi")
	m.MsgID = "msg-dup"
	e.HandleMensagem(context.Background(), m)
	e.HandleMensagem(context.Background(), m)
	if !esperarCerebro(br, 1) {
		t.Fatal("primeira entrega devia chegar ao cérebro")
	}
	time.Sleep(50 * time.Millisecond)
	if br.total() != 1 {
		t.Fatalf("mensagem duplicada não pode virar segundo turno; chamadas=%d", br.total())
	}
}

// TestMensagemAntigaDescartada (F10): mais velha que EVENTO_IDADE_MAX não
// gera resposta-fantasma.
func TestMensagemAntigaDescartada(t *testing.T) {
	e, _, _, _ := engineCompleta(t)
	e.cfg.EventoIdadeMax = 10 * time.Minute
	rel := comRelogioFalso(e)
	m := msg("c-13", "oi")
	m.CriadoEm = rel.now().Add(-11 * time.Minute)
	e.HandleMensagem(context.Background(), m)
	if naFila(e, "c-13") {
		t.Fatal("mensagem antiga não entra na fila")
	}
	m.ConversationID, m.CriadoEm = "c-14", rel.now().Add(-9*time.Minute)
	e.HandleMensagem(context.Background(), m)
	if !naFila(e, "c-14") {
		t.Fatal("mensagem dentro da idade máxima entra na fila")
	}
}

// TestEncerrarEpisodioLimpaPropostaNoCerebro (ZB-1): o fim da conversa chama
// EncerrarEpisodio no cérebro com o id da conversa; cérebro fora não explode.
func TestEncerrarEpisodioLimpaPropostaNoCerebro(t *testing.T) {
	e, _, _, br := engineCompleta(t)
	ctx := context.Background()
	e.EncerrarEpisodio(ctx, "uuid-45")
	br.encerrarErr = errors.New("cérebro fora")
	e.EncerrarEpisodio(ctx, "uuid-46")
	if got := br.episodiosEncerrados(); len(got) != 2 || got[0] != "uuid-45" || got[1] != "uuid-46" {
		t.Fatalf("esperava EncerrarEpisodio para uuid-45 e uuid-46, veio %v", got)
	}
}

// ---------------------------------------------------------------------------
// ZC-3: transferência única e silenciosa
// ---------------------------------------------------------------------------

// TestCerebroFalhandoTransfereSilenciosamente: falha definitiva do cérebro
// (402 do OpenRouter) → mensagem neutra ao cliente, departamento humano e
// registro em `falhas` com o MESMO ID do cérebro. Nenhuma mensagem de erro
// técnico ao cliente.
func TestCerebroFalhandoTransfereSilenciosamente(t *testing.T) {
	e, st, tr, br := engineCompleta(t)
	br.respostas = []func() (*brain.ChatResponse, error){sempre(&brain.Erro{
		Classe: brain.ClasseDefinitiva, IDOcorrencia: "SOF-20261002-0911-55",
		Causa: "saldo do OpenRouter insuficiente (402)", Status: 500,
	})}
	ctx := context.Background()

	e.processarLote(ctx, []Inbound{msg("c-60", "como rastreio a NF?")})

	publicas := tr.mensagens("c-60")
	if len(publicas) != 1 || publicas[0] != FormatWhatsApp(MsgTransferencia) {
		t.Fatalf("esperava só a mensagem neutra de transferência, veio %q", publicas)
	}
	for _, m := range publicas {
		if strings.Contains(strings.ToLower(m), "problema técnico") || strings.Contains(m, "402") || strings.Contains(m, "SOF-") {
			t.Fatalf("erro técnico vazou ao cliente: %q", m)
		}
	}
	if got := tr.transferida("c-60"); len(got) != 1 || got[0] != deptHumano {
		t.Fatalf("esperava transferência para o departamento humano padrão, veio %v", got)
	}
	f, err := st.UltimaFalha(ctx, "c-60")
	if err != nil || f == nil {
		t.Fatalf("falha não registrada: %+v err=%v", f, err)
	}
	if f.ID != "SOF-20261002-0911-55" || f.Origem != OrigemCerebro || f.Classe != brain.ClasseDefinitiva {
		t.Fatalf("registro em falhas inesperado: %+v", f)
	}
	if !strings.Contains(f.Causa, "402") {
		t.Fatalf("causa do cérebro devia ir ao registro: %+v", f)
	}
}

// TestTransferenciaGeraIDQuandoCerebroNaoManda: cérebro caiu sem corpo (sem
// ID) → o gateway gera o ID no formato SOF-AAAAMMDD-HHMM-NN e o registra.
func TestTransferenciaGeraIDQuandoCerebroNaoManda(t *testing.T) {
	e, st, _, br := engineCompleta(t)
	br.respostas = []func() (*brain.ChatResponse, error){sempre(&brain.Erro{
		Classe: brain.ClasseDefinitiva, Causa: "erro não tratado no cérebro (status 500)", Status: 500,
	})}
	ctx := context.Background()
	e.processarLote(ctx, []Inbound{msg("c-61", "oi")})

	f, _ := st.UltimaFalha(ctx, "c-61")
	if f == nil || !reIDOcorrencia.MatchString(f.ID) {
		t.Fatalf("registro devia ter ID gerado no formato SOF-: %+v", f)
	}
}

// TestTransferenciaSemDepartamentoAindaAvisaERegistra: sem
// DEPARTAMENTO_HUMANO_PADRAO o cliente é avisado e a ocorrência registrada;
// só a movimentação no canal fica de fora (e vai para o log).
func TestTransferenciaSemDepartamentoAindaAvisaERegistra(t *testing.T) {
	e, st, tr, br := engineCompleta(t)
	e.cfg.DepartamentoHumanoPadrao = ""
	br.respostas = []func() (*brain.ChatResponse, error){sempre(erro402())}
	ctx := context.Background()
	e.processarLote(ctx, []Inbound{msg("c-62", "oi")})
	if got := tr.mensagens("c-62"); len(got) != 1 {
		t.Fatalf("cliente devia ser avisado mesmo sem departamento: %q", got)
	}
	if got := tr.transferida("c-62"); len(got) != 0 {
		t.Fatalf("sem departamento não há o que transferir no canal: %v", got)
	}
	if f, _ := st.UltimaFalha(ctx, "c-62"); f == nil {
		t.Fatal("ocorrência devia ser registrada")
	}
}

// TestFalhaNoTransporteNaoImpedeRegistro: o canal fora do ar na transferência
// não apaga a ocorrência nem derruba o turno.
func TestFalhaNoTransporteNaoImpedeRegistro(t *testing.T) {
	e, st, tr, br := engineCompleta(t)
	tr.enviarErr, tr.transferirErr = errors.New("canal fora"), errors.New("canal fora")
	br.respostas = []func() (*brain.ChatResponse, error){sempre(erro402())}
	ctx := context.Background()
	e.processarLote(ctx, []Inbound{msg("c-66", "oi")})
	if f, _ := st.UltimaFalha(ctx, "c-66"); f == nil || f.Origem != OrigemCerebro {
		t.Fatalf("ocorrência devia ser registrada apesar do canal fora: %+v", f)
	}
}

// ---------------------------------------------------------------------------
// ZC-5: nova tentativa só para falha transitória + teto do turno + disjuntor
// ---------------------------------------------------------------------------

// TestTransitoriaRepeteEDepoisTransfere: 3 chamadas (1 + 2 novas tentativas,
// esperas de 2 s e 5 s), então transferência silenciosa.
func TestTransitoriaRepeteEDepoisTransfere(t *testing.T) {
	e, st, tr, br := engineCompleta(t)
	rel := comRelogioFalso(e)
	br.respostas = []func() (*brain.ChatResponse, error){sempre(erroTransitorio())}

	turno(e, "c-70", rel.now())

	if br.total() != 3 {
		t.Fatalf("esperava 3 chamadas ao cérebro (1 + 2 novas tentativas), veio %d", br.total())
	}
	if len(rel.esperas) != 2 || rel.esperas[0] != 2*time.Second || rel.esperas[1] != 5*time.Second {
		t.Fatalf("esperas: queria [2s 5s], veio %v", rel.esperas)
	}
	if got := tr.mensagens("c-70"); len(got) != 1 || got[0] != FormatWhatsApp(MsgTransferencia) {
		t.Fatalf("esperava só a mensagem neutra, veio %q", got)
	}
	f, _ := st.UltimaFalha(context.Background(), "c-70")
	if f == nil || f.Classe != brain.ClasseTransitoria || !strings.Contains(f.Causa, "3 tentativas") {
		t.Fatalf("registro da transferência inesperado: %+v", f)
	}
}

// TestTransitoriaRecuperaNaNovaTentativa: a 2ª tentativa dá certo → resposta
// entregue, sem transferência.
func TestTransitoriaRecuperaNaNovaTentativa(t *testing.T) {
	e, st, tr, br := engineCompleta(t)
	comRelogioFalso(e)
	br.respostas = []func() (*brain.ChatResponse, error){sempre(erroTransitorio()), responde("tudo certo!")}

	turno(e, "c-71", e.agora())

	if br.total() != 2 {
		t.Fatalf("esperava 2 chamadas, veio %d", br.total())
	}
	if got := tr.mensagens("c-71"); len(got) != 1 || got[0] != "tudo certo!" {
		t.Fatalf("resposta da nova tentativa não chegou: %q", got)
	}
	if f, _ := st.UltimaFalha(context.Background(), "c-71"); f != nil {
		t.Fatalf("recuperou: não devia registrar falha: %+v", f)
	}
}

// TestDefinitivaNaoRepete: 402 → uma única chamada e transferência.
func TestDefinitivaNaoRepete(t *testing.T) {
	e, _, tr, br := engineCompleta(t)
	rel := comRelogioFalso(e)
	br.respostas = []func() (*brain.ChatResponse, error){sempre(erro402())}

	turno(e, "c-72", rel.now())

	if br.total() != 1 || len(rel.esperas) != 0 {
		t.Fatalf("definitiva não repete: chamadas=%d esperas=%v", br.total(), rel.esperas)
	}
	if got := tr.transferida("c-72"); len(got) != 1 || got[0] != deptHumano {
		t.Fatalf("esperava transferência ao departamento humano, veio %v", got)
	}
}

// TestTetoDoTurnoImpedeNovaTentativa: mensagem recebida há 44 s (rajada +
// cérebro lento) → sem tempo para esperar 2 s dentro do teto de 45 s: não
// repete, transfere.
func TestTetoDoTurnoImpedeNovaTentativa(t *testing.T) {
	e, st, _, br := engineCompleta(t)
	rel := comRelogioFalso(e)
	br.respostas = []func() (*brain.ChatResponse, error){sempre(erroTransitorio())}

	turno(e, "c-73", rel.now().Add(-44*time.Second))

	if br.total() != 1 || len(rel.esperas) != 0 {
		t.Fatalf("fora do teto não repete: chamadas=%d esperas=%v", br.total(), rel.esperas)
	}
	if f, _ := st.UltimaFalha(context.Background(), "c-73"); f == nil || !strings.Contains(f.Causa, "teto") {
		t.Fatalf("causa devia citar o teto: %+v", f)
	}
}

func conv(n int) string { return fmt.Sprintf("c-%d", n) }

// TestDisjuntorAbreNaTerceiraEFechaAposTeste: 3 falhas definitivas iguais em
// 5 min abrem o disjuntor (10 min sem chamar o cérebro, transferência direta);
// vencido o prazo, UMA chamada de teste bem-sucedida fecha.
func TestDisjuntorAbreNaTerceiraEFechaAposTeste(t *testing.T) {
	e, st, tr, br := engineCompleta(t)
	rel := comRelogioFalso(e)
	br.respostas = []func() (*brain.ChatResponse, error){sempre(erro402())}

	for n := 80; n < 83; n++ {
		turno(e, conv(n), rel.now())
		rel.avancar(30 * time.Second)
	}
	if br.total() != 3 {
		t.Fatalf("3 falhas → 3 chamadas, veio %d", br.total())
	}
	if aberto, _ := e.disjuntor.estado(); !aberto {
		t.Fatal("3ª falha definitiva igual devia abrir o disjuntor")
	}

	// Aberto: a 4ª conversa nem chama o cérebro e é transferida direto.
	turno(e, "c-83", rel.now())
	if br.total() != 3 {
		t.Fatalf("disjuntor aberto não pode chamar o cérebro; chamadas=%d", br.total())
	}
	f, _ := st.UltimaFalha(context.Background(), "c-83")
	if f == nil || f.Origem != OrigemDisjuntor || !strings.Contains(f.Causa, "402") {
		t.Fatalf("transferência pelo disjuntor inesperada: %+v", f)
	}
	if got := tr.mensagens("c-83"); len(got) != 1 || got[0] != FormatWhatsApp(MsgTransferencia) {
		t.Fatalf("cliente devia receber só a mensagem neutra: %q", got)
	}

	// Ainda dentro dos 10 min: continua fechado para o cérebro.
	rel.avancar(9 * time.Minute)
	turno(e, "c-84", rel.now())
	if br.total() != 3 {
		t.Fatalf("antes de 10 min o disjuntor segue aberto; chamadas=%d", br.total())
	}

	// Passaram 10 min e o cérebro voltou: a chamada de teste passa e fecha.
	rel.avancar(2 * time.Minute)
	br.respostas = []func() (*brain.ChatResponse, error){responde("voltei")}
	turno(e, "c-85", rel.now())
	if br.total() != 4 {
		t.Fatalf("após 10 min devia liberar a chamada de teste; chamadas=%d", br.total())
	}
	if got := tr.mensagens("c-85"); len(got) != 1 || got[0] != "voltei" {
		t.Fatalf("resposta da chamada de teste não chegou: %q", got)
	}
	if aberto, _ := e.disjuntor.estado(); aberto {
		t.Fatal("chamada de teste bem-sucedida devia fechar o disjuntor")
	}
	turno(e, "c-86", rel.now())
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
	for n := 90; n < 93; n++ {
		turno(e, conv(n), rel.now())
	}
	rel.avancar(11 * time.Minute)
	turno(e, "c-93", rel.now()) // chamada de teste: falha de novo
	if br.total() != 4 {
		t.Fatalf("esperava a chamada de teste (4ª), veio %d", br.total())
	}
	turno(e, "c-94", rel.now())
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
	for n := 95; n < 98; n++ {
		turno(e, conv(n), rel.now())
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
	for n := 100; n < 103; n++ {
		turno(e, conv(n), rel.now())
		rel.avancar(3 * time.Minute)
	}
	if aberto, _ := e.disjuntor.estado(); aberto {
		t.Fatal("3 falhas em 6 min (janela de 5) não abrem o disjuntor")
	}
}

// ---------------------------------------------------------------------------
// ZC-7: alertas ao responsável
// ---------------------------------------------------------------------------

// TestFalhaDefinitivaDoCerebroAlerta: 402 → um alerta `falha_definitiva` com o
// ID da ocorrência e a conversa; nada de alerta no caminho feliz.
func TestFalhaDefinitivaDoCerebroAlerta(t *testing.T) {
	e, _, _, br := engineCompleta(t)
	comRelogioFalso(e)
	n := comNotificadorFalso(e)

	br.respostas = []func() (*brain.ChatResponse, error){responde("ok")}
	turno(e, "c-120", e.agora())
	if len(n.alertas) != 0 {
		t.Fatalf("caminho feliz não alerta: %+v", n.alertas)
	}

	br.respostas = []func() (*brain.ChatResponse, error){sempre(&brain.Erro{
		Classe: brain.ClasseDefinitiva, IDOcorrencia: "SOF-20261002-0911-55", Causa: "saldo do OpenRouter insuficiente (402)", Status: 500,
	})}
	turno(e, "c-121", e.agora())
	al := n.doTipo(alerta.TipoFalhaDefinitiva)
	if len(al) != 1 || al[0].IDOcorrencia != "SOF-20261002-0911-55" || al[0].ConversationID != "c-121" {
		t.Fatalf("esperava 1 alerta de falha definitiva com o ID do cérebro: %+v", n.alertas)
	}
	if !strings.Contains(al[0].Texto, "402") {
		t.Fatalf("alerta devia trazer a causa: %+v", al[0])
	}
}

// TestTransitoriaEsgotadaContaNaTaxa: transitória não gera alerta de
// definitiva; 3 turnos com falha em 15 min geram o de taxa.
func TestTransitoriaEsgotadaContaNaTaxa(t *testing.T) {
	e, _, _, br := engineCompleta(t)
	comRelogioFalso(e)
	n := comNotificadorFalso(e)
	br.respostas = []func() (*brain.ChatResponse, error){sempre(erroTransitorio())}
	for i := 130; i < 133; i++ {
		turno(e, conv(i), e.agora())
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
	for i := 140; i < 144; i++ { // a 4ª já cai no disjuntor
		turno(e, conv(i), e.agora())
	}
	if al := n.doTipo(alerta.TipoDisjuntorAberto); len(al) != 1 || !strings.Contains(al[0].Texto, "402") {
		t.Fatalf("esperava exatamente 1 alerta de disjuntor aberto: %+v", n.alertas)
	}
}

// ---------------------------------------------------------------------------
// Pedido explícito de atendente (origem cliente)
// ---------------------------------------------------------------------------

// TestPedidoDeAtendentePassaPelaTransferenciaUnica: "quero falar com
// atendente" → sem chamada ao cérebro, mensagem da persona, departamento
// humano e registro com origem cliente.
func TestPedidoDeAtendentePassaPelaTransferenciaUnica(t *testing.T) {
	e, st, tr, br := engineCompleta(t)
	comRelogioFalso(e)
	e.processarLote(context.Background(), []Inbound{msg("c-153", "quero falar com atendente")})
	if br.total() != 0 {
		t.Fatalf("pedido de atendente não vai ao cérebro; chamadas=%d", br.total())
	}
	if got := tr.transferida("c-153"); len(got) != 1 || got[0] != deptHumano {
		t.Fatalf("esperava transferência ao departamento humano, veio %v", got)
	}
	if f, _ := st.UltimaFalha(context.Background(), "c-153"); f == nil || f.Origem != OrigemCliente {
		t.Fatalf("registro com origem cliente esperado: %+v", f)
	}
	if got := tr.mensagens("c-153"); len(got) != 1 || !strings.Contains(got[0], "encaminhar") {
		t.Fatalf("cliente devia receber a confirmação do encaminhamento: %q", got)
	}
}

// TestPedeAtendente: palavra inteira; relato longo sem pedido não dispara.
func TestPedeAtendente(t *testing.T) {
	sim := []string{
		"atendente", "quero falar com atendente", "Me passa pra um humano", "tem algum atendente aí?",
		"quero uma pessoa de verdade",
		"preciso falar com uma pessoa de verdade, um atendente humano, porque o app não abre desde ontem e já reinstalei duas vezes",
		// Hipótese de trabalho declarada em PedeAtendente: negação/pergunta
		// curtas transferem (falso positivo barato).
		"não quero atendente, me responde você", "você é um atendente virtual?",
	}
	nao := []string{
		"oi", "10596693664", "o boleto não abre",
		"o atendente me disse ontem que o boleto ia atualizar sozinho depois da sincronização, mas até agora continua em aberto na tela",
		"desumano esse trânsito, mas enfim: a fatura 123 não aparece no app", // "desumano" não é a palavra inteira
	}
	for _, m := range sim {
		if !PedeAtendente(m) {
			t.Errorf("devia detectar pedido de atendente: %q", m)
		}
	}
	for _, m := range nao {
		if PedeAtendente(m) {
			t.Errorf("não devia detectar pedido de atendente: %q", m)
		}
	}
}

// ---------------------------------------------------------------------------
// Mídia avisa e transfere
// ---------------------------------------------------------------------------

// TestMensagemSoComMidiaAvisaETransfere: áudio/imagem sem texto → o cliente
// recebe o aviso (não fica em silêncio), a conversa vai ao departamento
// humano com registro de origem anexo, e o cérebro NÃO é chamado.
func TestMensagemSoComMidiaAvisaETransfere(t *testing.T) {
	e, st, tr, br := engineCompleta(t)
	ctx := context.Background()

	e.processarLote(ctx, []Inbound{{ConversationID: "c-170", Telefone: telefoneCliente, TemMidia: true}})

	if br.total() != 0 {
		t.Fatalf("mídia não vai ao cérebro; chamadas=%d", br.total())
	}
	publicas := tr.mensagens("c-170")
	if len(publicas) != 1 || publicas[0] != FormatWhatsApp(MsgMidia) {
		t.Fatalf("esperava só o aviso de mídia ao cliente, veio %q", publicas)
	}
	if !strings.Contains(publicas[0], "áudio") || !strings.Contains(publicas[0], "atendente") {
		t.Fatalf("aviso devia citar a mídia e o encaminhamento: %q", publicas[0])
	}
	if got := tr.transferida("c-170"); len(got) != 1 || got[0] != deptHumano {
		t.Fatalf("esperava transferência ao departamento humano, veio %v", got)
	}
	if f, _ := st.UltimaFalha(ctx, "c-170"); f == nil || f.Origem != OrigemAnexo || !reIDOcorrencia.MatchString(f.ID) {
		t.Fatalf("registro com origem anexo e ID esperado: %+v", f)
	}
}

// TestMidiaComLegendaTambemTransfere: imagem com legenda ("segue o print") —
// o bot não vê a imagem, então a legenda sozinha não vai ao cérebro.
func TestMidiaComLegendaTambemTransfere(t *testing.T) {
	e, _, tr, br := engineCompleta(t)
	legenda := msg("c-171", "segue o print do erro")
	legenda.TemMidia = true
	e.processarLote(context.Background(), []Inbound{msg("c-171", "oi"), legenda})
	if br.total() != 0 {
		t.Fatalf("rajada com mídia não vai ao cérebro; chamadas=%d", br.total())
	}
	if got := tr.mensagens("c-171"); len(got) != 1 || got[0] != FormatWhatsApp(MsgMidia) {
		t.Fatalf("esperava o aviso de mídia, veio %q", got)
	}
}

// TestMensagemSoComMidiaEntraNaFila: na borda, mensagem sem texto e com mídia
// NÃO é descartada (era a causa das mensagens sem resposta): entra na fila e
// o worker avisa + transfere.
func TestMensagemSoComMidiaEntraNaFila(t *testing.T) {
	e, _, tr, br := engineCompleta(t)
	m := Inbound{ConversationID: "c-172", Telefone: telefoneCliente, TemMidia: true, MsgID: "m-172"}

	e.HandleMensagem(context.Background(), m)

	if !naFila(e, "c-172") {
		t.Fatal("mensagem só com mídia devia entrar na fila da conversa (não ser descartada)")
	}
	if got := esperarMensagens(tr, "c-172", 1); len(got) != 1 || !strings.Contains(got[0], "áudio") {
		t.Fatalf("cliente devia receber o aviso de mídia: %q", got)
	}
	if br.total() != 0 {
		t.Fatalf("mídia não vai ao cérebro; chamadas=%d", br.total())
	}
}

// ---------------------------------------------------------------------------
// Persona
// ---------------------------------------------------------------------------

// contarEmojis conta runas de símbolo (categoria So — onde vivem os emojis)
// fora do ASCII; modificadores/ZWJ não contam, o que basta para "≤ 1".
func contarEmojis(s string) int {
	n := 0
	for _, r := range s {
		if r > 0x7f && unicode.Is(unicode.So, r) {
			n++
		}
	}
	return n
}

// TestMensagensDaPersona: pt-BR informal ("você"), no máximo 1 emoji, sem
// prometer prazo e sem o nome do produto antigo.
func TestMensagensDaPersona(t *testing.T) {
	proibidos := []string{"instante", "minuto", "logo", "em breve", "já já", "Boníssim"}
	for nome, m := range map[string]string{
		"transferencia": MsgTransferencia, "pedido": MsgPedidoAtendente, "midia": MsgMidia, "sem_resposta": MsgSemResposta,
	} {
		if n := contarEmojis(m); n > 1 {
			t.Errorf("%s: mais de um emoji (%d): %q", nome, n, m)
		}
		for _, p := range proibidos {
			if strings.Contains(strings.ToLower(m), strings.ToLower(p)) {
				t.Errorf("%s: promete prazo ou usa tom antigo (%q): %q", nome, p, m)
			}
		}
	}
	if nomeAssistente != "Sofia" {
		t.Fatalf("persona: esperava Sofia, veio %q", nomeAssistente)
	}
	if !strings.HasPrefix(NovoIDOcorrencia(time.Now()), "SOF-") {
		t.Fatal("ID de ocorrência devia começar com SOF-")
	}
}
