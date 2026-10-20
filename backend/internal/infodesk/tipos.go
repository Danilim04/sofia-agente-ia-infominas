package infodesk

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Referencia é um item de GET /desks ou GET /departments: `{id, name}`
// (02-dados-referencia.md). IDs são opacos (UUID nos exemplos).
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

// NovoTicket é o corpo de POST /tickets (03-tickets.md). Só DeskID e Title
// são obrigatórios; os ponteiros ficam fora do JSON quando nil.
//
// Hipóteses de trabalho (docs/infodesk/07): Channel "WHATSAPP" para tickets
// vindos da conversa (T2); PriorityID, ClientID e RequesterID omitidos na v1
// porque não há rota para obtê-los (T3, T4). Quem preenche é o código a
// partir da classificação e da tabela de roteamento, nunca o LLM.
type NovoTicket struct {
	DeskID       string  `json:"deskId"`
	Title        string  `json:"title"`
	Description  string  `json:"description,omitempty"`
	Channel      string  `json:"channel,omitempty"`
	ContactPhone string  `json:"contactPhone,omitempty"`
	PriorityID   *string `json:"priorityId,omitempty"`
	ClientID     *string `json:"clientId,omitempty"`
	RequesterID  *string `json:"requesterId,omitempty"`
}

// Ticket é a resposta de POST /tickets e GET /tickets/:number. Só `number` é
// garantido pela documentação, e o tipo dele é desconhecido (T1): o decode
// aceita número ou string e normaliza para string. O resto fica em Bruto.
//
// TODO(P3): tipar status, deskId, createdAt etc. quando a Infominas mandar o
// JSON real (docs/infodesk/07, item T1).
type Ticket struct {
	Number string
	Bruto  map[string]any
}

// UnmarshalJSON é tolerante: `number` numérico (sem perder dígitos — usa
// json.Number) ou string; ausente vira "". Um corpo que não é objeto JSON é
// erro.
func (t *Ticket) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return err
	}
	t.Bruto = m
	t.Number = numeroComoTexto(m["number"])
	return nil
}

func numeroComoTexto(v any) string {
	switch n := v.(type) {
	case json.Number:
		return n.String()
	case string:
		return strings.TrimSpace(n)
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64)
	default:
		return ""
	}
}

// Nomes dos eventos de webhook e valores observados no único payload
// documentado (05-webhooks.md).
const (
	EventoMensagemRecebida  = "message.received"
	EventoConversaIniciada  = "conversation.started"
	EventoConversaEncerrada = "conversation.finished"

	RemetenteCliente = "CLIENT"
	CanalWhatsApp    = "WHATSAPP"
)

// Evento é o envelope de toda entrega de webhook. Data fica crua porque só o
// payload de message.received é documentado (W1): decodifique com
// DecodificarMensagemRecebida ou DecodificarIDConversa conforme Event.
type Evento struct {
	ID             string          // id da ENTREGA — chave de dedup
	Event          string          // também vem no header X-Infominas-Event
	CreatedAt      time.Time       // zero quando ausente/ilegível
	OrganizationID string          // validar contra o esperado (W5)
	Data           json.RawMessage // payload específico do evento
}

type eventoJSON struct {
	ID             string          `json:"id"`
	Event          string          `json:"event"`
	CreatedAt      string          `json:"createdAt"`
	OrganizationID string          `json:"organizationId"`
	Data           json.RawMessage `json:"data"`
}

// UnmarshalJSON tolera createdAt ausente ou fora do ISO-8601 (fica zero; o
// receptor decide se descarta).
func (e *Evento) UnmarshalJSON(b []byte) error {
	var aux eventoJSON
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	*e = Evento{
		ID:             aux.ID,
		Event:          aux.Event,
		CreatedAt:      analisarInstante(aux.CreatedAt),
		OrganizationID: aux.OrganizationID,
		Data:           bytes.Clone(aux.Data), // sem alias do buffer de entrada
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

// Contato é `data.contact` do webhook. Name pode ser apelido do perfil;
// Phone vem em E.164. Ambos são PII: mascarar em log.
type Contato struct {
	Name  string `json:"name"`
	Phone string `json:"phone"`
}

// Mensagem é `data.message` do webhook. Sender só tem "CLIENT" documentado
// (W2); outros valores devem ser ignorados pelo receptor.
type Mensagem struct {
	ID        string
	Body      string
	Sender    string
	CreatedAt time.Time
}

// MensagemRecebida é `data` do evento message.received.
type MensagemRecebida struct {
	ConversationID string
	Channel        string
	Contact        Contato
	Message        Mensagem
}

type mensagemRecebidaJSON struct {
	ConversationID string  `json:"conversationId"`
	Channel        string  `json:"channel"`
	Contact        Contato `json:"contact"`
	Message        struct {
		ID        string `json:"id"`
		Body      string `json:"body"`
		Sender    string `json:"sender"`
		CreatedAt string `json:"createdAt"`
	} `json:"message"`
}

// ErrSemConversa: payload sem `conversationId` — não dá para rotear.
var ErrSemConversa = errors.New("infodesk: payload sem conversationId")

// DecodificarMensagemRecebida lê `data` de um message.received. Exige
// conversationId; o resto é tolerante (campo ausente = vazio).
func DecodificarMensagemRecebida(raw json.RawMessage) (MensagemRecebida, error) {
	var aux mensagemRecebidaJSON
	if err := json.Unmarshal(raw, &aux); err != nil {
		return MensagemRecebida{}, err
	}
	if strings.TrimSpace(aux.ConversationID) == "" {
		return MensagemRecebida{}, ErrSemConversa
	}
	return MensagemRecebida{
		ConversationID: aux.ConversationID,
		Channel:        aux.Channel,
		Contact:        aux.Contact,
		Message: Mensagem{
			ID:        aux.Message.ID,
			Body:      aux.Message.Body,
			Sender:    aux.Message.Sender,
			CreatedAt: analisarInstante(aux.Message.CreatedAt),
		},
	}, nil
}

// TemMidia aplica a hipótese W3: nenhum campo de anexo é documentado, então
// body vazio (ou só espaços) é tratado como mídia — e o receptor transfere
// para humano com aviso. TODO(P3): usar o campo real quando a Infominas
// documentar áudio/imagem/arquivo.
func (m MensagemRecebida) TemMidia() bool {
	return strings.TrimSpace(m.Message.Body) == ""
}

// DoCliente informa se a mensagem veio do contato: sender exatamente
// "CLIENT", o único valor documentado. Qualquer outro (ou variação) é
// ignorado e logado pelo receptor (W2).
func (m MensagemRecebida) DoCliente() bool {
	return m.Message.Sender == RemetenteCliente
}

// DecodificarIDConversa extrai `data.conversationId` de conversation.started
// e conversation.finished, cujos payloads não estão documentados (W1): a
// hipótese de trabalho é que ao menos esse campo existe.
func DecodificarIDConversa(raw json.RawMessage) (string, error) {
	var aux struct {
		ConversationID string `json:"conversationId"`
	}
	if err := json.Unmarshal(raw, &aux); err != nil {
		return "", err
	}
	if strings.TrimSpace(aux.ConversationID) == "" {
		return "", ErrSemConversa
	}
	return aux.ConversationID, nil
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
