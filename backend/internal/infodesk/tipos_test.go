package infodesk

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestTicket_DecodeTolerante(t *testing.T) {
	casos := map[string]struct {
		corpo  string
		number string
		erro   bool
	}{
		"numeroInteiro":   {`{"number":4521,"status":"OPEN"}`, "4521", false},
		"numeroGrande":    {`{"number":12345678901234567890}`, "12345678901234567890", false},
		"numeroString":    {`{"number":"INF-0042"}`, "INF-0042", false},
		"stringComEspaco": {`{"number":" 7 "}`, "7", false},
		"ausente":         {`{"id":"x"}`, "", false},
		"nulo":            {`{"number":null}`, "", false},
		"tipoEstranho":    {`{"number":{"a":1}}`, "", false},
		"objetoVazio":     {`{}`, "", false},
		"naoObjeto":       {`[1,2]`, "", true},
		"invalido":        {`{"number":`, "", true},
	}
	for nome, cs := range casos {
		t.Run(nome, func(t *testing.T) {
			var tk Ticket
			err := json.Unmarshal([]byte(cs.corpo), &tk)
			if (err != nil) != cs.erro {
				t.Fatalf("err = %v, esperava erro=%v", err, cs.erro)
			}
			if tk.Number != cs.number {
				t.Errorf("Number = %q, quer %q", tk.Number, cs.number)
			}
			if !cs.erro && tk.Bruto == nil {
				t.Error("Bruto deve ser preenchido")
			}
		})
	}
}

func TestNumeroComoTexto_Float(t *testing.T) {
	if got := numeroComoTexto(float64(88)); got != "88" {
		t.Errorf("float64 → %q", got)
	}
	if got := numeroComoTexto(true); got != "" {
		t.Errorf("bool → %q", got)
	}
}

func TestNovoTicket_JSONOmiteOpcionais(t *testing.T) {
	b, err := json.Marshal(NovoTicket{DeskID: "m1", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"deskId":"m1","title":"t"}` {
		t.Errorf("json = %s", b)
	}
	prio, cli, req := "p", "c", "r"
	b, _ = json.Marshal(NovoTicket{DeskID: "m1", Title: "t", PriorityID: &prio, ClientID: &cli, RequesterID: &req})
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m["priorityId"] != "p" || m["clientId"] != "c" || m["requesterId"] != "r" {
		t.Errorf("opcionais não serializados: %s", b)
	}
}

const payloadMensagemRecebida = `{
  "id": "entrega-1",
  "event": "message.received",
  "createdAt": "2026-06-29T12:00:00.000Z",
  "organizationId": "org-1",
  "data": {
    "conversationId": "conv-1",
    "channel": "WHATSAPP",
    "contact": { "name": "Maria", "phone": "+5531990000000" },
    "message": { "id": "msg-1", "body": "Bom dia, preciso de ajuda", "sender": "CLIENT", "createdAt": "2026-06-29T12:00:00Z" }
  }
}`

func TestDecodificarEvento(t *testing.T) {
	ev, err := DecodificarEvento([]byte(payloadMensagemRecebida))
	if err != nil {
		t.Fatal(err)
	}
	if ev.ID != "entrega-1" || ev.Event != EventoMensagemRecebida || ev.OrganizationID != "org-1" {
		t.Errorf("evento = %+v", ev)
	}
	if quer := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC); !ev.CreatedAt.Equal(quer) {
		t.Errorf("CreatedAt = %v", ev.CreatedAt)
	}
	if len(ev.Data) == 0 {
		t.Fatal("Data vazio")
	}

	m, err := DecodificarMensagemRecebida(ev.Data)
	if err != nil {
		t.Fatal(err)
	}
	if m.ConversationID != "conv-1" || m.Channel != CanalWhatsApp || m.Contact.Name != "Maria" || m.Contact.Phone != "+5531990000000" {
		t.Errorf("mensagem = %+v", m)
	}
	if m.Message.ID != "msg-1" || m.Message.Body != "Bom dia, preciso de ajuda" || m.Message.Sender != "CLIENT" || m.Message.CreatedAt.IsZero() {
		t.Errorf("message = %+v", m.Message)
	}
	if m.TemMidia() || !m.DoCliente() {
		t.Errorf("TemMidia=%v DoCliente=%v", m.TemMidia(), m.DoCliente())
	}
}

func TestDecodificarEvento_Tolerancias(t *testing.T) {
	t.Run("createdAtIlegivel", func(t *testing.T) {
		ev, err := DecodificarEvento([]byte(`{"id":"e","event":"conversation.started","createdAt":"ontem","data":{}}`))
		if err != nil || !ev.CreatedAt.IsZero() {
			t.Errorf("ev = %+v, err = %v", ev, err)
		}
	})
	t.Run("createdAtAusente", func(t *testing.T) {
		ev, err := DecodificarEvento([]byte(`{"id":"e","event":"conversation.finished"}`))
		if err != nil || !ev.CreatedAt.IsZero() || ev.Data != nil {
			t.Errorf("ev = %+v, err = %v", ev, err)
		}
	})
	t.Run("createdAtSemFracaoComOffset", func(t *testing.T) {
		ev, _ := DecodificarEvento([]byte(`{"id":"e","event":"x","createdAt":"2026-06-29T09:00:00-03:00"}`))
		if !ev.CreatedAt.Equal(time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)) {
			t.Errorf("CreatedAt = %v", ev.CreatedAt)
		}
	})
	t.Run("semId", func(t *testing.T) {
		if _, err := DecodificarEvento([]byte(`{"event":"x"}`)); !errors.Is(err, ErrEventoInvalido) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("semEvent", func(t *testing.T) {
		if _, err := DecodificarEvento([]byte(`{"id":"x"}`)); !errors.Is(err, ErrEventoInvalido) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("jsonInvalido", func(t *testing.T) {
		if _, err := DecodificarEvento([]byte(`{`)); err == nil {
			t.Error("esperado erro")
		}
	})
	t.Run("naoObjeto", func(t *testing.T) {
		if _, err := DecodificarEvento([]byte(`[]`)); err == nil {
			t.Error("esperado erro")
		}
	})
}

func TestDecodificarMensagemRecebida_Casos(t *testing.T) {
	t.Run("semConversationId", func(t *testing.T) {
		_, err := DecodificarMensagemRecebida([]byte(`{"message":{"body":"oi"}}`))
		if !errors.Is(err, ErrSemConversa) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("jsonInvalido", func(t *testing.T) {
		if _, err := DecodificarMensagemRecebida([]byte(`nope`)); err == nil {
			t.Error("esperado erro")
		}
	})
	t.Run("midiaBodyVazio", func(t *testing.T) {
		m, err := DecodificarMensagemRecebida([]byte(`{"conversationId":"c1","message":{"id":"m","sender":"CLIENT","body":"   "}}`))
		if err != nil || !m.TemMidia() {
			t.Errorf("m = %+v, err = %v", m, err)
		}
	})
	t.Run("midiaSemMessage", func(t *testing.T) {
		m, err := DecodificarMensagemRecebida([]byte(`{"conversationId":"c1"}`))
		if err != nil || !m.TemMidia() || m.DoCliente() {
			t.Errorf("m = %+v, err = %v", m, err)
		}
	})
	t.Run("remetenteNaoCliente", func(t *testing.T) {
		m, _ := DecodificarMensagemRecebida([]byte(`{"conversationId":"c1","message":{"sender":"USER","body":"oi"}}`))
		if m.DoCliente() {
			t.Error("USER não é cliente")
		}
		m2, _ := DecodificarMensagemRecebida([]byte(`{"conversationId":"c1","message":{"sender":" client ","body":"oi"}}`))
		if m2.DoCliente() {
			t.Error("só o valor documentado CLIENT conta (W2)")
		}
	})
}

func TestDecodificarIDConversa(t *testing.T) {
	id, err := DecodificarIDConversa([]byte(`{"conversationId":"c9","foo":1}`))
	if err != nil || id != "c9" {
		t.Errorf("id = %q, err = %v", id, err)
	}
	if _, err := DecodificarIDConversa([]byte(`{"foo":1}`)); !errors.Is(err, ErrSemConversa) {
		t.Errorf("err = %v", err)
	}
	if _, err := DecodificarIDConversa([]byte(`{`)); err == nil {
		t.Error("esperado erro")
	}
}

func TestAnalisarInstante(t *testing.T) {
	casos := map[string]bool{
		"":                            true,
		"   ":                         true,
		"ontem":                       true,
		"2026-06-29T12:00:00.000Z":    false,
		"2026-06-29T12:00:00Z":        false,
		"2026-06-29T12:00:00-0300":    false,
		"2026-06-29T12:00:00":         false,
		"2026-06-29T12:00:00.123456Z": false,
	}
	for s, zero := range casos {
		if got := analisarInstante(s).IsZero(); got != zero {
			t.Errorf("%q: IsZero = %v, quer %v", s, got, zero)
		}
	}
}
