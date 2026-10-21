package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func servidor(t *testing.T, status int, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, 2*time.Second)
}

func chamar(t *testing.T, c *Client) *Erro {
	t.Helper()
	_, err := c.Chat(context.Background(), ChatRequest{ConversationID: "1", Mensagem: "oi"})
	if err == nil {
		t.Fatal("esperava erro")
	}
	e, ok := ComoErro(err)
	if !ok {
		t.Fatalf("esperava *brain.Erro, veio %T: %v", err, err)
	}
	return e
}

// TestEncerrarEpisodioEnviaIdDaConversa (ZB-1): a resolução da conversa vira
// POST /encerrar-episodio com o conversation_id (string, como no Contrato A);
// 2xx = ok, sem erro.
func TestEncerrarEpisodioEnviaIdDaConversa(t *testing.T) {
	var caminho string
	var corpo EncerrarEpisodioRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caminho = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&corpo)
		_, _ = w.Write([]byte(`{"status":"ok","proposta_limpa":true}`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, 2*time.Second)

	if err := c.EncerrarEpisodio(context.Background(), "45"); err != nil {
		t.Fatalf("esperava sucesso, veio %v", err)
	}
	if caminho != "/encerrar-episodio" || corpo.ConversationID != "45" {
		t.Fatalf("requisição errada: path=%q corpo=%+v", caminho, corpo)
	}
}

// TestEncerrarEpisodioFalhaViraErro: status >= 400 e cérebro fora do ar viram
// erro (quem chama só loga — a limpeza é best-effort).
func TestEncerrarEpisodioFalhaViraErro(t *testing.T) {
	if err := servidor(t, 500, `{"status":"erro"}`).EncerrarEpisodio(context.Background(), "45"); err == nil {
		t.Fatal("500: esperava erro")
	}
	fora := NewClient("http://127.0.0.1:1", time.Second)
	if err := fora.EncerrarEpisodio(context.Background(), "45"); err == nil {
		t.Fatal("conexão recusada: esperava erro")
	}
}

// TestChatLeClasseEIDDoCorpo: o cérebro (ZC-2) devolve 500/503 com o envelope
// `erro` — classe, id_ocorrencia e causa chegam intactos ao gateway.
func TestChatLeClasseEIDDoCorpo(t *testing.T) {
	c := servidor(t, 500, `{"reply":"","acoes":[],"fontes":[],"erro":{"classe":"definitiva","id_ocorrencia":"SOF-20261002-0911-55","causa":"saldo do OpenRouter insuficiente (402)"}}`)
	e := chamar(t, c)
	if e.Classe != ClasseDefinitiva || e.IDOcorrencia != "SOF-20261002-0911-55" || e.Status != 500 {
		t.Fatalf("erro inesperado: %+v", e)
	}
	if e.Transitoria() {
		t.Fatal("402 de crédito é definitiva")
	}
	if e.Causa != "saldo do OpenRouter insuficiente (402)" {
		t.Fatalf("causa inesperada: %q", e.Causa)
	}
}

func TestChatSemCorpoDeduzClassePeloStatus(t *testing.T) {
	// Proxy/queda (503) sem corpo do Contrato A: transitória, sem ID.
	if e := chamar(t, servidor(t, 503, "Service Unavailable")); !e.Transitoria() || e.IDOcorrencia != "" {
		t.Fatalf("503 sem corpo: esperava transitória sem id, veio %+v", e)
	}
	// 500 sem corpo = exceção não tratada no cérebro: definitiva.
	if e := chamar(t, servidor(t, 500, "Internal Server Error")); e.Transitoria() {
		t.Fatalf("500 sem corpo: esperava definitiva, veio %+v", e)
	}
}

func TestChatTimeoutEhTransitoria(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, 50*time.Millisecond)
	e := chamar(t, c)
	if !e.Transitoria() || e.Status != 0 {
		t.Fatalf("timeout: esperava transitória de transporte, veio %+v", e)
	}
	if e.Err == nil || !errors.Is(e, e.Err) {
		t.Fatalf("Unwrap deve expor o erro de transporte: %+v", e)
	}
}

func TestChatOkNaoViraErro(t *testing.T) {
	c := servidor(t, 200, `{"reply":"olá","acoes":[],"fontes":["a.md"]}`)
	out, err := c.Chat(context.Background(), ChatRequest{ConversationID: "1", Mensagem: "oi"})
	if err != nil || out.Reply != "olá" || out.Erro != nil {
		t.Fatalf("resposta ok inesperada: %+v err=%v", out, err)
	}
}

func TestChaveDoDisjuntorAgrupaAMesmaFalha(t *testing.T) {
	a := &Erro{Classe: ClasseDefinitiva, Status: 500, Causa: "saldo do OpenRouter insuficiente (402)"}
	b := &Erro{Classe: ClasseDefinitiva, Status: 500, Causa: "saldo do OpenRouter insuficiente (402)"}
	c := &Erro{Classe: ClasseDefinitiva, Status: 500, Causa: "chave do OpenRouter inválida (401)"}
	if a.Chave() != b.Chave() || a.Chave() == c.Chave() {
		t.Fatalf("chaves: %q %q %q", a.Chave(), b.Chave(), c.Chave())
	}
}

// TestChatRequestCompativelComContratoA: o JSON de exemplo de
// contratos/contrato-a/chat-request.json decodifica sem campo desconhecido e
// volta idêntico — o ChatRequest do Go e o contrato não divergem (ADR 0006).
func TestChatRequestCompativelComContratoA(t *testing.T) {
	cru, err := os.ReadFile(filepath.Join("..", "..", "..", "contratos", "contrato-a", "chat-request.json"))
	if err != nil {
		t.Fatalf("contrato: %v", err)
	}
	var req ChatRequest
	dec := json.NewDecoder(bytes.NewReader(cru))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		t.Fatalf("o contrato tem campo que o ChatRequest não conhece: %v", err)
	}
	if req.NomeContato == "" || req.Contato == nil || req.Contato.Forma != FormaCPF || req.EstadoConversa != EstadoBot {
		t.Fatalf("campos novos não lidos: %+v", req)
	}
	volta, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var a, b map[string]any
	if err := json.Unmarshal(cru, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(volta, &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("re-serialização difere do contrato:\n contrato: %v\n go:       %v", a, b)
	}
}

// TestChatRequestContatoNuloVaiComoNull: telefone nunca identificado → `contato: null`.
func TestChatRequestContatoNuloVaiComoNull(t *testing.T) {
	b, err := json.Marshal(ChatRequest{ConversationID: "c", Canal: "whatsapp", Mensagem: "oi", EstadoConversa: EstadoBot})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"contato":null`) {
		t.Fatalf("contato nulo devia ser null no JSON: %s", b)
	}
}
