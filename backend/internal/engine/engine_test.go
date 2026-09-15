package engine

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"bot-azapfy/internal/chatwoot"
	"bot-azapfy/internal/config"
	"bot-azapfy/internal/store"
)

// engineComStore monta uma Engine só com o store (sem Chatwoot/gate/cérebro):
// os handlers de status não falam com ninguém além da persistência.
func engineComStore(t *testing.T) (*Engine, store.Store) {
	t.Helper()
	st, err := store.NewSQLite(filepath.Join(t.TempDir(), "gate.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(&config.Config{}, nil, nil, nil, st, nil), st
}

func mudancaDeStatus(convID int64, de, para string) *chatwoot.ConversationUpdated {
	ev := &chatwoot.ConversationUpdated{
		ChangedAttributes: []map[string]chatwoot.ChangeValue{{
			"status": {CurrentValue: json.RawMessage(`"` + para + `"`), PreviousValue: json.RawMessage(`"` + de + `"`)},
		}},
	}
	ev.ID = convID
	ev.Status = para
	return ev
}

func TestConversaResolvidaApagaEstadoDoGate(t *testing.T) {
	e, st := engineComStore(t)
	ctx := context.Background()
	const conv = int64(41)
	if err := st.SetGate(ctx, &store.GateState{ConversationID: conv, State: store.GateFalha}); err != nil {
		t.Fatal(err)
	}

	e.HandleConversationUpdated(ctx, mudancaDeStatus(conv, chatwoot.StatusOpen, chatwoot.StatusResolved))

	if gs, _ := st.GetGate(ctx, conv); gs != nil {
		t.Fatalf("resolvida: esperava gate apagado, ainda existe %+v", gs)
	}
}

func TestConversaResolvidaNaoMexeEmOutrasConversas(t *testing.T) {
	e, st := engineComStore(t)
	ctx := context.Background()
	_ = st.SetGate(ctx, &store.GateState{ConversationID: 42, State: store.GateIdentificado})
	_ = st.SetGate(ctx, &store.GateState{ConversationID: 43, State: store.GateFalha})

	e.HandleConversationUpdated(ctx, mudancaDeStatus(42, chatwoot.StatusOpen, chatwoot.StatusResolved))

	if gs, _ := st.GetGate(ctx, 43); gs == nil || gs.State != store.GateFalha {
		t.Fatalf("conversa 43 não podia mudar, veio %+v", gs)
	}
}

func TestConversaReabertaOuComEtiquetaNaoApagaGate(t *testing.T) {
	e, st := engineComStore(t)
	ctx := context.Background()
	const conv = int64(44)
	_ = st.SetGate(ctx, &store.GateState{ConversationID: conv, State: store.GateIdentificado, Data: `{"perfil":{}}`})

	// Reabertura (resolved → open) e mudança só de etiquetas: sem ação.
	e.HandleConversationUpdated(ctx, mudancaDeStatus(conv, chatwoot.StatusResolved, chatwoot.StatusOpen))
	soEtiquetas := &chatwoot.ConversationUpdated{
		ChangedAttributes: []map[string]chatwoot.ChangeValue{{
			"labels": {CurrentValue: json.RawMessage(`["fila-bot"]`), PreviousValue: json.RawMessage(`[]`)},
		}},
	}
	soEtiquetas.ID = conv
	soEtiquetas.Status = chatwoot.StatusResolved // status atual, mas não mudou NESTA atualização
	e.HandleConversationUpdated(ctx, soEtiquetas)

	if gs, _ := st.GetGate(ctx, conv); gs == nil || gs.State != store.GateIdentificado {
		t.Fatalf("gate não podia ser apagado, veio %+v", gs)
	}
}

func TestEventoStatusChangedResolvidoApagaGate(t *testing.T) {
	e, st := engineComStore(t)
	ctx := context.Background()
	const conv = int64(45)
	_ = st.SetGate(ctx, &store.GateState{ConversationID: conv, State: store.GateFalha})

	ev := &chatwoot.ConversationStatusChanged{}
	ev.ID = conv
	ev.Status = chatwoot.StatusResolved
	e.HandleConversationStatusChanged(ctx, ev)

	if gs, _ := st.GetGate(ctx, conv); gs != nil {
		t.Fatalf("status_changed resolved: esperava gate apagado, ainda existe %+v", gs)
	}
}

// TestMensagemDeGrupoNaoEntraNaFila: o bot não responde grupo de WhatsApp. A
// mensagem é descartada na borda — nenhum worker de conversa é criado (sem
// gate, sem cérebro, sem resposta).
func TestMensagemDeGrupoNaoEntraNaFila(t *testing.T) {
	e, _ := engineComStore(t)
	ctx := context.Background()

	msg := &chatwoot.MessageCreated{
		MessageType: chatwoot.MessageIncoming,
		Content:     "alguém sabe se o app caiu?",
		SourceID:    "waid-grupo-1",
		Sender:      chatwoot.Sender{Type: "contact", Name: "Fulano", Identifier: "120363041234567890@g.us"},
	}
	msg.Conversation.ID = 50
	msg.Conversation.Meta.Sender = chatwoot.Contact{Name: "Grupo Logística", Identifier: "120363041234567890@g.us"}

	e.HandleMessageCreated(ctx, msg)

	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.filas[50]; ok {
		t.Fatal("mensagem de grupo não pode abrir fila/worker de conversa")
	}
}

// TestConversaDeGrupoNaoEhAdotada: conversation_created de grupo na caixa do
// bot não recebe a etiqueta da fila do bot.
func TestConversaDeGrupoNaoEhAdotada(t *testing.T) {
	e, _ := engineComStore(t)
	e.cfg.LabelBot = "fila-bot"
	ctx := context.Background()

	conv := chatwoot.Conversation{ID: 51}
	conv.Meta.Sender = chatwoot.Contact{Name: "Grupo", PhoneNumber: "+120363041234567890"}
	if e.adotarConversa(ctx, &conv) {
		t.Fatal("conversa de grupo não pode ser adotada pela fila do bot")
	}
}

// TestPayloadRealDeGrupoNaoEntraNaFila repete o incidente de 2026-09-14
// (conversa 5, grupo de teste) com o webhook real: hoje a engine descarta na
// borda, mesmo com a conversa já etiquetada como fila-bot.
func TestPayloadRealDeGrupoNaoEntraNaFila(t *testing.T) {
	e, _ := engineComStore(t)
	e.cfg.LabelBot = "fila-bot"
	ctx := context.Background()

	var msg chatwoot.MessageCreated
	if err := json.Unmarshal([]byte(payloadGrupoReal), &msg); err != nil {
		t.Fatal(err)
	}
	e.HandleMessageCreated(ctx, &msg)

	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.filas[msg.Conversation.ID]; ok {
		t.Fatal("payload real de grupo não pode abrir fila/worker de conversa")
	}
}

// payloadGrupoReal: cópia do fixture de internal/chatwoot (telefone mascarado).
const payloadGrupoReal = `{
 "event": "message_created", "id": 118,
 "content": "**+55 (31) 9999-0000 - Participante Teste:**\n\nteste",
 "message_type": "incoming", "private": false, "source_id": "WAID:3EB03E00F600330A1AADC8",
 "sender": {"id": 3, "identifier": "120363428192696101@g.us", "name": " (GROUP)", "phone_number": null, "additional_attributes": {}},
 "conversation": {
  "id": 5, "inbox_id": 3, "labels": ["fila-bot"], "status": "open", "additional_attributes": {},
  "meta": {"sender": {"id": 3, "identifier": "120363428192696101@g.us", "name": " (GROUP)", "phone_number": null, "type": "contact"}, "assignee": null}
 },
 "account": {"id": 1, "name": "Omni Route"}
}`
