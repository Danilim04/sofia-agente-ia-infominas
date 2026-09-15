// Package chatwoot contém os tipos dos payloads de webhook do Chatwoot v4.4.0 e
// o cliente REST de saída.
package chatwoot

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// MessageType normaliza o campo message_type do webhook, que pode chegar como
// inteiro (0=incoming, 1=outgoing, 2=activity, 3=template) ou como string,
// dependendo da versão do Chatwoot.
type MessageType string

const (
	MessageIncoming MessageType = "incoming"
	MessageOutgoing MessageType = "outgoing"
	MessageActivity MessageType = "activity"
	MessageTemplate MessageType = "template"
)

// UnmarshalJSON aceita tanto a forma inteira quanto a forma string.
func (m *MessageType) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		return nil
	}
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		*m = MessageType(str)
		return nil
	}
	var n int
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	switch n {
	case 0:
		*m = MessageIncoming
	case 1:
		*m = MessageOutgoing
	case 2:
		*m = MessageActivity
	case 3:
		*m = MessageTemplate
	default:
		*m = MessageType(strconv.Itoa(n))
	}
	return nil
}

func (m MessageType) IsIncoming() bool { return m == MessageIncoming }
func (m MessageType) IsOutgoing() bool { return m == MessageOutgoing }

// Envelope é usado para descobrir o tipo do evento antes do parse completo.
type Envelope struct {
	Event string `json:"event"`
}

// FlexTime tolera os formatos de `created_at` que o Chatwoot emite conforme a
// versão/canal: unix (int ou float), RFC3339 ou "2006-01-02 15:04:05 UTC".
// Falha de parse vira zero-value (não derruba o unmarshal do evento).
type FlexTime struct{ time.Time }

func (t *FlexTime) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		return nil
	}
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return nil
		}
		for _, layout := range []string{
			time.RFC3339, "2006-01-02 15:04:05 MST", "2006-01-02 15:04:05 -0700",
			"2006-01-02T15:04:05.000-07:00",
		} {
			if parsed, err := time.Parse(layout, str); err == nil {
				t.Time = parsed
				return nil
			}
		}
		return nil
	}
	var n float64
	if err := json.Unmarshal(b, &n); err != nil {
		return nil
	}
	if n > 0 {
		t.Time = time.Unix(int64(n), 0)
	}
	return nil
}

// MessageCreated representa o evento message_created.
type MessageCreated struct {
	Event        string       `json:"event"`
	ID           int64        `json:"id"`
	Content      string       `json:"content"`
	MessageType  MessageType  `json:"message_type"`
	Private      bool         `json:"private"`
	SourceID     string       `json:"source_id"` // id da mensagem na origem (WAID no WhatsApp)
	CreatedAt    FlexTime     `json:"created_at"`
	Sender       Sender       `json:"sender"`
	Conversation Conversation `json:"conversation"`
	Account      Account      `json:"account"`
}

// Phone devolve o telefone do contato que originou a mensagem, tentando o
// sender e, em fallback, o sender do meta da conversa. Vazio quando ausente.
func (m *MessageCreated) Phone() string {
	if p := strings.TrimSpace(m.Sender.PhoneNumber); p != "" {
		return p
	}
	return strings.TrimSpace(m.Conversation.Meta.Sender.PhoneNumber)
}

// ConversationUpdated representa o evento conversation_updated. No payload do
// Chatwoot os atributos da conversa vêm "achatados" no nível raiz, por isso a
// struct Conversation é embutida.
type ConversationUpdated struct {
	Event             string                   `json:"event"`
	ChangedAttributes []map[string]ChangeValue `json:"changed_attributes"`
	Conversation
}

// ConversationCreated representa o evento conversation_created — o payload é a
// própria conversa serializada (mesma forma do conversation_updated).
type ConversationCreated struct {
	Event string `json:"event"`
	Conversation
}

// ConversationStatusChanged representa o evento conversation_status_changed —
// payload igual ao conversation_created (a conversa serializada, já com o
// status novo). O bot assina conversation_updated, que também carrega a
// mudança de status em changed_attributes; este evento é tratado por
// robustez, caso a assinatura do webhook mude.
type ConversationStatusChanged struct {
	Event string `json:"event"`
	Conversation
}

// Status de conversa do Chatwoot (enum: 0=open, 1=resolved, 2=pending,
// 3=snoozed). O webhook normalmente serializa a string.
const (
	StatusOpen     = "open"
	StatusResolved = "resolved"
	StatusPending  = "pending"
	StatusSnoozed  = "snoozed"
)

// statusFromRaw normaliza um valor de status que pode vir como string ou como
// inteiro do enum (mesmo cuidado do MessageType).
func statusFromRaw(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	if s[0] == '"' {
		var str string
		_ = json.Unmarshal(raw, &str)
		return str
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return ""
	}
	switch n {
	case 0:
		return StatusOpen
	case 1:
		return StatusResolved
	case 2:
		return StatusPending
	case 3:
		return StatusSnoozed
	}
	return strconv.Itoa(n)
}

// ChangeValue é o par current/previous de cada atributo alterado.
type ChangeValue struct {
	CurrentValue  json.RawMessage `json:"current_value"`
	PreviousValue json.RawMessage `json:"previous_value"`
}

// LabelsChanged informa se esta atualização inclui uma mudança na lista de
// etiquetas.
func (ev *ConversationUpdated) LabelsChanged() bool {
	for _, attr := range ev.ChangedAttributes {
		if _, ok := attr["labels"]; ok {
			return true
		}
	}
	return false
}

// StatusChangedTo informa se o status da conversa mudou para target NESTA
// atualização (entrada "status" em changed_attributes com current_value ==
// target). Uma atualização sem mudança de status devolve false — mesmo que o
// status atual da conversa seja target.
func (ev *ConversationUpdated) StatusChangedTo(target string) bool {
	for _, attr := range ev.ChangedAttributes {
		cv, ok := attr["status"]
		if !ok {
			continue
		}
		return statusFromRaw(cv.CurrentValue) == target
	}
	return false
}

// LabelJustAdded informa se target passou a constar nas etiquetas NESTA
// atualização: presente em current_value e ausente em previous_value.
func (ev *ConversationUpdated) LabelJustAdded(target string) bool {
	for _, attr := range ev.ChangedAttributes {
		cv, ok := attr["labels"]
		if !ok {
			continue
		}
		var current, previous []string
		_ = json.Unmarshal(cv.CurrentValue, &current)
		_ = json.Unmarshal(cv.PreviousValue, &previous)
		return HasLabel(current, target) && !HasLabel(previous, target)
	}
	return false
}

// Conversation reúne os campos da conversa que o bot utiliza.
type Conversation struct {
	ID                   int64          `json:"id"`
	AccountID            int64          `json:"account_id"`
	InboxID              int64          `json:"inbox_id"`
	Status               string         `json:"status"`
	Labels               []string       `json:"labels"`
	Meta                 Meta           `json:"meta"`
	CustomAttributes     map[string]any `json:"custom_attributes"`
	AdditionalAttributes map[string]any `json:"additional_attributes"`
}

// EhGrupo informa se a conversa é de um GRUPO de WhatsApp (e não de um
// contato individual). O bot nunca responde grupo: não há um cliente para
// identificar, e uma resposta ali vaza para todos os participantes.
//
// Os relays (Evolution/Baileys/WAHA) não têm um campo padronizado, então a
// detecção é por evidência — qualquer uma basta:
//   - identifier do contato termina em "@g.us" (JID de grupo do WhatsApp);
//   - nome do contato termina em "(GROUP)" (convenção do Evolution);
//   - o "telefone" não é um número: mais de 15 dígitos (teto do E.164 —
//     JIDs de grupo têm 18) ou o formato antigo "criador-timestamp";
//   - atributo is_group/isGroup/group verdadeiro no contato ou na conversa.
func (c *Conversation) EhGrupo() bool {
	return contatoEhGrupo(c.Meta.Sender.Identifier, c.Meta.Sender.PhoneNumber, c.Meta.Sender.Name, c.Meta.Sender.AdditionalAttributes) ||
		atributoGrupo(c.AdditionalAttributes)
}

// EhGrupo informa se a mensagem veio de um grupo — olha o sender da mensagem
// e, em fallback, o contato da conversa.
func (m *MessageCreated) EhGrupo() bool {
	return contatoEhGrupo(m.Sender.Identifier, m.Sender.PhoneNumber, m.Sender.Name, m.Sender.AdditionalAttributes) ||
		m.Conversation.EhGrupo()
}

const sufixoJIDGrupo = "@g.us"

func contatoEhGrupo(identifier, phone, nome string, attrs map[string]any) bool {
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(identifier)), sufixoJIDGrupo) {
		return true
	}
	// Evolution nomeia o contato do grupo como "<assunto> (GROUP)" — e só
	// " (GROUP)" quando não consegue ler o assunto (payload real, 2026-09-14).
	if strings.HasSuffix(strings.ToUpper(strings.TrimSpace(nome)), "(GROUP)") {
		return true
	}
	if telefoneEhJIDDeGrupo(phone) {
		return true
	}
	return atributoGrupo(attrs)
}

// telefoneEhJIDDeGrupo reconhece um id de grupo que o relay gravou no campo
// phone_number: "120363xxxxxxxxxxxx" (18 dígitos, além do teto de 15 do
// E.164) ou "5511999999999-1234567890" (formato antigo, com hífen entre dois
// blocos de dígitos).
func telefoneEhJIDDeGrupo(phone string) bool {
	p := strings.TrimSpace(phone)
	p = strings.TrimSuffix(strings.ToLower(p), sufixoJIDGrupo)
	if p == "" {
		return false
	}
	digitos := 0
	for _, r := range p {
		if r >= '0' && r <= '9' {
			digitos++
		}
	}
	if digitos > 15 {
		return true
	}
	// Formato antigo: dois blocos de dígitos separados por hífen.
	if i := strings.IndexByte(p, '-'); i > 0 && i < len(p)-1 {
		a, b := strings.TrimPrefix(p[:i], "+"), p[i+1:]
		if soDigitos(a) && soDigitos(b) && len(b) >= 9 {
			return true
		}
	}
	return false
}

func soDigitos(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// atributoGrupo procura uma marca explícita de grupo nos atributos livres que
// alguns relays preenchem.
func atributoGrupo(attrs map[string]any) bool {
	for _, k := range []string{"is_group", "isGroup", "group", "grupo"} {
		switch v := attrs[k].(type) {
		case bool:
			if v {
				return true
			}
		case string:
			if s := strings.ToLower(strings.TrimSpace(v)); s == "true" || s == "1" || s == "yes" || s == "sim" {
				return true
			}
		}
	}
	return false
}

// Meta carrega o contato (sender) e o agente designado da conversa.
type Meta struct {
	Sender   Contact `json:"sender"`
	Assignee *User   `json:"assignee"`
}

// Contact é o cliente final.
type Contact struct {
	ID                   int64          `json:"id"`
	Name                 string         `json:"name"`
	Email                string         `json:"email"`
	PhoneNumber          string         `json:"phone_number"`
	Identifier           string         `json:"identifier"` // id na origem (JID no WhatsApp: "...@s.whatsapp.net" | "...@g.us")
	AdditionalAttributes map[string]any `json:"additional_attributes"`
}

// User é um agente/atendente.
type User struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Sender é o autor de uma mensagem (contato ou agente).
type Sender struct {
	ID                   int64          `json:"id"`
	Name                 string         `json:"name"`
	Email                string         `json:"email"`
	PhoneNumber          string         `json:"phone_number"`
	Identifier           string         `json:"identifier"`
	AdditionalAttributes map[string]any `json:"additional_attributes"`
	Type                 string         `json:"type"` // "contact" | "user"
}

// IsContact informa se o remetente é o cliente final. Campo vazio é tratado
// como contato porque algumas versões omitem o type em mensagens de entrada.
func (s Sender) IsContact() bool {
	return s.Type == "contact" || s.Type == ""
}

// Account identifica a conta do Chatwoot.
type Account struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// HasLabel informa se a etiqueta target está presente na lista.
func HasLabel(labels []string, target string) bool {
	for _, l := range labels {
		if l == target {
			return true
		}
	}
	return false
}

// ReplaceLabel devolve uma nova lista de etiquetas removendo remove (se houver)
// e garantindo a presença de add. add vazio significa apenas remoção.
func ReplaceLabel(current []string, remove, add string) []string {
	out := make([]string, 0, len(current)+1)
	addPresent := false
	for _, l := range current {
		if l == remove {
			continue
		}
		if l == add {
			addPresent = true
		}
		out = append(out, l)
	}
	if add != "" && !addPresent {
		out = append(out, add)
	}
	return out
}
