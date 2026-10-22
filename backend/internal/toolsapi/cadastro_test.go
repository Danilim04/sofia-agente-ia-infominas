package toolsapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"bot-infominas/internal/config"
	"bot-infominas/internal/engine"
	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
)

// fakeCad é o Infodesk falso do cadastro: embute o fakeIdk (as outras tools) e
// acrescenta Lookup, Clientes e CriarSolicitante (CadastroAPI).
type fakeCad struct {
	*fakeIdk
	cmu sync.Mutex

	lookup    infodesk.Lookup
	lookupErr error
	telefones []string // telefones consultados no lookup

	clientes    []infodesk.Cliente
	clientesErr error
	buscas      []string // `q` das buscas de empresa

	criarErr error
	criarJA  bool // simula "já existia" (created: false)
	criados  []infodesk.NovoSolicitante
}

func (f *fakeCad) Lookup(_ context.Context, phone string) (infodesk.Lookup, error) {
	f.cmu.Lock()
	defer f.cmu.Unlock()
	f.telefones = append(f.telefones, phone)
	return f.lookup, f.lookupErr
}

func (f *fakeCad) Clientes(_ context.Context, q string) ([]infodesk.Cliente, error) {
	f.cmu.Lock()
	defer f.cmu.Unlock()
	f.buscas = append(f.buscas, q)
	return append([]infodesk.Cliente(nil), f.clientes...), f.clientesErr
}

func (f *fakeCad) CriarSolicitante(_ context.Context, n infodesk.NovoSolicitante) (infodesk.SolicitanteCriado, error) {
	f.cmu.Lock()
	defer f.cmu.Unlock()
	f.criados = append(f.criados, n)
	if f.criarErr != nil {
		return infodesk.SolicitanteCriado{}, f.criarErr
	}
	b, _ := json.Marshal(map[string]any{
		"id": idSolicitante, "name": n.Name, "email": n.Email, "phone": n.Phone, "clientId": n.ClientID, "created": !f.criarJA,
	})
	var out infodesk.SolicitanteCriado
	_ = json.Unmarshal(b, &out)
	return out, nil
}

func (f *fakeCad) totalCriados() int {
	f.cmu.Lock()
	defer f.cmu.Unlock()
	return len(f.criados)
}

const (
	idSolicitante = "r1000000-0000-4000-8000-000000000001"
	idAcme        = "k1000000-0000-4000-8000-00000000acme"
	idAcmeBrasil  = "k2000000-0000-4000-8000-00000000acbr"
)

func lookupComSolicitantes(t *testing.T, n int) infodesk.Lookup {
	t.Helper()
	var reqs []map[string]any
	for i := 0; i < n; i++ {
		reqs = append(reqs, map[string]any{"id": "rx", "name": "Maria", "clientId": idAcme})
	}
	b, _ := json.Marshal(map[string]any{"phone": telTeste, "requesters": reqs})
	var lk infodesk.Lookup
	if err := json.Unmarshal(b, &lk); err != nil {
		t.Fatal(err)
	}
	return lk
}

// novoAmbCad é o ambiente com o fakeCad no lugar do Infodesk e a empresa
// "ACME Ltda" como única candidata.
func novoAmbCad(t *testing.T, mods ...func(*Deps)) (*amb, *fakeCad) {
	t.Helper()
	a := novoAmb(t, mods...)
	a.conversa(convTeste, telTeste)
	fc := &fakeCad{fakeIdk: a.idk, clientes: []infodesk.Cliente{{ID: idAcme, Nome: "ACME Ltda"}}}
	a.h.d.Infodesk = fc
	return a, fc
}

func corpoPrepararCad(nome, empresa, email string) map[string]any {
	return map[string]any{"telefone": telTeste, "conversation_id": convTeste, "nome": nome, "empresa": empresa, "email": email}
}

func (a *amb) prepararCadastro(nome, empresa, email string) map[string]any {
	a.t.Helper()
	code, out := a.post("cadastro/preparar", corpoPrepararCad(nome, empresa, email))
	if code != http200 {
		a.t.Fatalf("HTTP %d", code)
	}
	return out
}

func (a *amb) cadastrar(chave string) map[string]any {
	a.t.Helper()
	code, out := a.post("cadastro/cadastrar", map[string]any{
		"telefone": telTeste, "conversation_id": convTeste,
		"proposta": map[string]any{"nome": "x", "chave": chave, "criada_em": "2026-10-22T14:33:10+00:00"},
	})
	if code != http200 {
		a.t.Fatalf("HTTP %d", code)
	}
	return out
}

func chaveDaProposta(t *testing.T, out map[string]any) string {
	t.Helper()
	p, ok := out["proposta"].(map[string]any)
	if !ok || out["status"] != true {
		t.Fatalf("esperava proposta, veio %+v", out)
	}
	return p["chave"].(string)
}

func motivoDe(out map[string]any) string { m, _ := out["motivo"].(string); return m }

// --- validações ---------------------------------------------------------------

func TestValidarNome(t *testing.T) {
	for entrada, ok := range map[string]bool{
		"Maria Souza": true, "  maria   souza  ": true, "João da Silva": true, "Ana-Paula D'Ávila": true, "Ana D’Ávila": true, "Dr. José Neto": true,
		"Maria": false, "": false, "   ": false, "Maria 123": false, "maria@acme.com Souza": false, "1 2": false,
		"- -": false, "Maria\nSouza": true /* controle vira espaço */, strings.Repeat("a", 70) + " " + strings.Repeat("b", 70): false,
	} {
		got, valido := validarNome(entrada)
		if valido != ok {
			t.Errorf("validarNome(%q) = (%q, %v), esperava %v", entrada, got, valido, ok)
		}
	}
	if n, _ := validarNome("  maria   souza "); n != "maria souza" {
		t.Errorf("espaços não colapsados: %q", n)
	}
}

func TestValidarEmail(t *testing.T) {
	for entrada, ok := range map[string]bool{
		"": true, "   ": true, "maria@acme.com.br": true, " maria@acme.com ": true,
		"maria": false, "maria@acme": false, "@acme.com": false, "maria@@acme.com": false,
		"mar ia@acme.com": false, strings.Repeat("a", 250) + "@b.co": false,
	} {
		if _, valido := validarEmail(entrada); valido != ok {
			t.Errorf("validarEmail(%q) = %v, esperava %v", entrada, valido, ok)
		}
	}
}

func TestValidarEmpresa(t *testing.T) {
	for entrada, ok := range map[string]bool{"ACME Ltda": true, "ACME": true, "a": false, "ab c": false, "Silva": true, "": false, "  ": false, "- - -": false, strings.Repeat("x", 121): false} {
		if _, valido := validarEmpresa(entrada); valido != ok {
			t.Errorf("validarEmpresa(%d chars) = %v, esperava %v", len(entrada), valido, ok)
		}
	}
}

func TestEscolherCliente(t *testing.T) {
	acme := infodesk.Cliente{ID: "1", Nome: "ACME Ltda"}
	brasil := infodesk.Cliente{ID: "2", Nome: "ACME Brasil S.A."}
	solo := infodesk.Cliente{ID: "3", Nome: "Solo Ltda"}
	semID := infodesk.Cliente{Nome: "Sem ID"}
	for nome, c := range map[string]struct {
		cands    []infodesk.Cliente
		digitado string
		quer     string // id escolhido; "" = não escolhe
		validos  int
	}{
		"um candidato só":               {[]infodesk.Cliente{solo}, "solo", "3", 1},
		"nome idêntico entre vários":    {[]infodesk.Cliente{acme, brasil}, "acme ltda", "1", 2},
		"nome idêntico sem acento":      {[]infodesk.Cliente{{ID: "9", Nome: "Açaí Ltda"}, brasil}, "ACAI LTDA", "9", 2},
		"vários sem nome idêntico":      {[]infodesk.Cliente{acme, brasil}, "acme", "", 2},
		"nenhum candidato":              {nil, "acme", "", 0},
		"candidato sem id é ignorado":   {[]infodesk.Cliente{semID}, "sem id", "", 0},
		"sem id não vira o único":       {[]infodesk.Cliente{semID, acme}, "outra", "1", 1},
		"dois idênticos são ambíguos":   {[]infodesk.Cliente{{ID: "a", Nome: "ACME"}, {ID: "b", Nome: "Acme"}}, "acme", "", 2},
		"um candidato diferente aceito": {[]infodesk.Cliente{acme}, "acme", "1", 1},
	} {
		t.Run(nome, func(t *testing.T) {
			got, validos, ok := escolherCliente(c.cands, c.digitado)
			if (c.quer != "") != ok || got.ID != c.quer || len(validos) != c.validos {
				t.Errorf("got=(%+v, %d válidos, %v), esperava id=%q e %d válidos", got, len(validos), ok, c.quer, c.validos)
			}
		})
	}
}

// --- preparar -----------------------------------------------------------------

func TestPrepararCadastro_OkGravaPropostaEUsaOTelefoneDaSessao(t *testing.T) {
	a, fc := novoAmbCad(t)
	// O LLM tenta empurrar telefone e clientId: nada disso é lido.
	_, out := a.post("cadastro/preparar", map[string]any{
		"telefone": "+5531977770000", "conversation_id": convTeste, "cliente_id": "k-forjado", "clientId": "k-forjado",
		"nome": "Maria Souza", "empresa": "ACME Ltda", "email": "Maria@Acme.com.br",
	})
	chave := chaveDaProposta(t, out)
	p := out["proposta"].(map[string]any)
	if p["telefone_mascarado"] != "****0001" || p["empresa"].(map[string]any)["nome"] != "ACME Ltda" {
		t.Errorf("proposta = %+v", p)
	}
	// Nome e e-mail não voltam no ToolMessage (checkpoint/trace mascaram por chave, não por texto).
	if _, tem := p["nome"]; tem {
		t.Error("a proposta não devolve o nome do cliente")
	}
	if _, tem := p["email"]; tem {
		t.Error("a proposta não devolve o e-mail do cliente")
	}
	linhaGravada, _ := a.st.ObterCadastro(context.Background(), chave)
	if linhaGravada == nil || linhaGravada.Nome != "Maria Souza" || linhaGravada.Email != "Maria@Acme.com.br" {
		t.Errorf("a linha gravada guarda nome e e-mail para o POST: %+v", linhaGravada)
	}
	for k := range p {
		if strings.Contains(strings.ToLower(k), "client") || k == "cliente_id" {
			t.Errorf("a proposta vazou %q para o LLM", k)
		}
	}
	if strings.Contains(a.log(), "k-forjado") || len(fc.telefones) != 1 || fc.telefones[0] != telTeste {
		t.Errorf("o lookup tem de usar o telefone da CONVERSA: %v", fc.telefones)
	}
	if want := chaveCadastro(convTeste, telTeste, idAcme, "Maria Souza", "maria@acme.com.br"); chave != want {
		t.Errorf("chave = %s, esperava %s", chave, want)
	}
	linha, _ := a.st.ObterCadastro(context.Background(), chave)
	if linha == nil || linha.Status != store.CadastroProposta || linha.ClienteID != idAcme || linha.Telefone != telTeste || linha.ConversationID != convTeste {
		t.Fatalf("linha = %+v", linha)
	}
	if fc.totalCriados() != 0 {
		t.Error("preparar não cria nada no Infodesk")
	}
	if len(fc.buscas) != 1 || fc.buscas[0] != "ACME Ltda" {
		t.Errorf("busca de empresa = %v", fc.buscas)
	}
}

func TestPrepararCadastro_CamposInvalidos(t *testing.T) {
	for nome, c := range map[string][3]string{
		"nome de uma palavra": {"Maria", "ACME Ltda", ""},
		"nome com dígitos":    {"Maria 2 Souza", "ACME Ltda", ""},
		"e-mail inválido":     {"Maria Souza", "ACME Ltda", "maria-sem-arroba"},
		"empresa vazia":       {"Maria Souza", "   ", ""},
	} {
		t.Run(nome, func(t *testing.T) {
			a, fc := novoAmbCad(t)
			out := a.prepararCadastro(c[0], c[1], c[2])
			if motivoDe(out) != "campo_invalido" || out["status"] != false || out["erro"] != txtCadCampoInvalido {
				t.Fatalf("out = %+v", out)
			}
			if len(fc.telefones) != 0 || len(fc.buscas) != 0 {
				t.Error("campo inválido não pode gastar chamada ao Infodesk")
			}
			if n, _ := a.st.IncrementarTentativasCadastro(context.Background(), convTeste); n != 1 {
				t.Errorf("campo inválido não conta como tentativa de empresa (contador = %d)", n-1)
			}
		})
	}
}

func TestPrepararCadastro_SessaoDesconhecidaOuSemConversa(t *testing.T) {
	a, _ := novoAmbCad(t)
	_, out := a.post("cadastro/preparar", map[string]any{
		"telefone": telTeste, "conversation_id": "conversa-que-nao-existe", "nome": "Maria Souza", "empresa": "ACME",
	})
	if motivoDe(out) != "campo_invalido" {
		t.Errorf("conversa inexistente não pode cadastrar por telefone vindo do corpo: %+v", out)
	}
}

func TestPrepararCadastro_JaCadastrado(t *testing.T) {
	t.Run("lookup achou o solicitante", func(t *testing.T) {
		a, fc := novoAmbCad(t)
		fc.lookup = lookupComSolicitantes(t, 1)
		out := a.prepararCadastro("Maria Souza", "ACME Ltda", "")
		if motivoDe(out) != "ja_cadastrado" || len(fc.buscas) != 0 {
			t.Fatalf("out = %+v buscas=%v", out, fc.buscas)
		}
	})
	t.Run("mais de um solicitante também", func(t *testing.T) {
		a, fc := novoAmbCad(t)
		fc.lookup = lookupComSolicitantes(t, 2)
		if out := a.prepararCadastro("Maria Souza", "ACME Ltda", ""); motivoDe(out) != "ja_cadastrado" {
			t.Fatalf("out = %+v", out)
		}
	})
	t.Run("contato local já ligado a um solicitante", func(t *testing.T) {
		a, fc := novoAmbCad(t)
		a.contato(store.Contato{Telefone: telTeste, Origem: store.OrigemLookup, Forma: store.FormaTelefone, RequesterID: "r-antigo"})
		if out := a.prepararCadastro("Maria Souza", "ACME Ltda", ""); motivoDe(out) != "ja_cadastrado" || len(fc.telefones) != 0 {
			t.Fatalf("out = %+v lookups=%v", out, fc.telefones)
		}
	})
}

func TestPrepararCadastro_EmpresaNaoEncontradaTransfereNaSegundaTentativa(t *testing.T) {
	a, fc := novoAmbCad(t)
	fc.clientes = nil

	out := a.prepararCadastro("Maria Souza", "Empresa Fantasma", "")
	if motivoDe(out) != "empresa_nao_encontrada" || out["erro"] != txtCadNaoEncontrada {
		t.Fatalf("1ª tentativa: %+v", out)
	}
	if len(a.tr.chamadas) != 0 {
		t.Fatal("a 1ª tentativa só pede de novo, não transfere")
	}

	out = a.prepararCadastro("Maria Souza", "Empresa Fantasma Ltda", "")
	if motivoDe(out) != "cadastro_transferido" || out["erro"] != txtCadTransferido {
		t.Fatalf("2ª tentativa: %+v", out)
	}
	if len(a.tr.chamadas) != 1 {
		t.Fatalf("transferências = %d, esperava 1", len(a.tr.chamadas))
	}
	tr := a.tr.chamadas[0]
	if tr.Mensagem != MsgCadastroSemEmpresa || tr.Origem == "" || strings.Contains(tr.Detalhe+tr.Causa, "Fantasma") {
		t.Errorf("transferência = %+v (aviso fixo; empresa digitada não entra na causa)", tr)
	}
	if fc.totalCriados() != 0 {
		t.Error("nunca cadastra sem clientId")
	}
}

func TestPrepararCadastro_EmpresaAmbigua(t *testing.T) {
	a, fc := novoAmbCad(t)
	fc.clientes = []infodesk.Cliente{{ID: idAcme, Nome: "ACME Ltda"}, {ID: idAcmeBrasil, Nome: "ACME Brasil S.A."}, {ID: "k3", Nome: "ACME Ltda"}}

	out := a.prepararCadastro("Maria Souza", "acme", "")
	if motivoDe(out) != "empresa_ambigua" || out["erro"] != txtCadAmbigua {
		t.Fatalf("out = %+v", out)
	}
	cands := out["candidatos"].([]any)
	if len(cands) != 2 { // dois nomes distintos (o repetido some)
		t.Fatalf("candidatos = %+v", cands)
	}
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), idAcme) || strings.Contains(string(b), "k3") || strings.Contains(string(b), `"id"`) {
		t.Errorf("ids de cliente vazaram para o LLM: %s", b)
	}

	// O cliente diz qual é a dele; o LLM repete com o nome COMPLETO: o gateway
	// escolhe por nome idêntico entre os candidatos.
	fc.clientes = []infodesk.Cliente{{ID: idAcme, Nome: "ACME Ltda"}, {ID: idAcmeBrasil, Nome: "ACME Brasil S.A."}}
	out = a.prepararCadastro("Maria Souza", "ACME Brasil S.A.", "")
	chave := chaveDaProposta(t, out)
	linha, _ := a.st.ObterCadastro(context.Background(), chave)
	if linha == nil || linha.ClienteID != idAcmeBrasil {
		t.Fatalf("linha = %+v (devia ser a ACME Brasil)", linha)
	}
	// o sucesso zera o contador
	if n, _ := a.st.IncrementarTentativasCadastro(context.Background(), convTeste); n != 1 {
		t.Errorf("sucesso devia zerar as tentativas (contador = %d)", n-1)
	}
}

func TestPrepararCadastro_SemClienteOferecerNaoTransfere(t *testing.T) {
	a, fc := novoAmbCad(t, func(d *Deps) {
		d.Opcoes.CadastroSemCliente = config.CadastroSemClienteOferecer
		d.Opcoes.CadastroMaxTentativas = 1
	})
	fc.clientes = nil
	out := a.prepararCadastro("Maria Souza", "Fantasma", "")
	if motivoDe(out) != "empresa_nao_encontrada" || out["erro"] != txtCadDesistir || len(a.tr.chamadas) != 0 {
		t.Fatalf("out = %+v transferências=%d", out, len(a.tr.chamadas))
	}
	// uma empresa corrigida depois ainda funciona
	fc.clientes = []infodesk.Cliente{{ID: idAcme, Nome: "ACME Ltda"}}
	if out = a.prepararCadastro("Maria Souza", "ACME Ltda", ""); out["status"] != true {
		t.Errorf("out = %+v", out)
	}
}

func TestPrepararCadastro_FalhasDoInfodesk(t *testing.T) {
	for nome, c := range map[string]struct {
		mexe    func(fc *fakeCad)
		motivo  string
		alertou bool
	}{
		"lookup sem escopo":      {func(fc *fakeCad) { fc.lookupErr = &infodesk.HTTPError{Status: 403, Code: infodesk.CodeForbidden} }, "escopo_insuficiente", true},
		"busca sem escopo":       {func(fc *fakeCad) { fc.clientesErr = &infodesk.HTTPError{Status: 403, Code: infodesk.CodeForbidden} }, "escopo_insuficiente", true},
		"lookup fora do ar":      {func(fc *fakeCad) { fc.lookupErr = &infodesk.HTTPError{Status: 503} }, "infodesk_indisponivel", false},
		"busca com erro de rede": {func(fc *fakeCad) { fc.clientesErr = &infodesk.ErroRede{Rota: "/clients", Err: errors.New("timeout")} }, "infodesk_indisponivel", false},
		"lookup ilegível":        {func(fc *fakeCad) { fc.lookup = infodesk.Lookup{Erro: errors.New("requesters: tipo inesperado")} }, "infodesk_indisponivel", false},
	} {
		t.Run(nome, func(t *testing.T) {
			a, fc := novoAmbCad(t)
			c.mexe(fc)
			out := a.prepararCadastro("Maria Souza", "ACME Ltda", "")
			if motivoDe(out) != c.motivo || out["status"] != false {
				t.Fatalf("out = %+v", out)
			}
			if a.av.teve(AlertaConfiguracao) != c.alertou {
				t.Errorf("alerta de configuração = %v, esperava %v", a.av.teve(AlertaConfiguracao), c.alertou)
			}
			b, _ := json.Marshal(out)
			if strings.Contains(string(b), "timeout") || strings.Contains(string(b), "HTTP") {
				t.Errorf("erro cru do Infodesk chegou ao LLM: %s", b)
			}
		})
	}
}

func TestPrepararCadastro_SemCadastroAPIDegradaParaIndisponivel(t *testing.T) {
	a := novoAmb(t) // Infodesk = fakeIdk puro, sem Lookup/Clientes/CriarSolicitante
	a.conversa(convTeste, telTeste)
	if out := a.prepararCadastro("Maria Souza", "ACME Ltda", ""); motivoDe(out) != "infodesk_indisponivel" {
		t.Errorf("out = %+v", out)
	}
}

// --- cadastrar ----------------------------------------------------------------

func TestCadastrar_OkPostaUmaVezComDadosDaLinhaEGravaOContato(t *testing.T) {
	a, fc := novoAmbCad(t)
	chave := chaveDaProposta(t, a.prepararCadastro("Maria Souza", "ACME Ltda", "maria@acme.com.br"))

	// Mesmo que o corpo traga outro telefone/nome, o POST usa a linha e a conversa.
	code, out := a.post("cadastro/cadastrar", map[string]any{
		"telefone": "+5531977770000", "conversation_id": convTeste,
		"proposta": map[string]any{"nome": "Outra Pessoa", "email": "x@y.com", "chave": chave, "clientId": "k-forjado"},
	})
	if code != http200 || out["status"] != true || out["cadastrado"] != true || out["criado"] != true || out["requester_id"] != idSolicitante {
		t.Fatalf("out = %+v", out)
	}
	if out["solicitante"].(map[string]any)["nome"] != "Maria S." || out["empresa"].(map[string]any)["nome"] != "ACME Ltda" {
		t.Errorf("out = %+v", out)
	}
	if fc.totalCriados() != 1 {
		t.Fatalf("POST /requesters = %d vezes, esperava 1", fc.totalCriados())
	}
	got := fc.criados[0]
	want := infodesk.NovoSolicitante{Name: "Maria Souza", ClientID: idAcme, Phone: telTeste, Email: "maria@acme.com.br", ConversationID: convTeste}
	if got != want {
		t.Errorf("POST = %+v, esperava %+v", got, want)
	}

	linha, _ := a.st.ObterCadastro(context.Background(), chave)
	if linha.Status != store.CadastroFeito || linha.RequesterID != idSolicitante || !linha.Criado {
		t.Errorf("linha = %+v", linha)
	}
	c, _ := a.st.ObterContato(context.Background(), telTeste, time.Now())
	if c == nil || c.Origem != store.OrigemCadastro || c.Forma != store.FormaTelefone || c.RequesterID != idSolicitante ||
		c.ClienteID != idAcme || c.ValorMascarado != "Maria S." || c.NomeCadastro != "Maria Souza" {
		t.Errorf("contato = %+v", c)
	}
	// o cadastro identifica: o `preparar` de ticket passa a reconhecer o telefone
	if a.h.contatoIdentificado(context.Background(), telTeste) == nil {
		t.Error("depois do cadastro o telefone precisa estar identificado")
	}
	for _, l := range []string{"Maria Souza", "maria@acme.com.br", telTeste} {
		if strings.Contains(a.log(), l) {
			t.Errorf("PII %q no log do gateway:\n%s", l, a.log())
		}
	}
}

func TestCadastrar_RepetirNaoFazNovoPOST(t *testing.T) {
	a, fc := novoAmbCad(t)
	chave := chaveDaProposta(t, a.prepararCadastro("Maria Souza", "ACME Ltda", ""))
	primeiro := a.cadastrar(chave)
	segundo := a.cadastrar(chave)
	if primeiro["status"] != true || segundo["status"] != true || segundo["requester_id"] != primeiro["requester_id"] {
		t.Fatalf("primeiro=%+v segundo=%+v", primeiro, segundo)
	}
	if fc.totalCriados() != 1 {
		t.Errorf("POST /requesters = %d vezes: repetir um cadastro concluído não pode ir de novo ao Infodesk", fc.totalCriados())
	}
}

func TestCadastrar_JaExistiaNoInfodeskCriadoFalse(t *testing.T) {
	a, fc := novoAmbCad(t)
	fc.criarJA = true
	chave := chaveDaProposta(t, a.prepararCadastro("Maria Souza", "ACME Ltda", ""))
	out := a.cadastrar(chave)
	if out["status"] != true || out["criado"] != false {
		t.Fatalf("out = %+v", out)
	}
}

func TestCadastrar_SemPropostaDoGatewayNaoExecuta(t *testing.T) {
	a, fc := novoAmbCad(t)
	chave := chaveDaProposta(t, a.prepararCadastro("Maria Souza", "ACME Ltda", ""))
	adulterada := chave[:len(chave)-1] + "0"
	if adulterada == chave {
		adulterada = chave[:len(chave)-1] + "1"
	}

	for nome, req := range map[string]map[string]any{
		"sem proposta":    {"telefone": telTeste, "conversation_id": convTeste},
		"chave vazia":     {"telefone": telTeste, "conversation_id": convTeste, "proposta": map[string]any{"chave": ""}},
		"chave inventada": {"telefone": telTeste, "conversation_id": convTeste, "proposta": map[string]any{"chave": adulterada}},
		"conversa sem id": {"telefone": telTeste, "conversation_id": "", "proposta": map[string]any{"chave": chave}},
		"outra conversa":  {"telefone": telTeste, "conversation_id": "outra-conversa", "proposta": map[string]any{"chave": chave}},
	} {
		t.Run(nome, func(t *testing.T) {
			_, out := a.post("cadastro/cadastrar", req)
			if motivoDe(out) != "sem_proposta" || out["erro"] != txtCadSemProposta {
				t.Fatalf("out = %+v", out)
			}
		})
	}
	if fc.totalCriados() != 0 {
		t.Errorf("nenhum POST /requesters sem proposta válida (vieram %d)", fc.totalCriados())
	}
}

func TestCadastrar_PropostaDeOutraConversaOuTelefoneNaoServe(t *testing.T) {
	a, fc := novoAmbCad(t)
	chave := chaveDaProposta(t, a.prepararCadastro("Maria Souza", "ACME Ltda", ""))
	// outra conversa (de outro telefone) tenta usar a chave da primeira
	const outraConv, outroTel = "9a9a9a9a-0000-4000-8000-000000000002", "+5531977770000"
	a.conversa(outraConv, outroTel)
	_, out := a.post("cadastro/cadastrar", map[string]any{
		"telefone": outroTel, "conversation_id": outraConv, "proposta": map[string]any{"chave": chave},
	})
	if motivoDe(out) != "sem_proposta" || fc.totalCriados() != 0 {
		t.Fatalf("out = %+v POSTs=%d", out, fc.totalCriados())
	}
}

func TestCadastrar_PropostaVencidaNaoExecuta(t *testing.T) {
	a, fc := novoAmbCad(t)
	chave := chaveDaProposta(t, a.prepararCadastro("Maria Souza", "ACME Ltda", ""))
	antes := propostaCadastroValidade
	propostaCadastroValidade = time.Millisecond
	t.Cleanup(func() { propostaCadastroValidade = antes })
	time.Sleep(5 * time.Millisecond)
	if out := a.cadastrar(chave); motivoDe(out) != "sem_proposta" || fc.totalCriados() != 0 {
		t.Fatalf("out = %+v POSTs=%d", out, fc.totalCriados())
	}
}

func TestCadastrar_FalhasDoInfodeskEReenvioSeguro(t *testing.T) {
	a, fc := novoAmbCad(t)
	chave := chaveDaProposta(t, a.prepararCadastro("Maria Souza", "ACME Ltda", ""))

	fc.criarErr = &infodesk.HTTPError{Status: 503, Code: infodesk.CodeInternalError}
	if out := a.cadastrar(chave); motivoDe(out) != "infodesk_indisponivel" || out["erro"] != txtCadFalhouAoCadastro {
		t.Fatalf("5xx: %+v", out)
	}
	linha, _ := a.st.ObterCadastro(context.Background(), chave)
	if linha.Status != store.CadastroProposta {
		t.Errorf("falha não pode marcar feito: %+v", linha)
	}
	if c, _ := a.st.ObterContato(context.Background(), telTeste, time.Now()); c != nil {
		t.Errorf("falha não pode identificar o telefone: %+v", c)
	}

	fc.criarErr = &infodesk.HTTPError{Status: 403, Code: infodesk.CodeForbidden}
	if out := a.cadastrar(chave); motivoDe(out) != "escopo_insuficiente" || !a.av.teve(AlertaConfiguracao) {
		t.Fatalf("403: %+v alertas=%v", out, a.av.tipos)
	}

	fc.criarErr = &infodesk.HTTPError{Status: 400, Code: infodesk.CodeValidationError}
	a.av.tipos = nil
	if out := a.cadastrar(chave); motivoDe(out) != "infodesk_indisponivel" || !a.av.teve(AlertaConfiguracao) {
		t.Fatalf("400: %+v alertas=%v", out, a.av.tipos)
	}

	// cada tentativa foi UM POST (o gateway não repete sozinho); com o Infodesk de volta, funciona
	if fc.totalCriados() != 3 {
		t.Errorf("POSTs = %d, esperava 3 (um por chamada)", fc.totalCriados())
	}
	fc.criarErr = nil
	if out := a.cadastrar(chave); out["status"] != true {
		t.Fatalf("depois do reparo: %+v", out)
	}
}

func TestCadastrar_ComIdentExigirCPFNaoDispensaOCPF(t *testing.T) {
	a, _ := novoAmbCad(t, func(d *Deps) { d.Opcoes.IdentExigirCPF = true })
	chave := chaveDaProposta(t, a.prepararCadastro("Maria Souza", "ACME Ltda", ""))
	if out := a.cadastrar(chave); out["status"] != true {
		t.Fatalf("out = %+v", out)
	}
	c, _ := a.st.ObterContato(context.Background(), telTeste, time.Now())
	if c == nil || c.Forma != "" || c.RequesterID != idSolicitante || c.Origem != store.OrigemCadastro {
		t.Errorf("contato = %+v (forma vazia: o CPF continua exigido; requester_id guardado)", c)
	}
	if a.h.contatoIdentificado(context.Background(), telTeste) != nil {
		t.Error("com IDENT_EXIGIR_CPF o cadastro não identifica sozinho")
	}
}

func TestCadastrar_PreservaCPFJaInformado(t *testing.T) {
	a, _ := novoAmbCad(t, func(d *Deps) { d.Opcoes.IdentExigirCPF = true })
	chave := chaveDaProposta(t, a.prepararCadastro("Maria Souza", "ACME Ltda", ""))
	a.identificadoPorCPF() // o cliente informou o CPF no meio do caminho
	if out := a.cadastrar(chave); out["status"] != true {
		t.Fatalf("out = %+v", out)
	}
	c, _ := a.st.ObterContato(context.Background(), telTeste, time.Now())
	if c == nil || c.Forma != store.FormaCPF || c.ValorMascarado != "***.***.789-**" || c.ValorHash != "h" || c.RequesterID != idSolicitante {
		t.Errorf("contato = %+v (o CPF informado não pode ser apagado)", c)
	}
}

func TestCadastro_RotasExigemTokenEPost(t *testing.T) {
	a, _ := novoAmbCad(t)
	for _, caminho := range []string{"cadastro/preparar", "cadastro/cadastrar"} {
		rota := "/tools/infodesk/" + caminho
		if code, _ := do(t, a.h, http.MethodPost, rota, "", "{}"); code != http.StatusUnauthorized {
			t.Errorf("%s sem token: HTTP %d, esperava 401", caminho, code)
		}
		if code, _ := do(t, a.h, http.MethodPost, rota, "errado", "{}"); code != http.StatusUnauthorized {
			t.Errorf("%s com token errado: HTTP %d, esperava 401", caminho, code)
		}
		if code, _ := do(t, a.h, http.MethodGet, rota, tokenTeste, ""); code != http.StatusMethodNotAllowed {
			t.Errorf("%s com GET: HTTP %d, esperava 405", caminho, code)
		}
	}
}

// A máscara de CPF do cérebro (src/agent/pii.py) troca qualquer corrida de 11
// dígitos por "***.***.789-**": a chave em hex puro seria corrompida em ~4% dos
// casos e o `cadastrar` falharia para sempre. Em grupos de 8, nunca há 9 seguidos.
func TestChaveCadastro_NuncaTemNoveDigitosSeguidos(t *testing.T) {
	reGrupos := regexp.MustCompile(`^[0-9a-f]{8}(-[0-9a-f]{8}){7}$`)
	reNove := regexp.MustCompile(`\d{9}`)
	for i := 0; i < 5000; i++ {
		c := chaveCadastro(convTeste, telTeste, fmt.Sprintf("cliente-%d", i), "Maria Souza", fmt.Sprintf("m%d@acme.com", i))
		if !reGrupos.MatchString(c) {
			t.Fatalf("formato inesperado: %s", c)
		}
		if reNove.MatchString(c) {
			t.Fatalf("a chave tem 9 dígitos seguidos (a regex de CPF casaria): %s", c)
		}
	}
}

// failSalvarContato falha a primeira gravação de contato (o POST já foi aceito).
type failSalvarContato struct {
	store.Store
	falhas int
}

func (f *failSalvarContato) SalvarContato(ctx context.Context, c *store.Contato) error {
	if f.falhas > 0 {
		f.falhas--
		return errors.New("disco cheio")
	}
	return f.Store.SalvarContato(ctx, c)
}

// Se o Infodesk cadastrou mas o contato não foi gravado, o retry (ramo `feito`)
// repara o contato — e continua sem fazer novo POST.
func TestCadastrar_RetryReparaOContatoSemNovoPOST(t *testing.T) {
	a, fc := novoAmbCad(t)
	chave := chaveDaProposta(t, a.prepararCadastro("Maria Souza", "ACME Ltda", ""))
	a.h.d.Store = &failSalvarContato{Store: a.st, falhas: 1}

	if out := a.cadastrar(chave); motivoDe(out) != "infodesk_indisponivel" {
		t.Fatalf("1ª chamada deveria falhar ao gravar o contato: %+v", out)
	}
	if c, _ := a.st.ObterContato(context.Background(), telTeste, time.Now()); c != nil {
		t.Fatalf("o contato não devia existir ainda: %+v", c)
	}
	if out := a.cadastrar(chave); out["status"] != true || out["requester_id"] != idSolicitante {
		t.Fatalf("retry: %+v", out)
	}
	c, _ := a.st.ObterContato(context.Background(), telTeste, time.Now())
	if c == nil || c.RequesterID != idSolicitante || c.Forma != store.FormaTelefone {
		t.Errorf("o retry devia gravar o contato: %+v", c)
	}
	if fc.totalCriados() != 1 {
		t.Errorf("POST /requesters = %d vezes, esperava 1", fc.totalCriados())
	}
}

// ctxTransf registra o contexto com que o handoff foi pedido.
type ctxTransf struct {
	*fakeTransf
	prazo time.Duration
	temDL bool
}

func (c *ctxTransf) TransferirParaHumano(ctx context.Context, conv string, t engine.Transferencia) string {
	dl, ok := ctx.Deadline()
	c.temDL = ok
	if ok {
		c.prazo = time.Until(dl)
	}
	return c.fakeTransf.TransferirParaHumano(ctx, conv, t)
}

// O handoff da empresa não resolvida roda em contexto próprio (desligado do da
// requisição, com o prazo de posTeto): se o cérebro desistir de esperar, ele conclui.
func TestPrepararCadastro_HandoffUsaContextoProprioComPrazo(t *testing.T) {
	a, fc := novoAmbCad(t)
	fc.clientes = nil
	ct := &ctxTransf{fakeTransf: a.tr}
	a.h.d.Transferidor = ct
	a.prepararCadastro("Maria Souza", "Fantasma Telecom", "")
	a.prepararCadastro("Maria Souza", "Fantasma Telecom", "")
	if len(a.tr.chamadas) != 1 || !ct.temDL || ct.prazo <= 0 || ct.prazo > posTeto {
		t.Fatalf("transferências=%d deadline=%v prazo=%v (esperado ≤ %v)", len(a.tr.chamadas), ct.temDL, ct.prazo, posTeto)
	}
}

// I2 (E4.5): concluído o cadastro, a linha não guarda mais nome nem e-mail; o
// retry ("sim" repetido) segue devolvendo o resultado, com o nome vindo do
// contato (que tem TTL), sem novo POST.
func TestCadastrar_ConcluidoZeraNomeEEmailENaoQuebraORetry(t *testing.T) {
	a, fc := novoAmbCad(t)
	chave := chaveDaProposta(t, a.prepararCadastro("Maria Souza", "ACME Ltda", "maria@acme.com.br"))
	linha, _ := a.st.ObterCadastro(context.Background(), chave)
	if linha.Nome == "" || linha.Email == "" {
		t.Fatal("antes de concluir a proposta guarda nome e e-mail (precisa deles para o POST)")
	}
	primeiro := a.cadastrar(chave)
	linha, _ = a.st.ObterCadastro(context.Background(), chave)
	if linha.Status != store.CadastroFeito || linha.Nome != "" || linha.Email != "" {
		t.Fatalf("depois de concluir: nome/e-mail zerados, status feito: %+v", linha)
	}
	segundo := a.cadastrar(chave)
	if segundo["status"] != true || segundo["requester_id"] != primeiro["requester_id"] || fc.totalCriados() != 1 {
		t.Fatalf("retry: %+v (POSTs=%d)", segundo, fc.totalCriados())
	}
	if segundo["solicitante"].(map[string]any)["nome"] != "Maria S." {
		t.Errorf("o nome do retry vem do contato: %+v", segundo)
	}
	// Chave de outra conversa continua sem servir para uma linha `feito`.
	_, out := a.post("cadastro/cadastrar", map[string]any{
		"telefone": telTeste, "conversation_id": "outra-conversa",
		"proposta": map[string]any{"chave": chave},
	})
	if motivoDe(out) != "sem_proposta" {
		t.Errorf("feito de outra conversa: %+v", out)
	}
}

// M5 (E4.5): as tentativas de empresa contam por TELEFONE — abrir outra conversa
// não zera o contador.
func TestPrepararCadastro_TentativasContamPorTelefoneEntreConversas(t *testing.T) {
	a, fc := novoAmbCad(t)
	fc.clientes = nil
	a.conversa("conversa-b", telTeste)

	out := a.prepararCadastro("Maria Souza", "Empresa Fantasma", "")
	if motivoDe(out) != "empresa_nao_encontrada" {
		t.Fatalf("1ª tentativa: %+v", out)
	}
	corpo := corpoPrepararCad("Maria Souza", "Empresa Fantasma Ltda", "")
	corpo["conversation_id"] = "conversa-b"
	_, out = a.post("cadastro/preparar", corpo)
	if motivoDe(out) != "cadastro_transferido" {
		t.Fatalf("a 2ª tentativa, em outra conversa do MESMO telefone, esgota o limite: %+v", out)
	}
}

// Degradação ACEITA (E4.5, I2): se o Infodesk cadastrou mas o contato não foi
// gravado, a linha já perdeu nome e e-mail; o retry repara o contato (sem novo
// POST), porém sem o nome — o cliente é identificado, mas o nome fica vazio (o
// cérebro ainda o tem na conversa). Nada disso vaza o nome em claro.
func TestCadastrar_RetryAposFalhaParcialReparaOContatoSemONome(t *testing.T) {
	a, fc := novoAmbCad(t)
	chave := chaveDaProposta(t, a.prepararCadastro("Maria Souza", "ACME Ltda", "maria@acme.com.br"))
	a.h.d.Store = &failSalvarContato{Store: a.st, falhas: 1}
	if out := a.cadastrar(chave); motivoDe(out) != "infodesk_indisponivel" {
		t.Fatalf("1ª chamada: %+v", out)
	}
	out := a.cadastrar(chave)
	if out["status"] != true || out["solicitante"].(map[string]any)["nome"] != "" || fc.totalCriados() != 1 {
		t.Fatalf("retry: %+v (POSTs=%d)", out, fc.totalCriados())
	}
	c, _ := a.st.ObterContato(context.Background(), telTeste, time.Now())
	if c == nil || c.RequesterID != idSolicitante || c.NomeCadastro != "" || c.ValorMascarado != "" {
		t.Fatalf("contato reparado sem o nome: %+v", c)
	}
	linha, _ := a.st.ObterCadastro(context.Background(), chave)
	if linha.Nome != "" || linha.Email != "" {
		t.Fatalf("a linha não guarda mais PII: %+v", linha)
	}
}
