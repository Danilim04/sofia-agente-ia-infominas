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
		"digitosColadosEmLetras": {
			corpo:  `{"message":"contact5531990000000 duplicado; ticket INF-4521 ok"}`,
			quer:   "contact[numero] duplicado; ticket INF-4521 ok",
			naoTem: []string{"5531"},
		},
		"doisNumerosSeguidos": {
			corpo:  `{"message":"12345678909 31999990001"}`,
			quer:   "[numero]", // o espaço conta como separador: vira uma máscara só
			naoTem: []string{"1234", "3199"},
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

func TestHTTPError_CodeDecideTransitorio(t *testing.T) {
	casos := []struct {
		nome   string
		status int
		code   string
		quer   bool
	}{
		// Transitórios pelo code.
		{"rateLimited", 429, CodeRateLimited, true},
		{"internalError", 500, CodeInternalError, true},
		// Definitivos / configuração pelo code (06-erros.md).
		{"unauthorized", 401, CodeUnauthorized, false},
		{"forbidden", 403, CodeForbidden, false},
		{"notFound", 404, CodeNotFound, false},
		{"validation", 400, CodeValidationError, false},
		{"invalidReference", 400, CodeInvalidReference, false},
		{"conflict", 409, CodeConflict, false},
		{"janelaWhatsAppFechada", 409, CodeWhatsAppWindowClosed, false},
		{"whatsappNaoConfigurado", 409, CodeWhatsAppNotConfigured, false},
		{"templateNaoEncontrado", 404, CodeTemplateNotFound, false},
		{"templateNaoAprovado", 422, CodeTemplateNotApproved, false},
		{"templateParams", 422, CodeTemplateParamsMismatch, false},
		{"midiaUrl", 400, CodeMediaURLRequired, false},
		{"midiaDownload", 422, CodeMediaDownloadFailed, false},
		// O code vence o status quando o conhecemos.
		{"codeDefinitivoEm5xx", 503, CodeNotFound, false},
		{"codeTransitorioEm4xx", 400, CodeRateLimited, true},
		// Fallback por status HTTP quando o code vem vazio.
		{"semCode429", 429, "", true},
		{"semCode500", 500, "", true},
		{"semCode502", 502, "", true},
		{"semCode503", 503, "", true},
		{"semCode504", 504, "", true},
		{"semCode400", 400, "", false},
		{"semCode401", 401, "", false},
		{"semCode404", 404, "", false},
		{"semCode409", 409, "", false},
		// Code desconhecido também cai no status.
		{"desconhecido503", 503, "NOVO_CODIGO_DA_API", true},
		{"desconhecido429", 429, "NOVO_CODIGO_DA_API", true},
		{"desconhecido400", 400, "NOVO_CODIGO_DA_API", false},
		{"desconhecido422", 422, "NOVO_CODIGO_DA_API", false},
	}
	for _, cs := range casos {
		t.Run(cs.nome, func(t *testing.T) {
			e := &HTTPError{Status: cs.status, Code: cs.code}
			if e.Transitorio() != cs.quer || errors.Is(e, ErrTransitorio) != cs.quer || Transitorio(e) != cs.quer {
				t.Errorf("Transitorio = %v, quer %v", e.Transitorio(), cs.quer)
			}
		})
	}
}

func TestHTTPError_ErrorIncluiCode(t *testing.T) {
	e := &HTTPError{Status: 409, Code: CodeWhatsAppWindowClosed, Rota: "/conversations/c1/messages", Mensagem: "janela fechada"}
	if quer := "infodesk /conversations/c1/messages: status http 409 [WHATSAPP_WINDOW_CLOSED]: janela fechada"; e.Error() != quer {
		t.Errorf("Error() = %q, quer %q", e.Error(), quer)
	}
	if quer := "infodesk /desks: status http 429 [RATE_LIMITED]"; (&HTTPError{Status: 429, Code: CodeRateLimited, Rota: "/desks"}).Error() != quer {
		t.Errorf("sem mensagem: %q", (&HTTPError{Status: 429, Code: CodeRateLimited, Rota: "/desks"}).Error())
	}
}

func TestExtrairCode(t *testing.T) {
	casos := map[string]string{
		`{"statusCode":409,"code":"WHATSAPP_WINDOW_CLOSED","message":"x","lastInboundAt":"2026-10-18T09:12:00.000Z"}`: "WHATSAPP_WINDOW_CLOSED",
		`{"statusCode":429,"code":"RATE_LIMITED","message":"x"}`:                                                      "RATE_LIMITED",
		`{"statusCode":401,"message":"Chave inválida"}`:                                                               "", // formato antigo, sem code
		`{"code":null}`:                              "",
		`{"code":42}`:                                "",
		`{"code":""}`:                                "",
		`{"code":"minusculo"}`:                       "",
		`{"code":"COM ESPACO"}`:                      "",
		`{"code":"SQL'; DROP TABLE"}`:                "",
		`{"code":"` + strings.Repeat("A", 80) + `"}`: "", // longo demais
		`<html>erro</html>`:                          "",
		``:                                           "",
		`[1,2]`:                                      "",
	}
	for corpo, quer := range casos {
		if got := extrairCode([]byte(corpo)); got != quer {
			t.Errorf("%.60q: code = %q, quer %q", corpo, got, quer)
		}
	}
}
