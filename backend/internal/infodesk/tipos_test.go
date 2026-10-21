package infodesk

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// ticketPDF é o objeto `ticket` literal do PDF v1 (docs/infodesk/03, 9.1).
const ticketPDF = `{
  "id": "t1abc", "number": 4521, "title": "Impressora do financeiro não imprime",
  "description": "…", "channel": "WHATSAPP", "ticketType": "INCIDENT",
  "impact": null, "urgency": null, "priorityLevel": null,
  "desk": { "id": "d1", "name": "Suporte" },
  "status": { "id": "s1", "name": "Aberto", "isOpen": true, "isClose": false, "isCanceled": false },
  "stage": { "id": "e1", "name": "Triagem" },
  "priority": { "id": "p1", "name": "Alta" },
  "catalogItemId": null,
  "client": { "id": "k1", "name": "ACME Ltda" },
  "requester": { "id": "r1", "name": "Maria Souza", "email": "maria@acme.com.br", "phone": "+5531990000000" },
  "resourceId": null, "responsible": { "id": "u1", "username": "joao", "email": "j@acme.com.br" },
  "contactPhone": null, "conversationId": "c1d2",
  "isClosed": false, "closedAt": null, "isDraft": false,
  "serviceDueAt": "2026-10-19T14:00:00.000Z", "solutionDueAt": "2026-10-20T14:00:00.000Z", "firstRespondedAt": null,
  "customValues": null, "createdAt": "2026-10-19T12:00:00.000Z", "updatedAt": "2026-10-19T12:00:00.000Z"
}`

func TestTicket_DecodePDF(t *testing.T) {
	var tk Ticket
	if err := json.Unmarshal([]byte(ticketPDF), &tk); err != nil {
		t.Fatal(err)
	}
	if tk.Number != 4521 {
		t.Errorf("Number = %d", tk.Number)
	}
	if want := (Status{ID: "s1", Name: "Aberto", IsOpen: true}); tk.Status != want {
		t.Errorf("Status = %+v, quer %+v", tk.Status, want)
	}
	if tk.IsClosed {
		t.Error("IsClosed deveria ser false")
	}
	if tk.ConversationID != "c1d2" {
		t.Errorf("ConversationID = %q", tk.ConversationID)
	}
	if tk.ContactPhone != "" { // null
		t.Errorf("ContactPhone = %q", tk.ContactPhone)
	}
	if tk.Requester == nil || tk.Requester.ID != "r1" || tk.Requester.Name != "Maria Souza" || tk.Requester.Phone != "+5531990000000" {
		t.Errorf("Requester = %+v", tk.Requester)
	}
	if tk.Desk != (Referencia{ID: "d1", Nome: "Suporte"}) || tk.Priority != (Referencia{ID: "p1", Nome: "Alta"}) {
		t.Errorf("Desk = %+v, Priority = %+v", tk.Desk, tk.Priority)
	}
	// O resto fica em Bruto.
	if tk.Bruto["title"] != "Impressora do financeiro não imprime" || tk.Bruto["channel"] != "WHATSAPP" {
		t.Errorf("Bruto = %v", tk.Bruto)
	}
	if _, ok := tk.Bruto["stage"].(map[string]any); !ok {
		t.Errorf("stage deveria estar em Bruto: %v", tk.Bruto["stage"])
	}
}

func TestTicket_DecodeTolerante(t *testing.T) {
	casos := map[string]struct {
		corpo  string
		number int
		erro   bool
	}{
		"numeroInteiro":      {`{"number":4521,"title":"x"}`, 4521, false},
		"numeroComoTexto":    {`{"number":"4521"}`, 4521, false}, // tolerância; o PDF diz inteiro
		"numeroComEspaco":    {`{"number":" 7 "}`, 7, false},
		"numeroDecimalZero":  {`{"number":88.0}`, 88, false},
		"stringNaoNumerica":  {`{"number":"INF-0042"}`, 0, false}, // vira "sem número", nunca sucesso
		"numeroNegativo":     {`{"number":-3}`, 0, false},
		"numeroZero":         {`{"number":0}`, 0, false},
		"numeroFracionario":  {`{"number":4.5}`, 0, false},
		"ausente":            {`{"id":"x"}`, 0, false},
		"nulo":               {`{"number":null}`, 0, false},
		"tipoEstranho":       {`{"number":{"a":1}}`, 0, false},
		"objetoVazio":        {`{}`, 0, false},
		"statusComoTexto":    {`{"number":5,"status":"OPEN"}`, 5, false}, // sub-objeto ilegível não derruba o ticket
		"requesterComoTexto": {`{"number":5,"requester":"Maria"}`, 5, false},
		"naoObjeto":          {`[1,2]`, 0, true},
		"nullLiteral":        {`null`, 0, true},
		"invalido":           {`{"number":`, 0, true},
	}
	for nome, cs := range casos {
		t.Run(nome, func(t *testing.T) {
			var tk Ticket
			err := json.Unmarshal([]byte(cs.corpo), &tk)
			if (err != nil) != cs.erro {
				t.Fatalf("err = %v, esperava erro=%v", err, cs.erro)
			}
			if tk.Number != cs.number {
				t.Errorf("Number = %d, quer %d", tk.Number, cs.number)
			}
			if !cs.erro && tk.Bruto == nil {
				t.Error("Bruto deve ser preenchido")
			}
		})
	}
}

func TestTicket_NumeroGrandeNaoPerdeDigitosNoBruto(t *testing.T) {
	var tk Ticket
	if err := json.Unmarshal([]byte(`{"number":4521,"serial":12345678901234567890}`), &tk); err != nil {
		t.Fatal(err)
	}
	if n, ok := tk.Bruto["serial"].(json.Number); !ok || n.String() != "12345678901234567890" {
		t.Errorf("serial = %#v", tk.Bruto["serial"])
	}
}

func TestStatus_EstadoDoChamado(t *testing.T) {
	var tk Ticket
	_ = json.Unmarshal([]byte(`{"number":9,"status":{"id":"s9","name":"Resolvido","isOpen":false,"isClose":true,"isCanceled":false},"isClosed":true}`), &tk)
	if !tk.Status.IsClose || tk.Status.IsOpen || tk.Status.IsCanceled || !tk.IsClosed || tk.Status.Name != "Resolvido" {
		t.Errorf("tk = %+v", tk)
	}
	_ = json.Unmarshal([]byte(`{"number":9,"status":{"id":"s8","name":"Cancelado","isOpen":false,"isClose":false,"isCanceled":true}}`), &tk)
	if !tk.Status.IsCanceled || tk.IsClosed {
		t.Errorf("tk = %+v", tk)
	}
}

func TestNumeroInteiro(t *testing.T) {
	casos := []struct {
		nome string
		in   any
		quer int
	}{
		{"jsonNumber", json.Number("4521"), 4521},
		{"float", float64(88), 88},
		{"floatFracionario", 88.5, 0},
		{"string", " 12 ", 12},
		{"stringTexto", "abc", 0},
		{"bool", true, 0},
		{"nil", nil, 0},
		{"negativo", json.Number("-1"), 0},
		{"gigante", json.Number("12345678901234567890"), 0},
	}
	for _, cs := range casos {
		if got := numeroInteiro(cs.in); got != cs.quer {
			t.Errorf("%s: numeroInteiro(%#v) = %d, quer %d", cs.nome, cs.in, got, cs.quer)
		}
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
	prio, cat, cli, req := "p", "i", "c", "r"
	b, _ = json.Marshal(NovoTicket{DeskID: "m1", Title: "t", PriorityID: &prio, CatalogItemID: &cat, ClientID: &cli, RequesterID: &req})
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m["priorityId"] != "p" || m["catalogItemId"] != "i" || m["clientId"] != "c" || m["requesterId"] != "r" {
		t.Errorf("opcionais não serializados: %s", b)
	}
}

func TestNovoTicketDaConversa_JSON(t *testing.T) {
	b, _ := json.Marshal(NovoTicketDaConversa{DeskID: "m1", Title: "t"})
	// finish vai SEMPRE explícito (false na v1; ver docs/infodesk/07 item C6).
	if string(b) != `{"deskId":"m1","title":"t","finish":false}` {
		t.Errorf("json = %s", b)
	}
	prio, cat := "p1", "i1"
	b, _ = json.Marshal(NovoTicketDaConversa{DeskID: "m1", Title: "t", Description: "d", PriorityID: &prio, CatalogItemID: &cat, Period: "session", Finish: true})
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for k, v := range map[string]any{"deskId": "m1", "title": "t", "description": "d", "priorityId": "p1", "catalogItemId": "i1", "period": "session", "finish": true} {
		if m[k] != v {
			t.Errorf("%s = %v, quer %v (json %s)", k, m[k], v, b)
		}
	}
}

func TestHandoff_JSON(t *testing.T) {
	dep, usr := "d1", "u1"
	b, _ := json.Marshal(Handoff{DepartmentID: &dep, Note: "Resumo"})
	if string(b) != `{"departmentId":"d1","note":"Resumo"}` {
		t.Errorf("json = %s", b)
	}
	b, _ = json.Marshal(Handoff{UserID: &usr, Note: "n"})
	if string(b) != `{"userId":"u1","note":"n"}` {
		t.Errorf("json = %s", b)
	}
	b, _ = json.Marshal(Handoff{})
	if string(b) != `{}` {
		t.Errorf("handoff vazio = %s", b)
	}
}

// eventoMensagemPDF é o envelope de message.received montado a partir dos
// literais do PDF v1 (docs/infodesk/05, 5.3).
const eventoMensagemPDF = `{
  "id": "evt_000000000123",
  "seq": 123,
  "event": "message.received",
  "createdAt": "2026-10-19T12:00:00.000Z",
  "organizationId": "8f0c-org",
  "data": {
    "conversation": { "id": "c1d2", "channel": "WHATSAPP", "status": "PENDING", "subject": null,
                      "botActive": true, "handledBy": "EXTERNAL_BOT",
                      "assignedUserId": null, "departmentId": null, "clientId": null, "requesterId": null,
                      "resourceId": null, "ticketNumber": null,
                      "contact": { "name": "Maria", "phone": "+5531990000000", "email": null, "groupJid": null },
                      "provider": "GUPSHUP", "lastMessageAt": "2026-10-19T12:00:00.000Z", "lastMessagePreview": "Bom dia",
                      "unreadCount": 1, "sessionStartedAt": "2026-10-19T11:59:00.000Z", "createdAt": "2026-10-19T11:59:00.000Z", "updatedAt": "2026-10-19T12:00:00.000Z" },
    "contact": { "name": "Maria", "phone": "+5531990000000", "email": null, "groupJid": null },
    "message": { "id": "m9", "conversationId": "c1d2", "sender": "CLIENT", "senderName": null,
                 "type": "TEXT", "status": null, "body": "Bom dia, minha impressora parou",
                 "internal": false, "deleted": false, "replyToId": null, "createdAt": "2026-10-19T12:00:00.000Z",
                 "attachment": null }
  }
}`

func TestDecodificarEvento_MensagemRecebida(t *testing.T) {
	ev, err := DecodificarEvento([]byte(eventoMensagemPDF))
	if err != nil {
		t.Fatal(err)
	}
	if ev.ID != "evt_000000000123" || ev.Seq != 123 || ev.Event != EventoMensagemRecebida || ev.OrganizationID != "8f0c-org" {
		t.Errorf("evento = %+v", ev)
	}
	if quer := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC); !ev.CreatedAt.Equal(quer) {
		t.Errorf("CreatedAt = %v", ev.CreatedAt)
	}

	c := ev.Data.Conversation
	if c == nil {
		t.Fatal("Data.Conversation nil")
	}
	if c.ID != "c1d2" || c.Channel != CanalWhatsApp || c.Status != "PENDING" || c.HandledBy != ConduzidaPorBotExterno || !c.BotActive {
		t.Errorf("conversation = %+v", c)
	}
	if c.AssignedUserID != "" || c.DepartmentID != "" || c.TicketNumber != 0 {
		t.Errorf("nulos deveriam ficar zerados: %+v", c)
	}
	if quer := time.Date(2026, 10, 19, 11, 59, 0, 0, time.UTC); !c.SessionStartedAt.Equal(quer) {
		t.Errorf("SessionStartedAt = %v", c.SessionStartedAt)
	}
	if c.Contact.Name != "Maria" || c.Contact.Phone != "+5531990000000" || c.Contact.GroupJid != "" {
		t.Errorf("contact = %+v", c.Contact)
	}
	if c.Bruto["provider"] != "GUPSHUP" {
		t.Errorf("Bruto = %v", c.Bruto)
	}

	m := ev.Data.Message
	if m == nil {
		t.Fatal("Data.Message nil")
	}
	if m.ID != "m9" || m.ConversationID != "c1d2" || m.Sender != RemetenteCliente || m.SenderName != "" ||
		m.Type != TipoTexto || m.Body != "Bom dia, minha impressora parou" || m.Internal || m.Attachment != nil || m.CreatedAt.IsZero() {
		t.Errorf("message = %+v", m)
	}
	if !m.DoCliente() || m.TemMidia() {
		t.Errorf("DoCliente=%v TemMidia=%v", m.DoCliente(), m.TemMidia())
	}
	if ev.Data.Contact == nil || ev.Data.Contact.Phone != "+5531990000000" {
		t.Errorf("Data.Contact = %+v", ev.Data.Contact)
	}
	if ev.Data.ConversaID() != "c1d2" {
		t.Errorf("ConversaID = %q", ev.Data.ConversaID())
	}
	if len(ev.Data.Bruto) == 0 {
		t.Error("Data.Bruto vazio")
	}
}

func TestDecodificarEvento_GrupoEMidia(t *testing.T) {
	ev, err := DecodificarEvento([]byte(`{
	  "id":"evt_2","seq":2,"event":"message.received","createdAt":"2026-10-19T12:01:00Z","organizationId":"o",
	  "data":{
	    "conversation":{"id":"g1","channel":"WHATSAPP","handledBy":"EXTERNAL_BOT","botActive":true,
	                    "contact":{"name":"Grupo","phone":"","groupJid":"120363000000@g.us"}},
	    "message":{"id":"m2","sender":"CLIENT","type":"IMAGE","body":"[imagem]","internal":false,
	               "attachment":{"url":"https://exemplo/arq.jpg","mimeType":"image/jpeg"}}
	  }}`))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Data.Conversation.Contact.GroupJid != "120363000000@g.us" {
		t.Errorf("groupJid = %q", ev.Data.Conversation.Contact.GroupJid)
	}
	m := ev.Data.Message
	if m.Attachment == nil || m.Attachment.URL != "https://exemplo/arq.jpg" || m.Attachment.Bruto["mimeType"] != "image/jpeg" {
		t.Errorf("Attachment = %+v", m.Attachment)
	}
	if !m.TemMidia() {
		t.Error("IMAGE com anexo é mídia")
	}
	if ev.Data.ConversaID() != "g1" {
		t.Errorf("ConversaID = %q", ev.Data.ConversaID())
	}
}

func TestMensagem_TemMidia(t *testing.T) {
	casos := map[string]struct {
		json string
		quer bool
	}{
		"textoSimples":        {`{"type":"TEXT","body":"oi","attachment":null}`, false},
		"textoVazioNaoEMidia": {`{"type":"TEXT","body":"","attachment":null}`, false}, // a hipótese antiga (body vazio = mídia) morreu
		"imagem":              {`{"type":"IMAGE","body":"[imagem]"}`, true},
		"arquivo":             {`{"type":"FILE"}`, true},
		"audio":               {`{"type":"AUDIO"}`, true},
		"contato":             {`{"type":"CONTACT"}`, true},
		"textoComAnexo":       {`{"type":"TEXT","body":"olha","attachment":{"url":"u"}}`, true},
		"anexoNaoObjeto":      {`{"type":"TEXT","attachment":"u"}`, true},
		"semType":             {`{"body":"oi"}`, false},
	}
	for nome, cs := range casos {
		t.Run(nome, func(t *testing.T) {
			var m Mensagem
			if err := json.Unmarshal([]byte(cs.json), &m); err != nil {
				t.Fatal(err)
			}
			if m.TemMidia() != cs.quer {
				t.Errorf("TemMidia = %v, quer %v (%+v)", m.TemMidia(), cs.quer, m)
			}
		})
	}
}

func TestMensagem_DoCliente(t *testing.T) {
	for sender, quer := range map[string]bool{"CLIENT": true, "AGENT": false, "SYSTEM": false, "client": false, " CLIENT ": false, "": false} {
		if got := (Mensagem{Sender: sender}).DoCliente(); got != quer {
			t.Errorf("DoCliente(%q) = %v, quer %v", sender, got, quer)
		}
	}
}

func TestDecodificarEvento_OutrosEventos(t *testing.T) {
	t.Run("assignedHandoffFinished", func(t *testing.T) {
		for _, ev := range []string{EventoConversaAtribuida, EventoConversaHandoff, EventoConversaEncerrada, EventoConversaIniciada, EventoConversaDepartamento} {
			raw := `{"id":"evt_9","seq":9,"event":"` + ev + `","createdAt":"2026-10-19T12:00:00Z","organizationId":"o",
			  "data":{"conversation":{"id":"c1","channel":"WHATSAPP","handledBy":"AGENT","botActive":false,"assignedUserId":"u1","ticketNumber":4521,
			          "contact":{"phone":"+5531990000000"}},
			          "contact":{"phone":"+5531990000000"},"note":"resumo","reason":"bot","previousDepartmentId":"d0","userId":"u1"}}`
			e, err := DecodificarEvento([]byte(raw))
			if err != nil {
				t.Fatalf("%s: %v", ev, err)
			}
			c := e.Data.Conversation
			if c == nil || c.HandledBy != ConduzidaPorAtendente || c.BotActive || c.AssignedUserID != "u1" || c.TicketNumber != 4521 {
				t.Errorf("%s: conversation = %+v", ev, c)
			}
			if e.Data.Message != nil {
				t.Errorf("%s: Message deveria ser nil", ev)
			}
			// Os extras ficam no Bruto (docs/infodesk/07 item W7).
			var extras map[string]any
			if err := json.Unmarshal(e.Data.Bruto, &extras); err != nil || extras["note"] != "resumo" || extras["reason"] != "bot" {
				t.Errorf("%s: extras = %v, err = %v", ev, extras, err)
			}
		}
	})
	t.Run("ticket", func(t *testing.T) {
		e, err := DecodificarEvento([]byte(`{"id":"evt_5","seq":5,"event":"ticket.closed","data":{"ticket":{"number":4521,"status":{"isClose":true}}}}`))
		if err != nil {
			t.Fatal(err)
		}
		if e.Data.Conversation != nil || e.Data.Message != nil {
			t.Errorf("data = %+v", e.Data)
		}
		var d struct {
			Ticket Ticket `json:"ticket"`
		}
		if err := json.Unmarshal(e.Data.Bruto, &d); err != nil || d.Ticket.Number != 4521 || !d.Ticket.Status.IsClose {
			t.Errorf("ticket = %+v, err = %v", d.Ticket, err)
		}
	})
	t.Run("ping", func(t *testing.T) {
		for _, data := range []string{`"qualquer coisa"`, `{}`, `null`, `[1]`, `42`} {
			e, err := DecodificarEvento([]byte(`{"id":"evt_1","seq":1,"event":"ping","data":` + data + `}`))
			if err != nil || e.Event != EventoPing || e.Data.Conversation != nil || e.Data.Message != nil {
				t.Errorf("data=%s: e = %+v, err = %v", data, e, err)
			}
		}
		e, err := DecodificarEvento([]byte(`{"id":"evt_1","event":"ping"}`))
		if err != nil || len(e.Data.Bruto) != 0 {
			t.Errorf("ping sem data: e = %+v, err = %v", e, err)
		}
	})
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
		if err != nil || !ev.CreatedAt.IsZero() || ev.Data.Bruto != nil || ev.Seq != 0 {
			t.Errorf("ev = %+v, err = %v", ev, err)
		}
	})
	t.Run("createdAtSemFracaoComOffset", func(t *testing.T) {
		ev, _ := DecodificarEvento([]byte(`{"id":"e","event":"x","createdAt":"2026-06-29T09:00:00-03:00"}`))
		if !ev.CreatedAt.Equal(time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)) {
			t.Errorf("CreatedAt = %v", ev.CreatedAt)
		}
	})
	t.Run("conversationIlegivelViraNil", func(t *testing.T) {
		ev, err := DecodificarEvento([]byte(`{"id":"e","event":"message.received","data":{"conversation":{"botActive":"talvez"},"message":{"id":"m","sender":"CLIENT","body":"oi"}}}`))
		if err != nil {
			t.Fatal(err)
		}
		if ev.Data.Conversation != nil {
			t.Errorf("conversation ilegível deveria ser nil: %+v", ev.Data.Conversation)
		}
		if ev.Data.Message == nil || ev.Data.Message.Body != "oi" {
			t.Errorf("message = %+v", ev.Data.Message)
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
	t.Run("dataNaoAliasaOBufferDeEntrada", func(t *testing.T) {
		buf := []byte(`{"id":"e","event":"ping","data":{"a":1}}`)
		ev, _ := DecodificarEvento(buf)
		for i := range buf {
			buf[i] = 'x'
		}
		if string(ev.Data.Bruto) != `{"a":1}` {
			t.Errorf("Data.Bruto foi sobrescrito: %q", ev.Data.Bruto)
		}
	})
}

func TestDadosEvento_ConversaID(t *testing.T) {
	if (DadosEvento{}).ConversaID() != "" {
		t.Error("evento sem conversa deve devolver vazio")
	}
	d := DadosEvento{Message: &Mensagem{ConversationID: "cm"}}
	if d.ConversaID() != "cm" {
		t.Errorf("ConversaID = %q", d.ConversaID())
	}
	d.Conversation = &Conversa{ID: "cc"}
	if d.ConversaID() != "cc" {
		t.Errorf("ConversaID = %q", d.ConversaID())
	}
}

func TestConversa_Decode(t *testing.T) {
	t.Run("ticketNumber", func(t *testing.T) {
		for corpo, quer := range map[string]int{`{"ticketNumber":4521}`: 4521, `{"ticketNumber":null}`: 0, `{}`: 0, `{"ticketNumber":"77"}`: 77, `{"ticketNumber":"x"}`: 0} {
			var c Conversa
			if err := json.Unmarshal([]byte(corpo), &c); err != nil || c.TicketNumber != quer {
				t.Errorf("%s: TicketNumber = %d, err = %v", corpo, c.TicketNumber, err)
			}
		}
	})
	t.Run("naoObjeto", func(t *testing.T) {
		for _, corpo := range []string{`[]`, `"x"`, `null`, `{`} {
			var c Conversa
			if err := json.Unmarshal([]byte(corpo), &c); err == nil {
				t.Errorf("%s: esperado erro", corpo)
			}
		}
	})
	t.Run("sessionStartedAtIlegivel", func(t *testing.T) {
		var c Conversa
		if err := json.Unmarshal([]byte(`{"id":"c","sessionStartedAt":"ontem"}`), &c); err != nil || !c.SessionStartedAt.IsZero() {
			t.Errorf("c = %+v, err = %v", c, err)
		}
	})
	t.Run("conversaCompleta", func(t *testing.T) {
		var c Conversa
		corpo := `{"id":"c1","handledBy":"AGENT","botActive":false,"assignedUserId":"u1","departmentId":"d1",
		  "whatsappWindow":{"open":true},"recentMessages":[{"id":"m1","sender":"CLIENT","body":"oi"}]}`
		if err := json.Unmarshal([]byte(corpo), &c); err != nil {
			t.Fatal(err)
		}
		if c.HandledBy != ConduzidaPorAtendente || c.AssignedUserID != "u1" || c.DepartmentID != "d1" {
			t.Errorf("c = %+v", c)
		}
		if _, ok := c.Bruto["recentMessages"].([]any); !ok {
			t.Errorf("recentMessages deveria estar em Bruto: %v", c.Bruto)
		}
	})
}

func TestMesa_Decode(t *testing.T) {
	t.Run("completa", func(t *testing.T) {
		var m Mesa
		corpo := `{"id":"d1","name":"Suporte","active":true,
		  "statuses":[{"id":"s1","name":"Aberto","isOpen":true,"isClose":false,"isCanceled":false},{"id":"s2","name":"Fechado","isOpen":false,"isClose":true,"isCanceled":false}],
		  "stages":[{"id":"e1","name":"Triagem"}],
		  "priorities":[{"id":"p1","name":"Alta","level":3},{"id":"p2","name":"Baixa"}],
		  "catalogItems":[{"id":"i1","name":"Impressora","extra":1}]}`
		if err := json.Unmarshal([]byte(corpo), &m); err != nil {
			t.Fatal(err)
		}
		if m.ID != "d1" || m.Nome != "Suporte" || len(m.Statuses) != 2 || !m.Statuses[1].IsClose || len(m.Stages) != 1 ||
			len(m.Priorities) != 2 || m.Priorities[0] != (Referencia{ID: "p1", Nome: "Alta"}) || len(m.CatalogItems) != 1 || m.CatalogItems[0].Nome != "Impressora" {
			t.Errorf("mesa = %+v", m)
		}
		if m.Bruto["active"] != true {
			t.Errorf("Bruto = %v", m.Bruto)
		}
	})
	t.Run("listaComFormatoInesperadoViraVazia", func(t *testing.T) {
		var m Mesa
		if err := json.Unmarshal([]byte(`{"id":"d1","name":"S","priorities":"nenhuma","statuses":null,"catalogItems":{"a":1}}`), &m); err != nil {
			t.Fatal(err)
		}
		if m.ID != "d1" || len(m.Priorities) != 0 || len(m.Statuses) != 0 || len(m.CatalogItems) != 0 {
			t.Errorf("mesa = %+v", m)
		}
	})
	t.Run("naoObjeto", func(t *testing.T) {
		var m Mesa
		if err := json.Unmarshal([]byte(`[]`), &m); err == nil {
			t.Error("esperado erro")
		}
	})
}

func TestLookup_Decode(t *testing.T) {
	corpo := `{
	  "phone": "+5531990000000",
	  "requesters": [{ "id": "r1", "name": "Maria Souza", "email": "maria@acme.com.br", "phone": "+5531990000000",
	                   "clientId": "k1", "client": { "id": "k1", "name": "ACME Ltda" } }],
	  "clientContacts": [],
	  "clientIds": ["k1"],
	  "openTickets": [ { "number": 4510, "title": "VPN não conecta" } ],
	  "conversation": { "id": "c1d2", "handledBy": "EXTERNAL_BOT" }
	}`
	var l Lookup
	if err := json.Unmarshal([]byte(corpo), &l); err != nil {
		t.Fatal(err)
	}
	if l.Phone != "+5531990000000" || len(l.Requesters) != 1 || l.Requesters[0].ID != "r1" || l.Requesters[0].ClientID != "k1" ||
		l.Requesters[0].Client == nil || l.Requesters[0].Client.Nome != "ACME Ltda" {
		t.Errorf("lookup = %+v", l)
	}
	if len(l.ClientIDs) != 1 || l.ClientIDs[0] != "k1" {
		t.Errorf("ClientIDs = %v", l.ClientIDs)
	}
	if len(l.OpenTickets) != 1 || l.OpenTickets[0].Number != 4510 || l.OpenTickets[0].Bruto["title"] != "VPN não conecta" {
		t.Errorf("OpenTickets = %+v", l.OpenTickets)
	}
	if l.Conversation == nil || l.Conversation.ID != "c1d2" || l.Conversation.HandledBy != ConduzidaPorBotExterno {
		t.Errorf("Conversation = %+v", l.Conversation)
	}
	if _, ok := l.Bruto["clientContacts"]; !ok {
		t.Error("clientContacts deveria ficar no Bruto (ignorado, 07 N2)")
	}

	var vazio Lookup
	if err := json.Unmarshal([]byte(`{"phone":"+5531","requesters":[],"clientIds":[],"openTickets":[],"conversation":null}`), &vazio); err != nil {
		t.Fatal(err)
	}
	if len(vazio.Requesters) != 0 || vazio.Conversation != nil {
		t.Errorf("vazio = %+v", vazio)
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
