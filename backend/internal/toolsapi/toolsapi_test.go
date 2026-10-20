package toolsapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bot-infominas/internal/mongo"
	"bot-infominas/internal/sac"
	"bot-infominas/internal/store"
)

const tokenTeste = "segredo-tools" //nolint:gosec // G101: valor de teste

// fakeSAC implementa SACClient sem rede, registrando o que foi chamado.
type fakeSAC struct {
	cfg         sac.Config
	criarErr    error
	prioErr     error
	relatorErr  error // falha em BuscarRelator (listagem dos chamados do relator)
	chamados    []sac.Chamado
	criados     []sac.NovoChamado
	prioridades []string
	buscas      int // vezes que BuscarRelator foi chamado
}

func (f *fakeSAC) GrupoEmp() string      { return "AZAPERS" }
func (f *fakeSAC) TemContaServico() bool { return true }
func (f *fakeSAC) BuscarConfig(_ context.Context, _ string) (sac.Config, error) {
	return f.cfg, nil
}
func (f *fakeSAC) Criar(_ context.Context, ch sac.NovoChamado) (string, error) {
	f.criados = append(f.criados, ch)
	if f.criarErr != nil {
		return "", f.criarErr
	}
	return "ZPRS25207690", nil
}
func (f *fakeSAC) DefinirPrioridade(_ context.Context, _, prioridade string) error {
	f.prioridades = append(f.prioridades, prioridade)
	return f.prioErr
}
func (f *fakeSAC) BuscarRelator(_ context.Context, _ string) ([]sac.Chamado, error) {
	f.buscas++
	if f.relatorErr != nil {
		return nil, f.relatorErr
	}
	return f.chamados, nil
}
func (f *fakeSAC) LinkChamado(cod, grupo, proto string) string {
	return "https://atendimento.azapfy.com.br/chat/" + cod + "/" + grupo + "/" + proto
}

func cfgPadrao() sac.Config {
	return sac.Config{
		Categorias: []string{"APLICATIVO"},
		Tipos: []sac.Tipo{
			{Nome: "LENTIDÃO OU TRAVAMENTOS", Categoria: "APLICATIVO", Prazo: 1, Item: "PROBLEMA"},
		},
	}
}

// perfilPadrao é a identidade em cache dos testes: cliente da empresa MEARIM
// (≠ AZAPERS, a equipe/desk do SAC — assim os testes distinguem a equipe da
// empresa do cliente).
func perfilPadrao() mongo.Perfil {
	return mongo.Perfil{
		Encontrado: true, Login: "10596693664", Nome: "DANIEL",
		Email:    "daniel.ferraz@azapfy.com.br",
		Empresas: []mongo.Empresa{{GrupoEmpresa: "MEARIM"}},
	}
}

func novoHandler(t *testing.T, sacClient SACClient, identificado bool) *Handler {
	t.Helper()
	if !identificado {
		return novoHandlerComPerfil(t, sacClient, nil, nil)
	}
	perfil := perfilPadrao()
	return novoHandlerComPerfil(t, sacClient, &perfil, nil)
}

// novoHandlerComPerfil monta o handler com a identidade dada em cache (nil =
// ninguém identificado) e um logger opcional (nil = default).
func novoHandlerComPerfil(t *testing.T, sacClient SACClient, perfil *mongo.Perfil, log *slog.Logger) *Handler {
	t.Helper()
	st, err := store.NewSQLite(filepath.Join(t.TempDir(), "tools.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if perfil != nil {
		b, _ := json.Marshal(perfil)
		if err := st.PutIdentity(context.Background(), &store.CachedIdentity{
			Phone: "5531983857490", Login: perfil.Login, Perfil: string(b),
			ExpiresAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatalf("PutIdentity: %v", err)
		}
	}
	return New(st, sacClient, tokenTeste, log)
}

// linhaDeLog acha no buffer (slog JSON, um registro por linha) o registro com
// a `msg` dada.
func linhaDeLog(t *testing.T, buf *bytes.Buffer, msg string) map[string]any {
	t.Helper()
	for l := range strings.Lines(buf.String()) {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil && m["msg"] == msg {
			return m
		}
	}
	t.Fatalf("registro de log %q não encontrado em: %s", msg, buf.String())
	return nil
}

func do(t *testing.T, h *Handler, path, token string, body map[string]any) (int, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	h.Register(mux)
	b, _ := json.Marshal(body)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(string(b)))
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
	h := novoHandler(t, &fakeSAC{cfg: cfgPadrao()}, true)
	code, _ := do(t, h, "/tools/sac/criar", "", map[string]any{"telefone": "5531983857490"})
	if code != http.StatusUnauthorized {
		t.Fatalf("esperava 401 sem token, veio %d", code)
	}
	code, _ = do(t, h, "/tools/sac/criar", "errado", map[string]any{"telefone": "5531983857490"})
	if code != http.StatusUnauthorized {
		t.Fatalf("esperava 401 com token errado, veio %d", code)
	}
}

func TestCriarFelizDefinePrioridadeEMontaLink(t *testing.T) {
	f := &fakeSAC{cfg: cfgPadrao()}
	h := novoHandler(t, f, true)
	_, out := do(t, h, "/tools/sac/criar", tokenTeste, map[string]any{
		"telefone": "5531983857490", "categoria": "aplicativo",
		"ocorrencia": "lentidão ou travamentos", "prioridade": "alta",
		"resumo": "App travando", "descricao": "Trava ao bipar nota",
	})
	if out["status"] != true {
		t.Fatalf("esperava status true: %+v", out)
	}
	if out["protocolo"] != "ZPRS25207690" {
		t.Fatalf("protocolo errado: %+v", out)
	}
	if !strings.Contains(out["link"].(string), "/AZAPERS/ZPRS25207690") {
		t.Fatalf("link mal montado: %v", out["link"])
	}
	if out["prioridade_definida"] != true {
		t.Fatalf("prioridade deveria ter sido definida: %+v", out)
	}
	// Relator vem da identidade (não do LLM); classificação vem da config.
	if len(f.criados) != 1 || f.criados[0].Email != "daniel.ferraz@azapfy.com.br" {
		t.Fatalf("relator não veio da identidade: %+v", f.criados)
	}
	// A empresa do chamado é a do CLIENTE (perfil), não a equipe do desk.
	if f.criados[0].ClienteGrupo != "MEARIM" {
		t.Fatalf("empresa do chamado deveria ser a do perfil (MEARIM): %+v", f.criados[0])
	}
	if f.criados[0].Item != "PROBLEMA" || f.criados[0].Prazo != 1 {
		t.Fatalf("item/prazo não vieram da config: %+v", f.criados[0])
	}
	if len(f.prioridades) != 1 || f.prioridades[0] != "ALTA" {
		t.Fatalf("prioridade normalizada errada: %+v", f.prioridades)
	}
}

func TestCriarOcorrenciaInvalida(t *testing.T) {
	h := novoHandler(t, &fakeSAC{cfg: cfgPadrao()}, true)
	_, out := do(t, h, "/tools/sac/criar", tokenTeste, map[string]any{
		"telefone": "5531983857490", "categoria": "APLICATIVO",
		"ocorrencia": "OCORRENCIA QUE NAO EXISTE", "resumo": "x", "descricao": "y",
	})
	if out["status"] != false || out["motivo"] != "ocorrencia_invalida" {
		t.Fatalf("esperava recusa por ocorrência inválida: %+v", out)
	}
}

func TestCriarNaoIdentificado(t *testing.T) {
	h := novoHandler(t, &fakeSAC{cfg: cfgPadrao()}, false)
	_, out := do(t, h, "/tools/sac/criar", tokenTeste, map[string]any{
		"telefone": "5531000000000", "categoria": "APLICATIVO",
		"ocorrencia": "LENTIDÃO OU TRAVAMENTOS", "resumo": "x", "descricao": "y",
	})
	if out["status"] != false || out["motivo"] != "nao_identificado" {
		t.Fatalf("esperava nao_identificado: %+v", out)
	}
}

func TestCriarPrioridadeBestEffort(t *testing.T) {
	f := &fakeSAC{cfg: cfgPadrao(), prioErr: errSimulado{}}
	h := novoHandler(t, f, true)
	_, out := do(t, h, "/tools/sac/criar", tokenTeste, map[string]any{
		"telefone": "5531983857490", "categoria": "APLICATIVO",
		"ocorrencia": "LENTIDÃO OU TRAVAMENTOS", "resumo": "x", "descricao": "y",
	})
	// Chamado criado mesmo com falha ao definir prioridade.
	if out["status"] != true || out["prioridade_definida"] != false {
		t.Fatalf("chamado deveria existir com prioridade_definida=false: %+v", out)
	}
}

func TestListarSoAbertosComLinks(t *testing.T) {
	f := &fakeSAC{chamados: []sac.Chamado{
		{Protocolo: "ZP1", GrupoEmp: "AZAPERS", Status: "PENDENTE"},
		{Protocolo: "ZP2", GrupoEmp: "AZAPERS", Status: "CONCLUIDO"},
		{Protocolo: "ZP3", GrupoEmp: "AZAPERS", Status: "EM ANDAMENTO"},
	}}
	h := novoHandler(t, f, true)
	_, out := do(t, h, "/tools/sac/listar", tokenTeste, map[string]any{"telefone": "5531983857490"})
	if out["status"] != true {
		t.Fatalf("status: %+v", out)
	}
	lista, _ := out["chamados"].([]any)
	if len(lista) != 2 {
		t.Fatalf("esperava 2 abertos, veio %d: %+v", len(lista), out)
	}
	first := lista[0].(map[string]any)
	if !strings.Contains(first["link"].(string), "/AZAPERS/ZP") {
		t.Fatalf("link do chamado faltando: %+v", first)
	}
}

func TestTiposListaOcorrencias(t *testing.T) {
	h := novoHandler(t, &fakeSAC{cfg: cfgPadrao()}, true)
	_, out := do(t, h, "/tools/sac/tipos", tokenTeste, map[string]any{"telefone": "5531983857490"})
	if out["status"] != true {
		t.Fatalf("status: %+v", out)
	}
	oc, _ := out["ocorrencias"].([]any)
	if len(oc) != 1 {
		t.Fatalf("esperava 1 ocorrência: %+v", out)
	}
}

func TestPrepararDevolveCanonicoSemCriar(t *testing.T) {
	f := &fakeSAC{cfg: cfgPadrao()}
	h := novoHandler(t, f, true)
	_, out := do(t, h, "/tools/sac/preparar", tokenTeste, map[string]any{
		"telefone": "5531983857490", "categoria": "aplicativo",
		"ocorrencia": "lentidão ou travamentos", "prioridade": "alta",
		"resumo": "App\ntravando", "descricao": "Trava ao bipar nota",
	})
	if out["status"] != true {
		t.Fatalf("esperava status true: %+v", out)
	}
	proposta, _ := out["proposta"].(map[string]any)
	if proposta == nil {
		t.Fatalf("esperava proposta no dry-run: %+v", out)
	}
	// Valores canônicos: classificação da config, prioridade normalizada,
	// resumo sanitizado (1 linha) — é isso que o agente apresenta ao cliente.
	if proposta["categoria"] != "APLICATIVO" || proposta["ocorrencia"] != "LENTIDÃO OU TRAVAMENTOS" {
		t.Fatalf("classificação não canônica: %+v", proposta)
	}
	if proposta["prioridade"] != "ALTA" || proposta["resumo"] != "App travando" {
		t.Fatalf("prioridade/resumo não normalizados: %+v", proposta)
	}
	if proposta["empresa"] != "MEARIM" {
		t.Fatalf("empresa não resolvida do perfil: %+v", proposta)
	}
	// Dry-run de verdade: nada foi criado nem priorizado.
	if len(f.criados) != 0 || len(f.prioridades) != 0 {
		t.Fatalf("preparar não pode criar chamado: criados=%d", len(f.criados))
	}
}

func TestPrepararOcorrenciaInvalidaTrazOpcoes(t *testing.T) {
	f := &fakeSAC{cfg: cfgPadrao()}
	h := novoHandler(t, f, true)
	_, out := do(t, h, "/tools/sac/preparar", tokenTeste, map[string]any{
		"telefone": "5531983857490", "categoria": "APLICATIVO",
		"ocorrencia": "NAO EXISTE", "resumo": "x", "descricao": "y",
	})
	if out["status"] != false || out["motivo"] != "ocorrencia_invalida" {
		t.Fatalf("esperava ocorrencia_invalida: %+v", out)
	}
	// A recusa carrega as opções válidas — o agente corrige no próprio loop,
	// sem voltar ao cliente nem gastar outra chamada de tipos.
	opcoes, _ := out["ocorrencias"].([]any)
	if len(opcoes) != 1 {
		t.Fatalf("esperava a lista de opções na recusa: %+v", out)
	}
	if len(f.criados) != 0 {
		t.Fatalf("não deveria ter criado chamado: %+v", f.criados)
	}
}

func TestCriarSanitizaConteudo(t *testing.T) {
	f := &fakeSAC{cfg: cfgPadrao()}
	h := novoHandler(t, f, true)
	_, out := do(t, h, "/tools/sac/criar", tokenTeste, map[string]any{
		"telefone": "5531983857490", "categoria": "APLICATIVO",
		"ocorrencia": "LENTIDÃO OU TRAVAMENTOS",
		"resumo":     "App\ntravando\r\x1b   muito",
		"descricao":  "Linha 1\r\nLinha 2\x00 fim  ",
	})
	if out["status"] != true {
		t.Fatalf("esperava status true: %+v", out)
	}
	if got := f.criados[0].Resumo; got != "App travando muito" {
		t.Fatalf("resumo não sanitizado: %q", got)
	}
	// Descrição preserva \n; \r e controle viram espaço; pontas trimadas.
	if got := f.criados[0].Descricao; !strings.Contains(got, "Linha 1") ||
		!strings.Contains(got, "\nLinha 2") || strings.ContainsAny(got, "\r\x00\x1b") {
		t.Fatalf("descricao não sanitizada: %q", got)
	}
}

func TestCriarRecusaConteudoVazioAposLimpeza(t *testing.T) {
	f := &fakeSAC{cfg: cfgPadrao()}
	h := novoHandler(t, f, true)
	_, out := do(t, h, "/tools/sac/criar", tokenTeste, map[string]any{
		"telefone": "5531983857490", "categoria": "APLICATIVO",
		"ocorrencia": "LENTIDÃO OU TRAVAMENTOS",
		"resumo":     " \r\x00\x1b \t ", "descricao": "descrição válida",
	})
	if out["status"] != false || out["motivo"] != "campo_invalido" {
		t.Fatalf("esperava recusa campo_invalido: %+v", out)
	}
	if len(f.criados) != 0 {
		t.Fatalf("não deveria ter criado chamado: %+v", f.criados)
	}
}

func TestCriarTruncaNoTeto(t *testing.T) {
	f := &fakeSAC{cfg: cfgPadrao()}
	h := novoHandler(t, f, true)
	_, out := do(t, h, "/tools/sac/criar", tokenTeste, map[string]any{
		"telefone": "5531983857490", "categoria": "APLICATIVO",
		"ocorrencia": "LENTIDÃO OU TRAVAMENTOS",
		"resumo":     strings.Repeat("á", 500), "descricao": "ok",
	})
	if out["status"] != true {
		t.Fatalf("esperava status true: %+v", out)
	}
	if n := len([]rune(f.criados[0].Resumo)); n != resumoMax {
		t.Fatalf("resumo deveria ter %d runas, tem %d", resumoMax, n)
	}
}

func TestCriarRecusaCorpoGigante(t *testing.T) {
	f := &fakeSAC{cfg: cfgPadrao()}
	h := novoHandler(t, f, true)
	code, _ := do(t, h, "/tools/sac/criar", tokenTeste, map[string]any{
		"telefone": "5531983857490", "categoria": "APLICATIVO",
		"ocorrencia": "LENTIDÃO OU TRAVAMENTOS",
		"resumo":     "x", "descricao": strings.Repeat("a", corpoMax+1024),
	})
	if code != http.StatusBadRequest {
		t.Fatalf("esperava 400 para corpo além do teto, veio %d", code)
	}
	if len(f.criados) != 0 {
		t.Fatalf("não deveria ter criado chamado: %+v", f.criados)
	}
}

func TestCriarPerfilSemEmpresaRecusa(t *testing.T) {
	f := &fakeSAC{cfg: cfgPadrao()}
	perfil := perfilPadrao()
	perfil.Empresas = nil
	h := novoHandlerComPerfil(t, f, &perfil, nil)
	for _, path := range []string{"/tools/sac/preparar", "/tools/sac/criar"} {
		_, out := do(t, h, path, tokenTeste, map[string]any{
			"telefone": "5531983857490", "categoria": "APLICATIVO",
			"ocorrencia": "LENTIDÃO OU TRAVAMENTOS", "resumo": "x", "descricao": "y",
		})
		// Sem grupo no perfil não há empresa para o chamado: recusa com motivo
		// próprio — nunca um valor fixo no lugar.
		if out["status"] != false || out["motivo"] != "sem_empresa" {
			t.Fatalf("%s: esperava recusa sem_empresa: %+v", path, out)
		}
	}
	if len(f.criados) != 0 {
		t.Fatalf("não deveria ter criado chamado: %+v", f.criados)
	}
}

// TestCriarPerfilSemEmailRecusaSemChamarSAC usa o cliente SAC REAL contra um
// servidor falso que conta requisições: cadastro sem e-mail (o SAC exige
// cod_relator e devolveria 422) é recusado no dry-run e na abertura com motivo
// `sem_email` — e nenhuma requisição chega ao SAC, nem a de config.
func TestCriarPerfilSemEmailRecusaSemChamarSAC(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = io.WriteString(w, `{"status":true,"protocolo":"ZP1","parametros":{}}`)
	}))
	t.Cleanup(srv.Close)
	perfil := perfilPadrao()
	perfil.Email = ""
	h := novoHandlerComPerfil(t, sac.New(sac.Options{BaseURL: srv.URL, GrupoEmp: "AZAPERS"}), &perfil, nil)
	for _, path := range []string{"/tools/sac/preparar", "/tools/sac/criar"} {
		_, out := do(t, h, path, tokenTeste, map[string]any{
			"telefone": "5531983857490", "categoria": "APLICATIVO",
			"ocorrencia": "LENTIDÃO OU TRAVAMENTOS", "resumo": "x", "descricao": "y",
		})
		if out["status"] != false || out["motivo"] != "sem_email" {
			t.Fatalf("%s: esperava recusa sem_email: %+v", path, out)
		}
		if erro, _ := out["erro"].(string); !strings.Contains(erro, "e-mail") {
			t.Fatalf("%s: a recusa precisa explicar o motivo ao cérebro: %+v", path, out)
		}
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("cadastro sem e-mail não pode gerar chamada ao SAC; hits=%d", hits)
	}
}

// TestCriarFalhaDoSACLogaCorpoEConversa: com o cliente SAC REAL e um servidor
// falso devolvendo 422 (corpo ecoando dados do relator), o log de erro do
// gateway traz o corpo higienizado (motivo legível, sem dado pessoal) e o
// conversation_id enviado pelo cérebro; ao cérebro vai só a recusa genérica.
func TestCriarFalhaDoSACLogaCorpoEConversa(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sac/incidente/criar" { // config/buscar
			_, _ = io.WriteString(w, `{"status":true,"parametros":{"categorias":["APLICATIVO"],"tipos":[{"nome":"LENTIDÃO OU TRAVAMENTOS","categoria":"APLICATIVO","prazo":1}]}}`)
			return
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"message":"The given data was invalid.","errors":{"contato_relator.email":["O campo contato relator.email \u00e9 obrigat\u00f3rio."]},"input":{"nome_relator":"DANIEL","cod_relator":"daniel.ferraz@azapfy.com.br","contato_relator":{"telefone":"5531983857490"}}}`)
	}))
	t.Cleanup(srv.Close)
	var buf bytes.Buffer
	perfil := perfilPadrao()
	h := novoHandlerComPerfil(t, sac.New(sac.Options{BaseURL: srv.URL, GrupoEmp: "AZAPERS"}), &perfil,
		slog.New(slog.NewJSONHandler(&buf, nil)))
	_, out := do(t, h, "/tools/sac/criar", tokenTeste, map[string]any{
		"telefone": "5531983857490", "conversation_id": "45", "categoria": "APLICATIVO",
		"ocorrencia": "LENTIDÃO OU TRAVAMENTOS", "resumo": "App travando", "descricao": "Trava ao bipar nota",
	})
	// Para o cérebro, só a recusa genérica — o corpo do SAC não vai ao LLM.
	if out["status"] != false || out["erro"] != "não consegui abrir o chamado agora" {
		t.Fatalf("resposta ao cérebro inesperada: %+v", out)
	}
	linha := linhaDeLog(t, &buf, "toolsapi criar: sac.Criar")
	if linha["conversation_id"] != "45" || linha["level"] != "ERROR" || linha["empresa"] != "MEARIM" {
		t.Fatalf("log sem o id da conversa/empresa: %+v", linha)
	}
	errTxt, _ := linha["err"].(string)
	for _, quer := range []string{"status http 422", "contato_relator.email", "é obrigatório"} {
		if !strings.Contains(errTxt, quer) {
			t.Fatalf("log deveria trazer o corpo do erro (%q): %+v", quer, linha)
		}
	}
	for _, pii := range []string{"DANIEL", "daniel.ferraz@azapfy.com.br", "5531983857490"} {
		if strings.Contains(errTxt, pii) {
			t.Fatalf("log vazou dado pessoal %q: %+v", pii, linha)
		}
	}
}

func TestCriarLogaEmpresaDoChamado(t *testing.T) {
	var buf bytes.Buffer
	perfil := perfilPadrao()
	h := novoHandlerComPerfil(t, &fakeSAC{cfg: cfgPadrao()}, &perfil, slog.New(slog.NewJSONHandler(&buf, nil)))
	_, out := do(t, h, "/tools/sac/criar", tokenTeste, map[string]any{
		"telefone": "5531983857490", "categoria": "APLICATIVO",
		"ocorrencia": "LENTIDÃO OU TRAVAMENTOS", "resumo": "x", "descricao": "y",
	})
	if out["status"] != true {
		t.Fatalf("esperava status true: %+v", out)
	}
	linha := linhaDeLog(t, &buf, "chamado aberto via bot")
	if linha["empresa"] != "MEARIM" || linha["protocolo"] != "ZPRS25207690" {
		t.Fatalf("log do chamado sem a empresa do cliente: %+v", linha)
	}
}

// ---------------------------------------------------------------------------
// ZB-2 — listar os chamados do relator ANTES de preparar/abrir outro. Caso de
// produção (conversa #45): o cliente citou um protocolo existente e o bot
// abriu um chamado novo; nada consultava os abertos.
// ---------------------------------------------------------------------------

func pedidoPadrao(extra map[string]any) map[string]any {
	req := map[string]any{
		"telefone": "5531983857490", "conversation_id": "45", "categoria": "APLICATIVO",
		"ocorrencia": "LENTIDÃO OU TRAVAMENTOS", "resumo": "App travando", "descricao": "Trava ao bipar nota",
	}
	for k, v := range extra {
		req[k] = v
	}
	return req
}

func chamadoAberto(protocolo string) sac.Chamado {
	ch := sac.Chamado{Protocolo: protocolo, GrupoEmp: "AZAPERS", Status: "PENDENTE", DtAbertura: "2026-09-29 10:00:00"}
	ch.Incidente.Resumo = "APP TRAVANDO"
	ch.Incidente.Categoria = "APLICATIVO"
	ch.Incidente.Ocorrencia = "LENTIDÃO OU TRAVAMENTOS"
	return ch
}

// TestPrepararECriarComChamadoAbertoDevolvemAbertosERecusam: relator com um
// chamado em aberto (e um concluído, que não conta) → /preparar e /criar
// recusam com `chamado_aberto`, devolvendo os abertos (protocolo, resumo,
// link) para o modelo mostrá-los; nada é criado.
func TestPrepararECriarComChamadoAbertoDevolvemAbertosERecusam(t *testing.T) {
	f := &fakeSAC{cfg: cfgPadrao(), chamados: []sac.Chamado{
		chamadoAberto("ZPRS25208011"),
		{Protocolo: "ZPRS25207000", GrupoEmp: "AZAPERS", Status: "CONCLUIDO"},
	}}
	h := novoHandler(t, f, true)
	for _, path := range []string{"/tools/sac/preparar", "/tools/sac/criar"} {
		_, out := do(t, h, path, tokenTeste, pedidoPadrao(nil))
		if out["status"] != false || out["motivo"] != "chamado_aberto" {
			t.Fatalf("%s: esperava recusa chamado_aberto: %+v", path, out)
		}
		abertos, _ := out["chamados_abertos"].([]any)
		if len(abertos) != 1 || out["total"] != float64(1) {
			t.Fatalf("%s: esperava 1 chamado aberto na recusa: %+v", path, out)
		}
		ch := abertos[0].(map[string]any)
		if ch["protocolo"] != "ZPRS25208011" || ch["resumo"] != "APP TRAVANDO" ||
			!strings.Contains(ch["link"].(string), "/AZAPERS/ZPRS25208011") {
			t.Fatalf("%s: chamado aberto incompleto: %+v", path, ch)
		}
		if erro, _ := out["erro"].(string); !strings.Contains(erro, "novo_chamado=true") {
			t.Fatalf("%s: a recusa precisa ensinar o pedido explícito ao cérebro: %+v", path, out)
		}
	}
	if len(f.criados) != 0 || len(f.prioridades) != 0 {
		t.Fatalf("relator com chamado aberto não pode gerar chamado novo: %+v", f.criados)
	}
	if f.buscas != 2 {
		t.Fatalf("esperava uma consulta ao relator por chamada, veio %d", f.buscas)
	}
}

// TestPedidoExplicitoDeNovoChamadoPassaEVaiNaProposta: com `novo_chamado=true`
// o dry-run aprova (a proposta carrega o pedido) e a abertura, repetindo-o,
// cria o chamado mesmo com outro em aberto.
func TestPedidoExplicitoDeNovoChamadoPassaEVaiNaProposta(t *testing.T) {
	f := &fakeSAC{cfg: cfgPadrao(), chamados: []sac.Chamado{chamadoAberto("ZPRS25208011")}}
	h := novoHandler(t, f, true)
	_, out := do(t, h, "/tools/sac/preparar", tokenTeste, pedidoPadrao(map[string]any{"novo_chamado": true}))
	if out["status"] != true {
		t.Fatalf("preparar com pedido explícito deveria passar: %+v", out)
	}
	proposta, _ := out["proposta"].(map[string]any)
	if proposta["novo_chamado"] != true {
		t.Fatalf("proposta deveria carregar novo_chamado=true: %+v", proposta)
	}
	_, out = do(t, h, "/tools/sac/criar", tokenTeste, pedidoPadrao(map[string]any{"novo_chamado": true}))
	if out["status"] != true || len(f.criados) != 1 {
		t.Fatalf("criar com pedido explícito deveria abrir: %+v criados=%d", out, len(f.criados))
	}
}

// TestSemChamadoAbertoNaoExigePedidoExplicito: relator sem nada em aberto
// (só concluídos) segue como antes — a proposta sai com novo_chamado=false.
func TestSemChamadoAbertoNaoExigePedidoExplicito(t *testing.T) {
	f := &fakeSAC{cfg: cfgPadrao(), chamados: []sac.Chamado{
		{Protocolo: "ZPRS25207000", GrupoEmp: "AZAPERS", Status: "CONCLUIDO"},
	}}
	h := novoHandler(t, f, true)
	_, out := do(t, h, "/tools/sac/preparar", tokenTeste, pedidoPadrao(nil))
	if out["status"] != true {
		t.Fatalf("sem chamado aberto o preparo deveria passar: %+v", out)
	}
	if proposta, _ := out["proposta"].(map[string]any); proposta["novo_chamado"] != false {
		t.Fatalf("proposta deveria sair com novo_chamado=false: %+v", proposta)
	}
	if _, out = do(t, h, "/tools/sac/criar", tokenTeste, pedidoPadrao(nil)); out["status"] != true || len(f.criados) != 1 {
		t.Fatalf("sem chamado aberto a abertura deveria passar: %+v", out)
	}
}

// TestFalhaAoListarChamadosDoRelatorRecusa: sem conseguir conferir os abertos,
// o gateway não prepara nem abre (não abre no escuro) e explica o motivo.
func TestFalhaAoListarChamadosDoRelatorRecusa(t *testing.T) {
	f := &fakeSAC{cfg: cfgPadrao(), relatorErr: errSimulado{}}
	h := novoHandler(t, f, true)
	for _, path := range []string{"/tools/sac/preparar", "/tools/sac/criar"} {
		_, out := do(t, h, path, tokenTeste, pedidoPadrao(nil))
		if out["status"] != false || out["motivo"] != "chamados_indisponiveis" {
			t.Fatalf("%s: esperava recusa chamados_indisponiveis: %+v", path, out)
		}
	}
	if len(f.criados) != 0 {
		t.Fatalf("não deveria ter criado chamado: %+v", f.criados)
	}
}

type errSimulado struct{}

func (errSimulado) Error() string { return "falha simulada" }
