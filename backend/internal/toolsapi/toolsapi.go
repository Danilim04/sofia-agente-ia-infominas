// Package toolsapi expõe a API interna de tools de dados que o cérebro Python
// chama durante o loop do agente (abrir/listar chamados, consultar ocorrências).
//
// Princípio de segurança: os campos do RELATOR (nome, email=cod_relator,
// telefone, grupo) vêm SEMPRE da identidade resolvida no gate (cache
// telefone→perfil) — nunca de argumentos do LLM. O agente só fornece conteúdo
// (resumo/descrição) e classificação (categoria/ocorrência/prioridade), que aqui
// são validados contra a configuração do SAC antes de criar o chamado. Antes
// de preparar ou abrir, os chamados EM ABERTO do relator são consultados: com
// chamado aberto, só um pedido explícito de chamado novo (`novo_chamado`)
// permite seguir (ZB-2).
//
// As rotas são protegidas por um token compartilhado (header X-Tools-Token).
package toolsapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"unicode"

	"bot-azapfy/internal/mongo"
	"bot-azapfy/internal/sac"
	"bot-azapfy/internal/store"
)

// SACClient é o subconjunto do cliente SAC usado aqui (interface p/ testar).
type SACClient interface {
	GrupoEmp() string
	TemContaServico() bool
	BuscarConfig(ctx context.Context, grupoEmp string) (sac.Config, error)
	Criar(ctx context.Context, ch sac.NovoChamado) (string, error)
	DefinirPrioridade(ctx context.Context, protocolo, prioridade string) error
	BuscarRelator(ctx context.Context, email string) ([]sac.Chamado, error)
	LinkChamado(codRelator, grupoEmp, protocolo string) string
}

// Handler serve a API de tools.
type Handler struct {
	store store.Store
	sac   SACClient
	token string
	log   *slog.Logger
}

// New cria o handler.
func New(st store.Store, sacClient SACClient, token string, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{store: st, sac: sacClient, token: token, log: log}
}

// Register registra as rotas no mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/tools/sac/tipos", h.auth(h.handleTipos))
	mux.HandleFunc("/tools/sac/preparar", h.auth(h.handlePreparar))
	mux.HandleFunc("/tools/sac/criar", h.auth(h.handleCriar))
	mux.HandleFunc("/tools/sac/listar", h.auth(h.handleListar))
}

var prioridadesValidas = map[string]bool{"BAIXA": true, "MEDIA": true, "ALTA": true, "URGENTE": true}

// Tetos do conteúdo gerado pelo LLM em /criar — o SAC não impõe limite, nós
// impomos aqui (input não-confiável nunca chega cru no sistema de chamados).
const (
	resumoMax    = 200  // título de 1 linha
	descricaoMax = 4000 // corpo do incidente
	corpoMax     = 64 << 10
)

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

type reqTelefone struct {
	Telefone       string `json:"telefone"`
	ConversationID string `json:"conversation_id"` // opcional: id da conversa (Contrato A), só para correlação nos logs
}

func (h *Handler) handleTipos(w http.ResponseWriter, r *http.Request) {
	var req reqTelefone
	if !decode(w, r, &req) {
		return
	}
	perfil, ok := h.resolver(w, r.Context(), req.Telefone)
	if !ok {
		return
	}
	cfg, err := h.sac.BuscarConfig(r.Context(), h.sac.GrupoEmp())
	if err != nil {
		h.log.Error("toolsapi tipos: buscarConfig", "conversation_id", req.ConversationID, "err", err)
		writeJSON(w, http.StatusOK, map[string]any{"status": false, "erro": "não consegui consultar as opções de chamado agora"})
		return
	}
	_ = perfil
	writeJSON(w, http.StatusOK, map[string]any{
		"status":      true,
		"categorias":  cfg.Categorias,
		"ocorrencias": opcoesOcorrencias(cfg),
	})
}

func opcoesOcorrencias(cfg sac.Config) []map[string]any {
	tipos := cfg.TiposValidos()
	out := make([]map[string]any, 0, len(tipos))
	for _, t := range tipos {
		out = append(out, map[string]any{
			"categoria":  t.Categoria,
			"ocorrencia": t.Nome,
			"descricao":  t.Descricao,
			"prazo":      t.Prazo,
		})
	}
	return out
}

type reqCriar struct {
	Telefone       string `json:"telefone"`
	ConversationID string `json:"conversation_id"` // opcional: id da conversa (Contrato A), só para correlação nos logs
	GrupoEmp       string `json:"grupo_emp"`       // opcional: desambigua quando o usuário tem várias empresas
	Categoria      string `json:"categoria"`
	Ocorrencia     string `json:"ocorrencia"`
	Prioridade     string `json:"prioridade"`
	Resumo         string `json:"resumo"`
	Descricao      string `json:"descricao"`
	// NovoChamado: o cliente pediu EXPLICITAMENTE um chamado novo mesmo tendo
	// chamado(s) em aberto (ZB-2). Sem ele, relator com chamado aberto é
	// recusado com motivo `chamado_aberto` e a lista dos abertos. O cérebro só
	// manda true depois de a política dele conferir o pedido na mensagem do
	// cliente; a proposta do dry-run carrega o valor para a abertura.
	NovoChamado bool `json:"novo_chamado"`
}

// pedidoValidado é o resultado da análise campo a campo de um pedido de
// abertura: conteúdo sanitizado, empresa resolvida contra o perfil e
// classificação autoritativa da config do SAC.
type pedidoValidado struct {
	telefone       string
	conversationID string // só para correlação nos logs (pode vir vazio)
	resumo         string
	descricao      string
	clienteGrupo   string
	prioridade     string
	tipo           sac.Tipo
	perfil         *mongo.Perfil
	novoChamado    bool // pedido explícito de chamado novo apesar de abertos (ZB-2)
}

// validarCriar decodifica e analisa CADA campo do pedido, sem confiar no
// input (que vem do LLM). É o caminho único de /preparar (dry-run) e /criar —
// o que o dry-run aprovou é exatamente o que a abertura aceita. Em recusa,
// escreve a resposta (com a lista de opções quando a classificação não casa,
// para o agente corrigir no próprio loop) e devolve ok=false.
func (h *Handler) validarCriar(w http.ResponseWriter, r *http.Request) (*pedidoValidado, bool) {
	var req reqCriar
	if !decode(w, r, &req) {
		return nil, false
	}
	telefone := strings.TrimSpace(req.Telefone)
	perfil, ok := h.resolver(w, r.Context(), telefone)
	if !ok {
		return nil, false
	}
	// O SAC exige o e-mail do relator (cod_relator) e devolve 422 sem ele.
	// Cadastro sem e-mail é recusado AQUI — antes de qualquer chamada ao SAC
	// (inclusive a de config) — com um motivo que o cérebro sabe explicar. O
	// e-mail vem do cadastro, nunca da conversa: pedir ao cliente não resolve.
	if strings.TrimSpace(perfil.Email) == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": false, "motivo": "sem_email",
			"erro": "o cadastro do cliente não tem e-mail e o SAC exige e-mail para abrir chamado — não é possível abrir por aqui; explique isso ao cliente, oriente-o a regularizar o e-mail do cadastro e ofereça encaminhar a um atendente (não peça o e-mail na conversa: o dado tem que vir do cadastro)",
		})
		return nil, false
	}
	// Conteúdo do LLM é analisado campo a campo, nunca repassado cru: controle
	// fora, resumo vira 1 linha, e texto que sobrevive à limpeza é obrigatório.
	resumo, resumoTruncado := sanitizarTexto(req.Resumo, resumoMax, true)
	descricao, descTruncada := sanitizarTexto(req.Descricao, descricaoMax, false)
	if resumo == "" || descricao == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": false, "erro": "resumo e descricao são obrigatórios (texto real, não só espaços)",
			"motivo": "campo_invalido",
		})
		return nil, false
	}
	if resumoTruncado || descTruncada {
		h.log.Warn("toolsapi criar: conteúdo truncado no teto",
			"resumo_truncado", resumoTruncado, "descricao_truncada", descTruncada)
	}
	// A empresa do chamado (incidente.empresa) é SEMPRE uma do perfil do
	// cliente — nunca a equipe (SAC_GRUPO_EMP) nem um valor fixo. Perfil sem
	// grupo não tem como abrir chamado: recusa com motivo próprio (não é caso
	// de desambiguação).
	if len(gruposDe(perfil)) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": false, "motivo": "sem_empresa",
			"erro": "o cadastro do cliente não tem empresa ativa vinculada, e o chamado precisa ser aberto em nome de uma empresa — não é possível abrir por aqui; explique isso ao cliente e ofereça encaminhar a um atendente",
		})
		return nil, false
	}
	clienteGrupo, ok := escolherGrupo(perfil, req.GrupoEmp)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": false, "erro": "informe a empresa do chamado", "motivo": "empresa_ambigua",
			"empresas": gruposDe(perfil),
		})
		return nil, false
	}

	cfg, err := h.sac.BuscarConfig(r.Context(), h.sac.GrupoEmp())
	if err != nil {
		h.log.Error("toolsapi criar: buscarConfig", "conversation_id", req.ConversationID, "err", err)
		writeJSON(w, http.StatusOK, map[string]any{"status": false, "erro": "não consegui validar a categoria agora"})
		return nil, false
	}
	tipo, achou := cfg.AcharTipo(req.Categoria, req.Ocorrencia)
	if !achou {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": false, "erro": "categoria/ocorrência inválida — escolha uma das opções em `ocorrencias`",
			"motivo": "ocorrencia_invalida", "ocorrencias": opcoesOcorrencias(cfg),
		})
		return nil, false
	}

	prioridade := normalizarPrioridade(req.Prioridade)
	if proposta := strings.ToUpper(strings.TrimSpace(req.Prioridade)); proposta != "" && proposta != prioridade {
		h.log.Warn("toolsapi criar: prioridade inválida normalizada",
			"proposta", req.Prioridade, "usada", prioridade)
	}

	// Chamados em aberto do relator ANTES de preparar/abrir outro (ZB-2): o
	// cliente que já tem chamado aberto quase sempre está falando dele (na
	// conversa #45 citou o protocolo e ganhou um chamado novo). Os abertos
	// voltam ao cérebro e a recusa só cai com o pedido EXPLÍCITO de chamado
	// novo. Falha ao consultar = recusa (não abrimos no escuro).
	abertos, err := h.chamadosAbertos(r.Context(), perfil)
	if err != nil {
		h.log.Error("toolsapi criar: buscarRelator", "conversation_id", req.ConversationID, "err", err)
		writeJSON(w, http.StatusOK, map[string]any{
			"status": false, "motivo": "chamados_indisponiveis",
			"erro": "não consegui conferir os chamados em aberto do cliente agora — não abra um chamado; tente de novo em instantes ou ofereça encaminhar a um atendente",
		})
		return nil, false
	}
	if len(abertos) > 0 && !req.NovoChamado {
		h.log.Info("toolsapi criar: recusado — relator com chamado em aberto",
			"conversation_id", req.ConversationID, "abertos", len(abertos))
		writeJSON(w, http.StatusOK, map[string]any{
			"status": false, "motivo": "chamado_aberto",
			"total": len(abertos), "chamados_abertos": abertos,
			"erro": "o cliente já tem chamado(s) em aberto — não abra outro por conta própria: mostre a ele os chamados abertos (protocolo, resumo e LINK) e pergunte se o problema é de um deles; o atendimento segue pelo chat do chamado. Só prepare um chamado NOVO se o cliente pedir isso explicitamente, repetindo a chamada com novo_chamado=true",
		})
		return nil, false
	}
	if len(abertos) > 0 {
		h.log.Info("toolsapi criar: chamado novo com pedido explícito apesar de abertos",
			"conversation_id", req.ConversationID, "abertos", len(abertos))
	}
	return &pedidoValidado{
		telefone: telefone, conversationID: strings.TrimSpace(req.ConversationID),
		resumo: resumo, descricao: descricao,
		clienteGrupo: clienteGrupo, prioridade: prioridade, tipo: tipo, perfil: perfil,
		novoChamado: req.NovoChamado,
	}, true
}

// chamadosAbertos lista os chamados EM ABERTO do relator (pendente / em
// andamento), já no formato devolvido ao cérebro (com link). Usado por
// /listar e pela checagem de duplicidade de /preparar e /criar.
func (h *Handler) chamadosAbertos(ctx context.Context, perfil *mongo.Perfil) ([]map[string]any, error) {
	chamados, err := h.sac.BuscarRelator(ctx, perfil.Email)
	if err != nil {
		return nil, err
	}
	abertos := make([]map[string]any, 0, len(chamados))
	for _, ch := range chamados {
		if !ch.Aberto() {
			continue
		}
		abertos = append(abertos, map[string]any{
			"protocolo":   ch.Protocolo,
			"resumo":      ch.Incidente.Resumo,
			"status":      ch.Status,
			"categoria":   ch.Incidente.Categoria,
			"ocorrencia":  ch.Incidente.Ocorrencia,
			"dt_abertura": ch.DtAbertura,
			"link":        h.sac.LinkChamado(perfil.Email, ch.GrupoEmp, ch.Protocolo),
		})
	}
	return abertos, nil
}

// handlePreparar é o dry-run da abertura: valida tudo que /criar validaria e
// devolve a proposta canônica SEM criar nada. O cérebro guarda essa proposta
// no estado da conversa e a abertura (pós-confirmação do cliente) executa
// exatamente ela.
func (h *Handler) handlePreparar(w http.ResponseWriter, r *http.Request) {
	p, ok := h.validarCriar(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": true,
		"proposta": map[string]any{
			"resumo":     p.resumo,
			"descricao":  p.descricao,
			"categoria":  p.tipo.Categoria,
			"ocorrencia": p.tipo.Nome,
			"prioridade": p.prioridade,
			"empresa":    p.clienteGrupo,
			"prazo":      p.tipo.Prazo,
			// A abertura repete o pedido explícito: com chamado aberto no meio
			// do caminho, /criar sem ele recusa de novo (chamado_aberto).
			"novo_chamado": p.novoChamado,
		},
	})
}

func (h *Handler) handleCriar(w http.ResponseWriter, r *http.Request) {
	p, ok := h.validarCriar(w, r)
	if !ok {
		return
	}
	perfil, tipo, prioridade := p.perfil, p.tipo, p.prioridade
	proto, err := h.sac.Criar(r.Context(), sac.NovoChamado{
		NomeRelator:  perfil.Nome,
		Email:        perfil.Email,
		Telefone:     p.telefone,
		ClienteGrupo: p.clienteGrupo,
		Categoria:    tipo.Categoria, // autoritativo (da config)
		Ocorrencia:   tipo.Nome,
		Item:         tipo.Item,
		Prazo:        tipo.Prazo,
		Resumo:       p.resumo,
		Descricao:    p.descricao,
	})
	if err != nil {
		// O erro do cliente SAC já traz o corpo da resposta higienizado (sem
		// dado pessoal); o id da conversa liga a falha ao atendimento.
		h.log.Error("toolsapi criar: sac.Criar", "conversation_id", p.conversationID,
			"empresa", p.clienteGrupo, "err", err)
		writeJSON(w, http.StatusOK, map[string]any{"status": false, "erro": "não consegui abrir o chamado agora"})
		return
	}

	prioridadeDefinida := false
	if err := h.sac.DefinirPrioridade(r.Context(), proto, prioridade); err != nil {
		// Best-effort: o chamado já existe; só a prioridade não foi setada.
		h.log.Warn("toolsapi criar: prioridade não definida", "conversation_id", p.conversationID,
			"protocolo", proto, "err", err)
	} else {
		prioridadeDefinida = true
	}

	link := h.sac.LinkChamado(perfil.Email, h.sac.GrupoEmp(), proto)
	h.log.Info("chamado aberto via bot", "conversation_id", p.conversationID, "protocolo", proto,
		"empresa", p.clienteGrupo, "categoria", tipo.Categoria, "ocorrencia", tipo.Nome,
		"prioridade", prioridade, "prioridade_definida", prioridadeDefinida)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":              true,
		"protocolo":           proto,
		"link":                link,
		"categoria":           tipo.Categoria,
		"ocorrencia":          tipo.Nome,
		"prioridade":          prioridade,
		"prioridade_definida": prioridadeDefinida,
	})
}

func (h *Handler) handleListar(w http.ResponseWriter, r *http.Request) {
	var req reqTelefone
	if !decode(w, r, &req) {
		return
	}
	perfil, ok := h.resolver(w, r.Context(), req.Telefone)
	if !ok {
		return
	}
	if strings.TrimSpace(perfil.Email) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"status": false, "erro": "sem e-mail na identidade para consultar chamados"})
		return
	}
	abertos, err := h.chamadosAbertos(r.Context(), perfil)
	if err != nil {
		h.log.Error("toolsapi listar: buscarRelator", "conversation_id", req.ConversationID, "err", err)
		writeJSON(w, http.StatusOK, map[string]any{"status": false, "erro": "não consegui consultar seus chamados agora"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": true, "total": len(abertos), "chamados": abertos})
}

// ---------------------------------------------------------------------------
// Auxiliares
// ---------------------------------------------------------------------------

// resolver carrega a identidade em cache pelo telefone. Se ausente/expirada,
// responde {status:false, motivo:"nao_identificado"} e devolve ok=false.
func (h *Handler) resolver(w http.ResponseWriter, ctx context.Context, telefone string) (*mongo.Perfil, bool) {
	telefone = strings.TrimSpace(telefone)
	if telefone == "" {
		writeJSON(w, http.StatusOK, map[string]any{"status": false, "erro": "telefone ausente", "motivo": "nao_identificado"})
		return nil, false
	}
	ci, err := h.store.GetIdentity(ctx, telefone)
	if err != nil {
		h.log.Error("toolsapi: getIdentity", "err", err)
		writeJSON(w, http.StatusOK, map[string]any{"status": false, "erro": "falha ao resolver identidade"})
		return nil, false
	}
	if ci == nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": false, "erro": "usuário não identificado", "motivo": "nao_identificado"})
		return nil, false
	}
	var perfil mongo.Perfil
	if err := json.Unmarshal([]byte(ci.Perfil), &perfil); err != nil {
		h.log.Error("toolsapi: perfil inválido em cache", "err", err)
		writeJSON(w, http.StatusOK, map[string]any{"status": false, "erro": "identidade inválida"})
		return nil, false
	}
	return &perfil, true
}

// escolherGrupo decide a empresa do chamado (incidente.empresa). Com 1 empresa,
// usa-a; com várias, exige `grupoPedido` ∈ empresas do usuário. Devolve
// ok=false se ambíguo (ou sem empresa alguma).
func escolherGrupo(p *mongo.Perfil, grupoPedido string) (string, bool) {
	grupos := gruposDe(p)
	if len(grupos) == 0 {
		return "", false
	}
	if grupoPedido = strings.TrimSpace(grupoPedido); grupoPedido != "" {
		for _, g := range grupos {
			if strings.EqualFold(g, grupoPedido) {
				return g, true
			}
		}
		return "", false // pediu uma empresa que o usuário não tem acesso
	}
	if len(grupos) == 1 {
		return grupos[0], true
	}
	return "", false // várias empresas e nenhuma escolhida → desambiguar
}

func gruposDe(p *mongo.Perfil) []string {
	out := make([]string, 0, len(p.Empresas))
	for _, e := range p.Empresas {
		if e.GrupoEmpresa != "" {
			out = append(out, e.GrupoEmpresa)
		}
	}
	return out
}

// sanitizarTexto limpa texto vindo do LLM antes de entrar no SAC: caracteres
// de controle viram espaço (\n e \t sobrevivem quando multilinha), texto de
// 1 linha tem os espaços colapsados, e o resultado é trimado e cortado em
// `max` runas. Devolve também se houve truncamento (para log).
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

func normalizarPrioridade(p string) string {
	p = strings.ToUpper(strings.TrimSpace(p))
	if prioridadesValidas[p] {
		return p
	}
	return "MEDIA"
}

func (h *Handler) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
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

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	// Input não-confiável não dita o tamanho: corpo além do teto é recusado.
	r.Body = http.MaxBytesReader(w, r.Body, corpoMax)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"status": false, "erro": "json inválido ou grande demais"})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
