package infodesk

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// servidor é um Infodesk falso: registra cada requisição e responde o que o
// teste mandar.
type servidor struct {
	t         *testing.T
	srv       *httptest.Server
	chamadas  atomic.Int32
	ultima    atomic.Pointer[requisicao]
	responder func(w http.ResponseWriter, r *http.Request)
}

type requisicao struct {
	metodo, caminho, query string
	uri                    string // RequestURI: caminho ainda escapado
	cabecalhos             http.Header
	corpo                  string
}

func novoServidor(t *testing.T, responder func(w http.ResponseWriter, r *http.Request)) *servidor {
	t.Helper()
	s := &servidor{t: t, responder: responder}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.ultima.Store(&requisicao{metodo: r.Method, caminho: r.URL.Path, query: r.URL.RawQuery, uri: r.RequestURI, cabecalhos: r.Header.Clone(), corpo: string(b)})
		s.chamadas.Add(1)
		s.responder(w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *servidor) cliente(ajustes ...func(*Options)) *Client {
	o := Options{BaseURL: s.srv.URL, APIKey: "ik_live_teste"}
	for _, a := range ajustes {
		a(&o)
	}
	return New(o)
}

func responderJSON(status int, corpo string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, corpo)
	}
}

// chamada descreve um método do cliente para os testes em matriz.
type chamada struct {
	nome      string
	metodo    string
	caminho   string // após /integration/v1
	temSaida  bool   // decodifica corpo 2xx (sensível a JSON inválido)
	sucessoOK string // corpo 2xx válido
	exec      func(ctx context.Context, c *Client) error
}

var chamadas = []chamada{
	{"Me", http.MethodGet, "/me", true, `{"organizationId":"o1","keyId":"k1","name":"Sofia","scopes":["tickets:read"],"handlesConversations":true}`,
		func(ctx context.Context, c *Client) error { _, err := c.Me(ctx); return err }},
	{"Lookup", http.MethodGet, "/contacts/lookup", true, `{"phone":"+5531990000000","requesters":[],"clientIds":[],"openTickets":[],"conversation":null}`,
		func(ctx context.Context, c *Client) error { _, err := c.Lookup(ctx, "+5531990000000"); return err }},
	{"EnviarMensagem", http.MethodPost, "/conversations/c1/messages", false, `{}`,
		func(ctx context.Context, c *Client) error { return c.EnviarMensagem(ctx, "c1", "oi", false) }},
	{"NotaDaConversa", http.MethodPost, "/conversations/c1/messages", false, `{}`,
		func(ctx context.Context, c *Client) error { return c.EnviarMensagem(ctx, "c1", "resumo", true) }},
	{"Handoff", http.MethodPost, "/conversations/c1/handoff", false, `{}`,
		func(ctx context.Context, c *Client) error {
			return c.Handoff(ctx, "c1", Handoff{Note: "resumo"})
		}},
	{"VincularSolicitante", http.MethodPost, "/conversations/c1/requester", false, `{}`,
		func(ctx context.Context, c *Client) error { return c.VincularSolicitante(ctx, "c1", "r1") }},
	{"TransferirDepartamento", http.MethodPost, "/conversations/c1/department", false, `{}`,
		func(ctx context.Context, c *Client) error { return c.TransferirDepartamento(ctx, "c1", "d1") }},
	{"RemoverDepartamento", http.MethodPost, "/conversations/c1/department", false, `{}`,
		func(ctx context.Context, c *Client) error { return c.RemoverDepartamento(ctx, "c1") }},
	{"Atribuir", http.MethodPost, "/conversations/c1/assign", false, `{}`,
		func(ctx context.Context, c *Client) error { return c.Atribuir(ctx, "c1", "u1") }},
	{"Encerrar", http.MethodPost, "/conversations/c1/finish", false, ``,
		func(ctx context.Context, c *Client) error { return c.Encerrar(ctx, "c1") }},
	{"Mesas", http.MethodGet, "/desks", true, `[{"id":"m1","name":"Suporte"}]`,
		func(ctx context.Context, c *Client) error { _, err := c.Mesas(ctx); return err }},
	{"Departamentos", http.MethodGet, "/departments", true, `[{"id":"d1","name":"N1"}]`,
		func(ctx context.Context, c *Client) error { _, err := c.Departamentos(ctx); return err }},
	{"Atendentes", http.MethodGet, "/users", true, `[{"id":"u1","username":"ana","email":"a@x.com"}]`,
		func(ctx context.Context, c *Client) error { _, err := c.Atendentes(ctx); return err }},
	{"Mesa", http.MethodGet, "/desks/m1", true, `{"id":"m1","name":"Suporte","statuses":[],"stages":[],"priorities":[],"catalogItems":[]}`,
		func(ctx context.Context, c *Client) error { _, err := c.Mesa(ctx, "m1"); return err }},
	{"Catalogo", http.MethodGet, "/catalog", true, `[{"id":"i1","name":"Impressora"}]`,
		func(ctx context.Context, c *Client) error { _, err := c.Catalogo(ctx, "m1"); return err }},
	{"CriarTicket", http.MethodPost, "/tickets", true, `{"number":123}`,
		func(ctx context.Context, c *Client) error {
			_, err := c.CriarTicket(ctx, NovoTicket{DeskID: "m1", Title: "t"})
			return err
		}},
	{"CriarTicketDaConversa", http.MethodPost, "/conversations/c1/ticket", true, `{"number":123}`,
		func(ctx context.Context, c *Client) error {
			_, err := c.CriarTicketDaConversa(ctx, "c1", NovoTicketDaConversa{DeskID: "m1", Title: "t"})
			return err
		}},
	{"Ticket", http.MethodGet, "/tickets/123", true, `{"number":123}`,
		func(ctx context.Context, c *Client) error { _, err := c.Ticket(ctx, 123); return err }},
	{"ListarTickets", http.MethodGet, "/tickets", true, `{"data":[{"number":123}],"nextCursor":null}`,
		func(ctx context.Context, c *Client) error {
			_, err := c.ListarTickets(ctx, FiltroTickets{Phone: "+5531990000000", Status: "open"})
			return err
		}},
	{"ResponderTicket", http.MethodPost, "/tickets/123/reply", false, `{}`,
		func(ctx context.Context, c *Client) error { return c.ResponderTicket(ctx, 123, "ok") }},
	{"NotaInterna", http.MethodPost, "/tickets/123/notes", false, `{}`,
		func(ctx context.Context, c *Client) error { return c.NotaInterna(ctx, 123, "nota") }},
	{"Conversas", http.MethodGet, "/conversations", true, `{"data":[{"id":"c1"}],"nextCursor":null}`,
		func(ctx context.Context, c *Client) error { _, err := c.Conversas(ctx, FiltroConversas{}); return err }},
	{"Conversa", http.MethodGet, "/conversations/c1", true, `{"id":"c1"}`,
		func(ctx context.Context, c *Client) error { _, err := c.Conversa(ctx, "c1"); return err }},
	{"Eventos", http.MethodGet, "/events", true, `{"data":[],"nextAfter":0,"hasMore":false}`,
		func(ctx context.Context, c *Client) error { _, err := c.Eventos(ctx, 0, 500); return err }},
}

func TestClient_SucessoRotasEHeaders(t *testing.T) {
	for _, ch := range chamadas {
		t.Run(ch.nome, func(t *testing.T) {
			s := novoServidor(t, responderJSON(http.StatusOK, ch.sucessoOK))
			c := s.cliente()
			if err := ch.exec(context.Background(), c); err != nil {
				t.Fatalf("erro inesperado: %v", err)
			}
			r := s.ultima.Load()
			if r.metodo != ch.metodo || r.caminho != PrefixoRotas+ch.caminho {
				t.Errorf("rota: %s %s, esperado %s %s", r.metodo, r.caminho, ch.metodo, PrefixoRotas+ch.caminho)
			}
			if got := r.cabecalhos.Get("Authorization"); got != "Bearer ik_live_teste" {
				t.Errorf("Authorization = %q", got)
			}
			if got := r.cabecalhos.Get("Accept"); got != "application/json" {
				t.Errorf("Accept = %q", got)
			}
			if ch.metodo == http.MethodPost {
				if got := r.cabecalhos.Get("Content-Type"); got != "application/json" {
					t.Errorf("Content-Type = %q", got)
				}
				if !json.Valid([]byte(r.corpo)) {
					t.Errorf("corpo enviado não é JSON: %q", r.corpo)
				}
			}
		})
	}
}

func TestClient_ErrosHTTP(t *testing.T) {
	casos := []struct {
		nome        string
		status      int
		corpo       string
		code        string
		transitorio bool
		mensagem    string
	}{
		// Formato v1: `code` estável (06-erros.md).
		{"401", http.StatusUnauthorized, `{"statusCode":401,"code":"UNAUTHORIZED","message":"Chave inválida"}`, CodeUnauthorized, false, "Chave inválida"},
		{"403", http.StatusForbidden, `{"statusCode":403,"code":"FORBIDDEN","message":"Escopo insuficiente. Necessário: tickets:write."}`, CodeForbidden, false, "Escopo insuficiente. Necessário: tickets:write."},
		{"404", http.StatusNotFound, `{"statusCode":404,"code":"NOT_FOUND","message":"Não encontrado"}`, CodeNotFound, false, "Não encontrado"},
		{"429", http.StatusTooManyRequests, `{"statusCode":429,"code":"RATE_LIMITED","message":"Limite"}`, CodeRateLimited, true, "Limite"},
		{"500", http.StatusInternalServerError, `{"statusCode":500,"code":"INTERNAL_ERROR","message":"erro"}`, CodeInternalError, true, "erro"},
		{"400Validation", http.StatusBadRequest, `{"statusCode":400,"code":"VALIDATION_ERROR","message":"corpo inválido","errors":[{"campo":"title"}]}`, CodeValidationError, false, `corpo inválido [errors: [{"campo":"title"}]]`},
		{"400InvalidReference", http.StatusBadRequest, `{"statusCode":400,"code":"INVALID_REFERENCE","message":"deskId inexistente"}`, CodeInvalidReference, false, "deskId inexistente"},
		{"409Conflict", http.StatusConflict, `{"statusCode":409,"code":"CONFLICT","message":"chamado preso a outro atendimento"}`, CodeConflict, false, "chamado preso a outro atendimento"},
		{"409JanelaFechada", http.StatusConflict, `{"statusCode":409,"code":"WHATSAPP_WINDOW_CLOSED","message":"janela fechada","lastInboundAt":"2026-10-18T09:12:00.000Z"}`, CodeWhatsAppWindowClosed, false, "janela fechada"},
		// Sem `code` (formato antigo ou proxy no meio): decide o status.
		{"401SemCode", http.StatusUnauthorized, `{"statusCode":401,"message":"Chave inválida"}`, "", false, "Chave inválida"},
		{"404SemCode", http.StatusNotFound, `{"statusCode":404,"message":"Não encontrado"}`, "", false, "Não encontrado"},
		{"429SemCode", http.StatusTooManyRequests, `{"statusCode":429,"message":"Limite"}`, "", true, "Limite"},
		{"500Html", http.StatusInternalServerError, `<html>erro</html>`, "", true, "<html>erro</html>"},
		{"502Vazio", http.StatusBadGateway, ``, "", true, ""},
		// `code` que não conhecemos: também cai no status.
		{"503CodeNovo", http.StatusServiceUnavailable, `{"statusCode":503,"code":"MANUTENCAO","message":"volte já"}`, "MANUTENCAO", true, "volte já"},
		{"422CodeNovo", http.StatusUnprocessableEntity, `{"statusCode":422,"code":"REGRA_NOVA","message":"não"}`, "REGRA_NOVA", false, "não"},
	}
	for _, ch := range chamadas {
		for _, cs := range casos {
			t.Run(ch.nome+"/"+cs.nome, func(t *testing.T) {
				s := novoServidor(t, responderJSON(cs.status, cs.corpo))
				err := ch.exec(context.Background(), s.cliente())
				var he *HTTPError
				if !errors.As(err, &he) {
					t.Fatalf("esperado *HTTPError, veio %T: %v", err, err)
				}
				if he.Status != cs.status {
					t.Errorf("Status = %d, esperado %d", he.Status, cs.status)
				}
				if he.Code != cs.code {
					t.Errorf("Code = %q, esperado %q", he.Code, cs.code)
				}
				if he.Rota != ch.caminho {
					t.Errorf("Rota = %q, esperado %q", he.Rota, ch.caminho)
				}
				if he.Transitorio() != cs.transitorio || Transitorio(err) != cs.transitorio {
					t.Errorf("Transitorio() = %v, esperado %v", he.Transitorio(), cs.transitorio)
				}
				if he.Mensagem != cs.mensagem {
					t.Errorf("Mensagem = %q, esperado %q", he.Mensagem, cs.mensagem)
				}
				if !strings.Contains(err.Error(), ch.caminho) {
					t.Errorf("Error() sem a rota: %q", err.Error())
				}
				if s.chamadas.Load() != 1 {
					t.Errorf("cliente repetiu a requisição: %d chamadas", s.chamadas.Load())
				}
			})
		}
	}
}

func TestClient_JSONInvalidoECorpoVazio(t *testing.T) {
	for _, ch := range chamadas {
		if !ch.temSaida {
			continue
		}
		for nome, corpo := range map[string]string{"invalido": `{"id": `, "vazio": ``, "tipoErrado": `"texto"`} {
			t.Run(ch.nome+"/"+nome, func(t *testing.T) {
				s := novoServidor(t, responderJSON(http.StatusOK, corpo))
				err := ch.exec(context.Background(), s.cliente())
				var er *ErroResposta
				if !errors.As(err, &er) {
					t.Fatalf("esperado *ErroResposta, veio %T: %v", err, err)
				}
				if Transitorio(err) {
					t.Error("resposta ilegível não é transitória")
				}
				if strings.Contains(err.Error(), "texto") {
					t.Errorf("corpo cru vazou no erro: %q", err.Error())
				}
			})
		}
	}
}

func TestClient_MetodosSemSaidaIgnoramCorpo(t *testing.T) {
	s := novoServidor(t, responderJSON(http.StatusCreated, `isto não é json`))
	if err := s.cliente().EnviarMensagem(context.Background(), "c1", "oi", false); err != nil {
		t.Fatalf("2xx com corpo irrelevante deve ser sucesso: %v", err)
	}
}

func TestClient_RetryAfter(t *testing.T) {
	agora := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	casos := map[string]struct {
		header string
		espera time.Duration
	}{
		"segundos":    {"7", 7 * time.Second},
		"dataHTTP":    {agora.Add(30 * time.Second).Format(http.TimeFormat), 30 * time.Second},
		"dataPassada": {agora.Add(-30 * time.Second).Format(http.TimeFormat), 0},
		"lixo":        {"amanhã", 0},
		"negativo":    {"-5", 0},
		"ausente":     {"", 0},
	}
	for nome, cs := range casos {
		t.Run(nome, func(t *testing.T) {
			s := novoServidor(t, func(w http.ResponseWriter, _ *http.Request) {
				if cs.header != "" {
					w.Header().Set("Retry-After", cs.header)
				}
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"statusCode":429,"message":"Limite de requisições excedido"}`)
			})
			c := s.cliente(func(o *Options) { o.Now = func() time.Time { return agora } })
			err := c.EnviarMensagem(context.Background(), "c1", "oi", false)
			var he *HTTPError
			if !errors.As(err, &he) {
				t.Fatalf("esperado *HTTPError, veio %v", err)
			}
			if he.RetryAfter != cs.espera {
				t.Errorf("RetryAfter = %v, esperado %v", he.RetryAfter, cs.espera)
			}
			if !he.Transitorio() {
				t.Error("429 deve ser transitório")
			}
		})
	}
}

func TestClient_RedeETimeoutSaoTransitorios(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		s := novoServidor(t, func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(300 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		})
		c := s.cliente(func(o *Options) { o.HTTP = &http.Client{Timeout: 50 * time.Millisecond} })
		err := c.Encerrar(context.Background(), "c1")
		var er *ErroRede
		if !errors.As(err, &er) {
			t.Fatalf("esperado *ErroRede, veio %T: %v", err, err)
		}
		if !er.Transitorio() || !Transitorio(err) {
			t.Error("timeout deve ser transitório")
		}
		if er.Rota != "/conversations/c1/finish" {
			t.Errorf("Rota = %q", er.Rota)
		}
	})
	t.Run("conexaoRecusada", func(t *testing.T) {
		s := novoServidor(t, responderJSON(http.StatusOK, `[]`))
		url := s.srv.URL
		s.srv.Close()
		c := New(Options{BaseURL: url, APIKey: "k"})
		_, err := c.Mesas(context.Background())
		if !Transitorio(err) {
			t.Fatalf("conexão recusada deve ser transitória: %v", err)
		}
		var er *ErroRede
		if !errors.As(err, &er) || errors.Unwrap(er) == nil {
			t.Errorf("ErroRede deve embrulhar a causa: %v", err)
		}
	})
	t.Run("contextoCancelado", func(t *testing.T) {
		s := novoServidor(t, responderJSON(http.StatusOK, `[]`))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := s.cliente().Mesas(ctx)
		if Transitorio(err) {
			t.Errorf("cancelamento do chamador não é transitório: %v", err)
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("deve expor context.Canceled: %v", err)
		}
	})
}

func TestClient_CriarTicketNaoRepeteOPOST(t *testing.T) {
	t.Run("500", func(t *testing.T) {
		s := novoServidor(t, responderJSON(http.StatusInternalServerError, `{"statusCode":500,"message":"boom"}`))
		_, err := s.cliente().CriarTicket(context.Background(), NovoTicket{DeskID: "m1", Title: "t"})
		if !Transitorio(err) {
			t.Fatalf("esperado transitório: %v", err)
		}
		if n := s.chamadas.Load(); n != 1 {
			t.Fatalf("POST /tickets foi enviado %d vezes; deve ser exatamente 1", n)
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
		_, err := s.cliente().CriarTicket(context.Background(), NovoTicket{DeskID: "m1", Title: "t"})
		var er *ErroRede
		if !errors.As(err, &er) {
			t.Fatalf("esperado *ErroRede, veio %T: %v", err, err)
		}
		if n := s.chamadas.Load(); n != 1 {
			t.Fatalf("POST /tickets foi enviado %d vezes; deve ser exatamente 1", n)
		}
	})
}

func TestClient_CriarTicket(t *testing.T) {
	t.Run("corpoEOmitempty", func(t *testing.T) {
		s := novoServidor(t, responderJSON(http.StatusCreated, `{"id":"abc","number":4521,"status":"OPEN"}`))
		prio := "p1"
		tk, err := s.cliente().CriarTicket(context.Background(), NovoTicket{
			DeskID: "m1", Title: "Impressora", Description: "não imprime", Channel: CanalWhatsApp,
			ContactPhone: "+5531990000000", PriorityID: &prio,
		})
		if err != nil {
			t.Fatalf("erro: %v", err)
		}
		if tk.Number != 4521 {
			t.Errorf("Number = %d", tk.Number)
		}
		if tk.Bruto["status"] != "OPEN" {
			t.Errorf("Bruto não preservado: %v", tk.Bruto)
		}
		var enviado map[string]any
		if err := json.Unmarshal([]byte(s.ultima.Load().corpo), &enviado); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"deskId", "title", "description", "channel", "contactPhone", "priorityId"} {
			if _, ok := enviado[k]; !ok {
				t.Errorf("corpo sem %s: %v", k, enviado)
			}
		}
		for _, k := range []string{"clientId", "requesterId"} {
			if _, ok := enviado[k]; ok {
				t.Errorf("corpo com %s nil (deveria ser omitido): %v", k, enviado)
			}
		}
	})
	t.Run("numeroComoTextoNumerico", func(t *testing.T) {
		s := novoServidor(t, responderJSON(http.StatusOK, `{"number":"4521"}`))
		tk, err := s.cliente().CriarTicket(context.Background(), NovoTicket{DeskID: "m1", Title: "t"})
		if err != nil || tk.Number != 4521 {
			t.Fatalf("Number = %d, err = %v", tk.Number, err)
		}
	})
	t.Run("numeroNaoNumericoEhSemNumero", func(t *testing.T) {
		s := novoServidor(t, responderJSON(http.StatusOK, `{"number":"INF-77"}`))
		tk, err := s.cliente().CriarTicket(context.Background(), NovoTicket{DeskID: "m1", Title: "t"})
		if !errors.Is(err, ErrTicketSemNumero) || tk.Number != 0 || tk.Bruto["number"] != "INF-77" {
			t.Fatalf("tk = %+v, err = %v", tk, err)
		}
	})
	t.Run("objetoCompletoDoPDF", func(t *testing.T) {
		s := novoServidor(t, responderJSON(http.StatusCreated, ticketPDF))
		tk, err := s.cliente().CriarTicket(context.Background(), NovoTicket{DeskID: "m1", Title: "t"})
		if err != nil || tk.Number != 4521 || !tk.Status.IsOpen || tk.ConversationID != "c1d2" || tk.Requester == nil {
			t.Fatalf("tk = %+v, err = %v", tk, err)
		}
	})
	t.Run("aceita200e201", func(t *testing.T) {
		for _, st := range []int{http.StatusOK, http.StatusCreated} {
			s := novoServidor(t, responderJSON(st, `{"number":5}`))
			if tk, err := s.cliente().CriarTicket(context.Background(), NovoTicket{DeskID: "m1", Title: "t"}); err != nil || tk.Number != 5 {
				t.Errorf("status %d: tk = %+v, err = %v", st, tk, err)
			}
		}
	})
	t.Run("semNumero", func(t *testing.T) {
		s := novoServidor(t, responderJSON(http.StatusCreated, `{"id":"abc"}`))
		tk, err := s.cliente().CriarTicket(context.Background(), NovoTicket{DeskID: "m1", Title: "t"})
		if !errors.Is(err, ErrTicketSemNumero) {
			t.Fatalf("esperado ErrTicketSemNumero, veio %v", err)
		}
		if tk.Bruto["id"] != "abc" {
			t.Errorf("Bruto deve vir mesmo sem número: %v", tk.Bruto)
		}
		if Transitorio(err) {
			t.Error("ticket sem número não é transitório (nunca repetir)")
		}
	})
	t.Run("camposObrigatorios", func(t *testing.T) {
		s := novoServidor(t, responderJSON(http.StatusOK, `{"number":1}`))
		c := s.cliente()
		for _, nt := range []NovoTicket{{Title: "t"}, {DeskID: "m1"}, {DeskID: " ", Title: " "}} {
			if _, err := c.CriarTicket(context.Background(), nt); !errors.Is(err, ErrCampoObrigatorio) {
				t.Errorf("%+v: esperado ErrCampoObrigatorio, veio %v", nt, err)
			}
		}
		if s.chamadas.Load() != 0 {
			t.Error("validação local não deve bater no servidor")
		}
	})
}

func TestClient_Ticket(t *testing.T) {
	s := novoServidor(t, responderJSON(http.StatusOK, `{"number":88,"title":"x","messages":[{"id":"m1"}]}`))
	tk, err := s.cliente().Ticket(context.Background(), 88)
	if err != nil || tk.Number != 88 || tk.Bruto["title"] != "x" {
		t.Fatalf("tk = %+v, err = %v", tk, err)
	}
	if s.ultima.Load().caminho != PrefixoRotas+"/tickets/88" {
		t.Errorf("caminho = %q", s.ultima.Load().caminho)
	}
}

func TestClient_CacheDeReferencia(t *testing.T) {
	agora := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	relogio := &agora
	s := novoServidor(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case PrefixoRotas + "/desks":
			_, _ = io.WriteString(w, `[{"id":"m1","name":"Suporte"},{"id":"m2","name":"Financeiro"}]`)
		case PrefixoRotas + "/departments":
			_, _ = io.WriteString(w, `[{"id":"d1","name":"N1"}]`)
		case PrefixoRotas + "/users":
			_, _ = io.WriteString(w, `[{"id":"u1","username":"ana","email":"ana@x.com"}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	c := s.cliente(func(o *Options) {
		o.Now = func() time.Time { return *relogio }
		o.CacheTTL = 10 * time.Minute
	})
	ctx := context.Background()

	mesas, err := c.Mesas(ctx)
	if err != nil || len(mesas) != 2 || mesas[1].Nome != "Financeiro" {
		t.Fatalf("mesas = %+v, err = %v", mesas, err)
	}
	deps, err := c.Departamentos(ctx)
	if err != nil || len(deps) != 1 || deps[0].ID != "d1" {
		t.Fatalf("deps = %+v, err = %v", deps, err)
	}
	ats, err := c.Atendentes(ctx)
	if err != nil || len(ats) != 1 || ats[0].Username != "ana" || ats[0].Email != "ana@x.com" {
		t.Fatalf("ats = %+v, err = %v", ats, err)
	}
	if n := s.chamadas.Load(); n != 3 {
		t.Fatalf("primeira carga: %d chamadas, esperado 3", n)
	}

	// Segunda chamada: cache (o servidor não é tocado); cópia independente.
	mesas[0].Nome = "alterado pelo chamador"
	mesas2, _ := c.Mesas(ctx)
	_, _ = c.Departamentos(ctx)
	_, _ = c.Atendentes(ctx)
	if n := s.chamadas.Load(); n != 3 {
		t.Fatalf("cache não usado: %d chamadas", n)
	}
	if mesas2[0].Nome != "Suporte" {
		t.Errorf("cache foi alterado por quem chamou: %+v", mesas2)
	}

	// Dentro do TTL ainda vale; depois expira.
	*relogio = agora.Add(9 * time.Minute)
	_, _ = c.Mesas(ctx)
	if n := s.chamadas.Load(); n != 3 {
		t.Fatalf("cache expirou cedo: %d chamadas", n)
	}
	*relogio = agora.Add(11 * time.Minute)
	_, _ = c.Mesas(ctx)
	if n := s.chamadas.Load(); n != 4 {
		t.Fatalf("cache não expirou com Now injetado: %d chamadas", n)
	}

	// InvalidarCache força nova carga de tudo.
	c.InvalidarCache()
	_, _ = c.Mesas(ctx)
	_, _ = c.Departamentos(ctx)
	_, _ = c.Atendentes(ctx)
	if n := s.chamadas.Load(); n != 7 {
		t.Fatalf("InvalidarCache não limpou: %d chamadas", n)
	}
}

func TestClient_FalhaNaoEntraNoCache(t *testing.T) {
	var falhar atomic.Bool
	falhar.Store(true)
	s := novoServidor(t, func(w http.ResponseWriter, _ *http.Request) {
		if falhar.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `[{"id":"m1","name":"Suporte"}]`)
	})
	c := s.cliente()
	if _, err := c.Mesas(context.Background()); !Transitorio(err) {
		t.Fatalf("esperado transitório: %v", err)
	}
	falhar.Store(false)
	mesas, err := c.Mesas(context.Background())
	if err != nil || len(mesas) != 1 {
		t.Fatalf("mesas = %+v, err = %v", mesas, err)
	}
	if s.chamadas.Load() != 2 {
		t.Errorf("chamadas = %d", s.chamadas.Load())
	}
}

func TestClient_Conversas(t *testing.T) {
	t.Run("queryEEnvelopeComCursor", func(t *testing.T) {
		s := novoServidor(t, responderJSON(http.StatusOK, `{"data":[{"id":"c1","handledBy":"EXTERNAL_BOT","botActive":true},{"id":"c2"}],"nextCursor":"cur2"}`))
		bot := true
		desde := time.Date(2026, 10, 19, 12, 30, 0, 0, time.FixedZone("BRT", -3*3600))
		pag, err := s.cliente().Conversas(context.Background(), FiltroConversas{
			Tab: "entrada", Status: "PENDING", Channel: "WHATSAPP", AssignedUserID: "none", DepartmentID: "d1",
			BotActive: &bot, Phone: "+5531990000000", UpdatedSince: desde, Limit: 100, Cursor: "cur1",
		})
		if err != nil || len(pag.Data) != 2 || pag.Data[1].ID != "c2" || pag.NextCursor != "cur2" {
			t.Fatalf("pag = %+v, err = %v", pag, err)
		}
		if pag.Data[0].HandledBy != ConduzidaPorBotExterno || !pag.Data[0].BotActive {
			t.Errorf("conversa tipada: %+v", pag.Data[0])
		}
		q, err := url.ParseQuery(s.ultima.Load().query)
		if err != nil {
			t.Fatal(err)
		}
		quer := map[string]string{
			"tab": "entrada", "status": "PENDING", "channel": "WHATSAPP", "assignedUserId": "none", "departmentId": "d1",
			"botActive": "true", "phone": "+5531990000000", "updatedSince": "2026-10-19T15:30:00Z", "limit": "100", "cursor": "cur1",
		}
		for k, v := range quer {
			if q.Get(k) != v {
				t.Errorf("query[%s] = %q, quer %q", k, q.Get(k), v)
			}
		}
		if len(q) != len(quer) {
			t.Errorf("query com chaves a mais: %v", q)
		}
		if !strings.Contains(s.ultima.Load().query, "phone=%2B5531990000000") {
			t.Errorf("o + do telefone precisa ir escapado: %q", s.ultima.Load().query)
		}
	})
	t.Run("botActiveFalseEhEnviado", func(t *testing.T) {
		s := novoServidor(t, responderJSON(http.StatusOK, `{"data":[]}`))
		nao := false
		if _, err := s.cliente().Conversas(context.Background(), FiltroConversas{BotActive: &nao}); err != nil {
			t.Fatal(err)
		}
		if s.ultima.Load().query != "botActive=false" {
			t.Errorf("query = %q", s.ultima.Load().query)
		}
	})
	t.Run("arrayPuroEUltimaPagina", func(t *testing.T) {
		s := novoServidor(t, responderJSON(http.StatusOK, `[{"id":"c1"},{"id":"c2"}]`))
		pag, err := s.cliente().Conversas(context.Background(), FiltroConversas{})
		if err != nil || len(pag.Data) != 2 || pag.NextCursor != "" {
			t.Fatalf("pag = %+v, err = %v", pag, err)
		}
	})
	t.Run("semFiltros", func(t *testing.T) {
		s := novoServidor(t, responderJSON(http.StatusOK, `{"data":[],"nextCursor":null}`))
		pag, err := s.cliente().Conversas(context.Background(), FiltroConversas{})
		if err != nil || len(pag.Data) != 0 || pag.NextCursor != "" {
			t.Fatalf("pag = %+v, err = %v", pag, err)
		}
		if s.ultima.Load().query != "" {
			t.Errorf("query inesperada: %q", s.ultima.Load().query)
		}
	})
	t.Run("formatoDesconhecido", func(t *testing.T) {
		// O formato antigo com chaves `items`/`results`/`conversations` morreu:
		// a v1 só tem `{data, nextCursor}` (06-erros.md, Paginação).
		for _, corpo := range []string{`{"foo":1}`, `[1,2]`, `"x"`, `{"data":"x"}`, `{"items":[{"id":"c1"}]}`, `{"results":[{"id":"c1"}]}`, `null`} {
			s := novoServidor(t, responderJSON(http.StatusOK, corpo))
			_, err := s.cliente().Conversas(context.Background(), FiltroConversas{})
			var er *ErroResposta
			if !errors.As(err, &er) {
				t.Errorf("%s: esperado *ErroResposta, veio %v", corpo, err)
			}
		}
	})
}

func TestClient_Conversa(t *testing.T) {
	s := novoServidor(t, responderJSON(http.StatusOK, `{"id":"c1","handledBy":"AGENT","botActive":false,"assignedUserId":"u1",
	  "whatsappWindow":{"open":true},"recentMessages":[{"id":"m1","sender":"CLIENT"}]}`))
	conv, err := s.cliente().Conversa(context.Background(), "c1")
	if err != nil || conv.ID != "c1" || conv.HandledBy != ConduzidaPorAtendente || conv.BotActive || conv.AssignedUserID != "u1" {
		t.Fatalf("conv = %+v, err = %v", conv, err)
	}
	if _, ok := conv.Bruto["recentMessages"]; !ok {
		t.Errorf("recentMessages deveria estar em Bruto: %v", conv.Bruto)
	}
}

func TestClient_CorposDasAcoesDeConversa(t *testing.T) {
	s := novoServidor(t, responderJSON(http.StatusOK, ``))
	c := s.cliente()
	ctx := context.Background()

	_ = c.EnviarMensagem(ctx, "c1", "Olá! 😊", false)
	// O campo é `text`, não `body` (mudança da API v1; 04-conversas.md).
	if got := s.ultima.Load().corpo; got != `{"text":"Olá! 😊"}` {
		t.Errorf("messages: %q", got)
	}
	_ = c.EnviarMensagem(ctx, "c1", "Resumo da triagem", true)
	if got := s.ultima.Load().corpo; got != `{"text":"Resumo da triagem","internal":true}` {
		t.Errorf("nota interna: %q", got)
	}
	_ = c.NotaInterna(ctx, 55, "Cliente confirmou.")
	if r := s.ultima.Load(); r.caminho != PrefixoRotas+"/tickets/55/notes" || r.corpo != `{"body":"Cliente confirmou."}` {
		t.Errorf("nota do chamado: %s %q", r.caminho, r.corpo)
	}
	_ = c.VincularSolicitante(ctx, "c1", "r7")
	if got := s.ultima.Load().corpo; got != `{"id":"r7"}` {
		t.Errorf("requester: %q", got)
	}
	dep := "d9"
	_ = c.Handoff(ctx, "c1", Handoff{DepartmentID: &dep, Note: "Cliente Maria. Chamado #4521."})
	if got := s.ultima.Load().corpo; got != `{"departmentId":"d9","note":"Cliente Maria. Chamado #4521."}` {
		t.Errorf("handoff: %q", got)
	}
	_ = c.TransferirDepartamento(ctx, "c1", "d9")
	if got := s.ultima.Load().corpo; got != `{"departmentId":"d9"}` {
		t.Errorf("department: %q", got)
	}
	_ = c.RemoverDepartamento(ctx, "c1")
	if got := s.ultima.Load().corpo; got != `{"departmentId":null}` {
		t.Errorf("department null: %q", got)
	}
	_ = c.Atribuir(ctx, "c1", "u7")
	if got := s.ultima.Load().corpo; got != `{"userId":"u7"}` {
		t.Errorf("assign: %q", got)
	}
	_ = c.Encerrar(ctx, "c1")
	if got := s.ultima.Load().corpo; got != `{}` {
		t.Errorf("finish: %q", got)
	}
	_ = c.ResponderTicket(ctx, 55, "Já estamos verificando.")
	if got := s.ultima.Load().corpo; got != `{"body":"Já estamos verificando."}` {
		t.Errorf("reply: %q", got)
	}
}

func TestClient_ValidacoesLocais(t *testing.T) {
	s := novoServidor(t, responderJSON(http.StatusOK, `{}`))
	c := s.cliente()
	ctx := context.Background()
	casos := map[string]struct {
		err  error
		quer error
	}{
		"mensagemSemConversa":       {c.EnviarMensagem(ctx, " ", "oi", false), ErrIDVazio},
		"mensagemVazia":             {c.EnviarMensagem(ctx, "c1", "  ", false), ErrCorpoVazio},
		"transferirSemConversa":     {c.TransferirDepartamento(ctx, "", "d1"), ErrIDVazio},
		"transferirSemDepartamento": {c.TransferirDepartamento(ctx, "c1", " "), ErrIDVazio},
		"removerSemConversa":        {c.RemoverDepartamento(ctx, ""), ErrIDVazio},
		"atribuirSemConversa":       {c.Atribuir(ctx, "", "u1"), ErrIDVazio},
		"atribuirSemUsuario":        {c.Atribuir(ctx, "c1", ""), ErrIDVazio},
		"encerrarSemConversa":       {c.Encerrar(ctx, ""), ErrIDVazio},
		"responderSemNumero":        {c.ResponderTicket(ctx, 0, "x"), ErrNumeroInvalido},
		"responderVazio":            {c.ResponderTicket(ctx, 1, ""), ErrCorpoVazio},
	}
	if _, err := c.Ticket(ctx, 0); !errors.Is(err, ErrNumeroInvalido) {
		t.Errorf("Ticket vazio: %v", err)
	}
	if _, err := c.Conversa(ctx, ""); !errors.Is(err, ErrIDVazio) {
		t.Errorf("Conversa vazia: %v", err)
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

func TestClient_IDsSaoEscapadosNaRota(t *testing.T) {
	s := novoServidor(t, responderJSON(http.StatusOK, `{}`))
	c := s.cliente()
	ctx := context.Background()

	_ = c.EnviarMensagem(ctx, "a/b c", "oi", false)
	if got := s.ultima.Load().uri; got != PrefixoRotas+"/conversations/a%2Fb%20c/messages" {
		t.Errorf("RequestURI = %q", got)
	}
	// Um id malicioso não pode subir na árvore de rotas.
	_ = c.EnviarMensagem(ctx, "../tickets", "oi", false)
	if got := s.ultima.Load().uri; got != PrefixoRotas+"/conversations/..%2Ftickets/messages" || strings.Contains(got, "/../") {
		t.Errorf("RequestURI = %q", got)
	}
	_, _ = c.Ticket(ctx, 12)
	if got := s.ultima.Load().uri; got != PrefixoRotas+"/tickets/12" {
		t.Errorf("RequestURI = %q", got)
	}
	_, _ = c.Mesa(ctx, "1/../2")
	if got := s.ultima.Load().uri; got != PrefixoRotas+"/desks/1%2F..%2F2" {
		t.Errorf("RequestURI = %q", got)
	}
	// deskId vai na query, escapado.
	_, _ = c.Catalogo(ctx, "a&b=c")
	if got := s.ultima.Load().uri; got != PrefixoRotas+"/catalog?deskId=a%26b%3Dc" {
		t.Errorf("RequestURI = %q", got)
	}
}

func TestNew_Defaults(t *testing.T) {
	c := New(Options{})
	if c.http == nil || c.http.Timeout != 15*time.Second {
		t.Errorf("timeout default = %v", c.http.Timeout)
	}
	if c.cacheTTL != 10*time.Minute {
		t.Errorf("TTL default = %v", c.cacheTTL)
	}
	if c.now == nil || c.now().IsZero() {
		t.Error("Now default ausente")
	}
	if err := c.Encerrar(context.Background(), "c1"); !errors.Is(err, ErrSemBaseURL) {
		t.Errorf("sem BaseURL: %v", err)
	}
}

func TestNew_NormalizaBaseURL(t *testing.T) {
	s := novoServidor(t, responderJSON(http.StatusOK, `[]`))
	for _, base := range []string{s.srv.URL, s.srv.URL + "/", s.srv.URL + "/integration/v1", s.srv.URL + "/integration/v1/", "  " + s.srv.URL + "  "} {
		c := New(Options{BaseURL: base, APIKey: "k"})
		if _, err := c.Mesas(context.Background()); err != nil {
			t.Fatalf("%q: %v", base, err)
		}
		if got := s.ultima.Load().caminho; got != PrefixoRotas+"/desks" {
			t.Errorf("%q → caminho %q", base, got)
		}
	}
}

func TestClient_ChaveNaoVazaNoErro(t *testing.T) {
	const chave = "ik_live_teste"
	t.Run("HTTPError", func(t *testing.T) {
		s := novoServidor(t, responderJSON(http.StatusUnauthorized, `{"statusCode":401,"message":"Chave inválida"}`))
		err := s.cliente().EnviarMensagem(context.Background(), "c1", "oi", false)
		if strings.Contains(err.Error(), chave) {
			t.Errorf("chave de API vazou no erro: %q", err.Error())
		}
	})
	t.Run("ErroRede", func(t *testing.T) {
		s := novoServidor(t, responderJSON(http.StatusOK, `[]`))
		url := s.srv.URL
		s.srv.Close()
		_, err := New(Options{BaseURL: url, APIKey: chave}).Conversas(context.Background(), FiltroConversas{Tab: "entrada", DepartmentID: "d1"})
		if strings.Contains(err.Error(), chave) {
			t.Errorf("chave de API vazou no erro de rede: %q", err.Error())
		}
	})
	t.Run("ErroResposta", func(t *testing.T) {
		s := novoServidor(t, responderJSON(http.StatusOK, `{"segredo":"`+chave+`"`))
		_, err := s.cliente().Conversa(context.Background(), "c1")
		if strings.Contains(err.Error(), chave) {
			t.Errorf("corpo vazou no erro: %q", err.Error())
		}
	})
}

func TestClient_CorpoInterrompidoApos2xxEDefinitivo(t *testing.T) {
	s := novoServidor(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "500")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"number":`)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
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
	_, err := s.cliente().CriarTicket(context.Background(), NovoTicket{DeskID: "m1", Title: "t"})
	var er *ErroResposta
	if !errors.As(err, &er) {
		t.Fatalf("esperado *ErroResposta (status já chegou), veio %T: %v", err, err)
	}
	if Transitorio(err) {
		t.Error("ticket pode existir: não pode parecer transitório")
	}
	if n := s.chamadas.Load(); n != 1 {
		t.Errorf("POST enviado %d vezes", n)
	}
}
