package sac

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSAC sobe um servidor que finge ser o backend SAC.
func fakeSAC(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := New(Options{
		BaseURL:    srv.URL,
		PortalURL:  "https://atendimento.azapfy.com.br",
		ServiceCod: "10596693664",
		GrupoEmp:   "AZAPERS",
		Empresa:    "AZAPFY",
		Timezone:   "America/Sao_Paulo",
		ConfigTTL:  time.Minute,
	})
	return c, srv
}

func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	b, _ := io.ReadAll(r.Body)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("body inválido em %s: %v", r.URL.Path, err)
	}
	return m
}

func TestCriarSucessoComAvisoSMTP(t *testing.T) {
	var gotBody map[string]any
	c, _ := fakeSAC(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sac/incidente/criar" {
			t.Fatalf("path inesperado: %s", r.URL.Path)
		}
		gotBody = decodeBody(t, r)
		// status:true mesmo com notificacao de erro de e-mail (530) — deve ser ok.
		_, _ = io.WriteString(w, `{"notificacao":{"mensagem":"530 erro smtp"},"status":true,"mensagem":"Incidente registrado com sucesso","protocolo":"ZPRS25207690"}`)
	})

	proto, err := c.Criar(context.Background(), NovoChamado{
		NomeRelator: "DANIEL", Email: "daniel.ferraz@azapfy.com.br", Telefone: "31983857490",
		ClienteGrupo: "AZAPERS", Categoria: "APLICATIVO", Ocorrencia: "LENTIDÃO OU TRAVAMENTOS",
		Item: "PROBLEMA", Prazo: 1, Resumo: "Teste", Descricao: "Teste",
	})
	if err != nil {
		t.Fatalf("Criar erro: %v", err)
	}
	if proto != "ZPRS25207690" {
		t.Fatalf("protocolo inesperado: %q", proto)
	}
	// Confere que o payload leva os campos que tornam o chamado "pronto".
	inc, _ := gotBody["incidente"].(map[string]any)
	if gotBody["grupo_emp"] != "AZAPERS" || gotBody["setor"] != "SUPORTE" {
		t.Fatalf("grupo_emp/setor errados: %+v", gotBody)
	}
	if inc["categoria"] != "APLICATIVO" || inc["empresa"] != "AZAPFY" || inc["cliente"] != "AZAPERS" {
		t.Fatalf("incidente mal montado: %+v", inc)
	}
}

func TestCriarStatusFalseViraErro(t *testing.T) {
	c, _ := fakeSAC(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"status":false,"mensagem":"Algo inesperado ocorreu"}`)
	})
	if _, err := c.Criar(context.Background(), NovoChamado{Email: "x@y.com"}); err == nil {
		t.Fatal("esperava erro quando status:false")
	}
}

func TestDefinirPrioridadeUsaCodServico(t *testing.T) {
	var gotBody map[string]any
	c, _ := fakeSAC(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sac/incidente/editar" {
			t.Fatalf("path inesperado: %s", r.URL.Path)
		}
		gotBody = decodeBody(t, r)
		_, _ = io.WriteString(w, `{"status":true,"mensagem":"Incidente editado com sucesso"}`)
	})
	if err := c.DefinirPrioridade(context.Background(), "ZPRS25207690", "MEDIA"); err != nil {
		t.Fatalf("DefinirPrioridade erro: %v", err)
	}
	if gotBody["cod"] != "10596693664" || gotBody["prioridade"] != "MEDIA" || gotBody["protocolo"] != "ZPRS25207690" {
		t.Fatalf("payload de editar errado: %+v", gotBody)
	}
	if _, temIncidente := gotBody["incidente"]; temIncidente {
		t.Fatal("editar de prioridade não deve enviar 'incidente' (sobrescreveria)")
	}
}

func TestDefinirPrioridadeSemContaServico(t *testing.T) {
	c := New(Options{BaseURL: "http://nao-usado", GrupoEmp: "AZAPERS"})
	if err := c.DefinirPrioridade(context.Background(), "ZP1", "ALTA"); err == nil {
		t.Fatal("esperava erro sem SAC_SERVICE_COD")
	}
}

func TestBuscarRelatorFiltraAbertos(t *testing.T) {
	c, _ := fakeSAC(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody := decodeBody(t, r)
		if gotBody["cod"] != "joao@x.com" {
			t.Fatalf("cod (email) errado: %+v", gotBody)
		}
		_, _ = io.WriteString(w, `{"status":true,"dados":[
			{"protocolo":"ZP1","grupo_emp":"AZAPERS","status":"PENDENTE","incidente":{"resumo":"A"}},
			{"protocolo":"ZP2","grupo_emp":"AZAPERS","status":"CONCLUIDO","incidente":{"resumo":"B"}},
			{"protocolo":"ZP3","grupo_emp":"AZAPERS","status":"EM ANDAMENTO","incidente":{"resumo":"C"}}
		]}`)
	})
	chamados, err := c.BuscarRelator(context.Background(), "joao@x.com")
	if err != nil {
		t.Fatalf("BuscarRelator erro: %v", err)
	}
	var abertos int
	for _, ch := range chamados {
		if ch.Aberto() {
			abertos++
		}
	}
	if abertos != 2 {
		t.Fatalf("esperava 2 abertos (PENDENTE, EM ANDAMENTO), veio %d", abertos)
	}
}

func TestBuscarConfigCacheiaEAcharTipo(t *testing.T) {
	var hits int32
	c, _ := fakeSAC(t, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = io.WriteString(w, `{"status":true,"parametros":{
			"categorias":["APLICATIVO","SISTEMA WEB"],
			"setores":["SUPORTE",{"nome":"FINANCEIRO"}],
			"tipos":[
				{"nome":"LENTIDÃO OU TRAVAMENTOS","categoria":"APLICATIVO","prazo":1,"descricao":"..."},
				{"nome":"AZP OU DEVOLUÇÃO","categoria":"SISTEMA WEB","prazo":2.5},
				{"nome":"SEM CATEGORIA","prazo":1}
			]}}`)
	})
	cfg, err := c.BuscarConfig(context.Background(), "AZAPERS")
	if err != nil {
		t.Fatalf("BuscarConfig erro: %v", err)
	}
	if len(cfg.TiposValidos()) != 2 {
		t.Fatalf("esperava 2 tipos válidos (com categoria), veio %d", len(cfg.TiposValidos()))
	}
	tipo, ok := cfg.AcharTipo("aplicativo", "lentidão ou travamentos")
	if !ok || tipo.Prazo != 1 || tipo.Categoria != "APLICATIVO" {
		t.Fatalf("AcharTipo não casou (tolerante a caixa): %+v ok=%v", tipo, ok)
	}
	// 2ª chamada deve sair do cache (sem novo hit no servidor).
	if _, err := c.BuscarConfig(context.Background(), "AZAPERS"); err != nil {
		t.Fatalf("BuscarConfig 2 erro: %v", err)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("config deveria vir do cache na 2ª chamada; hits=%d", hits)
	}
}

func TestLinkChamadoFormato(t *testing.T) {
	c := New(Options{PortalURL: "https://atendimento.azapfy.com.br", GrupoEmp: "AZAPERS"})
	got := c.LinkChamado("daniel.ferraz@azapfy.com.br", "AZAPERS", "ZPRS25207690")
	want := "https://atendimento.azapfy.com.br/chat/daniel.ferraz@azapfy.com.br/AZAPERS/ZPRS25207690"
	if got != want {
		t.Fatalf("link errado:\n got=%s\nwant=%s", got, want)
	}
}
