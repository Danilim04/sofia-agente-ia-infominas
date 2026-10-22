//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// cerebroFalso é um servidor HTTP mínimo que fala o Contrato A
// (contratos/contrato-a/) de forma scriptada, no lugar do cérebro Python:
// nada de LLM, JEV nem rede. Para o turno de ticket ele faz o que o cérebro
// real faz — chama `preparar` e `abrir` em /tools/infodesk/* do gateway com
// o token compartilhado — de modo que a abertura de chamado passa pelo
// gateway de verdade.
//
// O roteiro é decidido pelo texto da mensagem:
//
//	"internet caiu" → prepara o chamado e pede confirmação
//	"sim"           → abre o chamado preparado nesta conversa
//	qualquer outra  → resposta genérica
type cerebroFalso struct {
	srv        *httptest.Server
	gatewayURL string

	mu        sync.Mutex
	chamadas  []chamadaChat           // todo POST /chat recebido, em ordem
	propostas map[string]propostaFase // conversation_id → proposta aguardando "sim"
	tools     []chamadaTool           // o que o roteiro mandou ao gateway
}

// chamadaChat é um POST /chat recebido (o ChatRequest do Contrato A).
type chamadaChat struct {
	ConversationID string         `json:"conversation_id"`
	Canal          string         `json:"canal"`
	Mensagem       string         `json:"mensagem"`
	Telefone       string         `json:"telefone"`
	Contato        map[string]any `json:"contato"`
	EstadoConversa string         `json:"estado_conversa"`
}

// chamadaTool registra uma chamada do roteiro à API de tools do gateway.
type chamadaTool struct {
	ConversationID string
	Rota           string
	Resposta       map[string]any
}

type propostaFase struct {
	proposta map[string]any
	criadaEm string
}

func novoCerebroFalso(gatewayURL string) *cerebroFalso {
	c := &cerebroFalso{gatewayURL: gatewayURL, propostas: map[string]propostaFase{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/chat", c.chat)
	mux.HandleFunc("/encerrar-episodio", func(w http.ResponseWriter, _ *http.Request) {
		escreverJSON(w, 200, map[string]any{"status": "ok", "proposta_limpa": true})
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		escreverJSON(w, 200, map[string]any{"status": "ok"})
	})
	c.srv = httptest.NewServer(mux)
	return c
}

// chamadasDa devolve os POST /chat da conversa.
func (c *cerebroFalso) chamadasDa(conv string) []chamadaChat {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []chamadaChat
	for _, ch := range c.chamadas {
		if ch.ConversationID == conv {
			out = append(out, ch)
		}
	}
	return out
}

func (c *cerebroFalso) toolsDa(conv string) []chamadaTool {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []chamadaTool
	for _, t := range c.tools {
		if t.ConversationID == conv {
			out = append(out, t)
		}
	}
	return out
}

func (c *cerebroFalso) chat(w http.ResponseWriter, r *http.Request) {
	var req chamadaChat
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "json inválido", http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	c.chamadas = append(c.chamadas, req)
	c.mu.Unlock()

	msg := strings.ToLower(strings.TrimSpace(req.Mensagem))
	switch {
	case strings.Contains(msg, "internet caiu"):
		escreverJSON(w, 200, c.prepararChamado(req))
	case msg == "sim":
		escreverJSON(w, 200, c.abrirChamado(req))
	default:
		escreverJSON(w, 200, respostaChat("Oi! Eu sou a Sofia, da Infominas. Me conta o que está acontecendo?", nil))
	}
}

// classificacaoSemConexao é a classificação que o JEV real devolveria.
var classificacaoSemConexao = map[string]any{
	"intencao": "reclamacao_problema", "categoria": "sem_conexao",
	"prioridade": "alta", "confianca": 0.91, "origem": "jev",
}

func respostaChat(reply string, cls map[string]any) map[string]any {
	return map[string]any{"reply": reply, "acoes": []any{}, "classificacao": cls, "fontes": []any{}, "erro": nil}
}

// prepararChamado: 1ª fase (dry-run). Guarda a proposta e pede confirmação.
func (c *cerebroFalso) prepararChamado(req chamadaChat) map[string]any {
	out := c.chamarTool(req.ConversationID, "ticket/preparar", map[string]any{
		"telefone": req.Telefone, "conversation_id": req.ConversationID,
		"titulo":    "Internet sem conexão desde ontem à noite",
		"descricao": "Cliente relata internet totalmente fora desde ontem à noite; roteador reiniciado duas vezes, luzes vermelhas.",
		"categoria": "sem_conexao", "prioridade": "alta",
	})
	prop, _ := out["proposta"].(map[string]any)
	if ok, _ := out["status"].(bool); !ok || prop == nil {
		return respostaChat(fmt.Sprintf("[cérebro falso] preparar recusado: motivo=%v erro=%v", out["motivo"], out["erro"]), classificacaoSemConexao)
	}
	criada := time.Now().Format(time.RFC3339)
	c.mu.Lock()
	c.propostas[req.ConversationID] = propostaFase{proposta: prop, criadaEm: criada}
	c.mu.Unlock()
	return respostaChat("Poxa, que chato ficar sem internet. Posso abrir um chamado para o nosso suporte com esse resumo? Responde \"sim\" para confirmar.", classificacaoSemConexao)
}

// abrirChamado: 2ª fase. A confirmação ("sim") já foi checada pelo roteiro.
func (c *cerebroFalso) abrirChamado(req chamadaChat) map[string]any {
	c.mu.Lock()
	pf, ok := c.propostas[req.ConversationID]
	c.mu.Unlock()
	if !ok {
		return respostaChat("Certo! Sobre o que você quer falar?", nil)
	}
	prop := map[string]any{}
	for k, v := range pf.proposta {
		prop[k] = v
	}
	prop["criada_em"] = pf.criadaEm
	titulo, _ := prop["titulo"].(string)
	categoria, _ := prop["categoria"].(string)
	soma := sha256.Sum256([]byte(req.ConversationID + titulo + categoria + pf.criadaEm))
	out := c.chamarTool(req.ConversationID, "ticket/abrir", map[string]any{
		"telefone": req.Telefone, "conversation_id": req.ConversationID,
		"proposta": prop, "chave_idempotencia": hex.EncodeToString(soma[:]),
	})
	if ok, _ := out["status"].(bool); !ok {
		return respostaChat(fmt.Sprintf("[cérebro falso] abrir recusado: motivo=%v erro=%v", out["motivo"], out["erro"]), classificacaoSemConexao)
	}
	return respostaChat(fmt.Sprintf("Pronto! Abri o chamado %v e já passei para um atendente seguir com você.", out["numero"]), classificacaoSemConexao)
}

func (c *cerebroFalso) chamarTool(conv, rota string, corpo map[string]any) map[string]any {
	b, _ := json.Marshal(corpo)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, c.gatewayURL+"/tools/infodesk/"+rota, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tools-Token", tokenTools)
	out := map[string]any{"status": false, "motivo": "falha_de_transporte"}
	cli := &http.Client{Timeout: 40 * time.Second}
	if resp, err := cli.Do(req); err != nil {
		out["erro"] = err.Error()
	} else {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err := json.Unmarshal(raw, &out); err != nil || resp.StatusCode != 200 {
			out = map[string]any{"status": false, "motivo": fmt.Sprintf("http_%d", resp.StatusCode), "erro": string(raw)}
		}
	}
	c.mu.Lock()
	c.tools = append(c.tools, chamadaTool{ConversationID: conv, Rota: rota, Resposta: out})
	c.mu.Unlock()
	return out
}

func escreverJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
