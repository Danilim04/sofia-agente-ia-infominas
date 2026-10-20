// Package infodesk é o cliente Go da API de integração do Infodesk (Infominas
// Desk) e o ÚNICO pacote do gateway que fala HTTP com ela. Também traz os
// tipos do webhook e a verificação da assinatura HMAC. Fonte: docs/infodesk/
// (convertido do PDF oficial de 5 páginas); o que o PDF não responde está em
// docs/infodesk/07-pontos-ambiguos.md e aparece aqui como hipótese de
// trabalho marcada com o item (T1, C1, W3…) e `TODO(P3)`.
//
// O que a Infominas NÃO documenta e como este pacote lida com isso:
//
//   - Resposta de POST /tickets e GET /tickets/:number (T1): só `number` é
//     garantido e o tipo dele é desconhecido. Ticket guarda Number como string
//     (decodifica número ou string) e o resto em Bruto (map).
//   - Schema de GET /conversations e GET /conversations/:id (C1, C2):
//     devolvidos como map/[]map sem tipar; a listagem aceita array puro ou
//     envelope {data|items|conversations|results: [...]}.
//   - Corpo de POST /conversations/:id/finish (C5): nenhum documentado; envia
//     `{}`.
//   - 5xx e Retry-After (A4): tratados como transitórios; Retry-After é
//     exposto em HTTPError.RetryAfter — o cliente nunca dorme nem repete.
//   - Payload de conversation.started/finished (W1): só `conversationId` é
//     assumido (DecodificarIDConversa). Mídia (W3): body vazio = mídia.
//   - Escopo dos endpoints de referência (A3): 403 neles é erro de
//     configuração, como nos demais.
//
// Regras herdadas do plano: nenhum retry dentro do cliente (em especial em
// CriarTicket — ADR 0005); erro do Infodesk nunca chega cru ao cliente final
// nem ao LLM (HTTPError.Mensagem já é higienizada); a chave de API nunca
// aparece em erro ou log.
package infodesk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// PrefixoRotas é o prefixo de versão da API; Options.BaseURL é a raiz antes
// dele (https://infodesk.infominas.net/api). Se BaseURL já vier com o prefixo
// (como em INFODESK_BASE_URL do plano), ele não é duplicado.
const PrefixoRotas = "/integration/v1"

const (
	timeoutPadrao  = 15 * time.Second
	cacheTTLPadrao = 10 * time.Minute
	respostaMax    = 8 << 20 // bytes lidos de uma resposta 2xx
)

// Options configura o cliente. Zero values recebem defaults em New.
type Options struct {
	BaseURL  string           // raiz da API (sem /integration/v1 ou com — ambos aceitos)
	APIKey   string           // chave ik_live_… — vai em Authorization: Bearer
	HTTP     *http.Client     // opcional (default: timeout 15 s)
	Now      func() time.Time // opcional (default: time.Now); injetável para o cache
	CacheTTL time.Duration    // validade do cache de mesas/departamentos/atendentes (default 10 min)
}

// Client fala com o Infodesk. Seguro para uso concorrente.
type Client struct {
	http     *http.Client
	baseURL  string
	apiKey   string
	now      func() time.Time
	cacheTTL time.Duration

	mu    sync.Mutex
	cache map[string]entradaCache
}

type entradaCache struct {
	valor any
	exp   time.Time
}

// New constrói o cliente aplicando defaults.
func New(o Options) *Client {
	httpc := o.HTTP
	if httpc == nil {
		httpc = &http.Client{Timeout: timeoutPadrao}
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	ttl := o.CacheTTL
	if ttl <= 0 {
		ttl = cacheTTLPadrao
	}
	base := strings.TrimRight(strings.TrimSpace(o.BaseURL), "/")
	base = strings.TrimSuffix(base, PrefixoRotas)
	return &Client{
		http:     httpc,
		baseURL:  base,
		apiKey:   o.APIKey,
		now:      now,
		cacheTTL: ttl,
		cache:    map[string]entradaCache{},
	}
}

// ---------------------------------------------------------------------------
// Conversas (04-conversas.md)
// ---------------------------------------------------------------------------

// EnviarMensagem faz POST /conversations/:id/messages com `{body}`. Só texto:
// mídia, templates e botões não são documentados. Quebra em bolhas, formato
// WhatsApp e retry com dedup ficam no chamador (engine).
func (c *Client) EnviarMensagem(ctx context.Context, convID, body string) error {
	if err := exigirID(convID); err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return ErrCorpoVazio
	}
	return c.do(ctx, http.MethodPost, rotaConversa(convID, "messages"), nil, map[string]string{"body": body}, nil)
}

// TransferirDepartamento faz POST /conversations/:id/department com
// `{departmentId}`. É o caminho de "transferir para fila" do plano (C4).
// deptID vazio é ErrIDVazio — nunca vira `null` por acidente, porque `null`
// REMOVE a conversa de qualquer departamento (ver RemoverDepartamento): um
// nome ausente na tabela de roteamento tem de falhar, não tirar o cliente da
// fila.
func (c *Client) TransferirDepartamento(ctx context.Context, convID, deptID string) error {
	if err := exigirID(convID); err != nil {
		return err
	}
	if err := exigirID(deptID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, rotaConversa(convID, "department"), nil, map[string]string{"departmentId": deptID}, nil)
}

// RemoverDepartamento envia `{"departmentId": null}` ("ou null para remover",
// texto literal do PDF). Implementado por completude; não usado na v1.
func (c *Client) RemoverDepartamento(ctx context.Context, convID string) error {
	if err := exigirID(convID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, rotaConversa(convID, "department"), nil, map[string]*string{"departmentId": nil}, nil)
}

// Atribuir faz POST /conversations/:id/assign com `{userId}`. Implementado,
// não usado na v1 (plano: roteamento é por departamento).
func (c *Client) Atribuir(ctx context.Context, convID, userID string) error {
	if err := exigirID(convID); err != nil {
		return err
	}
	if err := exigirID(userID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, rotaConversa(convID, "assign"), nil, map[string]string{"userId": userID}, nil)
}

// Encerrar faz POST /conversations/:id/finish. Corpo não documentado (C5):
// envia `{}`. Implementado, não chamado na v1 (C6).
func (c *Client) Encerrar(ctx context.Context, convID string) error {
	if err := exigirID(convID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, rotaConversa(convID, "finish"), nil, map[string]any{}, nil)
}

// Conversas faz GET /conversations?tab=&departmentId= (ambos opcionais). O
// schema do item não é documentado (C1), por isso []map[string]any; aceita
// array puro ou envelope com a lista em data/items/conversations/results.
// TODO(P3): tipar quando a Infominas enviar o JSON real.
func (c *Client) Conversas(ctx context.Context, tab, deptID string) ([]map[string]any, error) {
	q := url.Values{}
	if strings.TrimSpace(tab) != "" {
		q.Set("tab", tab)
	}
	if strings.TrimSpace(deptID) != "" {
		q.Set("departmentId", deptID)
	}
	var raw json.RawMessage
	if err := c.do(ctx, http.MethodGet, "/conversations", q, nil, &raw); err != nil {
		return nil, err
	}
	return listaDeConversas("/conversations", raw)
}

// Conversa faz GET /conversations/:id (conversa com mensagens). Schema não
// documentado (C2): map cru. TODO(P3).
func (c *Client) Conversa(ctx context.Context, id string) (map[string]any, error) {
	if err := exigirID(id); err != nil {
		return nil, err
	}
	var out map[string]any
	if err := c.do(ctx, http.MethodGet, rotaConversa(id, ""), nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Dados de referência (02-dados-referencia.md) — cache em memória com TTL
// ---------------------------------------------------------------------------

// Mesas faz GET /desks (cache TTL).
func (c *Client) Mesas(ctx context.Context) ([]Referencia, error) {
	return cacheado(ctx, c, "/desks", func(ctx context.Context) ([]Referencia, error) {
		var out []Referencia
		err := c.do(ctx, http.MethodGet, "/desks", nil, nil, &out)
		return out, err
	})
}

// Departamentos faz GET /departments (cache TTL).
func (c *Client) Departamentos(ctx context.Context) ([]Referencia, error) {
	return cacheado(ctx, c, "/departments", func(ctx context.Context) ([]Referencia, error) {
		var out []Referencia
		err := c.do(ctx, http.MethodGet, "/departments", nil, nil, &out)
		return out, err
	})
}

// Atendentes faz GET /users (cache TTL). Email é PII.
func (c *Client) Atendentes(ctx context.Context) ([]Atendente, error) {
	return cacheado(ctx, c, "/users", func(ctx context.Context) ([]Atendente, error) {
		var out []Atendente
		err := c.do(ctx, http.MethodGet, "/users", nil, nil, &out)
		return out, err
	})
}

// InvalidarCache descarta mesas, departamentos e atendentes em cache; a
// próxima chamada volta à API.
func (c *Client) InvalidarCache() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache = map[string]entradaCache{}
}

// cacheado devolve o valor em cache se ainda válido (relógio injetado), senão
// busca e guarda. Devolve cópias: o chamador não altera o cache. Falha não é
// cacheada.
func cacheado[E any](ctx context.Context, c *Client, chave string, buscar func(context.Context) ([]E, error)) ([]E, error) {
	c.mu.Lock()
	if e, ok := c.cache[chave]; ok && c.now().Before(e.exp) {
		if v, ok := e.valor.([]E); ok {
			c.mu.Unlock()
			return slices.Clone(v), nil
		}
	}
	c.mu.Unlock()

	v, err := buscar(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.cache[chave] = entradaCache{valor: slices.Clone(v), exp: c.now().Add(c.cacheTTL)}
	c.mu.Unlock()
	return v, nil
}

// ---------------------------------------------------------------------------
// Tickets (03-tickets.md)
// ---------------------------------------------------------------------------

// CriarTicket faz POST /tickets UMA vez: a API não tem chave de idempotência,
// então nenhuma falha é repetida aqui. Erro de rede após o envio é ambíguo
// (o ticket pode existir): o chamador registra "pendente" (ADR 0005) e alerta.
// 2xx sem `number` reconhecível devolve o Ticket (Bruto preenchido) junto com
// ErrTicketSemNumero. Regra para o chamador: QUALQUER erro depois do envio
// (rede, 5xx, resposta ilegível, sem número) é "pendente" — Transitorio()
// serve para alertar, nunca para repetir este POST.
func (c *Client) CriarTicket(ctx context.Context, t NovoTicket) (Ticket, error) {
	if strings.TrimSpace(t.DeskID) == "" {
		return Ticket{}, fmt.Errorf("%w: deskId", ErrCampoObrigatorio)
	}
	if strings.TrimSpace(t.Title) == "" {
		return Ticket{}, fmt.Errorf("%w: title", ErrCampoObrigatorio)
	}
	var out Ticket
	if err := c.do(ctx, http.MethodPost, "/tickets", nil, t, &out); err != nil {
		return Ticket{}, err
	}
	if out.Number == "" {
		return out, ErrTicketSemNumero
	}
	return out, nil
}

// Ticket faz GET /tickets/:number. 404 = número desconhecido (definitivo).
func (c *Client) Ticket(ctx context.Context, number string) (Ticket, error) {
	if err := exigirID(number); err != nil {
		return Ticket{}, err
	}
	var out Ticket
	if err := c.do(ctx, http.MethodGet, rotaTicket(number, ""), nil, nil, &out); err != nil {
		return Ticket{}, err
	}
	return out, nil
}

// ResponderTicket faz POST /tickets/:number/reply com `{body}`: vai ao
// solicitante pelos canais do ticket, como resposta de atendente. Implementado,
// não usado na v1 (T7: risco de duplicar a conversa do WhatsApp).
func (c *Client) ResponderTicket(ctx context.Context, number, body string) error {
	if err := exigirID(number); err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return ErrCorpoVazio
	}
	return c.do(ctx, http.MethodPost, rotaTicket(number, "reply"), nil, map[string]string{"body": body}, nil)
}

// ---------------------------------------------------------------------------
// HTTP
// ---------------------------------------------------------------------------

// do executa uma requisição e decodifica a resposta 2xx em `saida` (nil =
// ignora o corpo). Nunca repete. Erros: *ErroRede (sem resposta), *HTTPError
// (>= 400, corpo higienizado), *ErroResposta (2xx ilegível).
func (c *Client) do(ctx context.Context, metodo, rota string, query url.Values, corpo, saida any) error {
	if c.baseURL == "" {
		return ErrSemBaseURL
	}
	var leitor io.Reader
	if corpo != nil {
		b, err := json.Marshal(corpo)
		if err != nil {
			return fmt.Errorf("infodesk %s: serializar corpo: %w", rota, err)
		}
		leitor = bytes.NewReader(b)
	}
	destino := c.baseURL + PrefixoRotas + rota
	if len(query) > 0 {
		destino += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, metodo, destino, leitor)
	if err != nil {
		return fmt.Errorf("infodesk %s: montar requisição: %w", rota, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "sofia-bot-infominas")
	if corpo != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return &ErroRede{Rota: rota, Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return c.erroHTTP(rota, resp)
	}
	if saida == nil {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, respostaMax))
	if err != nil {
		// O status 2xx já chegou: a operação pode ter acontecido (em
		// CriarTicket, o ticket pode existir). Definitivo, não transitório.
		return &ErroResposta{Rota: rota, Motivo: "corpo interrompido"}
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return &ErroResposta{Rota: rota, Motivo: "corpo vazio"}
	}
	if err := json.Unmarshal(b, saida); err != nil {
		return &ErroResposta{Rota: rota, Motivo: "JSON inválido"}
	}
	return nil
}

// erroHTTP monta o *HTTPError: lê no máximo corpoErroLeituraMax bytes,
// higieniza e captura Retry-After (sem dormir).
func (c *Client) erroHTTP(rota string, resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, corpoErroLeituraMax))
	e := &HTTPError{Status: resp.StatusCode, Rota: rota, Mensagem: higienizarCorpo(raw)}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		e.RetryAfter = analisarRetryAfter(ra, c.now())
	}
	return e
}

// listaDeConversas aceita `[...]` ou `{data|items|conversations|results: [...]}`.
func listaDeConversas(rota string, raw json.RawMessage) ([]map[string]any, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '[' {
		var lista []map[string]any
		if err := json.Unmarshal(raw, &lista); err != nil {
			return nil, &ErroResposta{Rota: rota, Motivo: "lista com item que não é objeto"}
		}
		return lista, nil
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, &ErroResposta{Rota: rota, Motivo: "nem lista nem objeto"}
	}
	for _, chave := range []string{"data", "items", "conversations", "results"} {
		v, ok := env[chave]
		if !ok {
			continue
		}
		var lista []map[string]any
		if err := json.Unmarshal(v, &lista); err == nil {
			return lista, nil
		}
	}
	return nil, &ErroResposta{Rota: rota, Motivo: "objeto sem lista reconhecível (ver docs/infodesk/07 C1)"}
}

func rotaConversa(id, acao string) string {
	r := "/conversations/" + url.PathEscape(id)
	if acao != "" {
		r += "/" + acao
	}
	return r
}

func rotaTicket(number, acao string) string {
	r := "/tickets/" + url.PathEscape(number)
	if acao != "" {
		r += "/" + acao
	}
	return r
}

func exigirID(id string) error {
	if strings.TrimSpace(id) == "" {
		return ErrIDVazio
	}
	return nil
}
