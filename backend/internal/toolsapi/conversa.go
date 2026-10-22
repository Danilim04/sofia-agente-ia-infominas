package toolsapi

import (
	"net/http"
	"strings"

	"bot-infominas/internal/engine"
	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
)

type reqTransferir struct {
	ConversationID string `json:"conversation_id"`
	Motivo         string `json:"motivo"`
	Categoria      string `json:"categoria"`
}

// motivosTransferir: o enum que o LLM pode passar (A6). Valor = origem da
// transferência na engine. As demais origens (mídia, falha, frustração,
// confiança...) são decisão de código e não passam por aqui.
var motivosTransferir = map[string]struct{ origem, causa string }{
	"pedido_cliente": {engine.OrigemCliente, "cliente pediu um atendente (pedido registrado pela tool)"},
	"fora_do_escopo": {engine.OrigemAcao, "assunto fora do que a Sofia resolve (pedido da tool)"},
	"nao_resolvido":  {engine.OrigemAcao, "a Sofia não resolveu o problema (pedido da tool)"},
}

// mensagemTransferencia é o texto FIXO do gateway ao cliente (o LLM só
// confirma em uma frase); sem prazo prometido, no máximo um emoji.
func mensagemTransferencia(departamento string) string {
	if departamento == "" {
		return "Estou te passando para um atendente. Ele continua com você por aqui."
	}
	return "Estou te passando para um atendente da equipe de " + departamento + ". Ele continua com você por aqui."
}

// handleTransferir — transferir_para_atendente (A6). Pelo CAMINHO ÚNICO da
// engine (ADR 0007): aviso ao cliente, ocorrência, estado `transferida` e
// `POST /conversations/:id/handoff` com a nota montada por código. O texto
// livre do LLM nunca chega ao Infodesk.
//
// Limites desta etapa (a engine não está na zona da E3.1): o departamento do
// handoff é sempre DEPARTAMENTO_HUMANO_PADRAO — `categoria` só é validada, ainda
// não escolhe o departamento (TODO(P11): Transferencia.Departamento na engine)
// — e uma falha do handoff é tratada DENTRO da engine (alerta handoff_falhou +
// vigia N1), não volta aqui como infodesk_indisponivel.
func (h *Handler) handleTransferir(w http.ResponseWriter, r *http.Request) {
	var req reqTransferir
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	conv := strings.TrimSpace(req.ConversationID)
	m, ok := motivosTransferir[req.Motivo]
	if conv == "" || !ok || (req.Categoria != "" && !reCategoria.MatchString(req.Categoria)) {
		responderRecusa(w, "campo_invalido", txtSessaoInvalida)
		return
	}

	// A conversa precisa existir no store (E4.5, M7): o id vem do cérebro, mas só
	// uma conversa que o webhook já viu pode ser transferida por esta tool.
	c, err := h.d.Store.ObterConversa(ctx, conv)
	switch {
	case err != nil:
		h.log.Error("transferir: ler conversa", "conversation_id", conv, "err", err)
		responderRecusa(w, "campo_invalido", txtSessaoInvalida)
		return
	case c == nil:
		h.log.Warn("transferir: conversa desconhecida; recusada", "conversation_id", conv)
		responderRecusa(w, "campo_invalido", txtSessaoInvalida)
		return
	case !botConduz(c):
		responderRecusa(w, "ja_transferida", txtJaTransferida)
		return
	}

	depto := h.nomeDepartamentoPadrao()
	id := h.d.Transferidor.TransferirParaHumano(ctx, conv, engine.Transferencia{
		Origem:   m.origem,
		Causa:    m.causa,
		Detalhe:  "motivo da tool: " + req.Motivo,
		Mensagem: mensagemTransferencia(depto),
	})
	if id == "" {
		responderRecusa(w, "ja_transferida", txtJaTransferida)
		return
	}
	h.log.Info("transferir_para_atendente", "conversation_id", conv, "motivo", req.Motivo, "id_ocorrencia", id)
	nome := depto
	if nome == "" {
		nome = "Atendimento"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":              true,
		"departamento":        map[string]any{"nome": nome},
		"mensagem_ao_cliente": mensagemTransferencia(depto),
	})
}

// nomeDepartamentoPadrao é o nome do departamento que a engine usa no handoff
// ("" se não configurado ou desconhecido).
func (h *Handler) nomeDepartamentoPadrao() string {
	id := strings.TrimSpace(h.op.DepartamentoHumanoPadrao)
	if id == "" || h.d.Taxonomia == nil {
		return ""
	}
	return h.d.Taxonomia.DepartamentoNome(id)
}

// botConduz espelha a regra da engine: estado `bot` (ou vazio) e o Infodesk
// não diz que um humano conduz (docs/plano/01, A6: `handled_by == EXTERNAL_BOT`).
func botConduz(c *store.Conversa) bool {
	if c.Estado != store.EstadoBot && c.Estado != "" {
		return false
	}
	return c.HandledBy == "" || c.HandledBy == infodesk.ConduzidaPorBotExterno
}
