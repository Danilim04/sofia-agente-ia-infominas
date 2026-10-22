package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bot-infominas/internal/config"
	"bot-infominas/internal/store"
)

func engineComLimites(t *testing.T, cfg *config.Config) (*Engine, *fakeInfodesk, *fakeBrain, *relogioFalso) {
	t.Helper()
	st, err := store.NewSQLite(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg.DepartamentoHumanoPadrao = deptHumano
	tr, br := novoFakeInfodesk(), &fakeBrain{}
	e := New(cfg, tr, br, st, nil)
	return e, tr, br, comRelogioFalso(e)
}

// M4: o texto coalescido que vai ao cérebro tem teto de caracteres (configurável,
// em runes — nunca parte um caractere).
func TestLoteCoalescidoTemTetoDeCaracteres(t *testing.T) {
	e, _, br, _ := engineComLimites(t, &config.Config{LoteMaxChars: 50})
	e.processarLote(context.Background(), []Inbound{
		msg("c-l1", strings.Repeat("á", 40)), msg("c-l1", strings.Repeat("é", 40)),
	})
	if br.total() != 1 {
		t.Fatalf("o lote vai ao cérebro: %d", br.total())
	}
	if got := []rune(br.ultimoPedido().Mensagem); len(got) > 50 {
		t.Fatalf("texto com %d caracteres, teto 50", len(got))
	}
	if !strings.HasPrefix(br.ultimoPedido().Mensagem, "ááá") {
		t.Fatalf("o começo da mensagem fica: %q", br.ultimoPedido().Mensagem)
	}
}

func TestLoteAbaixoDoTetoNaoMuda(t *testing.T) {
	e, _, br, _ := engineComLimites(t, &config.Config{})
	e.processarLote(context.Background(), []Inbound{msg("c-l2", "meu wifi caiu"), msg("c-l2", "desde ontem")})
	if got := br.ultimoPedido().Mensagem; got != "meu wifi caiu\ndesde ontem" {
		t.Fatalf("texto = %q", got)
	}
}

// M4: acima de TURNOS_POR_HORA_MAX por telefone a conversa vai a um atendente
// (uma vez, sem o cérebro); abrir OUTRA conversa do mesmo telefone não zera; e a
// janela é deslizante (1 h).
func TestLimiteDeTurnosPorTelefone(t *testing.T) {
	e, tr, br, rel := engineComLimites(t, &config.Config{TurnosPorHoraMax: 3})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		e.processarLote(ctx, []Inbound{msg("c-t1", "oi")})
	}
	if br.total() != 3 || len(tr.transferida("c-t1")) != 0 {
		t.Fatalf("até o teto vai ao cérebro: cérebro=%d handoffs=%v", br.total(), tr.transferida("c-t1"))
	}
	e.processarLote(ctx, []Inbound{msg("c-t1", "oi de novo")})
	if br.total() != 3 {
		t.Fatalf("o 4º turno não vai ao cérebro: %d", br.total())
	}
	if got := tr.transferida("c-t1"); len(got) != 1 {
		t.Fatalf("o 4º turno transfere (uma vez): %v", got)
	}
	// Outra conversa do MESMO telefone: já está acima do teto.
	e.processarLote(ctx, []Inbound{msg("c-t2", "oi")})
	if br.total() != 3 || len(tr.transferida("c-t2")) != 1 {
		t.Fatalf("outra conversa do mesmo telefone não zera o contador: cérebro=%d handoffs=%v", br.total(), tr.transferida("c-t2"))
	}
	// Outro telefone não é afetado.
	outro := msg("c-t3", "oi")
	outro.Telefone = "+5531977770000"
	e.processarLote(ctx, []Inbound{outro})
	if br.total() != 4 {
		t.Fatalf("outro telefone fala normalmente: %d", br.total())
	}
	// Passada 1 h, o telefone volta (conversa nova).
	rel.avancar(61 * time.Minute)
	e.processarLote(ctx, []Inbound{msg("c-t4", "oi")})
	if br.total() != 5 {
		t.Fatalf("depois da janela o telefone volta: %d", br.total())
	}
}

func TestContadorDeTurnosDescartaOQueSaiuDaJanela(t *testing.T) {
	var c contadorTurnos
	t0 := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	for i := 1; i <= 3; i++ {
		if n := c.registrar("+5531999990000", t0.Add(time.Duration(i)*time.Minute)); n != i {
			t.Fatalf("turno %d: contou %d", i, n)
		}
	}
	if n := c.registrar("+5531999990000", t0.Add(62*time.Minute)); n != 2 {
		t.Fatalf("só 2 na última hora (a das 12:03 e esta, 13:02): %d", n)
	}
}
