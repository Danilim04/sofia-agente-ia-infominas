// Package toolsapi expõe a API interna de tools que o cérebro Python chama
// durante o loop do agente (`/tools/infodesk/*`). As tools Python nunca falam
// com o Infodesk: tudo passa por aqui (regra 2 do CLAUDE.md).
//
// Princípio de segurança: o que identifica o cliente (telefone, identificação
// validada) e o que decide mesa/departamento/prioridade vem do gateway, da
// tabela de taxonomia ou do estado de sessão — nunca de argumentos do LLM.
// O agente só fornece conteúdo (título/descrição), que é sanitizado aqui antes
// de chegar ao Infodesk.
//
// Convenção de resposta (todas as rotas, sempre 200 em recusa de negócio):
//
//	{"status": true, ...}                           // sucesso
//	{"status": false, "erro": "...", "motivo": "x"} // recusa; `motivo` é um
//	                                                // código estável que o
//	                                                // cérebro sabe explicar
//
// HTTP 401/400/405 só para erro de protocolo (token, JSON, método). O erro do
// Infodesk nunca chega ao LLM nem ao cliente: vira um `motivo` do contrato.
//
// Rotas (docs/plano/01-ferramentas.md, seção A; exemplos em contratos/):
//
//	POST /tools/infodesk/contato/identificar   informar_identificacao
//	POST /tools/infodesk/ticket/preparar       preparar_abertura_chamado
//	POST /tools/infodesk/ticket/abrir          abrir_chamado
//	POST /tools/infodesk/ticket/consultar      consultar_status_chamado
//	POST /tools/infodesk/ticket/listar         listar_meus_chamados
//	POST /tools/infodesk/conversa/transferir   transferir_para_atendente
//	POST /tools/infodesk/cadastro/preparar     preparar_cadastro_solicitante
//	POST /tools/infodesk/cadastro/cadastrar    cadastrar_solicitante
//
// Protegidas por token compartilhado (header X-Tools-Token).
package toolsapi

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode"
)

// Handler serve a API de tools.
type Handler struct {
	token string
	log   *slog.Logger
	d     Deps
	op    Opcoes
}

// New cria o handler. Sem Deps.Token a API recusa tudo.
func New(d Deps) *Handler {
	d.Log = logOuPadrao(d.Log)
	if d.Agora == nil {
		d.Agora = time.Now
	}
	op := d.Opcoes.comPadroes()
	return &Handler{token: d.Token, log: d.Log, d: d, op: op}
}

func logOuPadrao(l *slog.Logger) *slog.Logger {
	if l == nil {
		return slog.Default()
	}
	return l
}

func (h *Handler) agora() time.Time { return h.d.Agora() }

// Register registra as rotas no mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/tools/infodesk/ping", h.auth(h.handlePing))
	mux.HandleFunc("/tools/infodesk/contato/identificar", h.post(h.handleIdentificar))
	mux.HandleFunc("/tools/infodesk/ticket/preparar", h.post(h.handlePreparar))
	mux.HandleFunc("/tools/infodesk/ticket/abrir", h.post(h.handleAbrir))
	mux.HandleFunc("/tools/infodesk/ticket/consultar", h.post(h.handleConsultar))
	mux.HandleFunc("/tools/infodesk/ticket/listar", h.post(h.handleListar))
	mux.HandleFunc("/tools/infodesk/conversa/transferir", h.post(h.handleTransferir))
	mux.HandleFunc("/tools/infodesk/cadastro/preparar", h.post(h.handlePrepararCadastro))
	mux.HandleFunc("/tools/infodesk/cadastro/cadastrar", h.post(h.handleCadastrar))
}

// corpoMax é o teto do corpo de qualquer requisição: input não-confiável não
// dita o tamanho.
const corpoMax = 64 << 10

// handlePing prova que o cérebro alcança o gateway com o token certo
// (health da fiação cérebro↔gateway; usado pelos testes e pelo E2E).
func (h *Handler) handlePing(w http.ResponseWriter, r *http.Request) {
	h.log.Debug("toolsapi ping", "metodo", r.Method)
	writeJSON(w, http.StatusOK, map[string]any{"status": true, "servico": "sofia-gateway"})
}

// sanitizarTexto limpa texto vindo do LLM antes de entrar no Infodesk:
// caracteres de controle viram espaço (\n e \t sobrevivem quando multilinha),
// texto de 1 linha tem os espaços colapsados, e o resultado é trimado e
// cortado em `max` runas. Devolve também se houve truncamento (para log).
func sanitizarTexto(s string, max int, umaLinha bool) (string, bool) {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			if umaLinha {
				b.WriteRune(' ')
			} else {
				b.WriteRune(r)
			}
		case unicode.IsControl(r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	out := b.String()
	if umaLinha {
		out = strings.Join(strings.Fields(out), " ")
	} else {
		out = strings.TrimSpace(out)
	}
	runas := []rune(out)
	if len(runas) <= max {
		return out, false
	}
	return strings.TrimSpace(string(runas[:max])), true
}

// auth exige o token compartilhado em X-Tools-Token (comparação em tempo
// constante) e só aceita GET/POST. Sem token configurado, TUDO é recusado.
func (h *Handler) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		got := r.Header.Get("X-Tools-Token")
		if h.token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(h.token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// post é auth + só POST (as rotas de tool têm corpo).
func (h *Handler) post(next http.HandlerFunc) http.HandlerFunc {
	return h.auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next(w, r)
	})
}

// decode lê o corpo JSON em v, com teto de tamanho. Em erro, responde 400 na
// convenção {status:false, erro, motivo} e devolve false.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, corpoMax)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"status": false, "erro": "json inválido ou grande demais", "motivo": "json_invalido"})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// recusa é o envelope de erro de negócio (HTTP 200).
func recusa(motivo, erro string) map[string]any {
	return map[string]any{"status": false, "erro": erro, "motivo": motivo}
}

func responderRecusa(w http.ResponseWriter, motivo, erro string) {
	writeJSON(w, http.StatusOK, recusa(motivo, erro))
}
