package infodesk

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Testes dos métodos acrescentados pela API v1 de 2026-10-19 (nota de quebra
// da E1.2): Me, Lookup, Handoff, Eventos, Mesa, Catalogo,
// CriarTicketDaConversa, ListarTickets, VincularSolicitante, NotaInterna e a
// paginação das listas de referência. Os helpers (novoServidor, responderJSON,
// ...) estão em client_test.go.

func TestClient_Me(t *testing.T) {
	s := novoServidor(t, responderJSON(http.StatusOK, `{"organizationId":"8f0c","keyId":"k1","name":"Sofia",
	  "scopes":["tickets:read","tickets:write","conversations:read","conversations:write","contacts:read","events:read"],"handlesConversations":true}`))
	me, err := s.cliente().Me(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if me.OrganizationID != "8f0c" || me.KeyID != "k1" || me.Name != "Sofia" || !me.HandlesConversations || len(me.Scopes) != 6 || me.Scopes[5] != "events:read" {
		t.Errorf("me = %+v", me)
	}
	if r := s.ultima.Load(); r.metodo != http.MethodGet || r.caminho != PrefixoRotas+"/me" {
		t.Errorf("rota: %s %s", r.metodo, r.caminho)
	}
}

func TestClient_Lookup(t *testing.T) {
	s := novoServidor(t, responderJSON(http.StatusOK, `{
	  "phone": "+5531990000000",
	  "requesters": [{ "id": "r1", "name": "Maria Souza", "email": "maria@acme.com.br", "phone": "+5531990000000",
	                   "clientId": "k1", "client": { "id": "k1", "name": "ACME Ltda" } }],
	  "clientContacts": [], "clientIds": ["k1"],
	  "openTickets": [ { "number": 4510, "title": "VPN não conecta" } ],
	  "conversation": { "id": "c1d2", "handledBy": "EXTERNAL_BOT", "botActive": true }
	}`))
	l, err := s.cliente().Lookup(context.Background(), "+5531990000000")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Requesters) != 1 || l.Requesters[0].Client.Nome != "ACME Ltda" || l.ClientIDs[0] != "k1" ||
		len(l.OpenTickets) != 1 || l.OpenTickets[0].Number != 4510 || l.Conversation == nil || !l.Conversation.BotActive {
		t.Errorf("lookup = %+v", l)
	}
	r := s.ultima.Load()
	if r.caminho != PrefixoRotas+"/contacts/lookup" {
		t.Errorf("caminho = %q", r.caminho)
	}
	// O "+" do E.164 vai escapado na query (senão viraria espaço).
	if r.query != "phone=%2B5531990000000" {
		t.Errorf("query = %q", r.query)
	}
}

func TestClient_Lookup_TelefoneNaoVazaNoErro(t *testing.T) {
	s := novoServidor(t, responderJSON(http.StatusForbidden, `{"statusCode":403,"code":"FORBIDDEN","message":"Escopo insuficiente. Necessário: contacts:read."}`))
	_, err := s.cliente().Lookup(context.Background(), "+5531990000000")
	var he *HTTPError
	if !errors.As(err, &he) || he.Code != CodeForbidden || Transitorio(err) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "5531990000000") {
		t.Errorf("telefone vazou no erro: %q", err.Error())
	}
}

func TestClient_Eventos(t *testing.T) {
	corpo := `{"data":[` +
		strings.ReplaceAll(strings.ReplaceAll(eventoMensagemPDF, "\n", ""), "\t", "") + `,` +
		`{"id":"evt_000000000124","seq":124,"event":"conversation.finished","createdAt":"2026-10-19T12:05:00.000Z","organizationId":"8f0c-org",
		  "data":{"conversation":{"id":"c1d2","handledBy":"EXTERNAL_BOT","botActive":true},"reason":"inactivity"}}` +
		`],"nextAfter":124,"hasMore":true}`
	s := novoServidor(t, responderJSON(http.StatusOK, corpo))
	feed, err := s.cliente().Eventos(context.Background(), 122, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Data) != 2 || feed.NextAfter != 124 || !feed.HasMore {
		t.Fatalf("feed = %+v", feed)
	}
	if e := feed.Data[0]; e.ID != "evt_000000000123" || e.Seq != 123 || e.Event != EventoMensagemRecebida || e.Data.Message == nil || e.Data.Message.Body == "" {
		t.Errorf("evento[0] = %+v", e)
	}
	if e := feed.Data[1]; e.Seq != 124 || e.Event != EventoConversaEncerrada || e.Data.Conversation == nil || e.Data.Conversation.ID != "c1d2" {
		t.Errorf("evento[1] = %+v", e)
	}
	r := s.ultima.Load()
	if r.caminho != PrefixoRotas+"/events" || r.query != "after=122&limit=500" {
		t.Errorf("rota: %s ?%s", r.caminho, r.query)
	}
}

func TestClient_Eventos_QueryEFeedVazio(t *testing.T) {
	s := novoServidor(t, responderJSON(http.StatusOK, `{"data":[],"nextAfter":0,"hasMore":false}`))
	feed, err := s.cliente().Eventos(context.Background(), 0, 0)
	if err != nil || len(feed.Data) != 0 || feed.HasMore || feed.NextAfter != 0 {
		t.Fatalf("feed = %+v, err = %v", feed, err)
	}
	// after sempre vai (0 é um cursor válido); limit 0 é omitido (padrão da API).
	if s.ultima.Load().query != "after=0" {
		t.Errorf("query = %q", s.ultima.Load().query)
	}
}

func TestClient_Eventos_Erros(t *testing.T) {
	t.Run("403SemEscopoEventsRead", func(t *testing.T) {
		s := novoServidor(t, responderJSON(http.StatusForbidden, `{"statusCode":403,"code":"FORBIDDEN","message":"Escopo insuficiente. Necessário: events:read."}`))
		_, err := s.cliente().Eventos(context.Background(), 0, 500)
		var he *HTTPError
		if !errors.As(err, &he) || he.Code != CodeForbidden || he.Transitorio() {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("eventoMalformadoNoFeedNaoDerrubaAPagina", func(t *testing.T) {
		// Um item sem id/event ainda decodifica (o receptor valida com
		// DecodificarEvento/ID); um item que nem é objeto derruba a resposta.
		s := novoServidor(t, responderJSON(http.StatusOK, `{"data":[{"seq":1}],"nextAfter":1,"hasMore":false}`))
		feed, err := s.cliente().Eventos(context.Background(), 0, 10)
		if err != nil || len(feed.Data) != 1 || feed.Data[0].ID != "" {
			t.Fatalf("feed = %+v, err = %v", feed, err)
		}
		s2 := novoServidor(t, responderJSON(http.StatusOK, `{"data":["x"],"nextAfter":1,"hasMore":false}`))
		_, err = s2.cliente().Eventos(context.Background(), 0, 10)
		var er *ErroResposta
		if !errors.As(err, &er) {
			t.Errorf("esperado *ErroResposta, veio %v", err)
		}
	})
}

func TestClient_Mesa(t *testing.T) {
	s := novoServidor(t, responderJSON(http.StatusOK, `{"id":"m1","name":"Suporte",
	  "statuses":[{"id":"s1","name":"Aberto","isOpen":true,"isClose":false,"isCanceled":false}],
	  "stages":[{"id":"e1","name":"Triagem"}],
	  "priorities":[{"id":"p1","name":"Alta"},{"id":"p2","name":"Baixa"}],
	  "catalogItems":[{"id":"i1","name":"Impressora"}]}`))
	m, err := s.cliente().Mesa(context.Background(), "m1")
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "m1" || m.Nome != "Suporte" || len(m.Statuses) != 1 || !m.Statuses[0].IsOpen || len(m.Stages) != 1 ||
		len(m.Priorities) != 2 || m.Priorities[1].ID != "p2" || len(m.CatalogItems) != 1 {
		t.Errorf("mesa = %+v", m)
	}
	if s.ultima.Load().caminho != PrefixoRotas+"/desks/m1" {
		t.Errorf("caminho = %q", s.ultima.Load().caminho)
	}
}

func TestClient_Catalogo(t *testing.T) {
	s := novoServidor(t, responderJSON(http.StatusOK, `{"data":[{"id":"i1","name":"Impressora"},{"id":"i2","name":"VPN"}],"nextCursor":null}`))
	itens, err := s.cliente().Catalogo(context.Background(), "m1")
	if err != nil || len(itens) != 2 || itens[1].Nome != "VPN" {
		t.Fatalf("itens = %+v, err = %v", itens, err)
	}
	r := s.ultima.Load()
	if r.caminho != PrefixoRotas+"/catalog" || r.query != "deskId=m1" {
		t.Errorf("rota: %s ?%s", r.caminho, r.query)
	}
}

func TestClient_CacheMesaECatalogo(t *testing.T) {
	agora := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	relogio := &agora
	s := novoServidor(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case PrefixoRotas + "/desks/m1":
			_, _ = io.WriteString(w, `{"id":"m1","name":"Suporte","priorities":[{"id":"p1","name":"Alta"}]}`)
		case PrefixoRotas + "/desks/m2":
			_, _ = io.WriteString(w, `{"id":"m2","name":"Financeiro","priorities":[]}`)
		case PrefixoRotas + "/catalog":
			_, _ = io.WriteString(w, `[{"id":"i-`+r.URL.Query().Get("deskId")+`","name":"Item"}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	c := s.cliente(func(o *Options) { o.Now = func() time.Time { return *relogio } })
	ctx := context.Background()

	m1, _ := c.Mesa(ctx, "m1")
	m2, _ := c.Mesa(ctx, "m2")
	cat1, _ := c.Catalogo(ctx, "m1")
	cat2, _ := c.Catalogo(ctx, "m2")
	if m1.Nome != "Suporte" || m2.Nome != "Financeiro" || cat1[0].ID != "i-m1" || cat2[0].ID != "i-m2" {
		t.Fatalf("chaves de cache misturadas: %+v %+v %+v %+v", m1, m2, cat1, cat2)
	}
	if n := s.chamadas.Load(); n != 4 {
		t.Fatalf("primeira carga: %d chamadas, esperado 4", n)
	}

	// Cache serve cópias independentes (inclusive listas internas da mesa).
	m1.Priorities[0].Nome = "alterado pelo chamador"
	m1.Bruto["name"] = "alterado"
	cat1[0].Nome = "alterado"
	m1b, _ := c.Mesa(ctx, "m1")
	cat1b, _ := c.Catalogo(ctx, "m1")
	if n := s.chamadas.Load(); n != 4 {
		t.Fatalf("cache não usado: %d chamadas", n)
	}
	if m1b.Priorities[0].Nome != "Alta" || m1b.Bruto["name"] != "Suporte" || cat1b[0].Nome != "Item" {
		t.Errorf("cache foi alterado por quem chamou: %+v %+v", m1b, cat1b)
	}

	*relogio = agora.Add(11 * time.Minute)
	_, _ = c.Mesa(ctx, "m1")
	_, _ = c.Catalogo(ctx, "m1")
	if n := s.chamadas.Load(); n != 6 {
		t.Fatalf("TTL não expirou: %d chamadas", n)
	}
	c.InvalidarCache()
	_, _ = c.Mesa(ctx, "m1")
	_, _ = c.Catalogo(ctx, "m1")
	if n := s.chamadas.Load(); n != 8 {
		t.Fatalf("InvalidarCache não limpou: %d chamadas", n)
	}
}

func TestClient_ListasDeReferencia_ArrayPuroOuEnvelope(t *testing.T) {
	// ver docs/infodesk/07 item D3: as duas formas valem em todas as listas.
	formas := map[string]string{
		"arrayPuro":    `[{"id":"x1","name":"Um","username":"um","email":"um@x.com"},{"id":"x2","name":"Dois","username":"dois","email":"dois@x.com"}]`,
		"envelope":     `{"data":[{"id":"x1","name":"Um","username":"um","email":"um@x.com"},{"id":"x2","name":"Dois","username":"dois","email":"dois@x.com"}],"nextCursor":null}`,
		"envelopeFim":  `{"data":[{"id":"x1","name":"Um","username":"um","email":"um@x.com"},{"id":"x2","name":"Dois","username":"dois","email":"dois@x.com"}],"nextCursor":""}`,
		"envelopeSemC": `{"data":[{"id":"x1","name":"Um","username":"um","email":"um@x.com"},{"id":"x2","name":"Dois","username":"dois","email":"dois@x.com"}]}`,
	}
	for forma, corpo := range formas {
		t.Run(forma, func(t *testing.T) {
			s := novoServidor(t, responderJSON(http.StatusOK, corpo))
			c := s.cliente()
			ctx := context.Background()
			if l, err := c.Mesas(ctx); err != nil || len(l) != 2 || l[1].Nome != "Dois" {
				t.Errorf("Mesas = %+v, err = %v", l, err)
			}
			if l, err := c.Departamentos(ctx); err != nil || len(l) != 2 || l[0].ID != "x1" {
				t.Errorf("Departamentos = %+v, err = %v", l, err)
			}
			if l, err := c.Atendentes(ctx); err != nil || len(l) != 2 || l[1].Username != "dois" || l[1].Email != "dois@x.com" {
				t.Errorf("Atendentes = %+v, err = %v", l, err)
			}
			if l, err := c.Catalogo(ctx, "m1"); err != nil || len(l) != 2 {
				t.Errorf("Catalogo = %+v, err = %v", l, err)
			}
		})
	}
}

func TestClient_ListasDeReferencia_SegueNextCursor(t *testing.T) {
	var visitas atomic.Int32
	s := novoServidor(t, func(w http.ResponseWriter, r *http.Request) {
		visitas.Add(1)
		switch r.URL.Query().Get("cursor") {
		case "":
			_, _ = io.WriteString(w, `{"data":[{"id":"1","name":"A"},{"id":"2","name":"B"}],"nextCursor":"p2"}`)
		case "p2":
			_, _ = io.WriteString(w, `{"data":[{"id":"3","name":"C"}],"nextCursor":"p3"}`)
		case "p3":
			_, _ = io.WriteString(w, `{"data":[{"id":"4","name":"D"}],"nextCursor":null}`)
		}
	})
	deps, err := s.cliente().Departamentos(context.Background())
	if err != nil || len(deps) != 4 || deps[3].Nome != "D" {
		t.Fatalf("deps = %+v, err = %v", deps, err)
	}
	if visitas.Load() != 3 {
		t.Errorf("páginas lidas = %d", visitas.Load())
	}
}

func TestClient_ListasDeReferencia_CursorQueNaoAcabaPara(t *testing.T) {
	t.Run("cursorRepetido", func(t *testing.T) {
		s := novoServidor(t, responderJSON(http.StatusOK, `{"data":[{"id":"1","name":"A"}],"nextCursor":"sempre"}`))
		if _, err := s.cliente().Mesas(context.Background()); err != nil {
			t.Fatal(err)
		}
		if n := s.chamadas.Load(); n != 2 {
			t.Errorf("chamadas = %d (primeira + a que repete o cursor)", n)
		}
	})
	t.Run("cursorSempreNovo", func(t *testing.T) {
		var n atomic.Int32
		s := novoServidor(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"data":[{"id":"1","name":"A"}],"nextCursor":"c`+string(rune('a'+n.Add(1)%26))+strings.Repeat("x", int(n.Load()))+`"}`)
		})
		_, err := s.cliente().Mesas(context.Background())
		var er *ErroResposta
		if !errors.As(err, &er) {
			t.Fatalf("esperado *ErroResposta, veio %v", err)
		}
		if got := s.chamadas.Load(); got != maxPaginasReferencia {
			t.Errorf("chamadas = %d, teto = %d", got, maxPaginasReferencia)
		}
	})
	t.Run("falhaNaSegundaPaginaNaoCacheia", func(t *testing.T) {
		var falhar atomic.Bool
		falhar.Store(true)
		s := novoServidor(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("cursor") == "" {
				_, _ = io.WriteString(w, `{"data":[{"id":"1","name":"A"}],"nextCursor":"p2"}`)
				return
			}
			if falhar.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = io.WriteString(w, `{"data":[{"id":"2","name":"B"}],"nextCursor":null}`)
		})
		c := s.cliente()
		if _, err := c.Mesas(context.Background()); !Transitorio(err) {
			t.Fatalf("esperado transitório: %v", err)
		}
		falhar.Store(false)
		if l, err := c.Mesas(context.Background()); err != nil || len(l) != 2 {
			t.Fatalf("l = %+v, err = %v", l, err)
		}
	})
}

func TestClient_CriarTicketDaConversa(t *testing.T) {
	t.Run("corpoERespostaDoPDF", func(t *testing.T) {
		s := novoServidor(t, responderJSON(http.StatusCreated, ticketPDF))
		prio, cat := "p1", "i1"
		tk, err := s.cliente().CriarTicketDaConversa(context.Background(), "c1d2", NovoTicketDaConversa{
			DeskID: "d1", Title: "Impressora do financeiro não imprime",
			Description: "Resumo da triagem: HP M428, erro 49.", PriorityID: &prio, CatalogItemID: &cat, Period: "session",
		})
		if err != nil {
			t.Fatal(err)
		}
		if tk.Number != 4521 || tk.ConversationID != "c1d2" || !tk.Status.IsOpen || tk.Requester == nil || tk.Desk.Nome != "Suporte" {
			t.Errorf("tk = %+v", tk)
		}
		r := s.ultima.Load()
		if r.metodo != http.MethodPost || r.caminho != PrefixoRotas+"/conversations/c1d2/ticket" {
			t.Errorf("rota: %s %s", r.metodo, r.caminho)
		}
		var enviado map[string]any
		if err := json.Unmarshal([]byte(r.corpo), &enviado); err != nil {
			t.Fatal(err)
		}
		quer := map[string]any{"deskId": "d1", "title": "Impressora do financeiro não imprime", "description": "Resumo da triagem: HP M428, erro 49.",
			"priorityId": "p1", "catalogItemId": "i1", "period": "session", "finish": false}
		if len(enviado) != len(quer) {
			t.Errorf("corpo = %v", enviado)
		}
		for k, v := range quer {
			if enviado[k] != v {
				t.Errorf("corpo[%s] = %v, quer %v", k, enviado[k], v)
			}
		}
	})
	t.Run("respostaEmbrulhadaEmTicket", func(t *testing.T) {
		// ver docs/infodesk/07 item T11: o formato da resposta não é garantido.
		s := novoServidor(t, responderJSON(http.StatusOK, `{"ticket":{"number":4521,"conversationId":"c1"}}`))
		tk, err := s.cliente().CriarTicketDaConversa(context.Background(), "c1", NovoTicketDaConversa{DeskID: "d1", Title: "t"})
		if err != nil || tk.Number != 4521 || tk.ConversationID != "c1" {
			t.Fatalf("tk = %+v, err = %v", tk, err)
		}
	})
	t.Run("semNumero", func(t *testing.T) {
		s := novoServidor(t, responderJSON(http.StatusCreated, `{"id":"abc"}`))
		tk, err := s.cliente().CriarTicketDaConversa(context.Background(), "c1", NovoTicketDaConversa{DeskID: "d1", Title: "t"})
		if !errors.Is(err, ErrTicketSemNumero) || tk.Bruto["id"] != "abc" || Transitorio(err) {
			t.Fatalf("tk = %+v, err = %v", tk, err)
		}
	})
	t.Run("camposObrigatoriosELocais", func(t *testing.T) {
		s := novoServidor(t, responderJSON(http.StatusOK, `{"number":1}`))
		c := s.cliente()
		ctx := context.Background()
		for _, nt := range []NovoTicketDaConversa{{Title: "t"}, {DeskID: "d"}, {DeskID: " ", Title: " "}} {
			if _, err := c.CriarTicketDaConversa(ctx, "c1", nt); !errors.Is(err, ErrCampoObrigatorio) {
				t.Errorf("%+v: esperado ErrCampoObrigatorio, veio %v", nt, err)
			}
		}
		if _, err := c.CriarTicketDaConversa(ctx, " ", NovoTicketDaConversa{DeskID: "d", Title: "t"}); !errors.Is(err, ErrIDVazio) {
			t.Errorf("conversa vazia: %v", err)
		}
		if s.chamadas.Load() != 0 {
			t.Error("validação local não deve bater no servidor")
		}
	})
	t.Run("conflictEDefinitivo", func(t *testing.T) {
		s := novoServidor(t, responderJSON(http.StatusConflict, `{"statusCode":409,"code":"CONFLICT","message":"conversa já tem chamado"}`))
		_, err := s.cliente().CriarTicketDaConversa(context.Background(), "c1", NovoTicketDaConversa{DeskID: "d1", Title: "t"})
		var he *HTTPError
		if !errors.As(err, &he) || he.Code != CodeConflict || Transitorio(err) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestClient_CriarTicketDaConversa_NaoRepeteOPOST(t *testing.T) {
	t.Run("500", func(t *testing.T) {
		s := novoServidor(t, responderJSON(http.StatusInternalServerError, `{"statusCode":500,"code":"INTERNAL_ERROR","message":"boom"}`))
		_, err := s.cliente().CriarTicketDaConversa(context.Background(), "c1", NovoTicketDaConversa{DeskID: "d1", Title: "t"})
		if !Transitorio(err) {
			t.Fatalf("esperado transitório: %v", err)
		}
		if n := s.chamadas.Load(); n != 1 {
			t.Fatalf("POST enviado %d vezes; deve ser exatamente 1", n)
		}
	})
	t.Run("429ComRetryAfter", func(t *testing.T) {
		s := novoServidor(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
		})
		_, err := s.cliente().CriarTicketDaConversa(context.Background(), "c1", NovoTicketDaConversa{DeskID: "d1", Title: "t"})
		var he *HTTPError
		if !errors.As(err, &he) || he.RetryAfter != time.Second {
			t.Fatalf("err = %v", err)
		}
		if n := s.chamadas.Load(); n != 1 {
			t.Fatalf("POST enviado %d vezes; deve ser exatamente 1", n)
		}
	})
	t.Run("conexaoDerrubada", func(t *testing.T) {
		s := novoServidor(t, func(w http.ResponseWriter, _ *http.Request) {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Skip("sem hijack")
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			_ = conn.Close()
		})
		_, err := s.cliente().CriarTicketDaConversa(context.Background(), "c1", NovoTicketDaConversa{DeskID: "d1", Title: "t"})
		var er *ErroRede
		if !errors.As(err, &er) {
			t.Fatalf("esperado *ErroRede, veio %T: %v", err, err)
		}
		if n := s.chamadas.Load(); n != 1 {
			t.Fatalf("POST enviado %d vezes; deve ser exatamente 1", n)
		}
	})
}

func TestClient_ListarTickets(t *testing.T) {
	t.Run("filtrosEPagina", func(t *testing.T) {
		s := novoServidor(t, responderJSON(http.StatusOK, `{"data":[{"number":4510,"status":{"isOpen":true},"conversationId":"c1"},{"number":4521,"conversationId":"c2"}],"nextCursor":"cur2"}`))
		pag, err := s.cliente().ListarTickets(context.Background(), FiltroTickets{Phone: "+5531990000000", Status: "open", DeskID: "d1", Limit: 50, Cursor: "cur1"})
		if err != nil || len(pag.Data) != 2 || pag.NextCursor != "cur2" {
			t.Fatalf("pag = %+v, err = %v", pag, err)
		}
		if pag.Data[0].Number != 4510 || !pag.Data[0].Status.IsOpen || pag.Data[1].ConversationID != "c2" {
			t.Errorf("tickets = %+v", pag.Data)
		}
		r := s.ultima.Load()
		if r.caminho != PrefixoRotas+"/tickets" {
			t.Errorf("caminho = %q", r.caminho)
		}
		q, _ := url.ParseQuery(r.query)
		for k, v := range map[string]string{"phone": "+5531990000000", "status": "open", "deskId": "d1", "limit": "50", "cursor": "cur1"} {
			if q.Get(k) != v {
				t.Errorf("query[%s] = %q, quer %q", k, q.Get(k), v)
			}
		}
		if !strings.Contains(r.query, "phone=%2B5531990000000") {
			t.Errorf("o + do telefone precisa ir escapado: %q", r.query)
		}
	})
	t.Run("semFiltrosEUltimaPagina", func(t *testing.T) {
		s := novoServidor(t, responderJSON(http.StatusOK, `{"data":[],"nextCursor":null}`))
		pag, err := s.cliente().ListarTickets(context.Background(), FiltroTickets{})
		if err != nil || len(pag.Data) != 0 || pag.NextCursor != "" || s.ultima.Load().query != "" {
			t.Fatalf("pag = %+v, err = %v, query = %q", pag, err, s.ultima.Load().query)
		}
	})
	t.Run("naoRepete", func(t *testing.T) {
		s := novoServidor(t, responderJSON(http.StatusServiceUnavailable, ``))
		_, err := s.cliente().ListarTickets(context.Background(), FiltroTickets{Phone: "+5531990000000"})
		if !Transitorio(err) || s.chamadas.Load() != 1 {
			t.Errorf("err = %v, chamadas = %d (o cliente não repete; quem reconcilia repete)", err, s.chamadas.Load())
		}
	})
}

func TestClient_NovasValidacoesLocais(t *testing.T) {
	s := novoServidor(t, responderJSON(http.StatusOK, `{}`))
	c := s.cliente()
	ctx := context.Background()
	casos := map[string]struct {
		err  error
		quer error
	}{
		"handoffSemConversa":      {c.Handoff(ctx, " ", Handoff{Note: "n"}), ErrIDVazio},
		"vincularSemConversa":     {c.VincularSolicitante(ctx, "", "r1"), ErrIDVazio},
		"vincularSemSolicitante":  {c.VincularSolicitante(ctx, "c1", " "), ErrIDVazio},
		"notaSemNumero":           {c.NotaInterna(ctx, 0, "x"), ErrNumeroInvalido},
		"notaNumeroNegativo":      {c.NotaInterna(ctx, -4, "x"), ErrNumeroInvalido},
		"notaVazia":               {c.NotaInterna(ctx, 1, "  "), ErrCorpoVazio},
		"responderNumeroNegativo": {c.ResponderTicket(ctx, -1, "x"), ErrNumeroInvalido},
	}
	if _, err := c.Lookup(ctx, "  "); !errors.Is(err, ErrIDVazio) {
		t.Errorf("Lookup vazio: %v", err)
	}
	if _, err := c.Mesa(ctx, ""); !errors.Is(err, ErrIDVazio) {
		t.Errorf("Mesa vazia: %v", err)
	}
	if _, err := c.Catalogo(ctx, " "); !errors.Is(err, ErrIDVazio) {
		t.Errorf("Catalogo sem mesa: %v", err)
	}
	for nome, cs := range casos {
		if !errors.Is(cs.err, cs.quer) {
			t.Errorf("%s: esperado %v, veio %v", nome, cs.quer, cs.err)
		}
	}
	if s.chamadas.Load() != 0 {
		t.Errorf("validação local bateu no servidor %d vezes", s.chamadas.Load())
	}
}

func TestClient_HandoffSemNotaOmiteOCampo(t *testing.T) {
	s := novoServidor(t, responderJSON(http.StatusOK, `{}`))
	dep := "d1"
	if err := s.cliente().Handoff(context.Background(), "c1", Handoff{DepartmentID: &dep}); err != nil {
		t.Fatal(err)
	}
	if got := s.ultima.Load().corpo; got != `{"departmentId":"d1"}` {
		t.Errorf("corpo = %q", got)
	}
}

// 429 com e sem Retry-After nas rotas novas (a matriz de client_test.go já
// cobre status/code/transitório em todas elas).
func TestClient_RetryAfter_RotasNovas(t *testing.T) {
	agora := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	execs := map[string]func(ctx context.Context, c *Client) error{
		"Eventos": func(ctx context.Context, c *Client) error { _, err := c.Eventos(ctx, 10, 500); return err },
		"Handoff": func(ctx context.Context, c *Client) error { return c.Handoff(ctx, "c1", Handoff{Note: "n"}) },
		"Lookup":  func(ctx context.Context, c *Client) error { _, err := c.Lookup(ctx, "+5531990000000"); return err },
		"Mesa":    func(ctx context.Context, c *Client) error { _, err := c.Mesa(ctx, "m1"); return err },
		"Listar": func(ctx context.Context, c *Client) error {
			_, err := c.ListarTickets(ctx, FiltroTickets{})
			return err
		},
		"Conversa": func(ctx context.Context, c *Client) error { _, err := c.Conversas(ctx, FiltroConversas{}); return err },
	}
	for nome, exec := range execs {
		for header, quer := range map[string]time.Duration{"": 0, "12": 12 * time.Second, agora.Add(20 * time.Second).Format(http.TimeFormat): 20 * time.Second} {
			t.Run(nome+"/RetryAfter="+header, func(t *testing.T) {
				s := novoServidor(t, func(w http.ResponseWriter, _ *http.Request) {
					if header != "" {
						w.Header().Set("Retry-After", header)
					}
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = io.WriteString(w, `{"statusCode":429,"code":"RATE_LIMITED","message":"Limite de requisições excedido"}`)
				})
				err := exec(context.Background(), s.cliente(func(o *Options) { o.Now = func() time.Time { return agora } }))
				var he *HTTPError
				if !errors.As(err, &he) || he.RetryAfter != quer || he.Code != CodeRateLimited || !he.Transitorio() {
					t.Fatalf("err = %v (%+v)", err, he)
				}
				if s.chamadas.Load() != 1 {
					t.Errorf("cliente repetiu: %d chamadas", s.chamadas.Load())
				}
			})
		}
	}
}

func TestClient_PIINaMensagemDoErroComCode(t *testing.T) {
	s := novoServidor(t, responderJSON(http.StatusBadRequest,
		`{"statusCode":400,"code":"VALIDATION_ERROR","message":"contactPhone +55 (31) 99000-0000 inválido para maria.silva@gmail.com"}`))
	_, err := s.cliente().CriarTicket(context.Background(), NovoTicket{DeskID: "m1", Title: "t"})
	var he *HTTPError
	if !errors.As(err, &he) || he.Code != CodeValidationError {
		t.Fatalf("err = %v", err)
	}
	for _, vazou := range []string{"99000", "maria", "gmail"} {
		if strings.Contains(err.Error(), vazou) {
			t.Errorf("PII %q vazou em %q", vazou, err.Error())
		}
	}
}
