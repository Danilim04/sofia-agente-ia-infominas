// Package taxonomia resolve categoria → destino no Infodesk (mesa, item de
// catálogo, departamento e prioridade). A fonte é `config/taxonomia.yaml`
// (nomes, compartilhado com o cérebro Python — docs/plano/02-classificacao.md)
// e a resolução dos UUIDs acontece no boot e a cada recarga do cache de
// referência, contra GET /desks, GET /desks/:id e GET /departments.
//
// Princípio (regra 1 do CLAUDE.md): mesa, catálogo, departamento e prioridade
// de um ticket vêm daqui — tabela + código —, nunca de texto livre do LLM. O
// cérebro só conhece os NOMES das categorias.
//
// Prioridades e itens de catálogo são POR MESA (docs/infodesk/02): o mesmo nome
// de prioridade tem UUID diferente em cada mesa. Nome que a mesa não tem não
// derruba o atendimento: o `priorityId` é omitido (o Desk aplica o padrão) e o
// problema vira alerta — em STRICT_TAXONOMIA=true vira falha no boot.
package taxonomia

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"bot-infominas/internal/infodesk"
)

// AlertaInvalida é o tipo de alerta (`sofia_alerta`) para taxonomia que não
// bate com o Infodesk (docs/plano/01, seção B, "Alertas").
const AlertaInvalida = "taxonomia_invalida"

// Prioridades canônicas da Sofia (as mesmas do JEV e do Contrato A), da menor
// para a maior.
var prioridades = []string{"baixa", "media", "alta", "urgente"}

// Categoria indefinida: sempre presente; destino da triagem.
const CategoriaIndefinida = "indefinida"

var (
	// ErrSemMapa: a categoria não existe na tabela, ou existe mas mesa ou
	// departamento não foram resolvidos no Infodesk. Mapeia para
	// `categoria_sem_mapa`.
	ErrSemMapa = errors.New("taxonomia: categoria sem mapa")
	// ErrReferenciaIndisponivel: os dados de referência do Infodesk não puderam
	// ser carregados (cache vazio). Mapeia para `referencia_indisponivel`.
	ErrReferenciaIndisponivel = errors.New("taxonomia: referência do Infodesk indisponível")
)

var reNomeCategoria = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// ---------------------------------------------------------------------------
// Definição (o arquivo)
// ---------------------------------------------------------------------------

// CategoriaDef é uma categoria do arquivo. Campos que só o cérebro usa
// (`descricao`, `exemplos`, `nao_e`) não são lidos aqui.
type CategoriaDef struct {
	Mesa             string `yaml:"mesa"`
	Catalogo         string `yaml:"catalogo"`     // opcional: nome do item de catálogo NA mesa
	Departamento     string `yaml:"departamento"` // destino do handoff
	Prioridade       string `yaml:"prioridade"`   // opcional: padrão quando a proposta não traz
	PrioridadeMinima string `yaml:"prioridade_minima"`
}

// Definicao é `config/taxonomia.yaml` lido e validado (só forma; o confronto
// com o Infodesk é de Taxonomia.Carregar).
type Definicao struct {
	Placeholder              bool                    `yaml:"placeholder"`
	Versao                   string                  `yaml:"versao"`
	MesaTriagem              string                  `yaml:"mesa_triagem"`
	DepartamentoHumanoPadrao string                  `yaml:"departamento_humano_padrao"`
	Categorias               map[string]CategoriaDef `yaml:"categorias"`
	// Prioridades: nome canônico → NOME da prioridade no Infodesk (resolvido
	// por mesa) ou, no formato antigo, o próprio UUID. Vazio/null = não enviar
	// `priorityId` para esse nome (decisão explícita do arquivo).
	Prioridades map[string]*string `yaml:"prioridades"`
}

// Ler lê e valida a forma do arquivo de taxonomia.
func Ler(caminho string) (*Definicao, error) {
	b, err := os.ReadFile(caminho) //nolint:gosec // G304: caminho vem da configuração do operador
	if err != nil {
		return nil, fmt.Errorf("taxonomia: ler %s: %w", caminho, err)
	}
	return Parse(b)
}

// Parse valida o conteúdo de um arquivo de taxonomia.
func Parse(b []byte) (*Definicao, error) {
	var d Definicao
	if err := yaml.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("taxonomia: yaml inválido: %w", err)
	}
	if len(d.Categorias) == 0 {
		return nil, errors.New("taxonomia: nenhuma categoria")
	}
	if _, ok := d.Categorias[CategoriaIndefinida]; !ok {
		return nil, fmt.Errorf("taxonomia: a categoria %q é obrigatória", CategoriaIndefinida)
	}
	for nome, c := range d.Categorias {
		if !reNomeCategoria.MatchString(nome) {
			return nil, fmt.Errorf("taxonomia: nome de categoria inválido %q (snake_case, sem acento)", nome)
		}
		if strings.TrimSpace(c.Mesa) == "" || strings.TrimSpace(c.Departamento) == "" {
			return nil, fmt.Errorf("taxonomia: categoria %q sem mesa ou departamento", nome)
		}
		for _, p := range []string{c.Prioridade, c.PrioridadeMinima} {
			if p != "" && indicePrioridade(p) < 0 {
				return nil, fmt.Errorf("taxonomia: categoria %q com prioridade %q fora de %v", nome, p, prioridades)
			}
		}
	}
	for nome := range d.Prioridades {
		if indicePrioridade(nome) < 0 {
			return nil, fmt.Errorf("taxonomia: prioridade %q fora de %v", nome, prioridades)
		}
	}
	return &d, nil
}

func indicePrioridade(nome string) int {
	for i, p := range prioridades {
		if p == nome {
			return i
		}
	}
	return -1
}

// PrioridadeValida diz se nome é uma prioridade canônica.
func PrioridadeValida(nome string) bool { return indicePrioridade(nome) >= 0 }

// ---------------------------------------------------------------------------
// Resolução contra o Infodesk
// ---------------------------------------------------------------------------

// Fonte é o que a taxonomia usa do Infodesk (satisfeito por *infodesk.Client).
type Fonte interface {
	Mesas(ctx context.Context) ([]infodesk.Referencia, error)
	Mesa(ctx context.Context, id string) (infodesk.Mesa, error)
	Catalogo(ctx context.Context, deskID string) ([]infodesk.Referencia, error)
	Departamentos(ctx context.Context) ([]infodesk.Referencia, error)
}

// invalidador é opcional: *infodesk.Client o implementa.
type invalidador interface{ InvalidarCache() }

// Ref é id + nome de um destino resolvido.
type Ref struct {
	ID   string
	Nome string
}

// Resolvida é o destino de uma categoria numa prioridade.
type Resolvida struct {
	Categoria      string
	Mesa           Ref
	Departamento   Ref
	CatalogItemID  *string // nil = não enviar
	PrioridadeNome string  // canônica (baixa|media|alta|urgente)
	PriorityID     *string // nil = não enviar
	// Avisos: o que degradou nesta resolução (prioridade sem UUID na mesa,
	// item de catálogo ausente). Sem PII; o chamador transforma em alerta.
	Avisos []string
}

type destino struct {
	categoria     string
	mesa, dept    Ref
	catalogItemID *string
	prioridadeIDs map[string]*string // canônica → UUID na mesa (nil = omitir)
	avisosPrio    map[string]string  // canônica → motivo de ter ficado nil por falta na mesa
	avisos        []string
	def           CategoriaDef
}

type tabela struct {
	destinos     map[string]*destino
	deptPorID    map[string]string
	deptPadrao   Ref
	carregadaEm  time.Time
	mesasLidas   int
	problemas    []string
	categoriasOK int
}

// Opcoes configura a Taxonomia.
type Opcoes struct {
	// Strict (STRICT_TAXONOMIA): qualquer divergência com o Infodesk falha o
	// Carregar. Sem Strict a divergência é devolvida em Relatorio.Problemas e
	// só as categorias afetadas ficam sem mapa.
	Strict bool
	Log    *slog.Logger
	Agora  func() time.Time
	// RecargaMinima evita martelar o Infodesk quando a referência está fora:
	// Resolver só tenta carregar de novo depois deste intervalo. Default 30 s.
	RecargaMinima time.Duration
}

// Relatorio é o resultado de um Carregar.
type Relatorio struct {
	Problemas   []string // divergências (sem PII): nome de categoria/mesa/departamento
	Categorias  int      // categorias resolvidas por completo
	Placeholder bool
}

// Taxonomia é a tabela viva categoria → destino.
type Taxonomia struct {
	def   *Definicao
	fonte Fonte
	opt   Opcoes

	mu           sync.RWMutex
	tab          *tabela
	ultimaTenta  time.Time
	carregandoMu sync.Mutex
}

// Nova cria a taxonomia; chame Carregar no boot.
func Nova(def *Definicao, fonte Fonte, opt Opcoes) *Taxonomia {
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	if opt.Agora == nil {
		opt.Agora = time.Now
	}
	if opt.RecargaMinima <= 0 {
		opt.RecargaMinima = 30 * time.Second
	}
	return &Taxonomia{def: def, fonte: fonte, opt: opt}
}

// Placeholder informa se o arquivo ainda é a hipótese de trabalho (P1).
func (t *Taxonomia) Placeholder() bool { return t.def.Placeholder }

// Carregar resolve a tabela inteira contra o Infodesk e a instala (troca
// atômica). Em erro ao falar com o Infodesk devolve o erro e MANTÉM a tabela
// anterior. Em STRICT, divergência de nomes também é erro (e não instala).
func (t *Taxonomia) Carregar(ctx context.Context) (Relatorio, error) {
	t.carregandoMu.Lock()
	defer t.carregandoMu.Unlock()
	t.mu.Lock()
	t.ultimaTenta = t.opt.Agora()
	t.mu.Unlock()

	tab, err := t.construir(ctx)
	rel := Relatorio{Placeholder: t.def.Placeholder}
	if err != nil {
		return rel, fmt.Errorf("%w: %w", ErrReferenciaIndisponivel, err)
	}
	rel.Problemas, rel.Categorias = tab.problemas, tab.categoriasOK
	if t.opt.Strict && len(tab.problemas) > 0 {
		return rel, fmt.Errorf("taxonomia inválida (STRICT_TAXONOMIA): %s", strings.Join(tab.problemas, "; "))
	}
	t.mu.Lock()
	t.tab = tab
	t.mu.Unlock()
	if t.def.Placeholder {
		t.opt.Log.Warn("taxonomia PLACEHOLDER (P1): categorias, mesas e departamentos são hipóteses de trabalho até o export do Infodesk",
			"versao", t.def.Versao)
	}
	for _, p := range tab.problemas {
		t.opt.Log.Warn("taxonomia diverge do Infodesk", "problema", p)
	}
	t.opt.Log.Info("taxonomia carregada", "categorias", tab.categoriasOK, "problemas", len(tab.problemas), "mesas", tab.mesasLidas)
	return rel, nil
}

// Recarregar invalida o cache de referência do cliente (quando suportado) e
// resolve de novo — para `INVALID_REFERENCE` (docs/infodesk/06): a taxonomia
// aponta para um UUID que não existe mais.
func (t *Taxonomia) Recarregar(ctx context.Context) error {
	if inv, ok := t.fonte.(invalidador); ok {
		inv.InvalidarCache()
	}
	_, err := t.Carregar(ctx)
	return err
}

// DepartamentoNome devolve o nome do departamento pelo id ("" se desconhecido).
func (t *Taxonomia) DepartamentoNome(id string) string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.tab == nil {
		return ""
	}
	return t.tab.deptPorID[id]
}

// Resolver devolve o destino da categoria para a prioridade pedida.
//
// A prioridade efetiva é a pedida, elevada a `prioridade_minima` da categoria
// quando esta for mais alta (regra de código que vence o modelo); pedida vazia
// usa `prioridade` da categoria e, na falta, "media". Sem tabela carregada tenta
// carregar (no máximo a cada RecargaMinima); sem sucesso →
// ErrReferenciaIndisponivel.
func (t *Taxonomia) Resolver(ctx context.Context, categoria, prioridade string) (Resolvida, error) {
	tab := t.tabela()
	if tab == nil {
		t.mu.RLock()
		cedo := t.opt.Agora().Sub(t.ultimaTenta) < t.opt.RecargaMinima && !t.ultimaTenta.IsZero()
		t.mu.RUnlock()
		if !cedo {
			if _, err := t.Carregar(ctx); err != nil {
				t.opt.Log.Error("taxonomia: carga sob demanda falhou", "err", err)
			}
			tab = t.tabela()
		}
		if tab == nil {
			return Resolvida{}, ErrReferenciaIndisponivel
		}
	}
	d, ok := tab.destinos[categoria]
	if !ok {
		return Resolvida{}, fmt.Errorf("%w: %s", ErrSemMapa, categoria)
	}
	nomePrio := prioridadeEfetiva(prioridade, d.def)
	r := Resolvida{
		Categoria: categoria, Mesa: d.mesa, Departamento: d.dept, CatalogItemID: d.catalogItemID,
		PrioridadeNome: nomePrio, PriorityID: d.prioridadeIDs[nomePrio],
		Avisos: append([]string(nil), d.avisos...),
	}
	if motivo := d.avisosPrio[nomePrio]; motivo != "" {
		r.Avisos = append(r.Avisos, motivo)
	}
	return r, nil
}

func (t *Taxonomia) tabela() *tabela {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.tab
}

// prioridadeEfetiva aplica pedida → padrão da categoria → "media", e o piso
// `prioridade_minima`.
func prioridadeEfetiva(pedida string, c CategoriaDef) string {
	p := pedida
	if indicePrioridade(p) < 0 {
		p = c.Prioridade
	}
	if indicePrioridade(p) < 0 {
		p = "media"
	}
	if c.PrioridadeMinima != "" && indicePrioridade(c.PrioridadeMinima) > indicePrioridade(p) {
		p = c.PrioridadeMinima
	}
	return p
}

// construir lê o Infodesk e monta a tabela. Erro = não deu para ler a
// referência; divergência de nome vira Problemas da tabela.
func (t *Taxonomia) construir(ctx context.Context) (*tabela, error) {
	mesas, err := t.fonte.Mesas(ctx)
	if err != nil {
		return nil, fmt.Errorf("mesas: %w", err)
	}
	depts, err := t.fonte.Departamentos(ctx)
	if err != nil {
		return nil, fmt.Errorf("departamentos: %w", err)
	}
	if len(mesas) == 0 {
		return nil, errors.New("mesas: lista vazia")
	}

	tab := &tabela{destinos: map[string]*destino{}, deptPorID: map[string]string{}, carregadaEm: t.opt.Agora()}
	for _, d := range depts {
		tab.deptPorID[d.ID] = d.Nome
	}
	var problemas []string
	problema := func(f string, a ...any) { problemas = append(problemas, fmt.Sprintf(f, a...)) }

	if padrao := strings.TrimSpace(t.def.DepartamentoHumanoPadrao); padrao != "" {
		if r, aviso, ok := achar(depts, padrao); ok {
			tab.deptPadrao = Ref{ID: r.ID, Nome: r.Nome}
			if aviso != "" {
				problema("departamento_humano_padrao %q: %s", padrao, aviso)
			}
		} else {
			problema("departamento_humano_padrao %q não existe no Infodesk", padrao)
		}
	}

	mesaCache := map[string]infodesk.Mesa{}
	nomes := make([]string, 0, len(t.def.Categorias))
	for n := range t.def.Categorias {
		nomes = append(nomes, n)
	}
	sort.Strings(nomes)

	for _, nome := range nomes {
		def := t.def.Categorias[nome]
		d := &destino{categoria: nome, def: def, prioridadeIDs: map[string]*string{}, avisosPrio: map[string]string{}}

		refMesa, aviso, ok := achar(mesas, def.Mesa)
		if !ok {
			problema("categoria %s: mesa %q não existe no Infodesk", nome, def.Mesa)
			continue
		}
		if aviso != "" {
			problema("categoria %s: mesa %q: %s", nome, def.Mesa, aviso)
		}
		refDept, aviso, ok := achar(depts, def.Departamento)
		if !ok {
			problema("categoria %s: departamento %q não existe no Infodesk", nome, def.Departamento)
			continue
		}
		if aviso != "" {
			problema("categoria %s: departamento %q: %s", nome, def.Departamento, aviso)
		}
		d.mesa = Ref{ID: refMesa.ID, Nome: refMesa.Nome}
		d.dept = Ref{ID: refDept.ID, Nome: refDept.Nome}

		mesa, lida := mesaCache[refMesa.ID]
		if !lida {
			m, err := t.fonte.Mesa(ctx, refMesa.ID)
			if err != nil {
				return nil, fmt.Errorf("mesa %s: %w", refMesa.ID, err)
			}
			mesa, mesaCache[refMesa.ID] = m, m
		}

		// Catálogo: opcional; "omitir = não enviar" (07 D5).
		if c := strings.TrimSpace(def.Catalogo); c != "" {
			itens := mesa.CatalogItems
			if len(itens) == 0 {
				if lista, err := t.fonte.Catalogo(ctx, refMesa.ID); err == nil {
					itens = lista
				}
			}
			if it, aviso, ok := achar(itens, c); ok {
				id := it.ID
				d.catalogItemID = &id
				if aviso != "" {
					problema("categoria %s: catálogo %q: %s", nome, c, aviso)
				}
			} else {
				problema("categoria %s: item de catálogo %q não existe na mesa %q", nome, c, refMesa.Nome)
				d.avisos = append(d.avisos, fmt.Sprintf("item de catálogo %q ausente na mesa %q: catalogItemId omitido", c, refMesa.Nome))
			}
		}

		// Prioridades: nome canônico → UUID na mesa.
		for _, canon := range prioridades {
			alvo, definido := t.def.Prioridades[canon]
			if !definido || alvo == nil || strings.TrimSpace(*alvo) == "" {
				d.prioridadeIDs[canon] = nil // omitir por decisão do arquivo (P4)
				continue
			}
			if id, aviso, ok := acharPrioridade(mesa.Priorities, strings.TrimSpace(*alvo)); ok {
				d.prioridadeIDs[canon] = &id
				if aviso != "" {
					problema("mesa %q: prioridade %q: %s", refMesa.Nome, *alvo, aviso)
				}
				continue
			}
			d.prioridadeIDs[canon] = nil
			msg := fmt.Sprintf("prioridade %q (%s) sem UUID na mesa %q: priorityId omitido", canon, *alvo, refMesa.Nome)
			d.avisosPrio[canon] = msg
			problema("mesa %q: prioridade %q (%s) não existe", refMesa.Nome, canon, *alvo)
		}
		tab.destinos[nome] = d
		tab.categoriasOK++
	}
	tab.mesasLidas = len(mesaCache)
	tab.problemas = dedup(problemas)
	return tab, nil
}

func dedup(s []string) []string {
	visto := map[string]bool{}
	out := s[:0:0]
	for _, v := range s {
		if !visto[v] {
			visto[v] = true
			out = append(out, v)
		}
	}
	return out
}

// achar procura o nome na lista: exato primeiro; senão pelo nome normalizado
// (caixa, acentos, espaços) com aviso — o 09 mostra grafias como "Suporte
// Tecnico N1". Ambíguo ou ausente = !ok.
func achar(lista []infodesk.Referencia, nome string) (infodesk.Referencia, string, bool) {
	for _, r := range lista {
		if r.Nome == nome {
			return r, "", true
		}
	}
	alvo := normalizar(nome)
	var achados []infodesk.Referencia
	for _, r := range lista {
		if normalizar(r.Nome) == alvo {
			achados = append(achados, r)
		}
	}
	if len(achados) == 1 {
		return achados[0], fmt.Sprintf("só o nome normalizado bate (cadastrado como %q)", achados[0].Nome), true
	}
	return infodesk.Referencia{}, "", false
}

// acharPrioridade aceita o NOME da prioridade na mesa ou, no formato antigo do
// arquivo, o próprio UUID.
func acharPrioridade(lista []infodesk.Referencia, alvo string) (string, string, bool) {
	for _, r := range lista {
		if r.ID == alvo {
			return r.ID, "", true
		}
	}
	r, aviso, ok := achar(lista, alvo)
	return r.ID, aviso, ok
}

var semAcento = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ã", "a", "ä", "a",
	"é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i",
	"ó", "o", "ò", "o", "ô", "o", "õ", "o", "ö", "o",
	"ú", "u", "ù", "u", "û", "u", "ü", "u", "ç", "c",
)

func normalizar(s string) string {
	return strings.Join(strings.Fields(semAcento.Replace(strings.ToLower(s))), " ")
}

// CaminhoDoAmbiente é o arquivo de taxonomia: TAXONOMIA_PATH, ou o primeiro
// que existir entre `config/taxonomia.yaml` (volume do compose) e
// `../agente-ia/config/taxonomia.yaml` (execução a partir de backend/ no repo).
func CaminhoDoAmbiente() string {
	if p := strings.TrimSpace(os.Getenv("TAXONOMIA_PATH")); p != "" {
		return p
	}
	candidatos := []string{"config/taxonomia.yaml", "../agente-ia/config/taxonomia.yaml"}
	for _, c := range candidatos {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return candidatos[0]
}

// StrictDoAmbiente lê STRICT_TAXONOMIA (default false): true faz o boot falhar
// quando a taxonomia diverge do Infodesk.
func StrictDoAmbiente() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("STRICT_TAXONOMIA")))
	return v == "1" || v == "true" || v == "t" || v == "sim" || v == "yes" || v == "on"
}
