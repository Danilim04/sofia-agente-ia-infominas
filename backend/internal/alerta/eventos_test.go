package alerta

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// registroJSON escreve num slog JSON e devolve o único registro emitido.
func registroJSON(t *testing.T, emitir func(*slog.Logger)) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	emitir(slog.New(slog.NewJSONHandler(&buf, nil)))
	var m map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &m); err != nil {
		t.Fatalf("log não é um JSON: %v: %q", err, buf.String())
	}
	return m
}

// camposDe devolve os atributos do registro (sem time/level/msg), ordenados.
func camposDe(m map[string]any) []string {
	var ks []string
	for k := range m {
		if k != "time" && k != "level" && k != "msg" {
			ks = append(ks, k)
		}
	}
	sort.Strings(ks)
	return ks
}

func esperados(evento string) []string {
	ks := append([]string(nil), CamposDoEvento[evento]...)
	sort.Strings(ks)
	return ks
}

func igual(a, b []string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

func TestEventoSofiaAlertaTemNomeECamposDoContrato(t *testing.T) {
	m := registroJSON(t, func(l *slog.Logger) {
		Log{L: l}.Notificar(context.Background(), Alerta{
			Tipo: TipoHandoffFalhou, Titulo: "Handoff falhou", Texto: "ocorrência SOF-20261022-0900-01",
			IDOcorrencia: "SOF-20261022-0900-01", ConversationID: "conv-1", Link: "http://x/conv-1",
		})
	})
	if m["msg"] != "sofia_alerta" || m["level"] != "ERROR" {
		t.Fatalf("nome/nível: %v %v", m["msg"], m["level"])
	}
	if got, quer := camposDe(m), esperados(EventoSofiaAlerta); !igual(got, quer) {
		t.Fatalf("campos = %v, contrato = %v", got, quer)
	}
}

func TestEventoTicketAbertoTemNomeECamposDoContrato(t *testing.T) {
	m := registroJSON(t, func(l *slog.Logger) {
		LogTicketAberto(l, TicketAberto{Numero: 4821, ConversationID: "conv-1", Categoria: "sem_conexao", Mesa: "Abertura de Ticket", Reconciliado: true})
	})
	if m["msg"] != "ticket_aberto" || m["numero"] != float64(4821) || m["reconciliado"] != true {
		t.Fatalf("registro: %v", m)
	}
	if got, quer := camposDe(m), esperados(EventoTicketAberto); !igual(got, quer) {
		t.Fatalf("campos = %v, contrato = %v", got, quer)
	}
}

func TestEventoTransferenciaTemNomeECamposDoContrato(t *testing.T) {
	m := registroJSON(t, func(l *slog.Logger) {
		LogTransferencia(l, Transferencia{IDOcorrencia: "SOF-1", ConversationID: "conv-1", Origem: "frustracao", Departamento: "Suporte"})
	})
	if m["msg"] != "transferencia" || m["origem"] != "frustracao" {
		t.Fatalf("registro: %v", m)
	}
	if got, quer := camposDe(m), esperados(EventoTransferencia); !igual(got, quer) {
		t.Fatalf("campos = %v, contrato = %v", got, quer)
	}
}

func TestContratoDeCamposNaoTemPII(t *testing.T) {
	proibidos := []string{"cpf", "telefone", "phone", "nome", "email", "contato", "mensagem"}
	for evento, campos := range CamposDoEvento {
		for _, c := range campos {
			for _, p := range proibidos {
				if c == p {
					t.Errorf("evento %s tem campo de PII %q", evento, c)
				}
			}
		}
	}
}

func TestMascararPII(t *testing.T) {
	casos := []struct{ entrada, saida string }{
		{"cliente 529.982.247-25 sem sinal", "cliente <CPF> sem sinal"},
		{"cpf 52998224725", "cpf <CPF>"},
		{"tel +5531999990001 e (31) 99999-0001", "tel <TEL> e <TEL>"},
		{"fale com maria@exemplo.com.br", "fale com <EMAIL>"},
		{"contrato 123456789012 vencido", "contrato <NUM> vencido"},
		{"ocorrência SOF-20261022-0900-01 (conversa c1d2e3f4a5b6)", "ocorrência SOF-20261022-0900-01 (conversa c1d2e3f4a5b6)"},
		{"ticket 4821 na mesa 7", "ticket 4821 na mesa 7"},
		{"", ""},
	}
	for _, c := range casos {
		if got := MascararPII(c.entrada); got != c.saida {
			t.Errorf("MascararPII(%q) = %q, quer %q", c.entrada, got, c.saida)
		}
	}
}

func TestSofiaAlertaMascaraPIIDoTextoEDoTitulo(t *testing.T) {
	m := registroJSON(t, func(l *slog.Logger) {
		Log{L: l}.Notificar(context.Background(), Alerta{
			Tipo: TipoFalhaDefinitiva, Titulo: "Falha do cliente 529.982.247-25",
			Texto: "erro externo citou +5531999990001", ConversationID: "conv-1",
		})
	})
	linha, _ := json.Marshal(m)
	for _, proibido := range []string{"529.982.247-25", "5531999990001"} {
		if strings.Contains(string(linha), proibido) {
			t.Fatalf("PII no log: %s", linha)
		}
	}
}

func TestWebhookMascaraPIIAntesDeSair(t *testing.T) {
	var recebido string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		recebido = string(b)
	}))
	defer srv.Close()
	Webhook{URL: srv.URL, HTTP: srv.Client()}.Notificar(context.Background(), Alerta{
		Tipo: TipoFalhaDefinitiva, Titulo: "cpf 529.982.247-25", Texto: "telefone +5531999990001",
	})
	if recebido == "" || strings.Contains(recebido, "529.982.247-25") || strings.Contains(recebido, "5531999990001") {
		t.Fatalf("webhook levou PII ou não chamou: %q", recebido)
	}
}
