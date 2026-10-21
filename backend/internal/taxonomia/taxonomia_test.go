package taxonomia

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"bot-infominas/internal/infodesk"
)

type fonteFake struct {
	mesas    []infodesk.Referencia
	porID    map[string]infodesk.Mesa
	depts    []infodesk.Referencia
	err      error
	catalogo map[string][]infodesk.Referencia
	chamadas int
	inval    int
}

func (f *fonteFake) Mesas(context.Context) ([]infodesk.Referencia, error) {
	f.chamadas++
	return f.mesas, f.err
}
func (f *fonteFake) Mesa(_ context.Context, id string) (infodesk.Mesa, error) {
	return f.porID[id], f.err
}
func (f *fonteFake) Catalogo(_ context.Context, id string) ([]infodesk.Referencia, error) {
	return f.catalogo[id], nil
}
func (f *fonteFake) Departamentos(context.Context) ([]infodesk.Referencia, error) {
	return f.depts, f.err
}
func (f *fonteFake) InvalidarCache() { f.inval++ }

func r(id, nome string) infodesk.Referencia { return infodesk.Referencia{ID: id, Nome: nome} }

func novaFonte() *fonteFake {
	prios := []infodesk.Referencia{r("p-b", "Baixa"), r("p-m", "Média"), r("p-a", "Alta"), r("p-u", "Urgente")}
	return &fonteFake{
		mesas: []infodesk.Referencia{r("m-sup", "Suporte Tecnico N1"), r("m-fin", "Financeiro"), r("m-tri", "Abertura de Ticket")},
		porID: map[string]infodesk.Mesa{
			"m-sup": {ID: "m-sup", Priorities: prios, CatalogItems: []infodesk.Referencia{r("i-1", "Serviços de Suporte")}},
			"m-fin": {ID: "m-fin", Priorities: []infodesk.Referencia{r("f-b", "Baixa"), r("f-m", "Média")}},
			"m-tri": {ID: "m-tri", Priorities: prios},
		},
		depts: []infodesk.Referencia{r("d-sup", "Suporte"), r("d-fin", "Financeiro"), r("d-atd", "Atendimento")},
	}
}

const yamlBase = `
placeholder: true
versao: 2026-10-21
departamento_humano_padrao: "Atendimento"
categorias:
  sem_conexao:
    descricao: "Internet fora"
    exemplos: ["caiu"]
    mesa: "Suporte Técnico N1"
    catalogo: "Serviços de Suporte"
    departamento: "Suporte"
    prioridade_minima: alta
  lentidao:
    mesa: "Suporte Técnico N1"
    departamento: "Suporte"
    prioridade: baixa
  cobranca:
    mesa: "Financeiro"
    departamento: "Financeiro"
  indefinida:
    mesa: "Abertura de Ticket"
    departamento: "Atendimento"
prioridades:
  baixa: "Baixa"
  media: "Média"
  alta: "Alta"
  urgente: "Urgente"
`

func carregada(t *testing.T, y string, strict bool, f *fonteFake) (*Taxonomia, Relatorio, error) {
	t.Helper()
	def, err := Parse([]byte(y))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	tx := Nova(def, f, Opcoes{Strict: strict, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	rel, err := tx.Carregar(context.Background())
	return tx, rel, err
}

func TestResolverPorMesa(t *testing.T) {
	tx, rel, err := carregada(t, yamlBase, false, novaFonte())
	if err != nil {
		t.Fatal(err)
	}
	if !tx.Placeholder() || !rel.Placeholder || rel.Categorias != 4 {
		t.Fatalf("relatório: %+v", rel)
	}
	res, err := tx.Resolver(context.Background(), "sem_conexao", "media")
	if err != nil {
		t.Fatal(err)
	}
	if res.Mesa.ID != "m-sup" || res.Mesa.Nome != "Suporte Tecnico N1" || res.Departamento.ID != "d-sup" {
		t.Fatalf("destino: %+v", res)
	}
	// prioridade_minima: alta eleva "media"; o UUID é o da mesa de destino.
	if res.PrioridadeNome != "alta" || res.PriorityID == nil || *res.PriorityID != "p-a" {
		t.Fatalf("prioridade: %+v", res)
	}
	if res.CatalogItemID == nil || *res.CatalogItemID != "i-1" {
		t.Fatalf("catálogo: %+v", res.CatalogItemID)
	}

	// pedida mais alta que o piso vence; vazia usa o padrão da categoria; sem nada, media
	if res, _ = tx.Resolver(context.Background(), "sem_conexao", "urgente"); res.PrioridadeNome != "urgente" || *res.PriorityID != "p-u" {
		t.Fatalf("urgente: %+v", res)
	}
	if res, _ = tx.Resolver(context.Background(), "lentidao", ""); res.PrioridadeNome != "baixa" || *res.PriorityID != "p-b" {
		t.Fatalf("padrão da categoria: %+v", res)
	}
	if res, _ = tx.Resolver(context.Background(), "cobranca", "lixo"); res.PrioridadeNome != "media" || *res.PriorityID != "f-m" {
		t.Fatalf("media por padrão, UUID da mesa Financeiro (outro que o do Suporte): %+v", res)
	}
	if res.CatalogItemID != nil {
		t.Fatal("categoria sem `catalogo` não envia catalogItemId")
	}
}

func TestPrioridadeAusenteNaMesaOmiteEAvisa(t *testing.T) {
	tx, rel, err := carregada(t, yamlBase, false, novaFonte())
	if err != nil {
		t.Fatal(err)
	}
	res, err := tx.Resolver(context.Background(), "cobranca", "alta") // Financeiro não tem "Alta"
	if err != nil {
		t.Fatal(err)
	}
	if res.PriorityID != nil || res.PrioridadeNome != "alta" {
		t.Fatalf("priorityId omitido: %+v", res)
	}
	if len(res.Avisos) != 1 || !strings.Contains(res.Avisos[0], "sem UUID na mesa") {
		t.Fatalf("avisos: %v", res.Avisos)
	}
	if !strings.Contains(strings.Join(rel.Problemas, "|"), `prioridade "alta"`) {
		t.Fatalf("problemas do boot: %v", rel.Problemas)
	}
	// a prioridade que a mesa tem não gera aviso
	if res, _ = tx.Resolver(context.Background(), "cobranca", "baixa"); res.PriorityID == nil || len(res.Avisos) != 0 {
		t.Fatalf("baixa existe em Financeiro: %+v", res)
	}
}

func TestNomeNormalizadoBateComAviso(t *testing.T) {
	_, rel, err := carregada(t, yamlBase, false, novaFonte())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(rel.Problemas, "|"), "só o nome normalizado bate") {
		t.Fatalf("\"Suporte Técnico N1\" ≠ \"Suporte Tecnico N1\" (09): resolve, mas avisa: %v", rel.Problemas)
	}
}

func TestStrictFalhaNoBoot(t *testing.T) {
	_, _, err := carregada(t, yamlBase, true, novaFonte())
	if err == nil || !strings.Contains(err.Error(), "STRICT_TAXONOMIA") {
		t.Fatalf("STRICT_TAXONOMIA=true com divergência devia falhar o boot: %v", err)
	}
	if errors.Is(err, ErrReferenciaIndisponivel) {
		t.Fatal("divergência não é referência indisponível")
	}
}

func TestMesaOuDepartamentoInexistenteFicaSemMapa(t *testing.T) {
	y := strings.Replace(yamlBase, `mesa: "Financeiro"`, `mesa: "Mesa Que Não Existe"`, 1)
	y = strings.Replace(y, `departamento: "Suporte"`, `departamento: "Depto Fantasma"`, 1) // sem_conexao
	tx, rel, err := carregada(t, y, false, novaFonte())
	if err != nil {
		t.Fatal(err)
	}
	for _, cat := range []string{"cobranca", "sem_conexao"} {
		if _, err := tx.Resolver(context.Background(), cat, ""); !errors.Is(err, ErrSemMapa) {
			t.Fatalf("%s: %v", cat, err)
		}
	}
	if _, err := tx.Resolver(context.Background(), "lentidao", ""); err != nil {
		t.Fatalf("as outras categorias seguem: %v", err)
	}
	if _, err := tx.Resolver(context.Background(), "nao_existe", ""); !errors.Is(err, ErrSemMapa) {
		t.Fatalf("categoria desconhecida: %v", err)
	}
	if rel.Categorias != 2 || len(rel.Problemas) < 2 {
		t.Fatalf("relatório: %+v", rel)
	}
}

func TestItemDeCatalogoAusenteOmiteCatalogItemID(t *testing.T) {
	y := strings.Replace(yamlBase, `catalogo: "Serviços de Suporte"`, `catalogo: "Item Inexistente"`, 1)
	tx, _, err := carregada(t, y, false, novaFonte())
	if err != nil {
		t.Fatal(err)
	}
	res, _ := tx.Resolver(context.Background(), "sem_conexao", "")
	if res.CatalogItemID != nil || len(res.Avisos) == 0 {
		t.Fatalf("catálogo ausente: omitido + aviso: %+v", res)
	}
}

func TestPrioridadeNullNoArquivoOmiteSemAvisoEUUIDLegadoFunciona(t *testing.T) {
	y := strings.Replace(yamlBase, "  media: \"Média\"", "  media: null", 1)
	y = strings.Replace(y, "  alta: \"Alta\"", "  alta: \"p-a\"", 1) // formato antigo: o próprio UUID
	tx, _, err := carregada(t, y, false, novaFonte())
	if err != nil {
		t.Fatal(err)
	}
	if res, _ := tx.Resolver(context.Background(), "lentidao", "media"); res.PriorityID != nil || len(res.Avisos) != 0 {
		t.Fatalf("null no arquivo = omitir por decisão, sem alerta: %+v", res)
	}
	if res, _ := tx.Resolver(context.Background(), "lentidao", "alta"); res.PriorityID == nil || *res.PriorityID != "p-a" {
		t.Fatalf("UUID literal presente na mesa: %+v", res)
	}
}

func TestCatalogoPelaRotaQuandoAMesaNaoTrazItens(t *testing.T) {
	f := novaFonte()
	m := f.porID["m-sup"]
	m.CatalogItems = nil
	f.porID["m-sup"] = m
	f.catalogo = map[string][]infodesk.Referencia{"m-sup": {r("i-9", "Serviços de Suporte")}}
	tx, _, err := carregada(t, yamlBase, false, f)
	if err != nil {
		t.Fatal(err)
	}
	if res, _ := tx.Resolver(context.Background(), "sem_conexao", ""); res.CatalogItemID == nil || *res.CatalogItemID != "i-9" {
		t.Fatalf("GET /catalog?deskId=: %+v", res)
	}
}

func TestInfodeskForaNoBootEResolverTentaSobDemanda(t *testing.T) {
	f := novaFonte()
	f.err = errors.New("fora do ar")
	agora := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	def, _ := Parse([]byte(yamlBase))
	tx := Nova(def, f, Opcoes{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Agora: func() time.Time { return agora }, RecargaMinima: 30 * time.Second})
	if _, err := tx.Carregar(context.Background()); !errors.Is(err, ErrReferenciaIndisponivel) {
		t.Fatalf("boot com Infodesk fora: %v", err)
	}
	// Dentro do intervalo mínimo: não martela o Infodesk.
	before := f.chamadas
	if _, err := tx.Resolver(context.Background(), "lentidao", ""); !errors.Is(err, ErrReferenciaIndisponivel) || f.chamadas != before {
		t.Fatalf("sem nova tentativa antes de RecargaMinima: err=%v chamadas %d→%d", err, before, f.chamadas)
	}
	// Passado o intervalo e com o Infodesk de volta: carrega sob demanda.
	f.err = nil
	agora = agora.Add(time.Minute)
	if _, err := tx.Resolver(context.Background(), "lentidao", ""); err != nil {
		t.Fatalf("carga sob demanda: %v", err)
	}
}

func TestRecarregarInvalidaOCacheEMantemATabelaSeFalhar(t *testing.T) {
	f := novaFonte()
	tx, _, err := carregada(t, yamlBase, false, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Recarregar(context.Background()); err != nil || f.inval != 1 {
		t.Fatalf("recarregar: err=%v inval=%d", err, f.inval)
	}
	f.err = errors.New("fora")
	if err := tx.Recarregar(context.Background()); !errors.Is(err, ErrReferenciaIndisponivel) {
		t.Fatalf("recarga falha: %v", err)
	}
	if _, err := tx.Resolver(context.Background(), "lentidao", ""); err != nil {
		t.Fatalf("a tabela anterior segue valendo: %v", err)
	}
	if got := tx.DepartamentoNome("d-fin"); got != "Financeiro" {
		t.Fatalf("DepartamentoNome: %q", got)
	}
}

func TestParseRecusaArquivoInvalido(t *testing.T) {
	casos := map[string]string{
		"sem indefinida":     "categorias:\n  a:\n    mesa: m\n    departamento: d\n",
		"sem mesa":           "categorias:\n  a:\n    departamento: d\n  indefinida:\n    mesa: m\n    departamento: d\n",
		"nome com acento":    "categorias:\n  conexão:\n    mesa: m\n    departamento: d\n  indefinida:\n    mesa: m\n    departamento: d\n",
		"prioridade errada":  "categorias:\n  a:\n    mesa: m\n    departamento: d\n    prioridade: critica\n  indefinida:\n    mesa: m\n    departamento: d\n",
		"nome de prioridade": "categorias:\n  indefinida:\n    mesa: m\n    departamento: d\nprioridades:\n  critica: x\n",
		"yaml quebrado":      "categorias: [",
		"vazio":              "",
	}
	for nome, y := range casos {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: devia recusar", nome)
		}
	}
}

// O arquivo versionado (placeholder) tem de continuar legível por este pacote:
// é o mesmo que o cérebro Python lê (uma fonte, dois processos).
func TestArquivoVersionadoEhLegivel(t *testing.T) {
	def, err := Ler("../../../agente-ia/config/taxonomia.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !def.Placeholder || def.Categorias[CategoriaIndefinida].Mesa == "" || def.Categorias["sem_conexao"].PrioridadeMinima != "alta" {
		t.Fatalf("taxonomia versionada: placeholder=%v %+v", def.Placeholder, def.Categorias["sem_conexao"])
	}
	if def.Versao == "" {
		t.Fatal("versao")
	}
	if _, err := Ler("não-existe.yaml"); err == nil {
		t.Fatal("arquivo ausente é erro")
	}
}
