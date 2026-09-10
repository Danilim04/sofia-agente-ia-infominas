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
