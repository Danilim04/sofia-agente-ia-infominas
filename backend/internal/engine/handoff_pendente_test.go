package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"bot-infominas/internal/alerta"
	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
)

var longeNoFuturo = time.Now().Add(100 * 365 * 24 * time.Hour)

func marcaDe(t *testing.T, st store.Store, conv string) *store.HandoffPendente {
	t.Helper()
	hs, err := st.HandoffsPendentes(context.Background(), longeNoFuturo)
	if err != nil {
		t.Fatal(err)
	}
	for i := range hs {
		if hs[i].ConversationID == conv {
			return &hs[i]
		}
	}
	return nil
}

// Handoff recusado de vez (4xx): a conversa fica transferida e MARCADA, com o
// que o vigia precisa para repetir; o alerta diz que o reconciliador repete.
func TestHandoffRecusadoFicaMarcadoParaRepetir(t *testing.T) {
	e, st, tr, _ := engineCompleta(t)
	comRelogioFalso(e)
	alertas := comNotificadorFalso(e)
	tr.handoffErr = &infodesk.HTTPError{Status: 400, Code: infodesk.CodeValidationError, Rota: "/conversations/x/handoff"}
	ctx := context.Background()

	id := e.TransferirParaHumano(ctx, "c-hp1", Transferencia{Origem: OrigemCliente, Causa: "pediu atendente"})
	if id == "" {
		t.Fatal("a transferência devia devolver o ID da ocorrência")
	}
	if c, _ := st.ObterConversa(ctx, "c-hp1"); c == nil || c.Estado != store.EstadoTransferida {
		t.Fatalf("o bot devia estar calado: %+v", c)
	}
	m := marcaDe(t, st, "c-hp1")
	if m == nil || m.Origem != OrigemCliente || m.IDOcorrencia != id || m.DepartamentoID != deptHumano ||
		!strings.Contains(m.Nota, "Sofia → humano") || m.Tentativas != 0 {
		t.Fatalf("marca de handoff não confirmado: %+v", m)
	}
	al := alertas.doTipo(alerta.TipoHandoffFalhou)
	if len(al) != 1 || !strings.Contains(al[0].Texto, "repetir o handoff") {
		t.Fatalf("alerta: %+v", al)
	}
}

// Falha transitória que esgota as tentativas da engine também deixa a marca.
func TestHandoffTransitorioEsgotadoFicaMarcado(t *testing.T) {
	e, st, tr, _ := engineCompleta(t)
	comRelogioFalso(e)
	tr.handoffErr = &infodesk.HTTPError{Status: 503, Rota: "/conversations/x/handoff"}
	e.TransferirParaHumano(context.Background(), "c-hp2", Transferencia{Origem: OrigemAnexo})
	if marcaDe(t, st, "c-hp2") == nil {
		t.Fatal("handoff que falhou após as tentativas da engine devia ficar marcado")
	}
}

// Handoff que dá certo não deixa marca.
func TestHandoffConfirmadoNaoDeixaMarca(t *testing.T) {
	e, st, tr, _ := engineCompleta(t)
	comRelogioFalso(e)
	e.TransferirParaHumano(context.Background(), "c-hp3", Transferencia{Origem: OrigemCliente})
	if marcaDe(t, st, "c-hp3") != nil || len(tr.transferida("c-hp3")) != 1 {
		t.Fatal("handoff ok: sem marca e um handoff feito")
	}
}

// ReenviarHandoff repete com o departamento e a nota guardados; só o sucesso
// apaga a marca.
func TestReenviarHandoffUsaAMarcaEApagaNoSucesso(t *testing.T) {
	e, st, tr, _ := engineCompleta(t)
	comRelogioFalso(e)
	ctx := context.Background()
	tr.handoffErr = errors.New("fora")
	e.TransferirParaHumano(ctx, "c-hp4", Transferencia{Origem: OrigemCliente})
	m := marcaDe(t, st, "c-hp4")
	if m == nil {
		t.Fatal("sem marca")
	}

	if err := e.ReenviarHandoff(ctx, *m); err == nil {
		t.Fatal("com o Infodesk ainda fora devia devolver o erro")
	}
	if marcaDe(t, st, "c-hp4") == nil {
		t.Fatal("falha não apaga a marca")
	}

	tr.mu.Lock()
	tr.handoffErr = nil
	tr.mu.Unlock()
	if err := e.ReenviarHandoff(ctx, *m); err != nil {
		t.Fatal(err)
	}
	if marcaDe(t, st, "c-hp4") != nil {
		t.Fatal("sucesso apaga a marca")
	}
	if got := tr.transferida("c-hp4"); len(got) != 1 || got[0] != deptHumano {
		t.Fatalf("handoff repetido devia ir ao departamento da marca: %v", got)
	}
	if notas := tr.notas("c-hp4"); len(notas) != 1 || notas[0] != m.Nota {
		t.Fatalf("handoff repetido devia levar a nota da marca: %v", notas)
	}
}

// Os eventos que tornam o handoff desnecessário (ou o confirmam) apagam a marca;
// o que não diz nada sobre ele, não.
func TestEventosLimpamAMarcaDeHandoffNaoConfirmado(t *testing.T) {
	casos := []struct {
		nome   string
		evento func(conv string, quando time.Time) ConversaVista
		limpa  bool
	}{
		{"conversation.handoff", func(c string, q time.Time) ConversaVista {
			v := vistaDe(c, infodesk.ConduzidaPorFila, q)
			v.Handoff = true
			return v
		}, true},
		{"atendente assumiu", func(c string, q time.Time) ConversaVista {
			return vistaDe(c, infodesk.ConduzidaPorAtendente, q)
		}, true},
		{"conversation.finished", func(c string, q time.Time) ConversaVista {
			v := vistaDe(c, infodesk.ConduzidaPorBotExterno, q)
			v.Encerrada = true
			return v
		}, true},
		{"mensagem do cliente sem mudar nada", func(c string, q time.Time) ConversaVista {
			v := vistaDe(c, infodesk.ConduzidaPorBotExterno, q)
			v.DoCliente, v.MsgID, v.MsgEm = true, "m-1", q
			return v
		}, false},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			e, st, tr, _ := engineCompleta(t)
			comRelogioFalso(e)
			ctx := context.Background()
			conv := "c-ev-" + strings.ReplaceAll(c.nome, " ", "-")
			tr.handoffErr = errors.New("fora")
			e.TransferirParaHumano(ctx, conv, Transferencia{Origem: OrigemCliente})
			if marcaDe(t, st, conv) == nil {
				t.Fatal("sem marca para começar")
			}
			res, err := e.AplicarConversa(ctx, c.evento(conv, e.agora().Add(time.Minute)))
			if err != nil || !res.Aplicado {
				t.Fatalf("evento devia ser aplicado: %+v %v", res, err)
			}
			if got := marcaDe(t, st, conv) == nil; got != c.limpa {
				t.Fatalf("marca limpa = %v, queria %v", got, c.limpa)
			}
		})
	}
}

// Reentrega velha (mais velha que o estado local) não pode apagar a marca do
// episódio atual.
func TestEventoVelhoNaoLimpaAMarca(t *testing.T) {
	e, st, tr, _ := engineCompleta(t)
	comRelogioFalso(e)
	ctx := context.Background()
	tr.handoffErr = errors.New("fora")
	e.TransferirParaHumano(ctx, "c-hp5", Transferencia{Origem: OrigemCliente})
	v := vistaDe("c-hp5", infodesk.ConduzidaPorFila, e.agora().Add(-time.Hour))
	v.Handoff = true
	if res, _ := e.AplicarConversa(ctx, v); res.Aplicado {
		t.Fatal("o evento velho devia ser ignorado")
	}
	if marcaDe(t, st, "c-hp5") == nil {
		t.Fatal("evento velho não apaga a marca")
	}
}

// Nova sessão com o bot conduzindo volta a conversa a `bot`: a marca do
// episódio anterior não pode sobreviver.
func TestNovaSessaoLimpaAMarca(t *testing.T) {
	e, st, tr, _ := engineCompleta(t)
	comRelogioFalso(e)
	ctx := context.Background()
	tr.handoffErr = errors.New("fora")
	e.TransferirParaHumano(ctx, "c-hp6", Transferencia{Origem: OrigemCliente})
	quando := e.agora().Add(time.Minute)
	inicial := vistaDe("c-hp6", infodesk.ConduzidaPorBotExterno, quando)
	inicial.SessaoIniciadaEm = quando.Add(-time.Second)
	_, _ = e.AplicarConversa(ctx, inicial) // grava a sessão atual; segue transferida
	nova := vistaDe("c-hp6", infodesk.ConduzidaPorBotExterno, quando.Add(time.Hour))
	nova.DoCliente, nova.MsgID, nova.MsgEm, nova.SessaoIniciadaEm = true, "m-9", quando.Add(time.Hour), quando.Add(time.Hour)
	if res, _ := e.AplicarConversa(ctx, nova); res.Estado != store.EstadoBot {
		t.Fatalf("nova sessão devia reabrir o bot: %+v", res)
	}
	if marcaDe(t, st, "c-hp6") != nil {
		t.Fatal("a marca do episódio anterior devia ter sido apagada")
	}
}

// Cada origem tem o seu motivo na nota interna, sem PII e sem texto livre.
func TestNotaPorOrigem(t *testing.T) {
	casos := []struct {
		nome    string
		t       Transferencia
		ticket  int
		contem  []string
		naoTem  []string
		semNota string
	}{
		{"vigia", Transferencia{Origem: OrigemVigia, Espera: 5 * time.Minute, Detalhe: "BOT_TIMEOUT_HANDOFF: parado há 6m"},
			0, []string{"motivo: sem resposta da Sofia em 5 min", "sem chamado"}, []string{"BOT_TIMEOUT"}, ""},
		{"vigia sem espera", Transferencia{Origem: OrigemVigia},
			0, []string{"motivo: sem resposta da Sofia no prazo"}, nil, ""},
		{"ticket_pendente", Transferencia{Origem: OrigemTicketPendente, Causa: "texto livre"},
			0, []string{"motivo: chamado possivelmente criado, confira no Desk antes de abrir outro"}, []string{"sem chamado", "texto livre"}, ""},
		{"ticket_aberto", Transferencia{Origem: OrigemTicketAberto, Detalhe: "chamado nº 4521"},
			4521, []string{"motivo: chamado #4521 aberto"}, []string{"chamado #4521 ·", "· chamado #4521", "sem chamado"}, ""},
		{"ticket_aberto sem número gravado", Transferencia{Origem: OrigemTicketAberto},
			0, []string{"motivo: chamado aberto"}, nil, ""},
		{"origem desconhecida", Transferencia{Origem: "inventada"},
			0, []string{"motivo: transferido pela Sofia"}, nil, ""},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			e, st, tr, _ := engineCompleta(t)
			comRelogioFalso(e)
			ctx := context.Background()
			conv := "c-nota-" + strings.ReplaceAll(c.nome, " ", "-")
			_, _ = st.AtualizarConversa(ctx, conv, func(cv *store.Conversa, _ bool) bool {
				cv.Telefone, cv.Estado, cv.TicketNumber = "+5531990000009", store.EstadoBot, c.ticket
				return true
			})
			e.TransferirParaHumano(ctx, conv, c.t)
			notas := tr.notas(conv)
			if len(notas) != 1 {
				t.Fatalf("notas = %v", notas)
			}
			nota := notas[0]
			for _, q := range c.contem {
				if !strings.Contains(nota, q) {
					t.Errorf("nota sem %q: %q", q, nota)
				}
			}
			for _, q := range c.naoTem {
				if strings.Contains(nota, q) {
					t.Errorf("nota não devia ter %q: %q", q, nota)
				}
			}
			if strings.Contains(nota, "+5531990000009") {
				t.Errorf("PII na nota: %q", nota)
			}
		})
	}
}

// O chatbot interno do Desk (INTERNAL_BOT sem atribuição) cala a Sofia mas não é
// atendente: a marca fica, para o vigia ainda avisar um humano.
func TestChatbotInternoNaoLimpaAMarca(t *testing.T) {
	e, st, tr, _ := engineCompleta(t)
	comRelogioFalso(e)
	ctx := context.Background()
	tr.handoffErr = errors.New("fora")
	e.TransferirParaHumano(ctx, "c-hp7", Transferencia{Origem: OrigemCliente})
	res, _ := e.AplicarConversa(ctx, vistaDe("c-hp7", infodesk.ConduzidaPorBotInterno, e.agora().Add(time.Minute)))
	if !res.Aplicado {
		t.Fatal("evento devia ser aplicado")
	}
	if marcaDe(t, st, "c-hp7") == nil {
		t.Fatal("INTERNAL_BOT não é humano: a marca devia ficar")
	}
}
