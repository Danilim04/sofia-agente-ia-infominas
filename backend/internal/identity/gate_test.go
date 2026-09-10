package identity

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bot-azapfy/internal/mongo"
	"bot-azapfy/internal/store"
)

type fakeRepo struct {
	docs map[string]mongo.UsuarioDoc
}

func (f fakeRepo) BuscarPorLogin(_ context.Context, login string) (mongo.UsuarioDoc, bool, error) {
	d, ok := f.docs[login]
	return d, ok, nil
}

// fakeExtractor simula o cérebro: devolve, para uma mensagem, o login que ele
// "extraiu". `chamado` registra se foi acionado (p/ checar que o determinístico
// não cai no fallback à toa). `err` força o caminho de indisponibilidade.
type fakeExtractor struct {
	respostas map[string]string
	err       error
	chamado   *bool
}

func (f fakeExtractor) ExtrairLogin(_ context.Context, mensagem string) (string, error) {
	if f.chamado != nil {
		*f.chamado = true
	}
	if f.err != nil {
		return "", f.err
	}
	return f.respostas[mensagem], nil
}

func docDaniel() mongo.UsuarioDoc {
	return mongo.UsuarioDoc{
		Login: "10596693664",
		Nome:  "Daniel Ferraz",
		Email: "daniel.ferraz@azapfy.com.br",
		Grupos: map[string]mongo.GrupoDoc{
			"AZAPERS": {
				Ativo: true, GrupoUser: "COLABORADOR", Area: "SAC",
				Bases: map[string]mongo.BaseDoc{
					"MATRIZ": {Nome: "MATRIZ", Sigla: "MAT", Modulos: map[string]mongo.ModuloDoc{
						"pesquisa":     {Ativo: true},
						"rastreamento": {Ativo: true},
					}},
				},
			},
			"AZAPFY": {Ativo: false}, // inativo: não deve entrar no perfil
		},
	}
}

func newGate(t *testing.T, repo UserRepo) (*Gate, store.Store) {
	t.Helper()
	return newGateComExtractor(t, repo, nil)
}

func newGateComExtractor(t *testing.T, repo UserRepo, extractor LoginExtractor) (*Gate, store.Store) {
	t.Helper()
	st, err := store.NewSQLite(filepath.Join(t.TempDir(), "gate.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(st, repo, "email", 3, time.Hour, time.Hour, extractor, nil), st
}

func TestGateFluxoFeliz(t *testing.T) {
	g, _ := newGate(t, fakeRepo{docs: map[string]mongo.UsuarioDoc{"10596693664": docDaniel()}})
	ctx := context.Background()
	const conv = int64(1)
	const phone = "5511999990001"

	if r := g.Process(ctx, conv, phone, "oi"); r.Acao != AcaoPerguntar {
		t.Fatalf("1º turno: esperava perguntar (login), veio %q", r.Acao)
	}
	if r := g.Process(ctx, conv, phone, "10596693664"); r.Acao != AcaoPerguntar {
		t.Fatalf("após login válido: esperava perguntar (confirmação), veio %q", r.Acao)
	}
	if r := g.Process(ctx, conv, phone, "errado@x.com"); r.Acao != AcaoPerguntar {
		t.Fatalf("confirmação errada: esperava perguntar de novo, veio %q", r.Acao)
	}
	// e-mail certo (caixa/espacos diferentes) → saudar
	r := g.Process(ctx, conv, phone, "  Daniel.Ferraz@AZAPFY.com.br ")
	if r.Acao != AcaoSaudar {
		t.Fatalf("confirmação certa: esperava saudar, veio %q", r.Acao)
	}
	// próxima mensagem → encaminhar ao cérebro, com perfil escopado
	r = g.Process(ctx, conv, phone, "como rastreio a NF 1?")
	if r.Acao != AcaoEncaminhar || r.Perfil == nil {
		t.Fatalf("identificado: esperava encaminhar com perfil, veio %q perfil=%v", r.Acao, r.Perfil)
	}
	if len(r.Perfil.Empresas) != 1 || r.Perfil.Empresas[0].GrupoEmpresa != "AZAPERS" {
		t.Fatalf("perfil deve conter só AZAPERS (AZAPFY inativo): %+v", r.Perfil.Empresas)
	}
}

func TestGateCacheHitNovaConversa(t *testing.T) {
	g, _ := newGate(t, fakeRepo{docs: map[string]mongo.UsuarioDoc{"10596693664": docDaniel()}})
	ctx := context.Background()
	const phone = "5511999990001"

	// Identifica na conversa 1.
	g.Process(ctx, 1, phone, "oi")
	g.Process(ctx, 1, phone, "10596693664")
	g.Process(ctx, 1, phone, "daniel.ferraz@azapfy.com.br")

	// Nova conversa, MESMO telefone → encaminha direto (base própria).
	r := g.Process(ctx, 2, phone, "tenho uma dúvida")
	if r.Acao != AcaoEncaminhar || r.Perfil == nil {
		t.Fatalf("cache hit: esperava encaminhar com perfil, veio %q perfil=%v", r.Acao, r.Perfil)
	}
}

func TestGateLoginNaoEncontradoRoteiaHumano(t *testing.T) {
	g, _ := newGate(t, fakeRepo{docs: map[string]mongo.UsuarioDoc{}})
	ctx := context.Background()
	const conv = int64(7)
	const phone = "5511000000000"

	g.Process(ctx, conv, phone, "oi") // pede login
	if r := g.Process(ctx, conv, phone, "naoexiste"); r.Acao != AcaoPerguntar {
		t.Fatalf("tentativa 1: esperava perguntar de novo, veio %q", r.Acao)
	}
	if r := g.Process(ctx, conv, phone, "naoexiste"); r.Acao != AcaoPerguntar {
		t.Fatalf("tentativa 2: esperava perguntar de novo, veio %q", r.Acao)
	}
	if r := g.Process(ctx, conv, phone, "naoexiste"); r.Acao != AcaoRotearHumano {
		t.Fatalf("tentativa 3 (máx): esperava rotear humano, veio %q", r.Acao)
	}
	// Depois de falhar, novas mensagens são ignoradas (já roteado).
	if r := g.Process(ctx, conv, phone, "oi de novo"); r.Acao != AcaoIgnorar {
		t.Fatalf("pós-falha: esperava ignorar, veio %q", r.Acao)
	}
}

func TestGateLoginCpfFormatadoNormaliza(t *testing.T) {
	// Banco guarda só dígitos ("10596693664"); cliente manda CPF pontuado.
	// Deve resolver SEM acionar a IA (normalização determinística de CPF/CNPJ).
	chamado := false
	repo := fakeRepo{docs: map[string]mongo.UsuarioDoc{"10596693664": docDaniel()}}
	g, _ := newGateComExtractor(t, repo, fakeExtractor{chamado: &chamado})
	ctx := context.Background()
	const conv = int64(11)
	const phone = "5511999990002"

	g.Process(ctx, conv, phone, "oi") // pede login
	if r := g.Process(ctx, conv, phone, "105.966.936-64"); r.Acao != AcaoPerguntar {
		t.Fatalf("CPF formatado: esperava perguntar (confirmação), veio %q", r.Acao)
	}
	if chamado {
		t.Fatal("não deveria chamar a IA quando o CPF formatado já normaliza")
	}
}

func TestGateLoginViaIAFallback(t *testing.T) {
	// Login não-numérico embutido em frase: determinístico falha, IA extrai.
	doc := docDaniel()
	doc.Login = "joao"
	repo := fakeRepo{docs: map[string]mongo.UsuarioDoc{"joao": doc}}
	extractor := fakeExtractor{respostas: map[string]string{"meu login é joao": "joao"}}
	g, _ := newGateComExtractor(t, repo, extractor)
	ctx := context.Background()
	const conv = int64(12)
	const phone = "5511999990003"

	g.Process(ctx, conv, phone, "oi") // pede login
	if r := g.Process(ctx, conv, phone, "meu login é joao"); r.Acao != AcaoPerguntar {
		t.Fatalf("login via IA: esperava perguntar (confirmação), veio %q", r.Acao)
	}
}

func TestGateExtractorIndisponivelNaoQuebra(t *testing.T) {
	// IA fora do ar + login não resolvível deterministicamente → trata como
	// não encontrado (pede de novo), sem erro fatal.
	repo := fakeRepo{docs: map[string]mongo.UsuarioDoc{"joao": docDaniel()}}
	extractor := fakeExtractor{err: errors.New("brain offline")}
	g, _ := newGateComExtractor(t, repo, extractor)
	ctx := context.Background()
	const conv = int64(13)

	g.Process(ctx, conv, "5511999990004", "oi")
	if r := g.Process(ctx, conv, "5511999990004", "meu login é joao"); r.Acao != AcaoPerguntar {
		t.Fatalf("IA indisponível: esperava perguntar de novo, veio %q", r.Acao)
	}
}

func TestGateFalhaExpiraEReinicia(t *testing.T) {
	// F1: GateFalha não pode ser terminal. Com falhaTTL mínimo, a mensagem
	// seguinte à falha já reinicia a identificação (pede login de novo).
	st, err := store.NewSQLite(filepath.Join(t.TempDir(), "gate.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	repo := fakeRepo{docs: map[string]mongo.UsuarioDoc{}}
	g := New(st, repo, "email", 3, time.Hour, time.Nanosecond, nil, nil)
	ctx := context.Background()
	const conv = int64(21)
	const phone = "5511000000001"

	g.Process(ctx, conv, phone, "oi")
	g.Process(ctx, conv, phone, "x")
	g.Process(ctx, conv, phone, "x")
	if r := g.Process(ctx, conv, phone, "x"); r.Acao != AcaoRotearHumano {
		t.Fatalf("3ª tentativa: esperava rotear humano, veio %q", r.Acao)
	}
	// TTL de 1ns já expirou → volta a perguntar o login em vez de ignorar.
	if r := g.Process(ctx, conv, phone, "oi de novo"); r.Acao != AcaoPerguntar {
		t.Fatalf("pós-falha expirada: esperava perguntar (reinício), veio %q", r.Acao)
	}
}

func TestGateResolvidoApagaEstadoEReiniciaSemEsperarTTL(t *testing.T) {
	// Conversa roteada a humano (GateFalha) e depois RESOLVIDA no Chatwoot: a
	// engine apaga o estado (DeleteGate). A próxima mensagem do cliente tem de
	// ser atendida na hora — sem esperar o GATE_FALHA_TTL (1h aqui).
	st, err := store.NewSQLite(filepath.Join(t.TempDir(), "gate.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	repo := fakeRepo{docs: map[string]mongo.UsuarioDoc{}}
	g := New(st, repo, "email", 3, time.Hour, time.Hour, nil, nil)
	ctx := context.Background()
	const conv = int64(23)
	const phone = "5511000000002"

	g.Process(ctx, conv, phone, "oi")
	g.Process(ctx, conv, phone, "x")
	g.Process(ctx, conv, phone, "x")
	if r := g.Process(ctx, conv, phone, "x"); r.Acao != AcaoRotearHumano {
		t.Fatalf("3ª tentativa: esperava rotear humano, veio %q", r.Acao)
	}
	// TTL vivo: sem a resolução, a conversa fica muda.
	if r := g.Process(ctx, conv, phone, "alguém aí?"); r.Acao != AcaoIgnorar {
		t.Fatalf("falha dentro do TTL: esperava ignorar, veio %q", r.Acao)
	}
	// O atendente resolveu a conversa → engine apaga o gate.
	if err := st.DeleteGate(ctx, conv); err != nil {
		t.Fatalf("deleteGate: %v", err)
	}
	if r := g.Process(ctx, conv, phone, "preciso abrir outro chamado"); r.Acao != AcaoPerguntar {
		t.Fatalf("pós-resolução: esperava perguntar (reinício), veio %q", r.Acao)
	}
}

func TestGateConfirmacaoToleraTextoAoRedor(t *testing.T) {
	// F2: assinatura de relay ("**Claude:**") ou frase em volta do e-mail não
	// podem queimar tentativa de cliente legítimo.
	g, _ := newGate(t, fakeRepo{docs: map[string]mongo.UsuarioDoc{"10596693664": docDaniel()}})
	ctx := context.Background()
	const conv = int64(22)
	const phone = "5511999990009"

	g.Process(ctx, conv, phone, "oi")
	g.Process(ctx, conv, phone, "10596693664")
	r := g.Process(ctx, conv, phone, "**Claude:**\nmeu email é Daniel.Ferraz@azapfy.com.br, viu?")
	if r.Acao != AcaoSaudar {
		t.Fatalf("confirmação com texto ao redor: esperava saudar, veio %q", r.Acao)
	}
}

func TestGateLoginCpfComPontuacaoErradaEmFrase(t *testing.T) {
	// F2: "105.966.936.64" (pontuação trocada) e texto ao redor resolvem
	// deterministicamente — sem custo/latência do extractor de IA.
	chamado := false
	repo := fakeRepo{docs: map[string]mongo.UsuarioDoc{"10596693664": docDaniel()}}
	g, _ := newGateComExtractor(t, repo, fakeExtractor{chamado: &chamado})
	ctx := context.Background()
	const conv = int64(23)

	g.Process(ctx, conv, "5511999990010", "oi")
	if r := g.Process(ctx, conv, "5511999990010", "meu login é 105.966.936.64"); r.Acao != AcaoPerguntar {
		t.Fatalf("CPF em frase: esperava perguntar (confirmação), veio %q", r.Acao)
	}
	if chamado {
		t.Fatal("não deveria chamar a IA quando o CPF embutido já normaliza")
	}
}

func TestGateLoginEmailEmFrase(t *testing.T) {
	doc := docDaniel()
	doc.Login = "daniel.ferraz@azapfy.com.br"
	repo := fakeRepo{docs: map[string]mongo.UsuarioDoc{"daniel.ferraz@azapfy.com.br": doc}}
	chamado := false
	g, _ := newGateComExtractor(t, repo, fakeExtractor{chamado: &chamado})
	ctx := context.Background()
	const conv = int64(24)

	g.Process(ctx, conv, "5511999990011", "oi")
	if r := g.Process(ctx, conv, "5511999990011", "pode usar o email Daniel.Ferraz@Azapfy.com.br"); r.Acao != AcaoPerguntar {
		t.Fatalf("e-mail em frase: esperava perguntar (confirmação), veio %q", r.Acao)
	}
	if chamado {
		t.Fatal("não deveria chamar a IA quando o e-mail embutido já resolve")
	}
}

func TestGateLoginInativoRoteiaHumano(t *testing.T) {
	inativo := mongo.UsuarioDoc{
		Login: "999", Nome: "Fulano", Email: "f@x.com",
		Grupos: map[string]mongo.GrupoDoc{"AZAPFY": {Ativo: false}},
	}
	g, _ := newGate(t, fakeRepo{docs: map[string]mongo.UsuarioDoc{"999": inativo}})
	ctx := context.Background()
	const conv = int64(9)

	g.Process(ctx, conv, "5511222220000", "oi")
	r := g.Process(ctx, conv, "5511222220000", "999")
	if r.Acao != AcaoRotearHumano {
		t.Fatalf("login sem empresa ativa: esperava rotear humano, veio %q", r.Acao)
	}
}

// fakeRepoMutavel conta lookups e permite mudar a base/forçar erro no meio do
// teste — p/ exercitar a revalidação de identidade com cache vencido.
type fakeRepoMutavel struct {
	docs     map[string]mongo.UsuarioDoc
	err      error
	chamadas int
}

func (f *fakeRepoMutavel) BuscarPorLogin(_ context.Context, login string) (mongo.UsuarioDoc, bool, error) {
	f.chamadas++
	if f.err != nil {
		return mongo.UsuarioDoc{}, false, f.err
	}
	d, ok := f.docs[login]
	return d, ok, nil
}

// identifica roda o fluxo feliz completo (login + confirmação) na conversa.
func identifica(t *testing.T, g *Gate, conv int64, phone string) {
	t.Helper()
	ctx := context.Background()
	g.Process(ctx, conv, phone, "oi")
	g.Process(ctx, conv, phone, "10596693664")
	if r := g.Process(ctx, conv, phone, "daniel.ferraz@azapfy.com.br"); r.Acao != AcaoSaudar {
		t.Fatalf("setup: esperava saudar ao identificar, veio %q", r.Acao)
	}
}

// newGateTTL constrói o gate com TTL de identidade configurável (ttl mínimo
// simula o cache telefone→perfil vencido numa conversa já identificada).
func newGateTTL(t *testing.T, repo UserRepo, ttl time.Duration) *Gate {
	t.Helper()
	st, err := store.NewSQLite(filepath.Join(t.TempDir(), "gate.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(st, repo, "email", 3, ttl, time.Hour, nil, nil)
}

func TestGateIdentificadoCacheVivoNaoConsultaOrigem(t *testing.T) {
	// Cache válido → encaminha sem NENHUM lookup extra no Mongo por mensagem.
	repo := &fakeRepoMutavel{docs: map[string]mongo.UsuarioDoc{"10596693664": docDaniel()}}
	g := newGateTTL(t, repo, time.Hour)
	identifica(t, g, 31, "5511999990031")
	antes := repo.chamadas

	r := g.Process(context.Background(), 31, "5511999990031", "como rastreio a NF 1?")
	if r.Acao != AcaoEncaminhar || r.Perfil == nil {
		t.Fatalf("cache vivo: esperava encaminhar com perfil, veio %q perfil=%v", r.Acao, r.Perfil)
	}
	if repo.chamadas != antes {
		t.Fatalf("cache vivo não deveria consultar o Mongo (antes=%d, depois=%d)", antes, repo.chamadas)
	}
}

func TestGateIdentificadoCacheVencidoRevalidaNaOrigem(t *testing.T) {
	// Cenário do bug em produção (conv 37): gate identificado, cache de
	// identidade vencido → o gate revalida no Mongo pelo login conhecido e
	// encaminha, sem re-pedir login (o invariante da API de tools volta a valer).
	repo := &fakeRepoMutavel{docs: map[string]mongo.UsuarioDoc{"10596693664": docDaniel()}}
	g := newGateTTL(t, repo, time.Nanosecond) // cache nasce vencido
	identifica(t, g, 32, "5511999990032")
	antes := repo.chamadas

	r := g.Process(context.Background(), 32, "5511999990032", "consegue abrir um chamado?")
	if r.Acao != AcaoEncaminhar || r.Perfil == nil {
		t.Fatalf("cache vencido: esperava encaminhar após revalidar, veio %q perfil=%v", r.Acao, r.Perfil)
	}
	if repo.chamadas != antes+1 {
		t.Fatalf("esperava exatamente 1 lookup de revalidação, veio %d", repo.chamadas-antes)
	}
	if len(r.Perfil.Empresas) != 1 || r.Perfil.Empresas[0].GrupoEmpresa != "AZAPERS" {
		t.Fatalf("perfil revalidado deve vir re-projetado da origem: %+v", r.Perfil.Empresas)
	}
}

func TestGateIdentificadoCacheVencidoOrigemFora(t *testing.T) {
	// Mongo fora durante a revalidação → mesma mensagem de erro temporário do
	// tratarLogin; o estado NÃO muda e a próxima mensagem tenta de novo.
	repo := &fakeRepoMutavel{docs: map[string]mongo.UsuarioDoc{"10596693664": docDaniel()}}
	g := newGateTTL(t, repo, time.Nanosecond)
	identifica(t, g, 33, "5511999990033")

	repo.err = errors.New("mongo fora")
	r := g.Process(context.Background(), 33, "5511999990033", "oi?")
	if r.Acao != AcaoPerguntar || r.Reply != msgErroTemporario {
		t.Fatalf("origem fora: esperava perguntar com erro temporário, veio %q reply=%q", r.Acao, r.Reply)
	}
	// Origem voltou → a mesma conversa encaminha sem re-identificação.
	repo.err = nil
	if r := g.Process(context.Background(), 33, "5511999990033", "oi de novo"); r.Acao != AcaoEncaminhar {
		t.Fatalf("origem de volta: esperava encaminhar, veio %q", r.Acao)
	}
}

func TestGateIdentificadoCacheVencidoLoginSumiuReinicia(t *testing.T) {
	// Usuário removido da base → recomeça a identificação (não segue com
	// perfil fantasma).
	repo := &fakeRepoMutavel{docs: map[string]mongo.UsuarioDoc{"10596693664": docDaniel()}}
	g := newGateTTL(t, repo, time.Nanosecond)
	identifica(t, g, 34, "5511999990034")

	delete(repo.docs, "10596693664")
	r := g.Process(context.Background(), 34, "5511999990034", "oi?")
	if r.Acao != AcaoPerguntar || !strings.Contains(r.Reply, "login") {
		t.Fatalf("login sumiu: esperava reiniciar pedindo login, veio %q reply=%q", r.Acao, r.Reply)
	}
}

func TestGateIdentificadoCacheVencidoAcessoRevogadoRoteiaHumano(t *testing.T) {
	// Acesso revogado na origem (nenhuma empresa ativa) → mesmo tratamento do
	// login inativo: rotear humano; é exatamente o propósito do TTL.
	repo := &fakeRepoMutavel{docs: map[string]mongo.UsuarioDoc{"10596693664": docDaniel()}}
	g := newGateTTL(t, repo, time.Nanosecond)
	identifica(t, g, 35, "5511999990035")

	revogado := docDaniel()
	revogado.Grupos = map[string]mongo.GrupoDoc{"AZAPERS": {Ativo: false}}
	repo.docs["10596693664"] = revogado
	r := g.Process(context.Background(), 35, "5511999990035", "oi?")
	if r.Acao != AcaoRotearHumano || r.Reply != msgSemAcesso {
		t.Fatalf("acesso revogado: esperava rotear humano, veio %q reply=%q", r.Acao, r.Reply)
	}
}

// Persona "Azapfy Suporte": a saudação de abertura acompanha o período do dia
// em Brasília — e à noite NÃO existe "boníssima noite".
func TestSaudacaoAbertura(t *testing.T) {
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Skipf("sem tzdata no host: %v", err)
	}
	casos := []struct {
		hora int
		quer string
	}{
		{7, "Boníssimo dia"},
		{11, "Boníssimo dia"},
		{12, "Boníssima tarde"},
		{17, "Boníssima tarde"},
		{18, "Boa noite"},
		{23, "Boa noite"},
		{3, "Boa noite"},
	}
	for _, c := range casos {
		agora := time.Date(2026, 8, 28, c.hora, 30, 0, 0, loc)
		if got := saudacaoAbertura(agora); got != c.quer {
			t.Errorf("hora %dh: esperava %q, veio %q", c.hora, c.quer, got)
		}
	}
}
