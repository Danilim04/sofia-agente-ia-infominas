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
package sac

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Options configura o cliente.
type Options struct {
	BaseURL    string        // raiz do backend SAC (ex.: https://backdev.azapfy.com.br)
	PortalURL  string        // raiz do portal p/ montar o link do chat
	ServiceCod string        // login privilegiado p/ `editar` (define prioridade)
	GrupoEmp   string        // "desk" do SAC (ex.: AZAPERS) — grupo onde o chamado é aberto
	Empresa    string        // incidente.empresa (ex.: AZAPFY)
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
	empresa    string
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
	empresa := opts.Empresa
	if empresa == "" {
		empresa = "AZAPFY"
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
		empresa:    empresa,
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
	ClienteGrupo string // incidente.cliente (grupo do usuário)
	Categoria    string
	Ocorrencia   string
	Item         string
	Prazo        float64
	Resumo       string
	Descricao    string
}

// Criar abre o incidente e devolve o protocolo. status:true é sucesso mesmo com
// aviso de e-mail (notificacao).
func (c *Client) Criar(ctx context.Context, ch NovoChamado) (string, error) {
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
			"empresa":    c.empresa,
			"icone":      "none",
			"prazo":      ch.Prazo,
			"cliente":    ch.ClienteGrupo,
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
	if err := c.post(ctx, "/api/sac/incidente/criar", body, &resp); err != nil {
		return "", err
	}
	if !resp.Status || resp.Protocolo == "" {
		return "", fmt.Errorf("sac criar: %s", firstNonEmpty(resp.Mensagem, "status false"))
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
	if err := c.post(ctx, "/api/sac/incidente/editar", body, &resp); err != nil {
		return err
	}
	if !resp.Status {
		return fmt.Errorf("sac editar prioridade: %s", firstNonEmpty(resp.Mensagem, "status false"))
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
	if err := c.post(ctx, "/api/sac/incidente/buscarrelator", body, &resp); err != nil {
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

func (c *Client) post(ctx context.Context, path string, body any, out any) error {
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
		return fmt.Errorf("sac %s: status http %d", path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("sac %s: decode: %w", path, err)
	}
	return nil
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
