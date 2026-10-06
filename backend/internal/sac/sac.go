// Package sac é o cliente do SAC da Azapfy (atendimento/chamados). Cobre a
// abertura de incidentes já preenchidos/categorizados, a definição de prioridade
// (via conta de serviço) e a listagem dos chamados do relator, além de cachear a
// configuração (categorias/ocorrências) usada para classificar corretamente.
//
// Decisões de design (ver plano):
//   - `criar` força status=PENDENTE e NÃO aceita prioridade; por isso a
//     prioridade é definida num `editar` separado com um `cod` privilegiado
//     (SAC/super) que passa no gate `IdentificaUsuario` do backend PHP.
//   - O link do chamado NÃO é devolvido pela API — é montado pelo padrão do
//     portal (LinkChamado).
//   - `criar` pode devolver `status:true` com um aviso de e-mail (SMTP) em
//     `notificacao`: isso NÃO invalida a criação; tratamos status:true como ok.
//   - No contrato do SAC, `grupo_emp` é a EQUIPE que atende (o desk, ex.:
//     AZAPERS — fixo, SAC_GRUPO_EMP) e `incidente.empresa` é a EMPRESA DO
//     CLIENTE (o grupo do perfil escolhido na abertura). Não existe empresa
//     fixa: sem grupo no perfil, a abertura é recusada antes de chamar o SAC.
package sac

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Options configura o cliente.
type Options struct {
	BaseURL    string        // raiz do backend SAC (ex.: https://backdev.azapfy.com.br)
	PortalURL  string        // raiz do portal p/ montar o link do chat
	ServiceCod string        // login privilegiado p/ `editar` (define prioridade)
	GrupoEmp   string        // equipe/desk do SAC (ex.: AZAPERS) — grupo onde o chamado é aberto
	Timezone   string        // ex.: America/Sao_Paulo
	APIToken   string        // bearer opcional
	ConfigTTL  time.Duration // validade do cache de configuração
	HTTP       *http.Client  // opcional (default: timeout 30s)
	Now        func() time.Time
}

// Client fala com o SAC.
type Client struct {
	http       *http.Client
	baseURL    string
	portalURL  string
	serviceCod string
	grupoEmp   string
	timezone   string
	apiToken   string
	now        func() time.Time

	cfgTTL   time.Duration
	cfgMu    sync.Mutex
	cfgCache map[string]cachedConfig
}

type cachedConfig struct {
	cfg Config
	exp time.Time
}

// New constrói o cliente, aplicando defaults sensatos.
func New(opts Options) *Client {
	httpc := opts.HTTP
	if httpc == nil {
		httpc = &http.Client{Timeout: 30 * time.Second}
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	grupo := opts.GrupoEmp
	if grupo == "" {
		grupo = "AZAPERS"
	}
	tz := opts.Timezone
	if tz == "" {
		tz = "America/Sao_Paulo"
	}
	portal := strings.TrimRight(opts.PortalURL, "/")
	if portal == "" {
		portal = "https://atendimento.azapfy.com.br"
	}
	ttl := opts.ConfigTTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &Client{
		http:       httpc,
		baseURL:    strings.TrimRight(opts.BaseURL, "/"),
		portalURL:  portal,
		serviceCod: opts.ServiceCod,
		grupoEmp:   grupo,
		timezone:   tz,
		apiToken:   opts.APIToken,
		now:        now,
		cfgTTL:     ttl,
		cfgCache:   map[string]cachedConfig{},
	}
}

// GrupoEmp devolve o desk configurado (grupo onde os chamados são abertos).
func (c *Client) GrupoEmp() string { return c.grupoEmp }

// TemContaServico informa se há um cod de serviço para definir prioridade.
func (c *Client) TemContaServico() bool { return strings.TrimSpace(c.serviceCod) != "" }

// ---------------------------------------------------------------------------
// Configuração (categorias/ocorrências) — fonte de verdade da categorização
// ---------------------------------------------------------------------------

// Tipo é uma ocorrência configurada: `Nome` é a ocorrência, ligada a uma
// categoria, com prazo (SLA, em horas) e item opcional.
type Tipo struct {
	Nome      string  `json:"nome"`      // = incidente.ocorrencia
	Categoria string  `json:"categoria"` // = incidente.categoria
	Descricao string  `json:"descricao"`
	Prazo     float64 `json:"prazo"`
	Item      string  `json:"item"` // pode vir vazio
}

// Config é o subconjunto de parametros_sac que usamos (ignora o resto).
type Config struct {
	Categorias []string `json:"categorias"`
	Tipos      []Tipo   `json:"tipos"`
}

// TiposValidos devolve apenas as ocorrências utilizáveis (com nome e categoria).
func (cfg Config) TiposValidos() []Tipo {
	out := make([]Tipo, 0, len(cfg.Tipos))
	for _, t := range cfg.Tipos {
		if strings.TrimSpace(t.Nome) == "" || strings.TrimSpace(t.Categoria) == "" {
			continue
		}
		out = append(out, t)
	}
	return out
}

// AcharTipo casa uma ocorrência (e, em desempate, a categoria) de forma
// tolerante a caixa. A categoria/prazo/item do tipo encontrado são a verdade.
func (cfg Config) AcharTipo(categoria, ocorrencia string) (Tipo, bool) {
	oc := strings.TrimSpace(strings.ToUpper(ocorrencia))
	cat := strings.TrimSpace(strings.ToUpper(categoria))
	var match *Tipo
	for i := range cfg.Tipos {
		t := cfg.Tipos[i]
		if strings.ToUpper(strings.TrimSpace(t.Nome)) != oc {
			continue
		}
		if strings.ToUpper(strings.TrimSpace(t.Categoria)) == cat {
			return t, true // casamento exato (ocorrência + categoria)
		}
		if match == nil {
			match = &cfg.Tipos[i] // guarda 1º por ocorrência caso a categoria não bata
		}
	}
	if match != nil {
		return *match, true
	}
	return Tipo{}, false
}

// BuscarConfig devolve a config do grupo (cacheada por TTL).
func (c *Client) BuscarConfig(ctx context.Context, grupoEmp string) (Config, error) {
	if grupoEmp == "" {
		grupoEmp = c.grupoEmp
	}
	c.cfgMu.Lock()
	if e, ok := c.cfgCache[grupoEmp]; ok && c.now().Before(e.exp) {
		c.cfgMu.Unlock()
		return e.cfg, nil
	}
	c.cfgMu.Unlock()

	var resp struct {
		Status     bool   `json:"status"`
		Parametros Config `json:"parametros"`
	}
	if err := c.post(ctx, "/api/sac/config/buscar", map[string]any{"grupo_emp": grupoEmp}, &resp); err != nil {
		return Config{}, err
	}
	if !resp.Status {
		return Config{}, fmt.Errorf("sac config/buscar: status false")
	}
	c.cfgMu.Lock()
	c.cfgCache[grupoEmp] = cachedConfig{cfg: resp.Parametros, exp: c.now().Add(c.cfgTTL)}
	c.cfgMu.Unlock()
	return resp.Parametros, nil
}

// ---------------------------------------------------------------------------
// Abertura / prioridade / listagem
// ---------------------------------------------------------------------------

// NovoChamado são os dados de um incidente a abrir. Os campos do relator vêm da
// identidade resolvida (nunca do LLM); conteúdo/classificação vêm do agente.
type NovoChamado struct {
	NomeRelator  string
	Email        string // cod_relator + contato_relator.email
	Telefone     string
	ClienteGrupo string // incidente.empresa: empresa (grupo) do cliente, escolhida entre as do perfil
	Categoria    string
	Ocorrencia   string
	Item         string
	Prazo        float64
	Resumo       string
	Descricao    string
}

// Pré-condições da abertura que o SAC recusaria (422): checadas aqui, antes de
// qualquer requisição — e nunca contornadas com um valor fixo.
var (
	// ErrSemEmail: o relator não tem e-mail no cadastro. O SAC exige
	// cod_relator/contato_relator.email; o dado vem do perfil, nunca do LLM.
	ErrSemEmail = errors.New("sac criar: relator sem e-mail no cadastro (cod_relator é obrigatório no SAC)")
	// ErrSemEmpresa: o chamado não tem a empresa do cliente (incidente.empresa).
	ErrSemEmpresa = errors.New("sac criar: chamado sem empresa do cliente (incidente.empresa)")
)

// Criar abre o incidente e devolve o protocolo. status:true é sucesso mesmo com
// aviso de e-mail (notificacao). Pré-condições são checadas ANTES de qualquer
// requisição: sem e-mail do relator ou sem empresa do cliente não há chamada
// ao SAC.
func (c *Client) Criar(ctx context.Context, ch NovoChamado) (string, error) {
	if strings.TrimSpace(ch.Email) == "" {
		return "", ErrSemEmail
	}
	if strings.TrimSpace(ch.ClienteGrupo) == "" {
		return "", ErrSemEmpresa
	}
	item := strings.TrimSpace(ch.Item)
	if item == "" {
		item = "INCIDENTE" // mesmo default do backend quando o tipo não traz item
	}
	body := map[string]any{
		"nome_relator":    ch.NomeRelator,
		"cod_relator":     ch.Email,
		"contato_relator": map[string]any{"email": ch.Email, "telefone": ch.Telefone},
		"grupo_emp":       c.grupoEmp,
		"incidente": map[string]any{
			"resumo":     ch.Resumo,
			"descricao":  ch.Descricao,
			"item":       item,
			"categoria":  ch.Categoria,
			"ocorrencia": ch.Ocorrencia,
			"empresa":    ch.ClienteGrupo, // empresa do CLIENTE — nunca a equipe nem um valor fixo
			"icone":      "none",
			"prazo":      ch.Prazo,
		},
		"anexos":     []any{},
		"status_doc": "",
		"setor":      "SUPORTE",
		"timezone":   c.timezone,
	}

	var resp struct {
		Status    bool   `json:"status"`
		Protocolo string `json:"protocolo"`
		Mensagem  string `json:"mensagem"`
	}
	// Valores pessoais que enviamos: se o SAC os ecoar num erro, saem do log.
	pessoais := []string{ch.NomeRelator, ch.Email, ch.Telefone, ch.Resumo, ch.Descricao}
	if err := c.post(ctx, "/api/sac/incidente/criar", body, &resp, pessoais...); err != nil {
		return "", err
	}
	if !resp.Status || resp.Protocolo == "" {
		return "", fmt.Errorf("sac criar: %s", firstNonEmpty(higienizarTexto(resp.Mensagem, pessoais...), "status false"))
	}
	return resp.Protocolo, nil
}

// DefinirPrioridade edita só a prioridade do chamado, usando o cod de serviço.
// Não enviar `incidente` aqui — `editar` sobrescreveria o objeto inteiro.
func (c *Client) DefinirPrioridade(ctx context.Context, protocolo, prioridade string) error {
	if !c.TemContaServico() {
		return fmt.Errorf("sem SAC_SERVICE_COD: prioridade não definida")
	}
	body := map[string]any{
		"grupo_emp":  c.grupoEmp,
		"cod":        c.serviceCod,
		"protocolo":  protocolo,
		"prioridade": prioridade,
	}
	var resp struct {
		Status   bool   `json:"status"`
		Mensagem string `json:"mensagem"`
	}
	if err := c.post(ctx, "/api/sac/incidente/editar", body, &resp, c.serviceCod); err != nil {
		return err
	}
	if !resp.Status {
		return fmt.Errorf("sac editar prioridade: %s", firstNonEmpty(higienizarTexto(resp.Mensagem, c.serviceCod), "status false"))
	}
	return nil
}

// Chamado é um incidente do relator (subconjunto que devolvemos ao agente).
type Chamado struct {
	Protocolo  string `json:"protocolo"`
	GrupoEmp   string `json:"grupo_emp"`
	CodRelator string `json:"cod_relator"`
	Status     string `json:"status"`
	DtAbertura string `json:"dt_abertura"`
	Incidente  struct {
		Resumo     string `json:"resumo"`
		Categoria  string `json:"categoria"`
		Ocorrencia string `json:"ocorrencia"`
	} `json:"incidente"`
}

// Aberto informa se o chamado está em aberto (PENDENTE ou EM ANDAMENTO).
func (ch Chamado) Aberto() bool {
	s := strings.ToUpper(strings.TrimSpace(ch.Status))
	return s == "PENDENTE" || s == "EM ANDAMENTO"
}

// BuscarRelator lista os chamados do relator (casa por cod_relator = email).
func (c *Client) BuscarRelator(ctx context.Context, email string) ([]Chamado, error) {
	body := map[string]any{"cod": email, "timezone": c.timezone}
	var resp struct {
		Status bool      `json:"status"`
		Dados  []Chamado `json:"dados"`
	}
	if err := c.post(ctx, "/api/sac/incidente/buscarrelator", body, &resp, email); err != nil {
		return nil, err
	}
	if !resp.Status {
		return nil, fmt.Errorf("sac buscarrelator: status false")
	}
	return resp.Dados, nil
}

// LinkChamado monta o link do chat do chamado (padrão do portal, não vem da API).
func (c *Client) LinkChamado(codRelator, grupoEmp, protocolo string) string {
	if grupoEmp == "" {
		grupoEmp = c.grupoEmp
	}
	// O portal espera o email cru no path (ex.: .../chat/joao@x.com/AZAPERS/ZP...).
	return c.portalURL + "/chat/" + codRelator + "/" + grupoEmp + "/" + protocolo
}

// ---------------------------------------------------------------------------
// HTTP helper
// ---------------------------------------------------------------------------

// post faz a requisição e decodifica a resposta em `out`. `redigir` são os
// valores pessoais enviados nesta chamada (nome, e-mail, telefone...): se o SAC
// os ecoar numa resposta de erro, eles são removidos antes de o corpo entrar
// no erro devolvido (e, por ele, no log).
func (c *Client) post(ctx context.Context, path string, body any, out any, redigir ...string) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", "https://atendimento.azapfy.com.br/")
	if c.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiToken)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("sac %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		// O corpo é a única pista do porquê (ex.: 422 de validação): entra no
		// erro já higienizado — sem dado pessoal, truncado — para o log de
		// quem chamou. O corpo cru nunca é guardado.
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, corpoErroLeituraMax))
		return &HTTPError{Path: path, Status: resp.StatusCode, Corpo: higienizarTexto(string(raw), redigir...)}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("sac %s: decode: %w", path, err)
	}
	return nil
}

// HTTPError é uma resposta de erro HTTP (>= 400) do SAC. Além do status, traz
// um trecho do corpo já higienizado (sem dado pessoal, truncado): o 422 de
// validação, por exemplo, só é diagnosticável pelo corpo.
type HTTPError struct {
	Path   string
	Status int
	Corpo  string
}

func (e *HTTPError) Error() string {
	if e.Corpo == "" {
		return fmt.Sprintf("sac %s: status http %d", e.Path, e.Status)
	}
	return fmt.Sprintf("sac %s: status http %d: %s", e.Path, e.Status, e.Corpo)
}

// Tetos do texto de erro do SAC que vai para o log.
const (
	corpoErroLeituraMax = 8 << 10 // bytes lidos da resposta de erro (o resto é descartado)
	corpoErroMax        = 500     // runas do trecho higienizado que entra no erro/log
)

var (
	// reEmailCorpo e reNumeroCorpo mascaram o que parecer dado pessoal em texto
	// vindo do SAC: e-mails e números longos (telefone, CPF/CNPJ, CEP — 7+
	// dígitos, com ou sem separadores). Protocolos (ZPRS25208042) continuam
	// legíveis: o prefixo de letras não forma fronteira de palavra antes dos
	// dígitos.
	reEmailCorpo  = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	reNumeroCorpo = regexp.MustCompile(`\b\d(?:[\s().\-/]{0,2}\d){6,}\b`)
)

// higienizarTexto prepara um texto vindo do SAC (corpo de erro HTTP, campo
// `mensagem`) para o log: normaliza JSON (desfaz os \uXXXX do PHP), remove os
// valores pessoais que nós mesmos enviamos (`redigir`), mascara o que parecer
// e-mail ou número longo, achata espaços/controle numa linha e corta em
// corpoErroMax runas.
func higienizarTexto(s string, redigir ...string) string {
	s = normalizarJSON(s)
	for _, v := range redigir {
		v = strings.TrimSpace(v)
		if utf8.RuneCountInString(v) < 3 {
			continue // curto demais: não identifica ninguém e redigir viraria ruído
		}
		formas := []string{v}
		if j := jsonTexto(v); j != v {
			formas = append(formas, j) // como o SAC ecoaria dentro de uma string JSON
		}
		for _, forma := range formas {
			s = regexp.MustCompile(`(?i)`+regexp.QuoteMeta(forma)).ReplaceAllLiteralString(s, "[omitido]")
		}
	}
	s = reEmailCorpo.ReplaceAllLiteralString(s, "[email]")
	s = reNumeroCorpo.ReplaceAllLiteralString(s, "[numero]")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	return truncarRunas(s, corpoErroMax)
}

// normalizarJSON reescreve um corpo JSON de forma canônica e legível (o PHP
// escapa não-ASCII como \u00e9 e barras como \/): assim a redação de valores
// conhecidos casa com o que o SAC ecoou e o log sai legível. Texto que não é
// JSON volta como está.
func normalizarJSON(s string) string {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return s
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return s
	}
	return strings.TrimSpace(buf.String())
}

// jsonTexto devolve v como apareceria dentro de uma string JSON, sem as aspas
// (quebras de linha viram \n etc.).
func jsonTexto(v string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return v
	}
	s := strings.TrimSpace(buf.String())
	if len(s) >= 2 {
		s = s[1 : len(s)-1]
	}
	return s
}

func truncarRunas(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "… [truncado]"
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
