package chatwoot

import (
	"encoding/json"
	"testing"
)

func TestEhGrupoPorIdentifierJID(t *testing.T) {
	var msg MessageCreated
	payload := `{"event":"message_created","message_type":"incoming",
		"sender":{"id":9,"name":"Time Logística","identifier":"120363041234567890@g.us","type":"contact"},
		"conversation":{"id":1,"meta":{"sender":{"name":"Time Logística","identifier":"120363041234567890@g.us"}}}}`
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		t.Fatal(err)
	}
	if !msg.EhGrupo() {
		t.Fatal("identifier @g.us: esperava grupo")
	}
}

func TestEhGrupoSoPelaConversaQuandoSenderNaoTraz(t *testing.T) {
	var msg MessageCreated
	payload := `{"event":"message_created","message_type":"incoming",
		"sender":{"id":9,"name":"Fulano","type":"contact"},
		"conversation":{"id":1,"meta":{"sender":{"name":"Grupo X","identifier":"120363041234567890@G.US"}}}}`
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		t.Fatal(err)
	}
	if !msg.EhGrupo() {
		t.Fatal("identifier de grupo no meta da conversa: esperava grupo")
	}
}

func TestEhGrupoPorTelefoneQueNaoEhNumero(t *testing.T) {
	casos := map[string]bool{
		"+5511999990001":           false, // E.164 normal
		"5511999990001":            false,
		"+120363041234567890":      true, // JID de grupo gravado como telefone (18 dígitos)
		"120363041234567890@g.us":  true,
		"5511999990001-1614000000": true,  // formato antigo criador-timestamp
		"+55 (11) 99999-0001":      false, // hífen de formatação, não de grupo
		"":                         false,
		"abc":                      false,
	}
	for tel, esperado := range casos {
		if got := telefoneEhJIDDeGrupo(tel); got != esperado {
			t.Errorf("telefone %q: esperava grupo=%v, veio %v", tel, esperado, got)
		}
	}
}

func TestEhGrupoPorAtributoExplicito(t *testing.T) {
	c := Conversation{AdditionalAttributes: map[string]any{"is_group": true}}
	if !c.EhGrupo() {
		t.Fatal("additional_attributes.is_group=true: esperava grupo")
	}
	c = Conversation{Meta: Meta{Sender: Contact{AdditionalAttributes: map[string]any{"isGroup": "true"}}}}
	if !c.EhGrupo() {
		t.Fatal("contato com isGroup=\"true\": esperava grupo")
	}
	c = Conversation{AdditionalAttributes: map[string]any{"is_group": false}}
	if c.EhGrupo() {
		t.Fatal("is_group=false não é grupo")
	}
}

func TestContatoIndividualNaoEhGrupo(t *testing.T) {
	var msg MessageCreated
	payload := `{"event":"message_created","message_type":"incoming",
		"sender":{"id":9,"name":"Daniel","phone_number":"+5511999990001","identifier":"5511999990001@s.whatsapp.net","type":"contact"},
		"conversation":{"id":1,"meta":{"sender":{"name":"Daniel","phone_number":"+5511999990001","identifier":"5511999990001@s.whatsapp.net"}}}}`
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.EhGrupo() {
		t.Fatal("contato individual não pode ser tratado como grupo")
	}
}

// payloadGrupoReal é o webhook message_created REAL (Chatwoot v4.15.1 via
// Evolution v2.3.6, 2026-09-14) da conversa de grupo de teste em que o Zapin
// respondeu indevidamente. Só o telefone do participante foi mascarado.
// Note: o `sender` do nível raiz NÃO traz `type`; só o meta.sender traz.
const payloadGrupoReal = `{
 "event": "message_created",
 "id": 118,
 "content": "**+55 (31) 9999-0000 - Participante Teste:**\n\nteste",
 "message_type": "incoming",
 "private": false,
 "source_id": "WAID:3EB03E00F600330A1AADC8",
 "sender": {
  "account": {"id": 1, "name": "Omni Route"},
  "additional_attributes": {},
  "avatar": "",
  "custom_attributes": {},
  "email": null,
  "id": 3,
  "identifier": "120363428192696101@g.us",
  "name": " (GROUP)",
  "phone_number": null,
  "thumbnail": "",
  "blocked": false
 },
 "conversation": {
  "additional_attributes": {},
  "id": 5,
  "inbox_id": 3,
  "labels": ["fila-bot"],
  "meta": {
   "sender": {
    "additional_attributes": {},
    "custom_attributes": {},
    "email": null,
    "id": 3,
    "identifier": "120363428192696101@g.us",
    "name": " (GROUP)",
    "phone_number": null,
    "thumbnail": "",
    "blocked": false,
    "type": "contact"
   },
   "assignee": null
  },
  "status": "open",
  "custom_attributes": {}
 },
 "account": {"id": 1, "name": "Omni Route"}
}`

func TestEhGrupoPayloadRealEvolution(t *testing.T) {
	var msg MessageCreated
	if err := json.Unmarshal([]byte(payloadGrupoReal), &msg); err != nil {
		t.Fatal(err)
	}
	if !msg.MessageType.IsIncoming() || !msg.Sender.IsContact() || msg.Private {
		t.Fatal("o payload real precisa passar no filtro de borda comum (incoming, contato, não privada)")
	}
	if !msg.EhGrupo() {
		t.Fatal("payload real de grupo (Evolution/Chatwoot 4.15): esperava grupo")
	}
	if !msg.Conversation.EhGrupo() {
		t.Fatal("a conversa sozinha (conversation_created) também precisa ser reconhecida como grupo")
	}
	// Cada sinal sozinho também basta: sem identifier, o nome "(GROUP)" segura.
	msg.Sender.Identifier, msg.Conversation.Meta.Sender.Identifier = "", ""
	if !msg.EhGrupo() {
		t.Fatal("só pelo nome \" (GROUP)\": esperava grupo")
	}
}
