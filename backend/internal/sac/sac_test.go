package sac

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
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
		ClienteGrupo: "MEARIM", Categoria: "APLICATIVO", Ocorrencia: "LENTIDÃO OU TRAVAMENTOS",
		Item: "PROBLEMA", Prazo: 1, Resumo: "Teste", Descricao: "Teste",
	})
	if err != nil {
		t.Fatalf("Criar erro: %v", err)
	}
	if proto != "ZPRS25207690" {
		t.Fatalf("protocolo inesperado: %q", proto)
	}
	// Confere que o payload leva os campos que tornam o chamado "pronto":
	// equipe (desk) em grupo_emp e a empresa do CLIENTE em incidente.empresa.
	inc, _ := gotBody["incidente"].(map[string]any)
	if gotBody["grupo_emp"] != "AZAPERS" || gotBody["setor"] != "SUPORTE" {
		t.Fatalf("grupo_emp/setor errados: %+v", gotBody)
	}
	if inc["categoria"] != "APLICATIVO" || inc["empresa"] != "MEARIM" {
		t.Fatalf("incidente mal montado: %+v", inc)
	}
	if _, tem := inc["cliente"]; tem {
		t.Fatalf("incidente.cliente não existe no contrato do SAC (a empresa do cliente vai em incidente.empresa): %+v", inc)
	}
}

// TestCriarContratoEmpresaDoCliente é o teste de contrato do payload de
// `criar`: a EQUIPE (grupo_emp) é fixa — AZAPERS, o desk — e a EMPRESA do
// incidente é a do cliente (grupo do perfil), sem valor fixo e sem o campo
// `cliente`, que o SAC ignora. Dois perfis de empresas diferentes garantem que
// um valor fixo não passa. A fixture é SINTÉTICA, no formato que o cliente
// envia hoje (o chamado real ZPRS25208042 citado no backlog não está no repo).
func TestCriarContratoEmpresaDoCliente(t *testing.T) {
	for _, empresa := range []string{"MEARIM", "TRANSPORTES EXEMPLO"} {
		t.Run(empresa, func(t *testing.T) {
			var gotBody map[string]any
			c, _ := fakeSAC(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/sac/incidente/criar" {
					t.Fatalf("path inesperado: %s", r.URL.Path)
				}
				gotBody = decodeBody(t, r)
				_, _ = io.WriteString(w, `{"status":true,"mensagem":"Incidente registrado com sucesso","protocolo":"ZPRS25208042"}`)
			})
			proto, err := c.Criar(context.Background(), NovoChamado{
				NomeRelator: "MARIA RELATORA", Email: "maria@cliente.example", Telefone: "5531999990000",
				ClienteGrupo: empresa, Categoria: "APLICATIVO", Ocorrencia: "LENTIDÃO OU TRAVAMENTOS",
				Item: "PROBLEMA", Prazo: 1, Resumo: "APP TRAVANDO", Descricao: "Trava ao bipar nota",
			})
			if err != nil || proto != "ZPRS25208042" {
				t.Fatalf("Criar: proto=%q err=%v", proto, err)
			}
			esperado := map[string]any{
				"nome_relator":    "MARIA RELATORA",
				"cod_relator":     "maria@cliente.example",
				"contato_relator": map[string]any{"email": "maria@cliente.example", "telefone": "5531999990000"},
				"grupo_emp":       "AZAPERS", // equipe que atende (desk) — fixa
				"incidente": map[string]any{
					"resumo":     "APP TRAVANDO",
					"descricao":  "Trava ao bipar nota",
					"item":       "PROBLEMA",
					"categoria":  "APLICATIVO",
					"ocorrencia": "LENTIDÃO OU TRAVAMENTOS",
					"empresa":    empresa, // empresa do CLIENTE (grupo do perfil)
					"icone":      "none",
					"prazo":      float64(1),
				},
				"anexos":     []any{},
				"status_doc": "",
				"setor":      "SUPORTE",
				"timezone":   "America/Sao_Paulo",
			}
			if !reflect.DeepEqual(gotBody, esperado) {
				t.Fatalf("payload fora do contrato: got=%#v want=%#v", gotBody, esperado)
			}
		})
	}
}

func TestCriarSemEmpresaNaoChamaSAC(t *testing.T) {
	var hits int32
	c, _ := fakeSAC(t, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = io.WriteString(w, `{"status":true,"protocolo":"ZP1"}`)
	})
	_, err := c.Criar(context.Background(), NovoChamado{
		NomeRelator: "MARIA RELATORA", Email: "maria@cliente.example", Telefone: "5531999990000",
		Categoria: "APLICATIVO", Ocorrencia: "LENTIDÃO OU TRAVAMENTOS", Resumo: "x", Descricao: "y",
	})
	if !errors.Is(err, ErrSemEmpresa) {
		t.Fatalf("esperava ErrSemEmpresa (nunca um valor fixo no lugar), veio %v", err)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("sem empresa do cliente não pode haver chamada ao SAC; hits=%d", hits)
	}
}

func TestCriarSemEmailNaoChamaSAC(t *testing.T) {
	var hits int32
	c, _ := fakeSAC(t, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = io.WriteString(w, `{"status":true,"protocolo":"ZP1"}`)
	})
	_, err := c.Criar(context.Background(), NovoChamado{
		NomeRelator: "MARIA RELATORA", Telefone: "5531999990000", ClienteGrupo: "MEARIM",
		Categoria: "APLICATIVO", Ocorrencia: "LENTIDÃO OU TRAVAMENTOS", Resumo: "x", Descricao: "y",
	})
	if !errors.Is(err, ErrSemEmail) {
		t.Fatalf("esperava ErrSemEmail, veio %v", err)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("relator sem e-mail não pode gerar chamada ao SAC (seria 422); hits=%d", hits)
	}
}

func TestCriarStatusFalseViraErro(t *testing.T) {
	c, _ := fakeSAC(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"status":false,"mensagem":"Algo inesperado ocorreu"}`)
	})
	if _, err := c.Criar(context.Background(), NovoChamado{Email: "x@y.com", ClienteGrupo: "MEARIM"}); err == nil {
		t.Fatal("esperava erro quando status:false")
	}
}

// TestErroHTTPTrazCorpoHigienizado: um 422 do SAC vira HTTPError com o corpo no
// erro — legível (JSON do PHP normalizado), sem dado pessoal (o que enviamos
// do relator e o que parecer e-mail/número longo) e com o status.
func TestErroHTTPTrazCorpoHigienizado(t *testing.T) {
	c, _ := fakeSAC(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		// Validação estilo Laravel ecoando a entrada, com unicode escapado como o PHP faz.
		_, _ = io.WriteString(w, `{"message":"The given data was invalid.","errors":{"cod_relator":["O campo cod relator \u00e9 obrigat\u00f3rio."]},"input":{"nome_relator":"MARIA RELATORA","cod_relator":"","contato_relator":{"email":"maria@cliente.example","telefone":"5531999990000"},"cpf":"105.966.936-64","obs":"ligar para (31) 98385-7490"}}`)
	})
	_, err := c.Criar(context.Background(), NovoChamado{
		NomeRelator: "Maria Relatora", Email: "maria@cliente.example", Telefone: "5531999990000",
		ClienteGrupo: "MEARIM", Categoria: "APLICATIVO", Ocorrencia: "LENTIDÃO OU TRAVAMENTOS",
		Resumo: "APP TRAVANDO", Descricao: "Trava ao bipar nota",
	})
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != 422 || httpErr.Path != "/api/sac/incidente/criar" {
		t.Fatalf("esperava HTTPError 422 do criar, veio %v", err)
	}
	msg := err.Error()
	for _, quer := range []string{"status http 422", "The given data was invalid.", "cod_relator", "é obrigatório"} {
		if !strings.Contains(msg, quer) {
			t.Fatalf("erro deveria trazer %q: %s", quer, msg)
		}
	}
	for _, pii := range []string{"MARIA RELATORA", "Maria Relatora", "maria@cliente.example", "5531999990000", "105.966.936-64", "98385-7490"} {
		if strings.Contains(msg, pii) {
			t.Fatalf("erro vazou dado pessoal %q: %s", pii, msg)
		}
	}
}

func TestErroHTTPTruncaECorpoNaoJSON(t *testing.T) {
	// Corpo longo e não-JSON (HTML de erro, com quebras de linha): vira uma
	// linha, cortada no teto — o log não incha nem quebra.
	c, _ := fakeSAC(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "<html>\n<body>\n  Erro interno 500  \n"+strings.Repeat("x", 3000)+"</body></html>")
	})
	_, err := c.BuscarRelator(context.Background(), "maria@cliente.example")
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != 500 {
		t.Fatalf("esperava HTTPError 500, veio %v", err)
	}
	if !strings.Contains(httpErr.Corpo, "<html> <body> Erro interno 500 ") || strings.Contains(httpErr.Corpo, "\n") {
		t.Fatalf("corpo deveria estar achatado numa linha: %q", httpErr.Corpo)
	}
	marca := "… [truncado]"
	if n := utf8.RuneCountInString(httpErr.Corpo); n != corpoErroMax+utf8.RuneCountInString(marca) || !strings.HasSuffix(httpErr.Corpo, marca) {
		t.Fatalf("corpo deveria estar truncado em %d runas (+marca), tem %d", corpoErroMax, n)
	}
}

func TestHigienizarTexto(t *testing.T) {
	casos := []struct {
		nome, entrada string
		redigir       []string
		quer, naoQuer []string
	}{
		{
			nome:    "json do php normalizado e e-mail mascarado",
			entrada: `{"mensagem":"Usu\u00e1rio n\u00e3o encontrado: joao@x.com"}`,
			quer:    []string{"Usuário não encontrado", "[email]"},
			naoQuer: []string{"joao@x.com", "u00e1"},
		},
		{
			nome:    "números longos mascarados, protocolo e status legíveis",
			entrada: "telefone 31 98385-7490, cpf 105.966.936-64, cnpj 12.345.678/0001-90; protocolo ZPRS25208042 status 422",
			quer:    []string{"ZPRS25208042", "status 422", "[numero]"},
			naoQuer: []string{"98385", "105.966", "0001-90"},
		},
		{
			nome:    "valor conhecido redigido mesmo ecoado em JSON e noutra caixa",
			entrada: `{"nome_relator":"MARIA RELATORA","descricao":"Linha 1\nLinha 2"}`,
			redigir: []string{"Maria Relatora", "Linha 1\nLinha 2"},
			quer:    []string{"[omitido]"},
			naoQuer: []string{"MARIA", "Linha"},
		},
		{
			nome:    "controle e espaços viram uma linha; valor curto não é redigido",
			entrada: "a\tb\x00\n\n c",
			redigir: []string{"a"},
			quer:    []string{"a b c"},
		},
	}
	for _, tc := range casos {
		t.Run(tc.nome, func(t *testing.T) {
			got := higienizarTexto(tc.entrada, tc.redigir...)
			for _, q := range tc.quer {
				if !strings.Contains(got, q) {
					t.Fatalf("faltou %q em %q", q, got)
				}
			}
			for _, nq := range tc.naoQuer {
				if strings.Contains(got, nq) {
					t.Fatalf("não deveria conter %q: %q", nq, got)
				}
			}
		})
	}
}

func TestCriarMensagemDeErroSemDadoPessoal(t *testing.T) {
	c, _ := fakeSAC(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"status":false,"mensagem":"Relator maria@cliente.example (5531999990000) sem permissão"}`)
	})
	_, err := c.Criar(context.Background(), NovoChamado{
		Email: "maria@cliente.example", Telefone: "5531999990000", ClienteGrupo: "MEARIM",
	})
	if err == nil || !strings.Contains(err.Error(), "sem permissão") {
		t.Fatalf("esperava o motivo do SAC no erro, veio %v", err)
	}
	if strings.Contains(err.Error(), "maria@") || strings.Contains(err.Error(), "5531999990000") {
		t.Fatalf("erro vazou dado pessoal: %v", err)
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
