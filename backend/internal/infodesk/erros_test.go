package infodesk

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestHigienizarCorpo_PII(t *testing.T) {
	casos := map[string]struct {
		corpo  string
		quer   string
		naoTem []string
	}{
		"envelopeComEmailETelefone": {
			corpo:  `{"statusCode":400,"message":"Contato maria.silva@gmail.com com telefone +55 (31) 99000-0000 já existe"}`,
			quer:   "Contato [email] com telefone +[numero] já existe",
			naoTem: []string{"maria", "99000"},
		},
		"cpfComPontuacao": {
			corpo:  `{"statusCode":422,"message":"CPF 123.456.789-09 inválido"}`,
			quer:   "CPF [numero] inválido",
			naoTem: []string{"123.456"},
		},
		"codigoHTTPFicaLegivel": {
			corpo: `erro 503 no upstream`,
			quer:  "erro 503 no upstream",
		},
		"listaDeMensagens": {
			corpo: `{"statusCode":400,"message":["title must be a string","deskId must be a UUID"],"error":"Bad Request"}`,
			quer:  "title must be a string; deskId must be a UUID",
		},
		"naoJSON": {
			corpo: "<html>\n\t<body>Gateway\r\nTimeout</body></html>",
			quer:  "<html> <body>Gateway Timeout</body></html>",
		},
		"vazio": {corpo: "", quer: ""},
		"jsonSemMessage": {
			corpo: `{"statusCode":500}`,
			quer:  `{"statusCode":500}`,
		},
		"messageNumerica": {
			corpo: `{"message":42}`,
			quer:  `{"message":42}`,
		},
	}
	for nome, cs := range casos {
		t.Run(nome, func(t *testing.T) {
			got := higienizarCorpo([]byte(cs.corpo))
			if got != cs.quer {
				t.Errorf("got %q, quer %q", got, cs.quer)
			}
			for _, n := range cs.naoTem {
				if strings.Contains(got, n) {
					t.Errorf("PII vazou: %q em %q", n, got)
				}
			}
		})
	}
}

func TestHigienizarTexto_Trunca(t *testing.T) {
	longo := strings.Repeat("ã", 800)
	got := higienizarTexto(longo)
	if !strings.HasSuffix(got, "… [truncado]") {
		t.Fatalf("sem marca de truncamento: %q", got[len(got)-30:])
	}
	if n := utf8.RuneCountInString(strings.TrimSuffix(got, "… [truncado]")); n != corpoErroMax {
		t.Errorf("runas = %d, esperado %d", n, corpoErroMax)
	}
	if got := higienizarTexto(strings.Repeat("a", corpoErroMax)); strings.Contains(got, "truncado") {
		t.Error("texto no limite não deve ser truncado")
	}
}

func TestErroHTTP_LeNoMaximo8KiB(t *testing.T) {
	grande := strings.Repeat("x", 64<<10) + " FIM-DO-CORPO"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(grande))
	}))
	t.Cleanup(srv.Close)
	err := New(Options{BaseURL: srv.URL}).Encerrar(context.Background(), "c1")
	var he *HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("esperado *HTTPError: %v", err)
	}
	if strings.Contains(he.Mensagem, "FIM-DO-CORPO") {
		t.Error("leu além do teto de 8 KiB")
	}
	if utf8.RuneCountInString(he.Mensagem) > corpoErroMax+len("… [truncado]") {
		t.Errorf("mensagem com %d runas", utf8.RuneCountInString(he.Mensagem))
	}
}

func TestHTTPError_ErrorETransitorio(t *testing.T) {
	semCorpo := &HTTPError{Status: 502, Rota: "/desks"}
	if semCorpo.Error() != "infodesk /desks: status http 502" {
		t.Errorf("Error() = %q", semCorpo.Error())
	}
	comCorpo := &HTTPError{Status: 403, Rota: "/tickets", Mensagem: "Escopo insuficiente"}
	if comCorpo.Error() != "infodesk /tickets: status http 403: Escopo insuficiente" {
		t.Errorf("Error() = %q", comCorpo.Error())
	}
	for status, quer := range map[int]bool{400: false, 401: false, 403: false, 404: false, 409: false, 422: false, 429: true, 500: true, 502: true, 503: true, 504: true} {
		e := &HTTPError{Status: status}
		if e.Transitorio() != quer || errors.Is(e, ErrTransitorio) != quer {
			t.Errorf("%d: Transitorio = %v, quer %v", status, e.Transitorio(), quer)
		}
	}
	if (&HTTPError{Status: 500}).Is(errors.New("outro")) {
		t.Error("Is só responde a ErrTransitorio")
	}
}

func TestErroRede(t *testing.T) {
	causa := errors.New("connection refused")
	e := &ErroRede{Rota: "/users", Err: causa}
	if !strings.Contains(e.Error(), "/users") || !strings.Contains(e.Error(), "connection refused") {
		t.Errorf("Error() = %q", e.Error())
	}
	if !errors.Is(e, causa) || !e.Transitorio() || !Transitorio(e) {
		t.Error("rede deve ser transitória e embrulhar a causa")
	}
	cancelado := &ErroRede{Rota: "/users", Err: context.Canceled}
	if cancelado.Transitorio() || Transitorio(cancelado) {
		t.Error("cancelamento não é transitório")
	}
	if cancelado.Is(errors.New("outro")) {
		t.Error("Is só responde a ErrTransitorio")
	}
}

func TestErroResposta(t *testing.T) {
	e := &ErroResposta{Rota: "/tickets/1", Motivo: "JSON inválido"}
	if e.Error() != "infodesk /tickets/1: resposta inválida: JSON inválido" {
		t.Errorf("Error() = %q", e.Error())
	}
	if Transitorio(e) {
		t.Error("não é transitório")
	}
}

func TestTransitorio_Helpers(t *testing.T) {
	if Transitorio(nil) {
		t.Error("nil não é transitório")
	}
	if Transitorio(errors.New("qualquer")) {
		t.Error("erro genérico não é transitório")
	}
	if !Transitorio(ErrTransitorio) {
		t.Error("a sentinela é transitória")
	}
}
