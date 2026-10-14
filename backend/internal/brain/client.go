// Package brain é o cliente HTTP do cérebro Python (Contrato A, POST /chat).
package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"bot-azapfy/internal/mongo"
)

// Classes de falha do cérebro (mesmos valores de agente-ia/src/agent/falhas.py).
const (
	ClasseTransitoria = "transitoria" // timeout, 429, 5xx, conexão — vale repetir
	ClasseDefinitiva  = "definitiva"  // 402 crédito, 401/403 chave, 400, bug — transferir na hora
)

// Erro é a falha classificada de uma chamada ao cérebro. Quando o cérebro
// respondeu com o corpo do Contrato A (`erro: {classe, id_ocorrencia, causa}`),
// os campos vêm de lá — e o ID é o MESMO que está no log do cérebro. Quando
// não há corpo (uvicorn caiu, rede, timeout), a classe é deduzida do
// transporte/status e o ID fica vazio (quem transfere gera um).
type Erro struct {
	Classe       string
	IDOcorrencia string
	Causa        string // linguagem simples, p/ nota privada e alerta (sem PII)
	Status       int    // status HTTP, 0 = falha de transporte
	Err          error  // erro de transporte subjacente, se houver
}

func (e *Erro) Error() string {
	var b strings.Builder
	b.WriteString("brain /chat: ")
	b.WriteString(e.Classe)
	if e.Status != 0 {
		fmt.Fprintf(&b, " status %d", e.Status)
	}
	if e.Causa != "" {
		b.WriteString(": " + e.Causa)
	}
	if e.IDOcorrencia != "" {
		b.WriteString(" [" + e.IDOcorrencia + "]")
	}
	if e.Err != nil {
		b.WriteString(": " + e.Err.Error())
	}
	return b.String()
}

func (e *Erro) Unwrap() error { return e.Err }

// Transitoria informa se vale uma nova tentativa.
func (e *Erro) Transitoria() bool { return e.Classe == ClasseTransitoria }

// Chave identifica "a mesma falha" para o disjuntor (3 iguais em 5 min):
// classe + status + causa — um 402 de crédito repete a mesma chave.
func (e *Erro) Chave() string {
	return fmt.Sprintf("%s|%d|%s", e.Classe, e.Status, e.Causa)
}

// ComoErro extrai o *Erro de um erro qualquer devolvido por Chat.
func ComoErro(err error) (*Erro, bool) {
	var e *Erro
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// Client chama o serviço Python que roda o agente.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient cria o cliente do cérebro.
func NewClient(baseURL string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &Client{baseURL: baseURL, http: &http.Client{Timeout: timeout}}
}

// ChatRequest é o corpo do Contrato A. Identidade é o perfil mínimo resolvido
// pelo gate — DADO de contexto para o agente (injetado no system prompt).
type ChatRequest struct {
	ConversationID string        `json:"conversation_id"`
	Canal          string        `json:"canal"`
	Mensagem       string        `json:"mensagem"`
	Identidade     *mongo.Perfil `json:"identidade,omitempty"`
	Telefone       string        `json:"telefone,omitempty"`
	SessionToken   string        `json:"session_token,omitempty"`
}

// ChatResponse é a resposta do Contrato A.
type ChatResponse struct {
	Reply  string           `json:"reply"`
	Acoes  []map[string]any `json:"acoes"`
	Fontes []string         `json:"fontes"`
	Erro   *ErroChat        `json:"erro,omitempty"`
}

// ErroChat é o envelope `erro` do Contrato A (falha classificada do turno).
type ErroChat struct {
	Classe       string `json:"classe"`
	IDOcorrencia string `json:"id_ocorrencia"`
	Causa        string `json:"causa"`
}

// statusTransitorios: sem corpo classificável, estes status valem retry.
var statusTransitorios = map[int]bool{408: true, 429: true, 502: true, 503: true, 504: true}

// erroDeTransporte classifica falha antes de haver resposta (timeout, DNS,
// conexão recusada): sempre transitória.
func erroDeTransporte(err error) *Erro {
	causa := "falha de conexão com o cérebro"
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		causa = "cérebro não respondeu a tempo (timeout)"
	}
	return &Erro{Classe: ClasseTransitoria, Causa: causa, Err: err}
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

// erroDeStatus classifica uma resposta >= 400: usa o corpo do Contrato A
// quando o cérebro o mandou; senão deduz do status (5xx de proxy/queda =
// transitória; o resto = definitiva — um 500 sem corpo é um bug no cérebro).
func erroDeStatus(status int, body []byte) *Erro {
	var corpo ChatResponse
	if json.Unmarshal(body, &corpo) == nil && corpo.Erro != nil && corpo.Erro.Classe != "" {
		return &Erro{
			Classe:       corpo.Erro.Classe,
			IDOcorrencia: corpo.Erro.IDOcorrencia,
			Causa:        corpo.Erro.Causa,
			Status:       status,
		}
	}
	if statusTransitorios[status] {
		return &Erro{Classe: ClasseTransitoria, Causa: fmt.Sprintf("cérebro indisponível (status %d)", status), Status: status}
	}
	return &Erro{Classe: ClasseDefinitiva, Causa: fmt.Sprintf("erro não tratado no cérebro (status %d)", status), Status: status}
}

// Chat envia a mensagem ao cérebro e devolve a resposta. Toda falha sai como
// *Erro classificado (ver ComoErro): o orquestrador decide repetir
// (transitória) ou transferir ao atendente (definitiva).
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, &Erro{Classe: ClasseDefinitiva, Causa: "requisição ao cérebro inválida", Err: err}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat", bytes.NewReader(payload))
	if err != nil {
		return nil, &Erro{Classe: ClasseDefinitiva, Causa: "requisição ao cérebro inválida", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, erroDeTransporte(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, erroDeTransporte(err)
	}
	if resp.StatusCode >= 400 {
		return nil, erroDeStatus(resp.StatusCode, body)
	}
	var out ChatResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, &Erro{Classe: ClasseDefinitiva, Causa: "resposta do cérebro em formato inválido", Status: resp.StatusCode, Err: err}
	}
	// Cérebro pode devolver 200 com `erro` preenchido (compat): trata igual.
	if out.Erro != nil && out.Erro.Classe != "" {
		return nil, &Erro{Classe: out.Erro.Classe, IDOcorrencia: out.Erro.IDOcorrencia, Causa: out.Erro.Causa, Status: resp.StatusCode}
	}
	return &out, nil
}

// EncerrarEpisodioRequest é o corpo de POST /encerrar-episodio.
type EncerrarEpisodioRequest struct {
	ConversationID string `json:"conversation_id"`
}

// EncerrarEpisodio avisa o cérebro que a conversa foi RESOLVIDA no Chatwoot:
// ele limpa a proposta de chamado pendente daquela conversa (ZB-1). O Chatwoot
// reutiliza a conversa quando o contato volta a escrever, e uma proposta de
// um episódio anterior não pode ser "confirmada" por uma mensagem qualquer do
// seguinte. O histórico da conversa no cérebro fica intacto. Qualquer status
// >= 400 (ou falha de transporte) é erro — quem chama só loga: a validade da
// proposta no cérebro é a rede de segurança.
func (c *Client) EncerrarEpisodio(ctx context.Context, conversationID string) error {
	payload, err := json.Marshal(EncerrarEpisodioRequest{ConversationID: conversationID})
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/encerrar-episodio", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("brain /encerrar-episodio: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("brain /encerrar-episodio: status %d", resp.StatusCode)
	}
	return nil
}

// ExtractLoginRequest é o corpo de POST /extract-login.
type ExtractLoginRequest struct {
	Mensagem string `json:"mensagem"`
}

// ExtractLoginResponse é a resposta de POST /extract-login. Login vazio/null =
// a IA não achou um identificador na frase.
type ExtractLoginResponse struct {
	Login string `json:"login"`
}

// ExtrairLogin pede ao cérebro o identificador (login) embutido na mensagem.
// Fallback do gate quando a normalização determinística não resolve. Satisfaz
// identity.LoginExtractor. O valor é só um candidato a validar no Mongo.
func (c *Client) ExtrairLogin(ctx context.Context, mensagem string) (string, error) {
	payload, err := json.Marshal(ExtractLoginRequest{Mensagem: mensagem})
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/extract-login", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("brain /extract-login: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("brain /extract-login: status %d", resp.StatusCode)
	}
	var out ExtractLoginResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("brain decode: %w", err)
	}
	return strings.TrimSpace(out.Login), nil
}
