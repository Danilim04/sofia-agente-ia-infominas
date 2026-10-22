package engine

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"bot-infominas/internal/alerta"
	"bot-infominas/internal/config"
	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
)

// engineComPiloto monta a engine com TESTADORES_TELEFONES = lista.
func engineComPiloto(t *testing.T, lista ...string) (*Engine, store.Store, *fakeInfodesk, *fakeBrain) {
	t.Helper()
	st, err := store.NewSQLite(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.Config{DepartamentoHumanoPadrao: deptHumano, TestadoresTelefones: lista}
	tr, br := novoFakeInfodesk(), &fakeBrain{}
	e := New(cfg, tr, br, st, nil)
	comRelogioFalso(e)
	return e, st, tr, br
}

func msgDe(conv, tel, texto string) Inbound {
	m := msg(conv, texto)
	m.Telefone = tel
	return m
}

// Lista vazia = desligada: todo mundo fala com a Sofia (comportamento de antes).
func TestPilotoDesligadoNaoMudaNada(t *testing.T) {
	e, _, tr, br := engineComPiloto(t)
	br.respostas = nil
	e.processarLote(context.Background(), []Inbound{msg("c-p0", "oi")})
	if br.total() != 1 || len(tr.transferida("c-p0")) != 0 || len(tr.mensagens("c-p0")) != 1 {
		t.Fatalf("desligado: cérebro=%d handoffs=%v msgs=%v", br.total(), tr.transferida("c-p0"), tr.mensagens("c-p0"))
	}
}

// Número da lista, em qualquer variante, vai ao cérebro e não é transferido.
func TestPilotoTestadorVaiAoCerebro(t *testing.T) {
	variantes := []string{"+5511999990060", "5511999990060", "11999990060", "+551199990060", "(11) 99999-0060"}
	for _, v := range variantes {
		t.Run(v, func(t *testing.T) {
			e, _, tr, br := engineComPiloto(t, "+5511999990060")
			e.processarLote(context.Background(), []Inbound{msgDe("c-p1", v, "oi")})
			if br.total() != 1 {
				t.Fatalf("o testador devia chegar ao cérebro: %d chamadas", br.total())
			}
			if len(tr.transferida("c-p1")) != 0 {
				t.Fatalf("testador não é transferido: %v", tr.transferida("c-p1"))
			}
		})
	}
}

// A lista pode vir com variantes diferentes das do evento (sem +55, sem o 9).
func TestPilotoListaEmOutraVarianteCasa(t *testing.T) {
	e, _, tr, br := engineComPiloto(t, "11999990060")
	e.processarLote(context.Background(), []Inbound{msgDe("c-p1b", "+5511999990060", "oi")})
	if br.total() != 1 || len(tr.transferida("c-p1b")) != 0 {
		t.Fatalf("variante da lista não casou: cérebro=%d", br.total())
	}
}

// Fora da lista: handoff imediato ao departamento padrão, SEM mensagem ao
// cliente, sem cérebro, nota por código, conversa transferida, sem ocorrência
// de falha nem alerta.
func TestPilotoForaDaListaHandoffSemMensagemSemCerebro(t *testing.T) {
	e, st, tr, br := engineComPiloto(t, "+5511999990060")
	alertas := comNotificadorFalso(e)
	ctx := context.Background()
	e.processarLote(ctx, []Inbound{msgDe("c-p2", "+5531988887777", "meu wifi caiu")})

	if br.total() != 0 {
		t.Fatalf("fora da lista não chama o cérebro: %d", br.total())
	}
	if got := tr.mensagens("c-p2"); len(got) != 0 {
		t.Fatalf("nenhuma mensagem ao cliente: %v", got)
	}
	if got := tr.transferida("c-p2"); len(got) != 1 || got[0] != deptHumano {
		t.Fatalf("um handoff ao departamento padrão: %v", got)
	}
	notas := tr.notas("c-p2")
	if len(notas) != 1 || !strings.Contains(notas[0], "motivo: fora do grupo de teste da Sofia") {
		t.Fatalf("nota: %v", notas)
	}
	if strings.Contains(notas[0], "sem chamado") || strings.Contains(notas[0], "31988887777") || strings.Contains(notas[0], "meu wifi") {
		t.Fatalf("nota com ruído ou dado do cliente: %q", notas[0])
	}
	if c, _ := st.ObterConversa(ctx, "c-p2"); c == nil || c.Estado != store.EstadoTransferida {
		t.Fatalf("a conversa devia ficar transferida: %+v", c)
	}
	if f, _ := st.UltimaFalha(ctx, "c-p2"); f != nil {
		t.Fatalf("fora do piloto não é falha: %+v", f)
	}
	if len(alertas.doTipo(alerta.TipoFalhaDefinitiva))+len(alertas.doTipo(alerta.TipoHandoffFalhou)) != 0 {
		t.Fatal("handoff ok fora do piloto não alerta")
	}
}

// Rajada de fora da lista = UM handoff: as mensagens do lote viram uma só
// transferência e o que chegar depois encontra a conversa transferida.
func TestPilotoRajadaDeForaViraUmHandoff(t *testing.T) {
	e, _, tr, br := engineComPiloto(t, "+5511999990060")
	ctx := context.Background()
	e.processarLote(ctx, []Inbound{
		msgDe("c-p3", "+5531988887777", "oi"), msgDe("c-p3", "+5531988887777", "alguém aí?"), msgDe("c-p3", "+5531988887777", "meu wifi caiu"),
	})
	e.processarLote(ctx, []Inbound{msgDe("c-p3", "+5531988887777", "e agora?")})
	if got := tr.transferida("c-p3"); len(got) != 1 {
		t.Fatalf("rajada de fora devia dar um handoff só: %v", got)
	}
	if br.total() != 0 || len(tr.mensagens("c-p3")) != 0 {
		t.Fatalf("cérebro=%d msgs=%v", br.total(), tr.mensagens("c-p3"))
	}
}

// Mídia de quem está fora da lista também não gera o aviso de "não leio anexos".
func TestPilotoMidiaDeForaNaoAvisaOCliente(t *testing.T) {
	e, _, tr, _ := engineComPiloto(t, "+5511999990060")
	m := msgDe("c-p4", "+5531988887777", "[imagem]")
	m.TemMidia = true
	e.processarLote(context.Background(), []Inbound{m})
	if len(tr.mensagens("c-p4")) != 0 || len(tr.transferida("c-p4")) != 1 {
		t.Fatalf("msgs=%v handoffs=%v", tr.mensagens("c-p4"), tr.transferida("c-p4"))
	}
}

// Sem telefone no evento usa o da conversa; sem telefone nenhum, fora (na
// dúvida, humano).
func TestPilotoTelefoneAusenteUsaAConversaOuVaiAHumano(t *testing.T) {
	e, st, tr, br := engineComPiloto(t, "+5511999990060")
	ctx := context.Background()
	_, _ = st.AtualizarConversa(ctx, "c-p5", func(c *store.Conversa, _ bool) bool {
		c.Telefone, c.Estado, c.HandledBy = "+5511999990060", store.EstadoBot, infodesk.ConduzidaPorBotExterno
		return true
	})
	e.processarLote(ctx, []Inbound{msgDe("c-p5", "", "oi")})
	if br.total() != 1 || len(tr.transferida("c-p5")) != 0 {
		t.Fatalf("o telefone da conversa é de testador: cérebro=%d", br.total())
	}
	e.processarLote(ctx, []Inbound{msgDe("c-p6", "", "oi")})
	if br.total() != 1 || len(tr.transferida("c-p6")) != 1 {
		t.Fatalf("sem telefone nenhum vai a humano: cérebro=%d handoffs=%v", br.total(), tr.transferida("c-p6"))
	}
}

// Handoff que falha fora do piloto fica marcado (E3.6): o vigia repete.
func TestPilotoHandoffQueFalhaFicaMarcado(t *testing.T) {
	e, st, tr, _ := engineComPiloto(t, "+5511999990060")
	tr.handoffErr = errors.New("fora")
	e.processarLote(context.Background(), []Inbound{msgDe("c-p7", "+5531988887777", "oi")})
	m := marcaDe(t, st, "c-p7")
	if m == nil || m.Origem != OrigemForaDoPiloto || m.DepartamentoID != deptHumano {
		t.Fatalf("marca: %+v", m)
	}
}

// O log tem o telefone mascarado, nunca o número, e o evento `transferencia`
// leva a origem fora_do_piloto.
func TestPilotoLogComTelefoneMascarado(t *testing.T) {
	e, _, _, _ := engineComPiloto(t, "+5511999990060")
	var buf strings.Builder
	e.log = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e.processarLote(context.Background(), []Inbound{msgDe("c-p8", "+5531988887777", "oi")})
	log := buf.String()
	if !strings.Contains(log, "fora_do_piloto") || !strings.Contains(log, "****7777") || strings.Contains(log, "31988887777") {
		t.Fatalf("log do piloto: %s", log)
	}
	if !strings.Contains(log, `"msg":"transferencia"`) || !strings.Contains(log, `"origem":"fora_do_piloto"`) {
		t.Fatalf("evento transferencia com a origem: %s", log)
	}
}
