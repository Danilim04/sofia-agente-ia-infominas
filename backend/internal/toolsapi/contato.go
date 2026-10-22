package toolsapi

import (
	"net/http"
	"strings"

	"bot-infominas/internal/store"
)

type reqIdentificar struct {
	Telefone       string `json:"telefone"`
	ConversationID string `json:"conversation_id"`
	CPF            string `json:"cpf"`
	Contrato       string `json:"contrato"`
}

// handleIdentificar — informar_identificacao (A1). Fallback do lookup: grava
// em `contatos` o CPF/contrato que o cliente informou, só como hash + sal e
// valor mascarado (o dado em claro existe apenas neste turno, em memória).
// Mantém o requester_id/cliente_id que o lookup do telefone já tinha achado.
func (h *Handler) handleIdentificar(w http.ResponseWriter, r *http.Request) {
	var req reqIdentificar
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	_, tel, ok := h.sessaoEstrita(ctx, req.ConversationID, req.Telefone)
	if !ok {
		responderRecusa(w, "campo_invalido", txtSessaoInvalida)
		return
	}

	cpf, contrato := somenteDigitos(req.CPF), strings.TrimSpace(req.Contrato)
	var forma, valor, mascarado string
	switch {
	case cpf != "":
		if !cpfValido(cpf) {
			responderRecusa(w, "campo_invalido", txtIdentCPFInvalido)
			return
		}
		forma, valor, mascarado = store.FormaCPF, cpf, mascararCPF(cpf)
	case contrato != "":
		if !h.op.contratoRE.MatchString(contrato) {
			responderRecusa(w, "campo_invalido", txtIdentContratoInvalido)
			return
		}
		forma, valor, mascarado = store.FormaContrato, contrato, mascararContrato(contrato)
	default:
		responderRecusa(w, "nao_identificado", txtIdentNenhum)
		return
	}

	sal := h.op.IdentSal
	if sal == "" {
		sal = h.token
	}
	c := &store.Contato{
		Telefone:       tel,
		Origem:         forma, // store.OrigemCPF / OrigemContrato têm o mesmo valor da forma
		Forma:          forma,
		ValorMascarado: mascarado,
		ValorHash:      hashComSal(sal, valor),
		ExpiresAt:      h.agora().Add(h.op.IdentTTL),
	}
	// Preserva o que o lookup achou (requester_id alimenta o vínculo da
	// conversa e a posse em `consultar`). Contato expirado = sem aproveitar.
	if antigo, err := h.d.Store.ObterContato(ctx, tel, h.agora()); err != nil {
		h.log.Error("ler contato antes de identificar", "err", err)
	} else if antigo != nil {
		c.RequesterID, c.ClienteID, c.NomeCadastro = antigo.RequesterID, antigo.ClienteID, antigo.NomeCadastro
	}
	if err := h.d.Store.SalvarContato(ctx, c); err != nil {
		h.log.Error("gravar identificação", "conversation_id", req.ConversationID, "err", err)
		responderRecusa(w, "nao_identificado", txtIdentNenhum)
		return
	}
	h.log.Info("cliente identificado", "conversation_id", req.ConversationID, "forma", forma, "valor", mascarado,
		"telefone", mascararTelefone(tel), "com_lookup", c.RequesterID != "")

	var clienteID, nome any
	if c.ClienteID != "" {
		clienteID = c.ClienteID
	}
	if c.NomeCadastro != "" {
		nome = c.NomeCadastro
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": true, "identificado": true, "forma": forma, "cliente_id": clienteID, "nome_cadastro": nome,
	})
}
