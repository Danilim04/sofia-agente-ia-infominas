// Package infodesk é o cliente Go da API de integração do Infodesk (Infominas
// Desk) e o ÚNICO pacote do gateway que fala HTTP com ela. Também traz os
// tipos do webhook/feed de eventos e a verificação da assinatura HMAC. Fonte:
// docs/infodesk/ (convertido do PDF oficial v1 de 2026-10-19); o que o PDF
// não responde está em docs/infodesk/07-pontos-ambiguos.md e aparece aqui
// como hipótese de trabalho marcada com o item (T10, C6, D3…).
//
// O que a Infominas NÃO documenta e como este pacote lida com isso:
//
//   - Base URL (A6): configurável, sem default; GET /me no boot valida.
//   - Listas de referência (D3, D5): aceitam array puro ou `{data,
//     nextCursor}`; Mesa traz as listas com tolerância e o objeto em Bruto.
//   - Resposta de POST /conversations/:id/ticket (T11): assume-se o objeto
//     `ticket`; um envelope `{ticket: {...}}` também é aceito.
//   - Corpo de POST /conversations/:id/finish (C5): nenhum documentado; envia
//     `{}`.
//   - `note` do handoff (08): obrigatoriedade e limite não ditos; vazio é
//     omitido.
//   - 5xx e Retry-After (A4): 429/5xx são transitórios; Retry-After é exposto
//     em HTTPError.RetryAfter — o cliente nunca dorme nem repete.
//
// Regras herdadas do plano: nenhum retry dentro do cliente (em especial em
// CriarTicket e CriarTicketDaConversa — ADR 0005); erro do Infodesk nunca
// chega cru ao cliente final nem ao LLM (HTTPError.Mensagem já é higienizada);
// a chave de API nunca aparece em erro ou log.
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
	"strconv"
	"strings"
	"sync"
	"time"
)

// PrefixoRotas é o prefixo de versão da API; Options.BaseURL é a raiz antes
// dele. Se BaseURL já vier com o prefixo (como em INFODESK_BASE_URL do plano),
// ele não é duplicado. A base real (a capa do PDF e o guia antigo divergem) é
// a copiada da tela Configurações › Integrações — ver docs/infodesk/07 item A6.
const PrefixoRotas = "/integration/v1"

const (
	timeoutPadrao  = 15 * time.Second
	cacheTTLPadrao = 10 * time.Minute
	respostaMax    = 8 << 20 // bytes lidos de uma resposta 2xx
	// maxPaginasReferencia limita quantas páginas uma lista de referência
	// percorre seguindo nextCursor (limit padrão 50 → 2500 itens): trava de
	// segurança contra cursor que nunca acaba.
	maxPaginasReferencia = 50
)

// Options configura o cliente. Zero values recebem defaults em New.
type Options struct {
	BaseURL  string           // raiz da API (sem /integration/v1 ou com — ambos aceitos)
	APIKey   string           // chave ik_live_… — vai em Authorization: Bearer
	HTTP     *http.Client     // opcional (default: timeout 15 s)
	Now      func() time.Time // opcional (default: time.Now); injetável para o cache e o Retry-After
	CacheTTL time.Duration    // validade do cache de mesas/catálogo/departamentos/atendentes (default 10 min)
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
// Chave e contatos (01-autenticacao.md, 04-conversas.md)
// ---------------------------------------------------------------------------

// Me faz GET /me: identidade da chave. No boot, o gateway exige
// HandlesConversations e os escopos mínimos, e guarda OrganizationID para
// conferir os webhooks (07 W5).
func (c *Client) Me(ctx context.Context) (Me, error) {
	var out Me
	if err := c.do(ctx, http.MethodGet, "/me", nil, nil, &out); err != nil {
		return Me{}, err
	}
	return out, nil
}

// Lookup faz GET /contacts/lookup?phone=: identifica o telefone (solicitantes,
// clientIds, chamados abertos, conversa ativa). Escopo contacts:read; 403 aqui
// é configuração, mas quem chama decide degradar para o fallback de CPF. O
// telefone é PII: vai só na query, nunca em erro ou log deste pacote.
func (c *Client) Lookup(ctx context.Context, phone string) (Lookup, error) {
	if strings.TrimSpace(phone) == "" {
		return Lookup{}, ErrIDVazio
	}
	var out Lookup
	if err := c.do(ctx, http.MethodGet, "/contacts/lookup", url.Values{"phone": {phone}}, nil, &out); err != nil {
		return Lookup{}, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Conversas (04-conversas.md, 08-bot-externo.md)
// ---------------------------------------------------------------------------

// EnviarMensagem faz POST /conversations/:id/messages com `{"text": …}`. Com
// interna=true acrescenta `"internal": true` e grava NOTA INTERNA (só a
// equipe vê, nada vai ao WhatsApp do cliente). Só texto: mídia e templates
// não são usados na v1. Quebra em bolhas, formato WhatsApp e dedup ficam no
// chamador (engine). 409 WHATSAPP_WINDOW_CLOSED é definitivo.
func (c *Client) EnviarMensagem(ctx context.Context, convID, texto string, interna bool) error {
	if err := exigirID(convID); err != nil {
		return err
	}
	if strings.TrimSpace(texto) == "" {
		return ErrCorpoVazio
	}
	corpo := corpoMensagem{Text: texto, Internal: interna}
	return c.do(ctx, http.MethodPost, rotaConversa(convID, "messages"), nil, corpo, nil)
}

type corpoMensagem struct {
	Text     string `json:"text"`
	Internal bool   `json:"internal,omitempty"`
}

// Handoff faz POST /conversations/:id/handoff: caminho ÚNICO de transferência
// para humano (ADR 0007). O bot sai, a conversa volta à fila e os atendentes
// são notificados. A resposta não é documentada e é ignorada.
func (c *Client) Handoff(ctx context.Context, convID string, h Handoff) error {
	if err := exigirID(convID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, rotaConversa(convID, "handoff"), nil, h, nil)
}

// TransferirDepartamento faz POST /conversations/:id/department com
// `{departmentId}`: muda de fila SEM soltar o bot. Não é transferência para
// humano (use Handoff). deptID vazio é ErrIDVazio — nunca vira `null` por
// acidente, porque `null` REMOVE a conversa de qualquer departamento (ver
// RemoverDepartamento).
func (c *Client) TransferirDepartamento(ctx context.Context, convID, deptID string) error {
	if err := exigirID(convID); err != nil {
		return err
	}
	if err := exigirID(deptID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, rotaConversa(convID, "department"), nil, map[string]string{"departmentId": deptID}, nil)
}

// RemoverDepartamento envia `{"departmentId": null}` ("null remove", 04).
// Implementado por completude; não usado na v1.
func (c *Client) RemoverDepartamento(ctx context.Context, convID string) error {
	if err := exigirID(convID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, rotaConversa(convID, "department"), nil, map[string]*string{"departmentId": nil}, nil)
}

// Atribuir faz POST /conversations/:id/assign com `{userId}`. Implementado,
// não usado na v1 (roteamento é por departamento; 07 C10).
func (c *Client) Atribuir(ctx context.Context, convID, userID string) error {
	if err := exigirID(convID); err != nil {
		return err
	}
	if err := exigirID(userID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, rotaConversa(convID, "assign"), nil, map[string]string{"userId": userID}, nil)
}

// Encerrar faz POST /conversations/:id/finish ("sem mensagem ao cliente").
// Corpo não documentado (C5): envia `{}`. Implementado, não chamado na v1
// (decisão de negócio — ver docs/infodesk/07 item C6).
func (c *Client) Encerrar(ctx context.Context, convID string) error {
	if err := exigirID(convID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, rotaConversa(convID, "finish"), nil, map[string]any{}, nil)
}

// VincularSolicitante faz POST /conversations/:id/requester com `{id}`: liga o
// solicitante à conversa (o cliente vem junto). Best-effort antes de abrir o
// ticket quando o Lookup identificou. requesterID vazio é ErrIDVazio — `null`
// (desvincular) não é oferecido.
func (c *Client) VincularSolicitante(ctx context.Context, convID, requesterID string) error {
	if err := exigirID(convID); err != nil {
		return err
	}
	if err := exigirID(requesterID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, rotaConversa(convID, "requester"), nil, map[string]string{"id": requesterID}, nil)
}

// Conversas faz GET /conversations com os filtros dados e devolve UMA página
// (`{data, nextCursor}`). O reconciliador usa Eventos, não esta listagem; ela
// serve ao vigia BOT_TIMEOUT_HANDOFF (07 N1) e a diagnóstico.
func (c *Client) Conversas(ctx context.Context, f FiltroConversas) (Pagina[Conversa], error) {
	q := url.Values{}
	setNaoVazio(q, "tab", f.Tab)
	setNaoVazio(q, "status", f.Status)
	setNaoVazio(q, "channel", f.Channel)
	setNaoVazio(q, "assignedUserId", f.AssignedUserID)
	setNaoVazio(q, "departmentId", f.DepartmentID)
	setNaoVazio(q, "phone", f.Phone)
	if f.BotActive != nil {
		q.Set("botActive", strconv.FormatBool(*f.BotActive))
	}
	if !f.UpdatedSince.IsZero() {
		q.Set("updatedSince", f.UpdatedSince.UTC().Format(time.RFC3339))
	}
	setLimiteECursor(q, f.Limit, f.Cursor)
	return paginaDe[Conversa](ctx, c, "/conversations", q)
}

// Conversa faz GET /conversations/:id. handledBy/botActive vêm tipados; o
// resto (whatsappWindow, recentMessages…) fica em Bruto. É a checagem "o bot
// ainda conduz?" quando um evento reentregue é mais velho que o estado local
// (07 W9).
func (c *Client) Conversa(ctx context.Context, id string) (Conversa, error) {
	if err := exigirID(id); err != nil {
		return Conversa{}, err
	}
	var out Conversa
	if err := c.do(ctx, http.MethodGet, rotaConversa(id, ""), nil, nil, &out); err != nil {
		return Conversa{}, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Eventos (05-webhooks.md, seção 5.5)
// ---------------------------------------------------------------------------

// Eventos faz GET /events?after=<seq>&limit=: o feed retroativo, no MESMO
// envelope do webhook. Escopo events:read (403 = chave sem o escopo). limit <=
// 0 usa o padrão da API. O cursor só deve avançar depois que a página foi
// processada: guarde Data[i].Seq / NextAfter no chamador.
func (c *Client) Eventos(ctx context.Context, after int64, limit int) (FeedEventos, error) {
	q := url.Values{"after": {strconv.FormatInt(after, 10)}}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var raw struct {
		Data      []Evento `json:"data"`
		NextAfter int64    `json:"nextAfter"`
		HasMore   bool     `json:"hasMore"`
	}
	if err := c.do(ctx, http.MethodGet, "/events", q, nil, &raw); err != nil {
		return FeedEventos{}, err
	}
	return FeedEventos{Data: raw.Data, NextAfter: raw.NextAfter, HasMore: raw.HasMore}, nil
}

// ---------------------------------------------------------------------------
// Dados de referência (02-dados-referencia.md) — cache em memória com TTL
// ---------------------------------------------------------------------------

// Mesas faz GET /desks (mesas ativas; cache TTL). Aceita array puro ou
// `{data, nextCursor}` — ver docs/infodesk/07 item D3.
func (c *Client) Mesas(ctx context.Context) ([]Referencia, error) {
	return cacheado(ctx, c, "/desks", cloneSlice[Referencia], func(ctx context.Context) ([]Referencia, error) {
		return listaCompleta[Referencia](ctx, c, "/desks", nil)
	})
}

// Mesa faz GET /desks/:id (cache TTL): statuses, etapas, prioridades e itens
// de catálogo da mesa. É de onde a taxonomia resolve `priorityId` e
// `catalogItemId` por nome.
func (c *Client) Mesa(ctx context.Context, id string) (Mesa, error) {
	if err := exigirID(id); err != nil {
		return Mesa{}, err
	}
	return cacheado(ctx, c, "/desks/"+id, Mesa.clonar, func(ctx context.Context) (Mesa, error) {
		var out Mesa
		err := c.do(ctx, http.MethodGet, "/desks/"+url.PathEscape(id), nil, nil, &out)
		return out, err
	})
}

// Catalogo faz GET /catalog?deskId= (serviços da mesa → `catalogItemId` do
// ticket; cache TTL). Só id e name são garantidos — ver docs/infodesk/07 item
// D5. Aceita array puro ou `{data, nextCursor}` (D3).
func (c *Client) Catalogo(ctx context.Context, deskID string) ([]Referencia, error) {
	if err := exigirID(deskID); err != nil {
		return nil, err
	}
	return cacheado(ctx, c, "/catalog?deskId="+deskID, cloneSlice[Referencia], func(ctx context.Context) ([]Referencia, error) {
		return listaCompleta[Referencia](ctx, c, "/catalog", url.Values{"deskId": {deskID}})
	})
}

// Departamentos faz GET /departments (departamentos de CHAT; cache TTL).
// Aceita array puro ou `{data, nextCursor}` — ver docs/infodesk/07 item D3.
func (c *Client) Departamentos(ctx context.Context) ([]Referencia, error) {
	return cacheado(ctx, c, "/departments", cloneSlice[Referencia], func(ctx context.Context) ([]Referencia, error) {
		return listaCompleta[Referencia](ctx, c, "/departments", nil)
	})
}

// Atendentes faz GET /users (cache TTL). Email é PII. Aceita array puro ou
// `{data, nextCursor}` — ver docs/infodesk/07 item D3.
func (c *Client) Atendentes(ctx context.Context) ([]Atendente, error) {
	return cacheado(ctx, c, "/users", cloneSlice[Atendente], func(ctx context.Context) ([]Atendente, error) {
		return listaCompleta[Atendente](ctx, c, "/users", nil)
	})
}

// InvalidarCache descarta mesas, catálogos, departamentos e atendentes em
// cache; a próxima chamada volta à API. Chamar ao receber INVALID_REFERENCE.
func (c *Client) InvalidarCache() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache = map[string]entradaCache{}
}

// cacheado devolve o valor em cache se ainda válido (relógio injetado), senão
// busca e guarda. Devolve cópias (clonar): o chamador não altera o cache.
// Falha não é cacheada.
func cacheado[T any](ctx context.Context, c *Client, chave string, clonar func(T) T, buscar func(context.Context) (T, error)) (T, error) {
	c.mu.Lock()
	if e, ok := c.cache[chave]; ok && c.now().Before(e.exp) {
		if v, ok := e.valor.(T); ok {
			c.mu.Unlock()
			return clonar(v), nil
		}
	}
	c.mu.Unlock()

	v, err := buscar(ctx)
	if err != nil {
		var zero T
		return zero, err
	}
	c.mu.Lock()
	c.cache[chave] = entradaCache{valor: clonar(v), exp: c.now().Add(c.cacheTTL)}
	c.mu.Unlock()
	return v, nil
}

// ---------------------------------------------------------------------------
// Tickets (03-tickets.md)
// ---------------------------------------------------------------------------

// CriarTicket faz POST /tickets UMA vez: a API não tem chave de idempotência,
// então nenhuma falha é repetida aqui. Erro de rede após o envio é ambíguo
// (o ticket pode existir): o chamador registra "pendente" (ADR 0005) e
// reconcilia por ListarTickets. 2xx sem `number` reconhecível devolve o Ticket
// (Bruto preenchido) junto com ErrTicketSemNumero. Regra para o chamador:
// QUALQUER erro depois do envio (rede, 5xx, resposta ilegível, sem número) é
// "pendente" — Transitorio() serve para alertar, nunca para repetir este POST.
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
	if out.Number == 0 {
		return out, ErrTicketSemNumero
	}
	return out, nil
}

// CriarTicketDaConversa faz POST /conversations/:id/ticket UMA vez (mesma
// regra de CriarTicket: sem idempotência, nenhum retry, qualquer erro depois
// do envio é "pendente"). A transcrição da conversa entra na descrição e o
// chamado sai vinculado à conversa. Assume-se que a resposta é o objeto
// `ticket` e que herda cliente/solicitante da conversa — ver docs/infodesk/07
// item T11; o que acontece se a conversa já tem `ticketNumber` é ABERTO (item
// T12): quem chama confere `ticketNumber` local antes.
func (c *Client) CriarTicketDaConversa(ctx context.Context, convID string, t NovoTicketDaConversa) (Ticket, error) {
	if err := exigirID(convID); err != nil {
		return Ticket{}, err
	}
	if strings.TrimSpace(t.DeskID) == "" {
		return Ticket{}, fmt.Errorf("%w: deskId", ErrCampoObrigatorio)
	}
	if strings.TrimSpace(t.Title) == "" {
		return Ticket{}, fmt.Errorf("%w: title", ErrCampoObrigatorio)
	}
	var out Ticket
	if err := c.do(ctx, http.MethodPost, rotaConversa(convID, "ticket"), nil, t, &out); err != nil {
		return Ticket{}, err
	}
	if out.Number == 0 {
		// ver docs/infodesk/07 item T11: tolera o objeto embrulhado em `ticket`.
		if inner, ok := out.Bruto["ticket"].(map[string]any); ok {
			if b, err := json.Marshal(inner); err == nil {
				var t2 Ticket
				if json.Unmarshal(b, &t2) == nil && t2.Number != 0 {
					return t2, nil
				}
			}
		}
		return out, ErrTicketSemNumero
	}
	return out, nil
}

// Ticket faz GET /tickets/:number. 404 NOT_FOUND = número desconhecido
// (definitivo).
func (c *Client) Ticket(ctx context.Context, number int) (Ticket, error) {
	if err := exigirNumero(number); err != nil {
		return Ticket{}, err
	}
	var out Ticket
	if err := c.do(ctx, http.MethodGet, rotaTicket(number, ""), nil, nil, &out); err != nil {
		return Ticket{}, err
	}
	return out, nil
}

// ListarTickets faz GET /tickets com os filtros dados e devolve UMA página
// (`{data, nextCursor}`; sem Status a API lista só os abertos). Serve ao
// "já tenho chamado?" e à reconciliação de um POST sem resposta (ADR 0005).
func (c *Client) ListarTickets(ctx context.Context, f FiltroTickets) (Pagina[Ticket], error) {
	q := url.Values{}
	setNaoVazio(q, "phone", f.Phone)
	setNaoVazio(q, "status", f.Status)
	setNaoVazio(q, "deskId", f.DeskID)
	setLimiteECursor(q, f.Limit, f.Cursor)
	return paginaDe[Ticket](ctx, c, "/tickets", q)
}

// ResponderTicket faz POST /tickets/:number/reply com `{body}`: vai ao
// solicitante pelos canais do ticket, como resposta de atendente. Implementado,
// não usado na v1 (T7: risco de duplicar a conversa do WhatsApp).
func (c *Client) ResponderTicket(ctx context.Context, number int, body string) error {
	if err := exigirNumero(number); err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return ErrCorpoVazio
	}
	return c.do(ctx, http.MethodPost, rotaTicket(number, "reply"), nil, corpoTicket{Body: body}, nil)
}

// NotaInterna faz POST /tickets/:number/notes com `{body}`: nota interna do
// chamado, visível só para a equipe (nada vai ao cliente).
func (c *Client) NotaInterna(ctx context.Context, number int, body string) error {
	if err := exigirNumero(number); err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return ErrCorpoVazio
	}
	return c.do(ctx, http.MethodPost, rotaTicket(number, "notes"), nil, corpoTicket{Body: body}, nil)
}

// corpoTicket é o corpo de /tickets/:number/reply e /notes — nestas rotas o
// campo é `body` (03-tickets.md); só as mensagens de conversa usam `text`.
type corpoTicket struct {
	Body string `json:"body"`
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
// extrai o `code`, higieniza a mensagem e captura Retry-After (sem dormir).
func (c *Client) erroHTTP(rota string, resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, corpoErroLeituraMax))
	e := &HTTPError{Status: resp.StatusCode, Code: extrairCode(raw), Rota: rota, Mensagem: higienizarCorpo(raw)}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		e.RetryAfter = analisarRetryAfter(ra, c.now())
	}
	return e
}

// ---------------------------------------------------------------------------
// Listas paginadas
// ---------------------------------------------------------------------------

// paginaDe faz um GET e devolve UMA página de lista.
func paginaDe[T any](ctx context.Context, c *Client, rota string, q url.Values) (Pagina[T], error) {
	var raw json.RawMessage
	if err := c.do(ctx, http.MethodGet, rota, q, nil, &raw); err != nil {
		return Pagina[T]{}, err
	}
	itens, prox, err := decodificarLista[T](rota, raw)
	if err != nil {
		return Pagina[T]{}, err
	}
	return Pagina[T]{Data: itens, NextCursor: prox}, nil
}

// listaCompleta percorre TODAS as páginas de uma lista de referência
// (idempotente, GET), seguindo nextCursor até acabar ou até
// maxPaginasReferencia. Array puro conta como uma página só.
func listaCompleta[T any](ctx context.Context, c *Client, rota string, base url.Values) ([]T, error) {
	var todos []T
	cursor := ""
	for range maxPaginasReferencia {
		q := url.Values{}
		for k, v := range base {
			q[k] = slices.Clone(v)
		}
		setNaoVazio(q, "cursor", cursor)
		p, err := paginaDe[T](ctx, c, rota, q)
		if err != nil {
			return nil, err
		}
		todos = append(todos, p.Data...)
		if p.NextCursor == "" || p.NextCursor == cursor {
			return todos, nil
		}
		cursor = p.NextCursor
	}
	return nil, &ErroResposta{Rota: rota, Motivo: "paginação sem fim"}
}

// decodificarLista aceita `[...]` ou `{data: [...], nextCursor: "…"|null}`.
func decodificarLista[T any](rota string, raw json.RawMessage) ([]T, string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '[' {
		var itens []T
		if err := json.Unmarshal(raw, &itens); err != nil {
			return nil, "", &ErroResposta{Rota: rota, Motivo: "lista com item inválido"}
		}
		return itens, "", nil
	}
	var env struct {
		Data       json.RawMessage `json:"data"`
		NextCursor json.RawMessage `json:"nextCursor"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, "", &ErroResposta{Rota: rota, Motivo: "nem lista nem objeto"}
	}
	if d := bytes.TrimSpace(env.Data); len(d) == 0 || d[0] != '[' {
		return nil, "", &ErroResposta{Rota: rota, Motivo: "objeto sem `data` em lista"}
	}
	var itens []T
	if err := json.Unmarshal(env.Data, &itens); err != nil {
		return nil, "", &ErroResposta{Rota: rota, Motivo: "lista com item inválido"}
	}
	var prox string
	if len(env.NextCursor) > 0 {
		_ = json.Unmarshal(env.NextCursor, &prox) // null ou tipo estranho = fim
	}
	return itens, prox, nil
}

func setNaoVazio(q url.Values, chave, valor string) {
	if strings.TrimSpace(valor) != "" {
		q.Set(chave, valor)
	}
}

func setLimiteECursor(q url.Values, limite int, cursor string) {
	if limite > 0 {
		q.Set("limit", strconv.Itoa(limite))
	}
	setNaoVazio(q, "cursor", cursor)
}

func rotaConversa(id, acao string) string {
	r := "/conversations/" + url.PathEscape(id)
	if acao != "" {
		r += "/" + acao
	}
	return r
}

func rotaTicket(number int, acao string) string {
	r := "/tickets/" + strconv.Itoa(number)
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

func exigirNumero(n int) error {
	if n <= 0 {
		return ErrNumeroInvalido
	}
	return nil
}
