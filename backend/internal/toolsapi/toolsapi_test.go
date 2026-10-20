package toolsapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const tokenTeste = "segredo-tools" //nolint:gosec // G101: valor de teste

func novoHandler() *Handler { return New(tokenTeste, nil) }

func do(t *testing.T, h *Handler, method, path, token, body string) (int, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	h.Register(mux)
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("X-Tools-Token", token)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestAuthRecusaSemToken(t *testing.T) {
	h := novoHandler()
	if code, _ := do(t, h, http.MethodPost, "/tools/infodesk/ping", "", "{}"); code != http.StatusUnauthorized {
		t.Fatalf("esperava 401 sem token, veio %d", code)
	}
	if code, _ := do(t, h, http.MethodPost, "/tools/infodesk/ping", "errado", "{}"); code != http.StatusUnauthorized {
		t.Fatalf("esperava 401 com token errado, veio %d", code)
	}
	// Sem token configurado no gateway, nada passa — nem com header.
	if code, _ := do(t, New("", nil), http.MethodGet, "/tools/infodesk/ping", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("token vazio no gateway devia recusar tudo, veio %d", code)
	}
}

func TestPingRespondeGETEPOST(t *testing.T) {
	h := novoHandler()
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		code, out := do(t, h, m, "/tools/infodesk/ping", tokenTeste, "")
		if code != http.StatusOK || out["status"] != true {
			t.Fatalf("%s: esperava 200 {status:true}, veio %d %+v", m, code, out)
		}
	}
	if code, _ := do(t, h, http.MethodPut, "/tools/infodesk/ping", tokenTeste, ""); code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT: esperava 405, veio %d", code)
	}
}

// TestDecodeRecusaCorpoGiganteEJSONInvalido: a borda não confia no tamanho
// nem no formato do input; a recusa segue a convenção {status,erro,motivo}.
func TestDecodeRecusaCorpoGiganteEJSONInvalido(t *testing.T) {
	casos := map[string]string{
		"gigante":  `{"descricao":"` + strings.Repeat("x", corpoMax+1) + `"}`,
		"invalido": `{"telefone": `,
	}
	for nome, body := range casos {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/x", strings.NewReader(body))
		rec := httptest.NewRecorder()
		var v map[string]any
		if decode(rec, req, &v) {
			t.Fatalf("%s: decode devia recusar", nome)
		}
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code != http.StatusBadRequest || out["status"] != false || out["erro"] == nil || out["motivo"] != "json_invalido" {
			t.Fatalf("%s: esperava 400 {status:false, erro, motivo}, veio %d %+v", nome, rec.Code, out)
		}
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/x", strings.NewReader(`{"a":1}`))
	var v map[string]any
	if !decode(httptest.NewRecorder(), req, &v) || v["a"] != float64(1) {
		t.Fatalf("json válido devia decodificar: %+v", v)
	}
}

// TestSanitizarTexto: controle vira espaço, 1 linha colapsa espaços, teto em
// runas com aviso de truncamento.
func TestSanitizarTexto(t *testing.T) {
	got, trunc := sanitizarTexto("  App \x00trava\n ao  bipar\t nota  ", 200, true)
	if got != "App trava ao bipar nota" || trunc {
		t.Fatalf("1 linha: %q trunc=%v", got, trunc)
	}
	got, trunc = sanitizarTexto("linha 1\nlinha\x07 2\n", 200, false)
	if got != "linha 1\nlinha  2" || trunc {
		t.Fatalf("multilinha: %q trunc=%v", got, trunc)
	}
	got, trunc = sanitizarTexto(strings.Repeat("á", 10), 4, true)
	if got != "áááá" || !trunc {
		t.Fatalf("teto em runas: %q trunc=%v", got, trunc)
	}
	if got, _ := sanitizarTexto(" \n\t ", 10, false); got != "" {
		t.Fatalf("só espaço devia virar vazio: %q", got)
	}
}
