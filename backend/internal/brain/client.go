// Package brain é o cliente HTTP do cérebro Python (Contrato A, POST /chat).
package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"bot-azapfy/internal/mongo"
)

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
}

// Chat envia a mensagem ao cérebro e devolve a resposta.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("brain /chat: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("brain /chat: status %d", resp.StatusCode)
	}
	var out ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("brain decode: %w", err)
	}
	return &out, nil
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
