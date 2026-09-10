package toolsapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bot-azapfy/internal/mongo"
	"bot-azapfy/internal/sac"
	"bot-azapfy/internal/store"
)

const tokenTeste = "segredo-tools"

// fakeSAC implementa SACClient sem rede, registrando o que foi chamado.
type fakeSAC struct {
	cfg         sac.Config
	criarErr    error
	prioErr     error
	chamados    []sac.Chamado
	criados     []sac.NovoChamado
	prioridades []string
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

func novoHandler(t *testing.T, sacClient SACClient, identificado bool) *Handler {
	t.Helper()
	st, err := store.NewSQLite(filepath.Join(t.TempDir(), "tools.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if identificado {
		perfil := mongo.Perfil{
			Encontrado: true, Login: "10596693664", Nome: "DANIEL",
			Email:    "daniel.ferraz@azapfy.com.br",
			Empresas: []mongo.Empresa{{GrupoEmpresa: "AZAPERS"}},
		}
		b, _ := json.Marshal(perfil)
		if err := st.PutIdentity(context.Background(), &store.CachedIdentity{
			Phone: "5531983857490", Login: perfil.Login, Perfil: string(b),
			ExpiresAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatalf("PutIdentity: %v", err)
		}
	}
	return New(st, sacClient, tokenTeste, nil)
}

func do(t *testing.T, h *Handler, path, token string, body map[string]any) (int, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	h.Register(mux)
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(b)))
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
	if proposta["empresa"] != "AZAPERS" {
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

type errSimulado struct{}

func (errSimulado) Error() string { return "falha simulada" }
