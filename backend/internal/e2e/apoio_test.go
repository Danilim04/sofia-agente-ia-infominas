//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------- proxy ----

// entregaWebhook é uma entrega do mock ao proxy.
type entregaWebhook struct {
	EventoID string // `id` do evento (estável entre reentregas)
	Evento   string
	Entrega  string // X-Infominas-Delivery (novo a cada tentativa)
	Telefone string // dígitos
	Reteve   bool   // o proxy NÃO repassou ao gateway
	Status   int    // resposta do gateway (0 se retida)
	Corpo    []byte
	Header   http.Header
}

// proxyWebhook fica entre o mock e o gateway: repassa cada entrega (corpo
// bruto e headers de assinatura intactos) e registra o que passou. Um
// telefone "retido" tem as entregas guardadas e respondidas com 200 sem ir ao
// gateway — é assim que o teste simula o webhook que se perdeu (cenário 7) e
// captura um envelope autêntico para reenviar com assinatura ruim (5 e 6).
type proxyWebhook struct {
	srv     *httptest.Server
	gateway string

	mu       sync.Mutex
	retidos  map[string]bool
	entregas []entregaWebhook
}

func novoProxyWebhook(gatewayURL string) *proxyWebhook {
	p := &proxyWebhook{gateway: gatewayURL, retidos: map[string]bool{}}
	p.srv = httptest.NewServer(http.HandlerFunc(p.servir))
	return p
}

func (p *proxyWebhook) reter(telefone string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.retidos[digitos(telefone)] = true
}

func (p *proxyWebhook) servir(w http.ResponseWriter, r *http.Request) {
	corpo, _ := io.ReadAll(r.Body)
	var env struct {
		ID    string `json:"id"`
		Event string `json:"event"`
		Data  struct {
			Contact struct {
				Phone string `json:"phone"`
			} `json:"contact"`
		} `json:"data"`
	}
	_ = json.Unmarshal(corpo, &env)
	e := entregaWebhook{
		EventoID: env.ID, Evento: env.Event, Entrega: r.Header.Get("X-Infominas-Delivery"),
		Telefone: digitos(env.Data.Contact.Phone), Corpo: corpo, Header: r.Header.Clone(),
	}

	p.mu.Lock()
	reter := p.retidos[e.Telefone]
	p.mu.Unlock()
	if reter {
		e.Reteve = true
		p.registrar(e)
		w.WriteHeader(http.StatusOK)
		return
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, p.gateway+"/webhook", bytes.NewReader(corpo))
	for k, vs := range r.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		p.registrar(e)
		http.Error(w, "proxy: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	e.Status = resp.StatusCode
	p.registrar(e)
	w.WriteHeader(resp.StatusCode)
}

func (p *proxyWebhook) registrar(e entregaWebhook) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entregas = append(p.entregas, e)
}

// entregasDe devolve as entregas do telefone, em ordem.
func (p *proxyWebhook) entregasDe(telefone string) []entregaWebhook {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []entregaWebhook
	for _, e := range p.entregas {
		if e.Telefone == digitos(telefone) {
			out = append(out, e)
		}
	}
	return out
}

// ultimaDe devolve a última entrega do telefone com o evento dado.
func (p *proxyWebhook) ultimaDe(t *testing.T, telefone, evento string) entregaWebhook {
	t.Helper()
	es := p.entregasDe(telefone)
	for i := len(es) - 1; i >= 0; i-- {
		if es[i].Evento == evento {
			return es[i]
		}
	}
	t.Fatalf("o proxy não viu nenhum %q do telefone %s (viu %d entrega(s))", evento, mascarar(telefone), len(es))
	return entregaWebhook{}
}

// assinar calcula X-Infominas-Signature: sha256=HMAC(secret, "<ts>.<corpo>").
func assinar(segredo, timestamp string, corpo []byte) string {
	m := hmac.New(sha256.New, []byte(segredo))
	m.Write([]byte(timestamp + "."))
	m.Write(corpo)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// postarWebhook entrega um corpo ao gateway com os headers dados e devolve o status.
func postarWebhook(t *testing.T, evento, entrega, timestamp, assinatura string, corpo []byte) int {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, pilha.gwURL+"/webhook", bytes.NewReader(corpo))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Infominas-Event", evento)
	req.Header.Set("X-Infominas-Delivery", entrega)
	req.Header.Set("X-Infominas-Timestamp", timestamp)
	req.Header.Set("X-Infominas-Signature", assinatura)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("POST /webhook no gateway falhou: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// ----------------------------------------------------------------- mock ----

var httpMock = &http.Client{Timeout: 15 * time.Second}

func mockPost(t *testing.T, caminho string, corpo, saida any) {
	t.Helper()
	b, _ := json.Marshal(corpo)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, pilha.mockURL+caminho, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpMock.Do(req)
	if err != nil {
		t.Fatalf("POST %s no mock falhou: %v", caminho, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("POST %s no mock: HTTP %d: %s", caminho, resp.StatusCode, raw)
	}
	if saida != nil {
		if err := json.Unmarshal(raw, saida); err != nil {
			t.Fatalf("POST %s no mock: resposta ilegível: %v (%s)", caminho, err, raw)
		}
	}
}

func mockGet(t *testing.T, caminho string, saida any) {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, pilha.mockURL+caminho, nil)
	req.Header.Set("Authorization", "Bearer "+chaveMock)
	resp, err := httpMock.Do(req)
	if err != nil {
		t.Fatalf("GET %s no mock falhou: %v", caminho, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("GET %s no mock: HTTP %d: %s", caminho, resp.StatusCode, raw)
	}
	if err := json.Unmarshal(raw, saida); err != nil {
		t.Fatalf("GET %s no mock: resposta ilegível: %v (%s)", caminho, err, raw)
	}
}

// mensagemMock é uma mensagem da conversa como o mock a mostra.
type mensagemMock struct {
	Sender   string `json:"sender"`
	Type     string `json:"type"`
	Body     string `json:"body"`
	Internal bool   `json:"internal"`
}

type conversaMock struct {
	ID             string         `json:"id"`
	Status         string         `json:"status"`
	HandledBy      string         `json:"handledBy"`
	BotActive      bool           `json:"botActive"`
	DepartmentName string         `json:"departmentName"`
	TicketNumber   *int           `json:"ticketNumber"`
	Messages       []mensagemMock `json:"messages"`
}

type ticketMock struct {
	Number         int    `json:"number"`
	Title          string `json:"title"`
	DeskName       string `json:"deskName"`
	ContactPhone   string `json:"contactPhone"`
	ConversationID string `json:"conversationId"`
}

type estadoMock struct {
	Conversa *conversaMock `json:"conversa"`
	Tickets  []ticketMock  `json:"tickets"`
}

// respostasDoBot são as bolhas públicas da Sofia (sender AGENT, não interna).
func (c *conversaMock) respostasDoBot() []string {
	var out []string
	if c == nil {
		return out
	}
	for _, m := range c.Messages {
		if m.Sender == "AGENT" && !m.Internal {
			out = append(out, m.Body)
		}
	}
	return out
}

// notasInternas são as notas internas da conversa (a do handoff entre elas).
func (c *conversaMock) notasInternas() []string {
	var out []string
	if c == nil {
		return out
	}
	for _, m := range c.Messages {
		if m.Internal {
			out = append(out, m.Body)
		}
	}
	return out
}

// ------------------------------------------------------------- cliente ----

var contadorTelefone atomic.Int64

// cliente é um cliente simulado: telefone próprio por cenário, escrevendo
// pela API de controle do mock (/mock/cliente/*), como o WhatsApp faria.
type cliente struct {
	t          *testing.T
	Telefone   string // E.164
	Nome       string
	ConversaID string
}

// novoCliente cria um cliente com telefone inédito. Cada cenário usa o seu: é
// o que isola um do outro (a conversa do mock é por telefone).
func novoCliente(t *testing.T) *cliente {
	n := contadorTelefone.Add(1)
	return &cliente{t: t, Telefone: fmt.Sprintf("+5531977%06d", n), Nome: "Cliente E2E"}
}

// clienteConhecido usa um telefone que o mock tem no cadastro de solicitantes
// (lookup identifica por telefone, sem pedir CPF).
func clienteConhecido(t *testing.T, telefone string) *cliente {
	return &cliente{t: t, Telefone: telefone, Nome: "Maria"}
}

func (c *cliente) diz(texto string) {
	c.t.Helper()
	var r struct {
		ConversationID string `json:"conversationId"`
	}
	mockPost(c.t, "/mock/cliente/mensagem", map[string]string{"telefone": c.Telefone, "nome": c.Nome, "texto": texto}, &r)
	c.ConversaID = r.ConversationID
}

func (c *cliente) mandaAudio() {
	c.t.Helper()
	var r struct {
		ConversationID string `json:"conversationId"`
	}
	mockPost(c.t, "/mock/cliente/audio", map[string]string{"telefone": c.Telefone, "nome": c.Nome}, &r)
	c.ConversaID = r.ConversationID
}

func (c *cliente) estado() estadoMock {
	c.t.Helper()
	var e estadoMock
	mockGet(c.t, "/mock/estado?telefone="+url.QueryEscape(c.Telefone), &e)
	return e
}

// ticketsDaConversa filtra os tickets do mock pela conversa do cliente.
func (e estadoMock) ticketsDaConversa(conv string) []ticketMock {
	var out []ticketMock
	for _, tk := range e.Tickets {
		if tk.ConversationID == conv {
			out = append(out, tk)
		}
	}
	return out
}

// eventosDoFeed conta os eventos de um tipo da conversa no feed GET /events.
func eventosDoFeed(t *testing.T, conv, tipo string) int {
	t.Helper()
	var f struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	q := url.Values{"after": {"0"}, "limit": {"500"}, "types": {tipo}, "conversationId": {conv}}
	mockGet(t, "/api/integration/v1/events?"+q.Encode(), &f)
	return len(f.Data)
}

// ------------------------------------------------------------- espera ----

// esperar faz polling de cond até ela ser verdadeira ou o limite estourar; a
// falha mostra o que `ver` observou por último (mensagem legível, não "timeout").
func esperar(t *testing.T, descricao string, limite time.Duration, cond func() bool, ver func() string) {
	t.Helper()
	fim := time.Now().Add(limite)
	for {
		if cond() {
			return
		}
		if time.Now().After(fim) {
			obs := ""
			if ver != nil {
				obs = "\nobservado: " + ver()
			}
			t.Fatalf("esperei %s por %s e não aconteceu%s", descricao, limite, obs)
		}
		time.Sleep(passoDePolling)
	}
}

// cenario prepara o teste: no fim, se falhou, mostra o fim dos logs.
func cenario(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("--- log do gateway (últimas linhas) ---\n%s", pilha.gateway.log.caudaUtil(25))
			t.Logf("--- log do mock (últimas linhas) ---\n%s", pilha.mock.log.cauda(10))
		}
	})
}

// esperarRespostaDoBot espera n bolhas públicas da Sofia na conversa.
func (c *cliente) esperarRespostaDoBot(n int) conversaMock {
	c.t.Helper()
	var ult estadoMock
	esperar(c.t, fmt.Sprintf("%d resposta(s) da Sofia no WhatsApp", n), esperaPadrao, func() bool {
		ult = c.estado()
		return ult.Conversa != nil && len(ult.Conversa.respostasDoBot()) >= n
	}, func() string { return descreverConversa(ult.Conversa) })
	return *ult.Conversa
}

func descreverConversa(c *conversaMock) string {
	if c == nil {
		return "(conversa ainda não existe no mock)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "handledBy=%s botActive=%v depto=%q mensagens:", c.HandledBy, c.BotActive, c.DepartmentName)
	for _, m := range c.Messages {
		tag := m.Sender
		if m.Internal {
			tag += "(nota)"
		}
		fmt.Fprintf(&b, "\n  %s [%s]: %q", tag, m.Type, m.Body)
	}
	return b.String()
}

func digitos(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// mascarar esconde o telefone nas mensagens de falha (regra 5 do CLAUDE.md).
func mascarar(tel string) string {
	d := digitos(tel)
	if len(d) <= 4 {
		return "****"
	}
	return "****" + d[len(d)-4:]
}

func agoraUnix() string { return strconv.FormatInt(time.Now().Unix(), 10) }
