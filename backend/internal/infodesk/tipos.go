package infodesk

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Referência (02-dados-referencia.md)
// ---------------------------------------------------------------------------

// Referencia é um item de GET /desks ou GET /departments, e também o
// `desk`/`priority`/`stage` embutidos no chamado: `{id, name}`. IDs são
// opacos (UUID nos exemplos).
type Referencia struct {
	ID   string `json:"id"`
	Nome string `json:"name"`
}

// Atendente é um item de GET /users: `{id, username, email}`.
// Email é PII: mascarar antes de logar.
type Atendente struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
}

// Status é o `status` do chamado: definido POR MESA (GET /desks/:id.statuses).
// O estado do chamado se lê dos três booleanos, nunca do nome.
type Status struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	IsOpen     bool   `json:"isOpen"`
	IsClose    bool   `json:"isClose"`
	IsCanceled bool   `json:"isCanceled"`
}

// Mesa é GET /desks/:id: a mesa com seus statuses, etapas, prioridades e
// itens de catálogo (prioridades são POR MESA — por isso a taxonomia mapeia
// nome → UUID resolvido aqui, docs/infodesk/02). O JSON literal desta rota não
// está no PDF: as listas são lidas com tolerância (formato inesperado vira
// lista vazia, e a taxonomia falha alto ao não achar o nome) e o objeto
// completo fica em Bruto. CatalogItems só garante id e name — ver
// docs/infodesk/07 item D5.
type Mesa struct {
	ID           string
	Nome         string
	Statuses     []Status
	Stages       []Referencia
	Priorities   []Referencia
	CatalogItems []Referencia
	Bruto        map[string]any
}

// UnmarshalJSON é tolerante por campo; só um corpo que não é objeto é erro.
func (m *Mesa) UnmarshalJSON(b []byte) error {
	bruto, campos, err := decodificarObjeto(b)
	if err != nil {
		return err
	}
	*m = Mesa{Bruto: bruto}
	lerCampo(campos, "id", &m.ID)
	lerCampo(campos, "name", &m.Nome)
	lerCampo(campos, "statuses", &m.Statuses)
	lerCampo(campos, "stages", &m.Stages)
	lerCampo(campos, "priorities", &m.Priorities)
	lerCampo(campos, "catalogItems", &m.CatalogItems)
	return nil
}

func (m Mesa) clonar() Mesa {
	m.Statuses = cloneSlice(m.Statuses)
	m.Stages = cloneSlice(m.Stages)
	m.Priorities = cloneSlice(m.Priorities)
	m.CatalogItems = cloneSlice(m.CatalogItems)
	m.Bruto = cloneMapa(m.Bruto)
	return m
}

// ---------------------------------------------------------------------------
// Chave (01-autenticacao.md)
// ---------------------------------------------------------------------------

// Me é a resposta de GET /me: a identidade da chave. OrganizationID é o valor
// a conferir contra o `organizationId` dos webhooks; HandlesConversations diz
// se a chave é o bot externo ("Conduz o atendimento").
type Me struct {
	OrganizationID       string   `json:"organizationId"`
	KeyID                string   `json:"keyId"`
	Name                 string   `json:"name"` // nome da chave = senderName das nossas mensagens
	Scopes               []string `json:"scopes"`
	HandlesConversations bool     `json:"handlesConversations"`
}

// ---------------------------------------------------------------------------
// Tickets (03-tickets.md)
// ---------------------------------------------------------------------------

// NovoTicket é o corpo de POST /tickets (03-tickets.md). Só DeskID e Title
// são obrigatórios; os ponteiros ficam fora do JSON quando nil.
//
// Hipóteses de trabalho (docs/infodesk/07): Channel "WHATSAPP" para tickets
// vindos da conversa; quais campos a organização exige é ABERTO — ver
// docs/infodesk/07 item T10. Quem preenche é o código a partir da
// classificação e da tabela de roteamento, nunca o LLM.
type NovoTicket struct {
	DeskID        string  `json:"deskId"`
	Title         string  `json:"title"`
	Description   string  `json:"description,omitempty"`
	Channel       string  `json:"channel,omitempty"`
	ContactPhone  string  `json:"contactPhone,omitempty"`
	PriorityID    *string `json:"priorityId,omitempty"`
	CatalogItemID *string `json:"catalogItemId,omitempty"`
	ClientID      *string `json:"clientId,omitempty"`
	RequesterID   *string `json:"requesterId,omitempty"`
}

// NovoTicketDaConversa é o corpo de POST /conversations/:id/ticket
// (03-tickets.md): abre o chamado a partir da conversa, com a transcrição na
// descrição. Só DeskID e Title são obrigatórios aqui também.
type NovoTicketDaConversa struct {
	DeskID        string  `json:"deskId"`
	Title         string  `json:"title"`
	Description   string  `json:"description,omitempty"`
	PriorityID    *string `json:"priorityId,omitempty"`
	CatalogItemID *string `json:"catalogItemId,omitempty"`
	// Period é a janela da transcrição: "session" (padrão do Desk quando
	// omitido), "today", "24h" ou "all".
	Period string `json:"period,omitempty"`
	// Finish encerra a conversa e manda ao cliente a mensagem configurada no
	// Desk. Sempre vai explícito no corpo; a decisão de negócio é false na v1
	// — ver docs/infodesk/07 item C6.
	Finish bool `json:"finish"`
}

// Solicitante é a pessoa do cliente (`requester`): item de
// GET /contacts/lookup e `ticket.requester`. Name, Email e Phone são PII:
// mascarar antes de logar.
type Solicitante struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Email    string      `json:"email"`
	Phone    string      `json:"phone"`
	ClientID string      `json:"clientId"`
	Client   *Referencia `json:"client"`
}

// Ticket é o objeto `ticket` (03-tickets.md, seção 9.1) devolvido por
// POST /tickets, POST /conversations/:id/ticket, GET /tickets/:number e pela
// listagem. Number é INTEIRO (docs/infodesk/07 item T1, respondido). O estado
// vem de Status (por mesa) e IsClosed. O resto do objeto (title, description,
// channel, stage, client, responsible, prazos de SLA e, em GET /tickets/:number,
// `messages` — formato não documentado, 07 item T13) fica em Bruto, com
// números como json.Number.
type Ticket struct {
	Number         int
	Status         Status
	IsClosed       bool
	ConversationID string
	ContactPhone   string // PII
	Requester      *Solicitante
	Desk           Referencia
	Priority       Referencia
	Bruto          map[string]any
}

// UnmarshalJSON é tolerante: `number` ausente ou irreconhecível fica 0 (o
// cliente trata como ErrTicketSemNumero, nunca como sucesso), e um campo com
// tipo inesperado fica no zero value em vez de derrubar a resposta de um
// POST que já teve efeito. Só um corpo que não é objeto JSON é erro.
func (t *Ticket) UnmarshalJSON(b []byte) error {
	bruto, campos, err := decodificarObjeto(b)
	if err != nil {
		return err
	}
	*t = Ticket{Bruto: bruto, Number: numeroInteiro(bruto["number"])}
	lerCampo(campos, "status", &t.Status)
	lerCampo(campos, "isClosed", &t.IsClosed)
	lerCampo(campos, "conversationId", &t.ConversationID)
	lerCampo(campos, "contactPhone", &t.ContactPhone)
	lerCampo(campos, "requester", &t.Requester)
	lerCampo(campos, "desk", &t.Desk)
	lerCampo(campos, "priority", &t.Priority)
	return nil
}

// ---------------------------------------------------------------------------
// Contatos (04-conversas.md)
// ---------------------------------------------------------------------------

// Lookup é a resposta de GET /contacts/lookup?phone=: quem é o telefone.
// `clientContacts` é ignorado (fica em Bruto) — docs/infodesk/07 item N2.
// OpenTickets usa o mesmo tipo Ticket (tolerante; `number` inteiro).
type Lookup struct {
	Phone        string
	Requesters   []Solicitante
	ClientIDs    []string
	OpenTickets  []Ticket
	Conversation *Conversa // conversa ativa do número; nil = nenhuma
	Bruto        map[string]any
}

// UnmarshalJSON é tolerante por campo.
func (l *Lookup) UnmarshalJSON(b []byte) error {
	bruto, campos, err := decodificarObjeto(b)
	if err != nil {
		return err
	}
	*l = Lookup{Bruto: bruto}
	lerCampo(campos, "phone", &l.Phone)
	lerCampo(campos, "requesters", &l.Requesters)
	lerCampo(campos, "clientIds", &l.ClientIDs)
	lerCampo(campos, "openTickets", &l.OpenTickets)
	lerCampo(campos, "conversation", &l.Conversation)
	return nil
}

// ---------------------------------------------------------------------------
// Conversas e mensagens (04-conversas.md)
// ---------------------------------------------------------------------------

// Valores documentados (04 e 05).
const (
	CanalWhatsApp = "WHATSAPP"

	// handledBy: quem conduz a conversa.
	ConduzidaPorBotExterno = "EXTERNAL_BOT" // nós
	ConduzidaPorBotInterno = "INTERNAL_BOT" // chatbot do Desk
	ConduzidaPorAtendente  = "AGENT"
	ConduzidaPorFila       = "QUEUE"

	// message.sender
	RemetenteCliente = "CLIENT"
	RemetenteAgente  = "AGENT" // atendente, bot ou integração
	RemetenteSistema = "SYSTEM"

	// message.type
	TipoTexto   = "TEXT"
	TipoImagem  = "IMAGE"
	TipoArquivo = "FILE"
	TipoAudio   = "AUDIO"
	TipoContato = "CONTACT"
)

// Contato é `contact` da conversa e do envelope do webhook. Name pode ser
// apelido do perfil; Phone vem em E.164. Todos PII: mascarar em log.
type Contato struct {
	Name  string `json:"name"`
	Phone string `json:"phone"`
	Email string `json:"email"`
	// GroupJid é não nulo em grupo de WhatsApp (grupos NÃO passam pelo bot);
	// "" = null = contato individual.
	GroupJid string `json:"groupJid"`
}

// Conversa é o objeto `conversation` (04): o de GET /conversations, o de
// GET /conversations/:id e o `data.conversation` de todo evento de mensagem e
// de conversa. Campos nulos viram "" (strings) ou 0 (TicketNumber). O resto
// (clientId, requesterId, lastMessage*, unreadCount, provider…) fica em
// Bruto — em GET /conversations/:id também `whatsappWindow` e
// `recentMessages`.
type Conversa struct {
	ID               string
	Channel          string
	Status           string // PENDING | IN_PROGRESS | FINISHED
	HandledBy        string // ConduzidaPor…
	BotActive        bool
	AssignedUserID   string
	DepartmentID     string
	TicketNumber     int // 0 = sem chamado vinculado
	SessionStartedAt time.Time
	Contact          Contato
	Bruto            map[string]any
}

type conversaJSON struct {
	ID               string  `json:"id"`
	Channel          string  `json:"channel"`
	Status           string  `json:"status"`
	HandledBy        string  `json:"handledBy"`
	BotActive        bool    `json:"botActive"`
	AssignedUserID   string  `json:"assignedUserId"`
	DepartmentID     string  `json:"departmentId"`
	TicketNumber     any     `json:"ticketNumber"`
	SessionStartedAt string  `json:"sessionStartedAt"`
	Contact          Contato `json:"contact"`
}

// UnmarshalJSON tolera sessionStartedAt ausente/ilegível (zero) e
// ticketNumber nulo. Um campo com tipo trocado (ex.: botActive texto) é erro:
// melhor falhar do que decidir "quem conduz" com dado errado.
func (c *Conversa) UnmarshalJSON(b []byte) error {
	bruto, _, err := decodificarObjeto(b)
	if err != nil {
		return err
	}
	var aux conversaJSON
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	*c = Conversa{
		ID:               aux.ID,
		Channel:          aux.Channel,
		Status:           aux.Status,
		HandledBy:        aux.HandledBy,
		BotActive:        aux.BotActive,
		AssignedUserID:   aux.AssignedUserID,
		DepartmentID:     aux.DepartmentID,
		TicketNumber:     numeroInteiro(aux.TicketNumber),
		SessionStartedAt: analisarInstante(aux.SessionStartedAt),
		Contact:          aux.Contact,
		Bruto:            bruto,
	}
	return nil
}

// Anexo é `message.attachment`. Só `url` é documentado (baixar com a chave via
// GET /attachments/:id); o resto — mimeType, filename, size — é NÃO
// DOCUMENTADO e fica em Bruto (docs/infodesk/07 item C9). Não baixar na v1.
type Anexo struct {
	URL   string
	Bruto map[string]any
}

// UnmarshalJSON é tolerante: o que não for objeto vira um Anexo vazio — o que
// importa para o gateway é a PRESENÇA do anexo (mídia → transfere para humano).
func (a *Anexo) UnmarshalJSON(b []byte) error {
	*a = Anexo{}
	bruto, campos, err := decodificarObjeto(b)
	if err != nil {
		return nil
	}
	a.Bruto = bruto
	lerCampo(campos, "url", &a.URL)
	return nil
}

// Mensagem é o objeto `message` (04) e o `data.message` dos eventos de
// mensagem. Body é conteúdo do cliente: DADO, nunca instrução, e PII em log.
type Mensagem struct {
	ID             string
	ConversationID string
	Sender         string // RemetenteCliente | RemetenteAgente | RemetenteSistema
	SenderName     string // autor; a nossa chave aparece aqui (eco → ignorar, 07 W8)
	Type           string // TipoTexto…
	Body           string
	Internal       bool // true = nota interna (só a equipe vê)
	Attachment     *Anexo
	CreatedAt      time.Time
}

type mensagemJSON struct {
	ID             string          `json:"id"`
	ConversationID string          `json:"conversationId"`
	Sender         string          `json:"sender"`
	SenderName     string          `json:"senderName"`
	Type           string          `json:"type"`
	Body           string          `json:"body"`
	Internal       bool            `json:"internal"`
	Attachment     json.RawMessage `json:"attachment"`
	CreatedAt      string          `json:"createdAt"`
}

// UnmarshalJSON tolera createdAt ausente/ilegível (zero).
func (m *Mensagem) UnmarshalJSON(b []byte) error {
	var aux mensagemJSON
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	*m = Mensagem{
		ID:             aux.ID,
		ConversationID: aux.ConversationID,
		Sender:         aux.Sender,
		SenderName:     aux.SenderName,
		Type:           aux.Type,
		Body:           aux.Body,
		Internal:       aux.Internal,
		CreatedAt:      analisarInstante(aux.CreatedAt),
	}
	if raw := bytes.TrimSpace(aux.Attachment); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		var a Anexo
		_ = a.UnmarshalJSON(raw)
		m.Attachment = &a
	}
	return nil
}

// DoCliente informa se a mensagem veio do contato: sender exatamente
// "CLIENT". Qualquer outro valor (AGENT, SYSTEM, variação) não é turno do
// cliente. Mensagens do atendente ou da API chegam em message.sent, não em
// message.received (07 W2, respondido).
func (m Mensagem) DoCliente() bool { return m.Sender == RemetenteCliente }

// TemMidia: `type` diferente de TEXT ou `attachment` presente (07 W3,
// respondido). A legenda/rótulo "[imagem]" que o WhatsApp manda como OUTRA
// mensagem é texto e não dispara isto sozinha.
func (m Mensagem) TemMidia() bool {
	return m.Attachment != nil || (m.Type != "" && m.Type != TipoTexto)
}

// ---------------------------------------------------------------------------
// Webhook e feed de eventos (05-webhooks.md)
// ---------------------------------------------------------------------------

// Nomes dos eventos (05, seção 5.2).
const (
	EventoMensagemRecebida     = "message.received"
	EventoMensagemEnviada      = "message.sent"
	EventoMensagemStatus       = "message.status"
	EventoConversaIniciada     = "conversation.started"
	EventoConversaAtribuida    = "conversation.assigned"
	EventoConversaDepartamento = "conversation.department_changed"
	EventoConversaHandoff      = "conversation.handoff"
	EventoConversaEncerrada    = "conversation.finished"
	EventoTicketCriado         = "ticket.created"
	EventoTicketAtualizado     = "ticket.updated"
	EventoTicketRespondido     = "ticket.replied"
	EventoTicketNotaAdicionada = "ticket.note_added"
	EventoTicketFechado        = "ticket.closed"
	EventoTicketReaberto       = "ticket.reopened"
	EventoTicketCancelado      = "ticket.canceled"
	EventoPing                 = "ping"
)

// DadosEvento é o `data` do envelope. Conversation, Contact e Message vêm dos
// eventos de mensagem e de conversa; nil quando ausentes ou ilegíveis (o
// receptor trata "evento de mensagem sem conversation" como não-turno). Os
// extras de cada evento — `note` (handoff), `reason` (finished),
// `previousDepartmentId`, `ticket`, `changes`, e o `data` do `ping` — NÃO
// estão tipados (docs/infodesk/07 item W7): decodifique de Bruto. Bruto é o
// `data` inteiro, como veio.
type DadosEvento struct {
	Conversation *Conversa
	Contact      *Contato
	Message      *Mensagem
	Bruto        json.RawMessage
}

// UnmarshalJSON nunca falha: um `data` que não é objeto (ex.: `ping`) fica só
// com o Bruto, e sub-objetos ilegíveis ficam nil. Quem decide o que fazer com
// um evento sem os dados esperados é o receptor.
func (d *DadosEvento) UnmarshalJSON(b []byte) error {
	*d = DadosEvento{Bruto: bytes.Clone(b)}
	var aux struct {
		Conversation json.RawMessage `json:"conversation"`
		Contact      json.RawMessage `json:"contact"`
		Message      json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(b, &aux); err != nil {
		return nil
	}
	d.Conversation = opcional[Conversa](aux.Conversation)
	d.Contact = opcional[Contato](aux.Contact)
	d.Message = opcional[Mensagem](aux.Message)
	return nil
}

// ConversaID devolve o id da conversa do evento (conversation.id ou, na
// falta, message.conversationId); "" quando o evento não é de conversa.
func (d DadosEvento) ConversaID() string {
	if d.Conversation != nil && d.Conversation.ID != "" {
		return d.Conversation.ID
	}
	if d.Message != nil {
		return d.Message.ConversationID
	}
	return ""
}

// Evento é o envelope de toda entrega de webhook e de cada item do feed
// GET /events — o MESMO formato nos dois (05, seção 5.3).
type Evento struct {
	ID             string // id do EVENTO (`evt_…`), estável entre reentregas — chave de dedup
	Seq            int64  // sequencial crescente: ordenação e cursor do feed
	Event          string // também vem no header X-Infominas-Event
	CreatedAt      time.Time
	OrganizationID string // conferir contra Me.OrganizationID (07 W5)
	Data           DadosEvento
}

type eventoJSON struct {
	ID             string          `json:"id"`
	Seq            int64           `json:"seq"`
	Event          string          `json:"event"`
	CreatedAt      string          `json:"createdAt"`
	OrganizationID string          `json:"organizationId"`
	Data           json.RawMessage `json:"data"`
}

// UnmarshalJSON tolera createdAt ausente ou fora do ISO-8601 (fica zero; o
// receptor decide se descarta) e qualquer formato de `data`.
func (e *Evento) UnmarshalJSON(b []byte) error {
	var aux eventoJSON
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	*e = Evento{
		ID:             aux.ID,
		Seq:            aux.Seq,
		Event:          aux.Event,
		CreatedAt:      analisarInstante(aux.CreatedAt),
		OrganizationID: aux.OrganizationID,
	}
	if raw := bytes.TrimSpace(aux.Data); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		_ = e.Data.UnmarshalJSON(raw)
	}
	return nil
}

// ErrEventoInvalido: entrega sem `id` ou sem `event`.
var ErrEventoInvalido = errors.New("infodesk: evento sem id ou sem event")

// DecodificarEvento lê o envelope de uma entrega. Exige `id` e `event`.
func DecodificarEvento(raw []byte) (Evento, error) {
	var e Evento
	if err := json.Unmarshal(raw, &e); err != nil {
		return Evento{}, err
	}
	if e.ID == "" || e.Event == "" {
		return Evento{}, ErrEventoInvalido
	}
	return e, nil
}

// FeedEventos é a resposta de GET /events?after=<seq>: `{data, nextAfter,
// hasMore}` — formato próprio, não o `{data, nextCursor}` geral (05, 5.5).
type FeedEventos struct {
	Data      []Evento
	NextAfter int64
	HasMore   bool
}

// ---------------------------------------------------------------------------
// Paginação e filtros
// ---------------------------------------------------------------------------

// Pagina é uma página de lista paginada `{data, nextCursor}` (06-erros.md,
// "Paginação"). NextCursor "" = última página; a próxima vem repetindo a
// chamada com Cursor = NextCursor.
type Pagina[T any] struct {
	Data       []T
	NextCursor string
}

// Handoff é o corpo de POST /conversations/:id/handoff (08-bot-externo.md): o
// bot sai, a conversa volta para a fila do departamento (ou do UserID, ou da
// distribuição automática), Note vira nota interna e os atendentes são
// notificados. Note é sempre escrita por código, sem PII em claro.
type Handoff struct {
	DepartmentID *string `json:"departmentId,omitempty"`
	UserID       *string `json:"userId,omitempty"`
	// Note vazia é omitida. Se ela é obrigatória e qual o limite de tamanho:
	// NÃO DITO — ver docs/infodesk/08 (seção Handoff).
	Note string `json:"note,omitempty"`
}

// FiltroTickets são os filtros de GET /tickets (03). Campos vazios são
// omitidos. Sem Status, a API lista só os ABERTOS.
type FiltroTickets struct {
	Phone  string // E.164, com "+"
	Status string // "open" | "closed" | "all"
	DeskID string
	Limit  int // 0 = padrão da API (50, máx. 100)
	Cursor string
}

// FiltroConversas são os filtros de GET /conversations (04). Campos vazios
// são omitidos.
type FiltroConversas struct {
	Tab            string // "entrada" (sem dono) | "abertas" | "encerrados" | "todas"
	Status         string // PENDING | IN_PROGRESS | FINISHED
	Channel        string
	AssignedUserID string // ou "none"
	DepartmentID   string
	BotActive      *bool
	Phone          string
	UpdatedSince   time.Time // zero = omitido
	Limit          int
	Cursor         string
}

// ---------------------------------------------------------------------------
// Decodificação tolerante (helpers)
// ---------------------------------------------------------------------------

// decodificarObjeto lê um objeto JSON em duas formas: Bruto (map[string]any,
// números como json.Number — sem perder dígitos) e campos crus por chave. Um
// corpo que não é objeto (inclusive `null`) é erro.
func decodificarObjeto(b []byte) (map[string]any, map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var bruto map[string]any
	if err := dec.Decode(&bruto); err != nil {
		return nil, nil, err
	}
	if bruto == nil {
		return nil, nil, errors.New("infodesk: esperado objeto JSON, veio null")
	}
	var campos map[string]json.RawMessage
	if err := json.Unmarshal(b, &campos); err != nil {
		return nil, nil, err
	}
	return bruto, campos, nil
}

// lerCampo decodifica campos[chave] em destino; ausente, null ou de tipo
// inesperado deixa o zero value.
func lerCampo(campos map[string]json.RawMessage, chave string, destino any) {
	raw, ok := campos[chave]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return
	}
	_ = json.Unmarshal(raw, destino)
}

// opcional decodifica raw em *T; vazio, null ou ilegível = nil.
func opcional[T any](raw json.RawMessage) *T {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return &v
}

// numeroInteiro converte um valor decodificado em inteiro positivo: aceita
// json.Number (4521), número com ".0" e string numérica ("4521"). Qualquer
// outra coisa, não positivo ou fora do int = 0.
func numeroInteiro(v any) int {
	var s string
	switch n := v.(type) {
	case json.Number:
		s = n.String()
	case string:
		s = strings.TrimSpace(n)
	case float64:
		s = strconv.FormatFloat(n, 'f', -1, 64)
	default:
		return 0
	}
	if i, err := strconv.Atoi(s); err == nil {
		if i > 0 {
			return i
		}
		return 0
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && f > 0 && f < 1<<53 && f == math.Trunc(f) {
		return int(f)
	}
	return 0
}

func cloneSlice[E any](s []E) []E {
	if s == nil {
		return nil
	}
	return append([]E(nil), s...)
}

func cloneMapa(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// analisarInstante aceita ISO-8601/RFC 3339 com ou sem fração; vazio ou
// ilegível vira zero.
func analisarInstante(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z0700", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
