package reconciliador

import (
	"context"
	"strings"
	"testing"
	"time"

	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
)

// opcoesComRelogio devolve Options de teste com um relógio que o teste avança.
func opcoesComRelogio(t0 time.Time) (Options, *time.Time) {
	relogio := t0
	o := opcoesDeTeste()
	o.Now = func() time.Time { return relogio }
	return o, &relogio
}

// conversaTransferidaComMarca deixa a conversa como a engine a deixa quando o
// handoff falha: estado transferida e marca de handoff não confirmado gravada.
func conversaTransferidaComMarca(t *testing.T, st store.Store, id string, quando time.Time) {
	t.Helper()
	ctx := context.Background()
	_, err := st.AtualizarConversa(ctx, id, func(c *store.Conversa, _ bool) bool {
		c.Telefone, c.Estado, c.HandledBy, c.UpdatedAt = telefone, store.EstadoTransferida, store.HandledByBotExterno, quando
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarcarHandoffPendente(ctx, &store.HandoffPendente{
		ConversationID: id, Origem: "cerebro", IDOcorrencia: "SOF-1", DepartamentoID: "dept-1",
		Nota: "Sofia → humano", CreatedAt: quando, ProximaEm: quando,
	}); err != nil {
		t.Fatal(err)
	}
}

func marca(t *testing.T, st store.Store, id string, ate time.Time) *store.HandoffPendente {
	t.Helper()
	hs, err := st.HandoffsPendentes(context.Background(), ate)
	if err != nil {
		t.Fatal(err)
	}
	for i := range hs {
		if hs[i].ConversationID == id {
			return &hs[i]
		}
	}
	return nil
}

// O handoff que falhou fica marcado e o vigia o repete, com backoff
// exponencial e alerta, até o Infodesk aceitar — e então a marca some.
func TestVigiaRepeteHandoffNaoConfirmadoComBackoffAteConfirmar(t *testing.T) {
	o, relogio := opcoesComRelogio(agora)
	a := novoAmbiente(t, nil, o, false, 16)
	conversaTransferidaComMarca(t, a.st, "conv-h1", agora)
	a.eng.errReenvio = &infodesk.HTTPError{Status: 503, Rota: "/conversations/x/handoff"}
	longe := agora.Add(24 * time.Hour)

	// 1ª repetição: falha, adia 30 s, alerta.
	a.rec.Executar(context.Background())
	if got := a.eng.reenviadas(); len(got) != 1 {
		t.Fatalf("reenvios = %v", got)
	}
	m := marca(t, a.st, "conv-h1", longe)
	if m == nil || m.Tentativas != 1 || !m.ProximaEm.Equal(agora.Add(30*time.Second)) {
		t.Fatalf("marca após a 1ª falha: %+v", m)
	}
	if al := a.alertas.doTipo(TipoHandoffNaoConfirmado); len(al) != 1 || !strings.Contains(al[0].Texto, "SOF-1") {
		t.Fatalf("alerta de handoff não confirmado: %+v", al)
	}

	// Antes do prazo não repete.
	*relogio = agora.Add(20 * time.Second)
	a.rec.Executar(context.Background())
	if got := a.eng.reenviadas(); len(got) != 1 {
		t.Fatalf("antes do backoff não pode repetir: %v", got)
	}

	// Passado o prazo repete; o backoff dobra (60 s).
	*relogio = agora.Add(31 * time.Second)
	a.rec.Executar(context.Background())
	if got := a.eng.reenviadas(); len(got) != 2 {
		t.Fatalf("devia repetir depois do prazo: %v", got)
	}
	if m := marca(t, a.st, "conv-h1", longe); m == nil || m.Tentativas != 2 || !m.ProximaEm.Equal(agora.Add(31*time.Second+time.Minute)) {
		t.Fatalf("backoff devia dobrar: %+v", m)
	}

	// O Infodesk volta: confirma e a marca some.
	a.eng.mu.Lock()
	a.eng.errReenvio = nil
	a.eng.mu.Unlock()
	*relogio = agora.Add(10 * time.Minute)
	a.rec.Executar(context.Background())
	if got := a.eng.reenviadas(); len(got) != 3 {
		t.Fatalf("3ª repetição: %v", got)
	}
	if marca(t, a.st, "conv-h1", longe) != nil {
		t.Fatal("a marca devia sumir com o handoff confirmado")
	}
	a.rec.Executar(context.Background())
	if got := a.eng.reenviadas(); len(got) != 3 {
		t.Fatalf("sem marca não repete: %v", got)
	}
}

// O backoff nunca passa de RECONCILIA_BACKOFF_MAX.
func TestEsperaDoHandoffRespeitaOTeto(t *testing.T) {
	r := New(Options{BackoffMax: 5 * time.Minute}, nil, nil, nil, nil, nil, nil)
	casos := map[int]time.Duration{1: 30 * time.Second, 2: time.Minute, 3: 2 * time.Minute, 4: 4 * time.Minute, 5: 5 * time.Minute, 20: 5 * time.Minute}
	for tent, quer := range casos {
		if got := r.esperaDoHandoff(tent); got != quer {
			t.Errorf("tentativa %d: %v, queria %v", tent, got, quer)
		}
	}
}

// A marca não vira handoff novo quando o Infodesk já mostra outro condutor, a
// conversa foi encerrada ou o estado local já não é transferida.
func TestVigiaLimpaMarcaQuandoOHandoffJaNaoFazSentido(t *testing.T) {
	a := novoAmbiente(t, nil, opcoesDeTeste(), false, 16)
	longe := agora.Add(24 * time.Hour)
	ids := []string{"conv-humano", "conv-fim", "conv-bot"}
	for _, id := range ids {
		conversaTransferidaComMarca(t, a.st, id, agora)
	}
	a.idk.conversas["conv-humano"] = infodesk.Conversa{ID: "conv-humano", HandledBy: infodesk.ConduzidaPorAtendente}
	a.idk.conversas["conv-fim"] = infodesk.Conversa{ID: "conv-fim", HandledBy: infodesk.ConduzidaPorBotExterno, Status: "FINISHED"}
	conversaDoBot(t, a.st, "conv-bot", agora, agora) // episódio novo: voltou a ser do bot

	a.rec.Executar(context.Background())
	if got := a.eng.reenviadas(); len(got) != 0 {
		t.Fatalf("nenhum handoff devia ser repetido: %v", got)
	}
	for _, id := range ids {
		if marca(t, a.st, id, longe) != nil {
			t.Fatalf("a marca de %s devia ter sido apagada", id)
		}
	}
}

// Falha da conferência no Infodesk não repete nem apaga (tenta no próximo
// ciclo); RATE_LIMITED no repique para o ciclo com backoff e não conta tentativa.
func TestVigiaHandoffComInfodeskLimitandoOuFalhando(t *testing.T) {
	a := novoAmbiente(t, nil, opcoesDeTeste(), false, 16)
	conversaTransferidaComMarca(t, a.st, "conv-h2", agora)
	longe := agora.Add(24 * time.Hour)

	a.idk.errConversa = &infodesk.HTTPError{Status: 503, Rota: "/conversations/x"}
	a.rec.Executar(context.Background())
	if len(a.eng.reenviadas()) != 0 || marca(t, a.st, "conv-h2", longe) == nil {
		t.Fatal("sem conferir não repete nem apaga")
	}

	a.idk.errConversa = nil
	a.eng.mu.Lock()
	a.eng.errReenvio = erroHTTP(429, infodesk.CodeRateLimited, 0)
	a.eng.mu.Unlock()
	if espera := a.rec.Executar(context.Background()); espera != 2*time.Minute {
		t.Fatalf("RATE_LIMITED no repique devia dar backoff, espera = %v", espera)
	}
	if m := marca(t, a.st, "conv-h2", longe); m == nil || m.Tentativas != 0 {
		t.Fatalf("rate limit não conta como tentativa: %+v", m)
	}
}

// A origem vigia leva o tempo de espera que a nota cita.
func TestVigiaPassaAEsperaParaANota(t *testing.T) {
	a := novoAmbiente(t, nil, opcoesDeTeste(), false, 16)
	conversaDoBot(t, a.st, "conv-espera", agora.Add(-20*time.Minute), time.Time{})
	a.rec.Executar(context.Background())
	a.eng.mu.Lock()
	defer a.eng.mu.Unlock()
	if len(a.eng.pedidos) != 1 || a.eng.pedidos[0].Espera != 5*time.Minute {
		t.Fatalf("pedidos = %+v", a.eng.pedidos)
	}
}
