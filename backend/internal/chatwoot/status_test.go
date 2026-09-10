package chatwoot

import (
	"encoding/json"
	"testing"
)

func TestStatusChangedToLeStringEInteiro(t *testing.T) {
	casos := []struct {
		nome    string
		payload string
		quer    bool
	}{
		{"string resolved", `{"event":"conversation_updated","id":7,"status":"resolved",
			"changed_attributes":[{"status":{"current_value":"resolved","previous_value":"open"}}]}`, true},
		{"enum inteiro 1", `{"event":"conversation_updated","id":7,"status":"resolved",
			"changed_attributes":[{"status":{"current_value":1,"previous_value":0}}]}`, true},
		{"reaberta (open)", `{"event":"conversation_updated","id":7,"status":"open",
			"changed_attributes":[{"status":{"current_value":"open","previous_value":"resolved"}}]}`, false},
		{"só etiquetas mudaram, status já era resolved", `{"event":"conversation_updated","id":7,"status":"resolved",
			"changed_attributes":[{"labels":{"current_value":["fila-bot"],"previous_value":[]}}]}`, false},
		{"sem changed_attributes", `{"event":"conversation_updated","id":7,"status":"resolved"}`, false},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			var ev ConversationUpdated
			if err := json.Unmarshal([]byte(c.payload), &ev); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := ev.StatusChangedTo(StatusResolved); got != c.quer {
				t.Fatalf("StatusChangedTo(resolved) = %v, queria %v", got, c.quer)
			}
		})
	}
}

func TestConversationStatusChangedPayload(t *testing.T) {
	var ev ConversationStatusChanged
	err := json.Unmarshal([]byte(`{"event":"conversation_status_changed","id":9,"inbox_id":5,"status":"resolved","labels":["fila-bot"]}`), &ev)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.ID != 9 || ev.Status != StatusResolved {
		t.Fatalf("payload lido errado: %+v", ev)
	}
}
