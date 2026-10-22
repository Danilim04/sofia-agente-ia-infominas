package toolsapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
)

// Testes de contrato (ADR 0006): o gateway responde, para cada cenário, exatamente
// o que está em contratos/tools-infodesk/<área>/<ação>/ — mesmas chaves, mesmos
// valores onde o contrato fixa um valor (null no contrato = qualquer valor),
// mesmo `erro` e `motivo`. O lado Python carrega os mesmos arquivos.

const dirContratos = "../../../contratos/tools-infodesk"

func lerContrato(t *testing.T, endpoint, arquivo string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dirContratos, filepath.FromSlash(endpoint), arquivo))
	if err != nil {
		t.Fatalf("contrato %s/%s: %v", endpoint, arquivo, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("contrato %s/%s: JSON inválido: %v", endpoint, arquivo, err)
	}
	return m
}

// conferir compara a resposta com o JSON do contrato: mesmas chaves em todos os
// níveis; folhas iguais, salvo `null` no contrato (aceita qualquer valor) e
// as chaves em `livres` (só o tipo é conferido).
func conferir(t *testing.T, caminho string, esperado, obtido any, livres map[string]bool) {
	t.Helper()
	if esperado == nil {
		return // null no contrato = depende de pendência/ambiente
	}
	switch e := esperado.(type) {
	case map[string]any:
		o, ok := obtido.(map[string]any)
		if !ok {
			t.Errorf("%s: esperava objeto, veio %T (%v)", caminho, obtido, obtido)
			return
		}
		for k := range e {
			if _, tem := o[k]; !tem {
				t.Errorf("%s.%s: chave do contrato ausente na resposta", caminho, k)
			}
		}
		for k := range o {
			if _, tem := e[k]; !tem {
				t.Errorf("%s.%s: chave na resposta que o contrato não tem", caminho, k)
			}
		}
		for k, v := range e {
			if ov, tem := o[k]; tem {
				conferir(t, caminho+"."+k, v, ov, livres)
			}
		}
	case []any:
		o, ok := obtido.([]any)
		if !ok {
			t.Errorf("%s: esperava lista, veio %T", caminho, obtido)
			return
		}
		if len(e) > 0 && len(o) == 0 {
			t.Errorf("%s: lista vazia, o contrato tem item", caminho)
			return
		}
		if len(e) > 0 {
			conferir(t, caminho+"[0]", e[0], o[0], livres)
		}
	default:
		if obtido == nil {
			t.Errorf("%s: o contrato tem valor (%v) e a resposta veio null", caminho, esperado)
			return
		}
		chave := caminho[strings.LastIndex(caminho, ".")+1:]
		if livres[chave] {
			if fmt.Sprintf("%T", esperado) != fmt.Sprintf("%T", obtido) {
				t.Errorf("%s: tipo %T no contrato, %T na resposta", caminho, esperado, obtido)
			}
			return
		}
		if fmt.Sprint(esperado) != fmt.Sprint(obtido) {
			t.Errorf("%s: contrato %v, resposta %v", caminho, esperado, obtido)
		}
	}
}

// cobertura registra os arquivos de contrato exercitados; o teste final falha
// se algum arquivo de contratos/tools-infodesk/** ficou sem teste.
type cobertura struct {
	vistos map[string]bool
}

func (c *cobertura) marca(endpoint, arquivo string) { c.vistos[endpoint+"/"+arquivo] = true }

func TestContrato_ToolsInfodesk(t *testing.T) {
	cob := &cobertura{vistos: map[string]bool{}}
	agoraFixo := time.Date(2026, 10, 20, 14, 33, 10, 0, time.FixedZone("", -3*3600))

	// ok roda um cenário: prep monta o ambiente, req vem do request.json
	// (alterado por mut), e a resposta é conferida com `arquivo`.
	tentar := func(endpoint, arquivo string, mods []func(*Deps), prep func(a *amb), mut func(req map[string]any), livres map[string]bool) {
		t.Run(endpoint+"/"+arquivo, func(t *testing.T) {
			a := novoAmb(t, mods...)
			a.conversa(convTeste, telTeste)
			if prep != nil {
				prep(a)
			}
			req := lerContrato(t, endpoint, "request.json")
			if mut != nil {
				mut(req)
			}
			code, got := a.post(endpoint, req)
			if code != http200 {
				t.Fatalf("HTTP %d (erro de negócio é sempre 200)", code)
			}
			esperado := lerContrato(t, endpoint, arquivo)
			if (esperado["status"] == true) != (got["status"] == true) {
				t.Fatalf("status: contrato %v, resposta %v (%+v)", esperado["status"], got["status"], got)
			}
			conferir(t, "$", esperado, got, livres)
			cob.marca(endpoint, arquivo)
			cob.marca(endpoint, "request.json")
		})
	}
	semMod := []func(*Deps){}
	cpf := func(a *amb) { a.identificadoPorCPF() }

	// --- contato/identificar -------------------------------------------------
	tentar("contato/identificar", "ok.json", semMod, nil, nil, nil)
	tentar("contato/identificar", "erro-campo_invalido.json", semMod, nil, func(r map[string]any) { r["cpf"] = "12345678900" }, nil)
	tentar("contato/identificar", "erro-nao_identificado.json", semMod, nil, func(r map[string]any) { r["cpf"], r["contrato"] = "", "" }, nil)

	// --- ticket/preparar -----------------------------------------------------
	tentar("ticket/preparar", "ok.json", semMod, cpf, nil, nil)
	tentar("ticket/preparar", "erro-campo_invalido.json", semMod, cpf, func(r map[string]any) { r["titulo"] = "" }, nil)
	tentar("ticket/preparar", "erro-categoria_sem_mapa.json", semMod, cpf, func(r map[string]any) { r["categoria"] = "nao_existe" }, nil)
	tentar("ticket/preparar", "erro-nao_identificado.json", semMod, nil, nil, nil)
	tentar("ticket/preparar", "erro-referencia_indisponivel.json", semMod, func(a *amb) {
		cpf(a)
		a.h.d.Taxonomia = novaTaxonomiaSemCarga(t, a.idk)
		a.idk.errRef = errors.New("fora do ar")
	}, nil, nil)
	tentar("ticket/preparar", "erro-chamado_aberto.json", semMod, func(a *amb) {
		cpf(a)
		a.h.d.Store = storeComConsulta{Store: a.st, tickets: []store.Ticket{{
			Chave: "k", ConversationID: "outra", Telefone: telTeste, Numero: 48213, Categoria: "sem_conexao",
			Status: store.TicketAberto, CreatedAt: time.Now().Add(-time.Hour).In(time.FixedZone("", -3*3600)),
		}}}
		a.idk.listados = []infodesk.Ticket{ticketJSON(map[string]any{"number": 48213, "title": "Internet sem conexão desde ontem à noite"})}
	}, nil, map[string]bool{"aberto_em": true})

	// --- ticket/abrir --------------------------------------------------------
	tentar("ticket/abrir", "ok.json", semMod, cpf, nil, nil)
	tentar("ticket/abrir", "erro-sem_proposta.json", semMod, cpf, func(r map[string]any) { delete(r, "proposta") }, nil)
	tentar("ticket/abrir", "erro-escopo_insuficiente.json", semMod, func(a *amb) {
		cpf(a)
		a.idk.postErr = &infodesk.HTTPError{Status: 403, Code: infodesk.CodeForbidden}
	}, nil, nil)
	tentar("ticket/abrir", "erro-infodesk_indisponivel.json", semMod, func(a *amb) {
		cpf(a)
		a.idk.postErr = &infodesk.HTTPError{Status: 503, Code: infodesk.CodeInternalError}
	}, nil, nil)
	tentar("ticket/abrir", "erro-em_andamento.json", semMod, func(a *amb) {
		cpf(a)
		a.idk.postErr = &infodesk.HTTPError{Status: 503, Code: infodesk.CodeInternalError}
		a.abrir("sem_conexao") // 1ª chamada deixa a linha pendente; a 2ª (a do contrato) cai no TTL
	}, nil, nil)

	// --- ticket/consultar ----------------------------------------------------
	proprio := func(a *amb) {
		a.idk.tickets[48213] = ticketJSON(map[string]any{
			"number": 48213, "title": "Internet sem conexão desde ontem à noite", "conversationId": convTeste,
			"createdAt": "2026-10-20T14:33:10-03:00", "desk": map[string]any{"id": idMesaSuporte, "name": "Suporte Técnico"},
		})
	}
	tentar("ticket/consultar", "ok.json", semMod, proprio, nil, nil)
	tentar("ticket/consultar", "erro-nao_encontrado.json", semMod, nil, nil, nil)
	tentar("ticket/consultar", "erro-posse_nao_verificada.json", semMod, func(a *amb) {
		a.idk.tickets[48213] = ticketJSON(map[string]any{"number": 48213, "contactPhone": "+5531977770000"})
	}, nil, nil)
	tentar("ticket/consultar", "erro-infodesk_indisponivel.json", semMod, func(a *amb) {
		a.idk.ticketErr = &infodesk.HTTPError{Status: 502}
	}, nil, nil)

	// --- ticket/listar -------------------------------------------------------
	tentar("ticket/listar", "ok.json", semMod, func(a *amb) {
		a.h.d.Store = storeComConsulta{Store: a.st, tickets: []store.Ticket{{
			Chave: "k", Telefone: telTeste, Numero: 48213, Categoria: "sem_conexao", Status: store.TicketAberto, CreatedAt: agoraFixo,
		}}}
		a.idk.listados = []infodesk.Ticket{ticketJSON(map[string]any{
			"number": 48213, "title": "Internet sem conexão desde ontem à noite", "createdAt": "2026-10-20T14:33:10-03:00",
		})}
	}, nil, nil)
	tentar("ticket/listar", "erro-chamados_indisponiveis.json", semMod, func(a *amb) {
		a.idk.listErr = &infodesk.ErroRede{Rota: "/tickets", Err: errors.New("timeout")}
	}, nil, nil)

	// --- cadastro/preparar e cadastro/cadastrar (A8, ADR 0009) ----------------
	comCad := func(ajusta func(fc *fakeCad)) func(a *amb) {
		return func(a *amb) {
			fc := &fakeCad{fakeIdk: a.idk, clientes: []infodesk.Cliente{{ID: idAcme, Nome: "ACME Ltda"}}}
			if ajusta != nil {
				ajusta(fc)
			}
			a.h.d.Infodesk = fc
		}
	}
	chaveLivre := map[string]bool{"chave": true}
	tentar("cadastro/preparar", "ok.json", semMod, comCad(nil), nil, chaveLivre)
	tentar("cadastro/preparar", "erro-campo_invalido.json", semMod, comCad(nil), func(r map[string]any) { r["nome"] = "Maria" }, nil)
	tentar("cadastro/preparar", "erro-ja_cadastrado.json", semMod, comCad(func(fc *fakeCad) { fc.lookup = lookupComSolicitantes(t, 1) }), nil, nil)
	tentar("cadastro/preparar", "erro-empresa_nao_encontrada.json", semMod, comCad(func(fc *fakeCad) { fc.clientes = nil }), nil, nil)
	tentar("cadastro/preparar", "erro-empresa_ambigua.json", semMod, comCad(func(fc *fakeCad) {
		fc.clientes = []infodesk.Cliente{{ID: idAcme, Nome: "ACME Ltda"}, {ID: idAcmeBrasil, Nome: "ACME Brasil S.A."}}
	}), func(r map[string]any) { r["empresa"] = "ACME" }, nil)
	tentar("cadastro/preparar", "erro-cadastro_transferido.json", semMod, func(a *amb) {
		comCad(func(fc *fakeCad) { fc.clientes = nil })(a)
		a.prepararCadastro("Maria Souza", "ACME Ltda", "maria@acme.com.br") // 1ª falha; a 2ª (a do contrato) esgota as tentativas
	}, nil, nil)
	tentar("cadastro/preparar", "erro-escopo_insuficiente.json", semMod, comCad(func(fc *fakeCad) {
		fc.clientesErr = &infodesk.HTTPError{Status: 403, Code: infodesk.CodeForbidden}
	}), nil, nil)
	tentar("cadastro/preparar", "erro-infodesk_indisponivel.json", semMod, comCad(func(fc *fakeCad) {
		fc.lookupErr = &infodesk.HTTPError{Status: 503, Code: infodesk.CodeInternalError}
	}), nil, nil)

	// `cadastrar`: o request.json do contrato leva uma chave ilustrativa; os
	// cenários que precisam de proposta trocam pela chave real que o `preparar` gravou.
	var chaveReal string
	preparada := func(ajusta func(fc *fakeCad)) func(a *amb) {
		return func(a *amb) {
			comCad(ajusta)(a)
			chaveReal = chaveDaProposta(t, a.prepararCadastro("Maria Souza", "ACME Ltda", "maria@acme.com.br"))
		}
	}
	comChaveReal := func(r map[string]any) { r["proposta"].(map[string]any)["chave"] = chaveReal }
	tentar("cadastro/cadastrar", "ok.json", semMod, preparada(nil), comChaveReal, nil)
	tentar("cadastro/cadastrar", "erro-sem_proposta.json", semMod, comCad(nil), nil, nil)
	tentar("cadastro/cadastrar", "erro-escopo_insuficiente.json", semMod, preparada(func(fc *fakeCad) {
		fc.criarErr = &infodesk.HTTPError{Status: 403, Code: infodesk.CodeForbidden}
	}), comChaveReal, nil)
	tentar("cadastro/cadastrar", "erro-infodesk_indisponivel.json", semMod, preparada(func(fc *fakeCad) {
		fc.criarErr = &infodesk.HTTPError{Status: 503, Code: infodesk.CodeInternalError}
	}), comChaveReal, nil)

	// --- conversa/transferir -------------------------------------------------
	suporte := []func(*Deps){func(d *Deps) { d.Opcoes.DepartamentoHumanoPadrao = idDeptSuporte }}
	tentar("conversa/transferir", "ok.json", suporte, nil, nil, nil)
	tentar("conversa/transferir", "erro-ja_transferida.json", suporte, func(a *amb) {
		_, _ = a.st.AtualizarConversa(context.Background(), convTeste, func(c *store.Conversa, _ bool) bool {
			c.Estado = store.EstadoTransferida
			return true
		})
	}, nil, nil)

	// `erro-infodesk_indisponivel` de conversa/transferir: o contrato o mantém
	// (E1.6) mas, nesta etapa, o gateway não consegue emiti-lo — a falha do
	// handoff é tratada dentro da engine (alerta handoff_falhou + vigia N1), que
	// não está na zona da E3.1. TODO(P11): a engine devolver o erro do handoff.
	naoEmitidos := map[string]string{
		"conversa/transferir/erro-infodesk_indisponivel.json": "o handoff falha DENTRO da engine (não volta ao toolsapi)",
	}

	// Todo arquivo de contrato precisa ter sido exercitado (ou justificado).
	t.Run("cobertura", func(t *testing.T) {
		var faltam []string
		_ = filepath.WalkDir(dirContratos, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.HasSuffix(p, "_notas.md") {
				return err
			}
			rel, _ := filepath.Rel(dirContratos, p)
			rel = filepath.ToSlash(rel)
			if _, just := naoEmitidos[rel]; just {
				return nil
			}
			if !cob.vistos[rel] {
				faltam = append(faltam, rel)
			}
			return nil
		})
		sort.Strings(faltam)
		if len(faltam) > 0 {
			t.Fatalf("arquivos de contrato sem teste: %v", faltam)
		}
	})
}

const http200 = 200

// TestContrato_ChaveDeIdempotenciaDoRequest: o request.json de `abrir` traz o
// sha256 real da fórmula do ADR 0005 — o gateway e o Python calculam igual.
func TestContrato_ChaveDeIdempotenciaDoRequest(t *testing.T) {
	req := lerContrato(t, "ticket/abrir", "request.json")
	p := req["proposta"].(map[string]any)
	want := chaveIdempotencia(req["conversation_id"].(string), p["titulo"].(string), p["categoria"].(string), p["criada_em"].(string))
	if req["chave_idempotencia"] != want {
		t.Fatalf("chave_idempotencia do contrato %v ≠ sha256(conv+titulo+categoria+criada_em) %s", req["chave_idempotencia"], want)
	}
}

// TestContrato_MotivosDoCatalogo: todo `motivo` de erro de cada endpoint existe
// nos arquivos erro-*.json (as chaves e motivos não mudam — nota da E1.6).
func TestContrato_MotivosDoCatalogo(t *testing.T) {
	esperados := map[string][]string{
		"contato/identificar": {"campo_invalido", "nao_identificado"},
		"ticket/preparar":     {"campo_invalido", "categoria_sem_mapa", "chamado_aberto", "nao_identificado", "referencia_indisponivel"},
		"ticket/abrir":        {"sem_proposta", "em_andamento", "infodesk_indisponivel", "escopo_insuficiente"},
		"ticket/consultar":    {"nao_encontrado", "posse_nao_verificada", "infodesk_indisponivel"},
		"ticket/listar":       {"chamados_indisponiveis"},
		"conversa/transferir": {"ja_transferida", "infodesk_indisponivel"},
		"cadastro/preparar":   {"campo_invalido", "ja_cadastrado", "empresa_nao_encontrada", "empresa_ambigua", "cadastro_transferido", "escopo_insuficiente", "infodesk_indisponivel"},
		"cadastro/cadastrar":  {"sem_proposta", "escopo_insuficiente", "infodesk_indisponivel"},
	}
	for ep, motivos := range esperados {
		for _, m := range motivos {
			e := lerContrato(t, ep, "erro-"+m+".json")
			if e["motivo"] != m || e["status"] != false || e["erro"] == nil {
				t.Errorf("%s/erro-%s.json: %+v", ep, m, e)
			}
		}
	}
}
