package toolsapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bot-infominas/internal/store"
)

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
	return rec.Code, jsonMapa(rec.Body.Bytes())
}

func TestAuthRecusaSemToken(t *testing.T) {
	h := New(Deps{Token: tokenTeste})
	for _, rota := range []string{"ping", "ticket/preparar", "ticket/abrir", "ticket/consultar", "ticket/listar", "contato/identificar", "conversa/transferir"} {
		if code, _ := do(t, h, http.MethodPost, "/tools/infodesk/"+rota, "", "{}"); code != http.StatusUnauthorized {
			t.Fatalf("%s: esperava 401 sem token, veio %d", rota, code)
		}
		if code, _ := do(t, h, http.MethodPost, "/tools/infodesk/"+rota, "errado", "{}"); code != http.StatusUnauthorized {
			t.Fatalf("%s: esperava 401 com token errado, veio %d", rota, code)
		}
	}
	// Sem token configurado no gateway, nada passa — nem com header.
	if code, _ := do(t, New(Deps{}), http.MethodGet, "/tools/infodesk/ping", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("token vazio no gateway devia recusar tudo, veio %d", code)
	}
}

func TestPingRespondeGETEPOSTEOutrasRotasSoPOST(t *testing.T) {
	h := New(Deps{Token: tokenTeste})
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		code, out := do(t, h, m, "/tools/infodesk/ping", tokenTeste, "")
		if code != http.StatusOK || out["status"] != true {
			t.Fatalf("%s: esperava 200 {status:true}, veio %d %+v", m, code, out)
		}
	}
	if code, _ := do(t, h, http.MethodPut, "/tools/infodesk/ping", tokenTeste, ""); code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT: esperava 405, veio %d", code)
	}
	if code, _ := do(t, h, http.MethodGet, "/tools/infodesk/ticket/listar", tokenTeste, ""); code != http.StatusMethodNotAllowed {
		t.Fatalf("GET numa rota de tool: esperava 405, veio %d", code)
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
		out := jsonMapa(rec.Body.Bytes())
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

func TestTelefoneToleraONonoDigito(t *testing.T) {
	if !mesmoTelefone("+553199990001", "+5531999990001") || !mesmoTelefone("5531999990001", "+55 (31) 99999-0001") {
		t.Fatal("o 9º dígito e a pontuação não podem separar o mesmo telefone")
	}
	if mesmoTelefone("+5531999990001", "+5531999990002") || mesmoTelefone("", "") {
		t.Fatal("telefones diferentes (ou vazios) não são o mesmo")
	}
	if got := mascararTelefone(telTeste); got != "****0001" {
		t.Fatalf("máscara: %q", got)
	}
}

func TestCPFValido(t *testing.T) {
	for cpf, ok := range map[string]bool{"12345678909": true, "11111111111": false, "12345678900": false, "1234": false} {
		if cpfValido(cpf) != ok {
			t.Fatalf("cpfValido(%s) != %v", cpf, ok)
		}
	}
}

// ---------------------------------------------------------------------------
// contato/identificar
// ---------------------------------------------------------------------------

func TestIdentificarGravaHashEMascaradoPreservandoORequesterDoLookup(t *testing.T) {
	a := novoAmb(t)
	a.conversa(convTeste, telTeste)
	a.contato(store.Contato{Telefone: telTeste, Origem: store.OrigemLookup, RequesterID: "r-1", ClienteID: "k-1", NomeCadastro: "Maria Souza"})

	code, out := a.post("contato/identificar", map[string]any{"telefone": telTeste, "conversation_id": convTeste, "cpf": "123.456.789-09"})
	if code != 200 || out["status"] != true || out["forma"] != "cpf" || out["identificado"] != true {
		t.Fatalf("identificar: %d %+v", code, out)
	}
	if out["cliente_id"] != "k-1" || out["nome_cadastro"] != "Maria Souza" {
		t.Fatalf("devia devolver o que o lookup achou: %+v", out)
	}
	c, _ := a.st.ObterContato(context.Background(), telTeste, a.h.agora())
	if c == nil || c.Forma != store.FormaCPF || c.Origem != store.OrigemCPF || c.RequesterID != "r-1" || c.ClienteID != "k-1" {
		t.Fatalf("contato gravado: %+v", c)
	}
	if c.ValorMascarado != "***.***.789-**" || len(c.ValorHash) != 64 || strings.Contains(c.ValorHash, "12345678909") {
		t.Fatalf("só hash + mascarado: %+v", c)
	}
	if ttl := c.ExpiresAt.Sub(a.h.agora()); ttl < 23*60*60*1e9 || ttl > 25*60*60*1e9 {
		t.Fatalf("TTL esperado ≈ 24h, veio %v", ttl)
	}
	if strings.Contains(a.log(), "12345678909") || strings.Contains(a.log(), "99999") {
		t.Fatalf("CPF/telefone em claro no log:\n%s", a.log())
	}
}

func TestIdentificarPorContratoERecusas(t *testing.T) {
	a := novoAmb(t)
	a.conversa(convTeste, telTeste)

	_, out := a.post("contato/identificar", map[string]any{"telefone": telTeste, "conversation_id": convTeste, "contrato": "000123456"})
	if out["status"] != true || out["forma"] != "contrato" || out["cliente_id"] != nil || out["nome_cadastro"] != nil {
		t.Fatalf("contrato: %+v", out)
	}
	if c, _ := a.st.ObterContato(context.Background(), telTeste, a.h.agora()); c == nil || c.ValorMascarado != "******456" {
		t.Fatalf("contrato mascarado: %+v", c)
	}

	casos := []struct {
		nome string
		body map[string]any
		want string
	}{
		{"cpf com dígito errado", map[string]any{"cpf": "12345678900"}, "campo_invalido"},
		{"contrato fora do padrão", map[string]any{"contrato": "ab"}, "campo_invalido"},
		{"nada informado", map[string]any{}, "nao_identificado"},
	}
	for _, c := range casos {
		c.body["telefone"], c.body["conversation_id"] = telTeste, convTeste
		code, out := a.post("contato/identificar", c.body)
		if code != 200 || out["status"] != false || out["motivo"] != c.want || out["erro"] == nil {
			t.Fatalf("%s: %d %+v", c.nome, code, out)
		}
	}
}

// ---------------------------------------------------------------------------
// conversa/transferir
// ---------------------------------------------------------------------------

func TestTransferirPeloCaminhoUnico(t *testing.T) {
	a := novoAmb(t)
	a.conversa(convTeste, telTeste)

	code, out := a.post("conversa/transferir", map[string]any{"conversation_id": convTeste, "motivo": "pedido_cliente", "categoria": "sem_conexao"})
	if code != 200 || out["status"] != true || out["mensagem_ao_cliente"] == nil {
		t.Fatalf("transferir: %d %+v", code, out)
	}
	if got := a.tr.origens(); len(got) != 1 || got[0] != "cliente" {
		t.Fatalf("devia ir pelo caminho único da engine com origem cliente: %v", got)
	}
	if m := a.tr.chamadas[0].Mensagem; m != out["mensagem_ao_cliente"] {
		t.Fatalf("o gateway envia o MESMO texto que devolve ao LLM: %q vs %v", m, out["mensagem_ao_cliente"])
	}
	_, out = a.post("conversa/transferir", map[string]any{"conversation_id": convTeste, "motivo": "fora_do_escopo"})
	if out["status"] != true || a.tr.chamadas[1].Origem != "acao" {
		t.Fatalf("fora_do_escopo: %+v / %+v", out, a.tr.chamadas)
	}
}

func TestTransferirJaTransferidaEEnumFechado(t *testing.T) {
	a := novoAmb(t)
	a.conversa(convTeste, telTeste)
	if _, err := a.st.AtualizarConversa(context.Background(), convTeste, func(c *store.Conversa, _ bool) bool {
		c.Estado = store.EstadoTransferida
		return true
	}); err != nil {
		t.Fatal(err)
	}
	_, out := a.post("conversa/transferir", map[string]any{"conversation_id": convTeste, "motivo": "pedido_cliente"})
	if out["motivo"] != "ja_transferida" || len(a.tr.chamadas) != 0 {
		t.Fatalf("conversa transferida não transfere de novo: %+v / %v", out, a.tr.chamadas)
	}

	// A engine pode concluir que já não é do bot (humano assumiu entre o check e a chamada).
	b := novoAmb(t)
	b.tr.ja = true
	_, out = b.post("conversa/transferir", map[string]any{"conversation_id": convTeste, "motivo": "nao_resolvido"})
	if out["motivo"] != "ja_transferida" {
		t.Fatalf("engine devolveu '' (já não era do bot): %+v", out)
	}

	// Motivo fora do enum e categoria malformada nunca chegam à engine.
	for _, body := range []map[string]any{
		{"conversation_id": convTeste, "motivo": "porque_sim"},
		{"conversation_id": convTeste, "motivo": "pedido_cliente", "categoria": "x; DROP"},
		{"motivo": "pedido_cliente"},
	} {
		_, out = b.post("conversa/transferir", body)
		if out["status"] != false || out["motivo"] != "campo_invalido" {
			t.Fatalf("%v: %+v", body, out)
		}
	}
}

func TestTransferirNomeiaODepartamentoQueAEngineUsa(t *testing.T) {
	a := novoAmb(t)
	a.conversa(convTeste, telTeste)
	_, out := a.post("conversa/transferir", map[string]any{"conversation_id": convTeste, "motivo": "pedido_cliente"})
	dep, _ := out["departamento"].(map[string]any)
	if dep["nome"] != "Atendimento" || !strings.Contains(out["mensagem_ao_cliente"].(string), "equipe de Atendimento") {
		t.Fatalf("o nome é o do DEPARTAMENTO_HUMANO_PADRAO (o que a engine usa): %+v", out)
	}
}
