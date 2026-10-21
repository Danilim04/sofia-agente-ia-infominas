package toolsapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"bot-infominas/internal/alerta"
	"bot-infominas/internal/infodesk"
)

type reqConsultar struct {
	Telefone       string `json:"telefone"`
	ConversationID string `json:"conversation_id"`
	Numero         string `json:"numero"`
	// `identificacao` (visão mascarada do estado) é ignorada: a posse pelo
	// solicitante usa o `requester_id` de `contatos`, buscado pelo telefone.
}

// handleConsultar — consultar_status_chamado (A4). `GET /tickets/:number` e
// então a POSSE, decidida por código: sem posse verificada o gateway não devolve
// nenhum campo do ticket (o LLM não vê dado de chamado alheio).
func (h *Handler) handleConsultar(w http.ResponseWriter, r *http.Request) {
	var req reqConsultar
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	conv, tel, ok := h.sessao(ctx, req.ConversationID, req.Telefone)
	if !ok {
		responderRecusa(w, "posse_nao_verificada", txtPosseNaoVerificada)
		return
	}
	numero := strings.TrimSpace(req.Numero)
	n, err := strconv.Atoi(numero)
	if !reDigitos.MatchString(numero) || err != nil || n <= 0 {
		responderRecusa(w, "nao_encontrado", txtConsultarNaoEncontrado)
		return
	}

	tk, err := h.d.Infodesk.Ticket(ctx, n)
	if err != nil {
		var he *infodesk.HTTPError
		switch {
		case errors.As(err, &he) && (he.Code == infodesk.CodeNotFound || he.Status == http.StatusNotFound):
			responderRecusa(w, "nao_encontrado", txtConsultarNaoEncontrado)
		case errors.As(err, &he) && (he.Code == infodesk.CodeForbidden || he.Code == infodesk.CodeUnauthorized):
			h.alertar(alerta.TipoChaveSemEscopo, "Chave do Infodesk sem permissão para consultar chamados",
				"GET /tickets/:number recusado ("+he.Code+"): confira o escopo tickets:read.", conv)
			responderRecusa(w, "infodesk_indisponivel", txtConsultarIndisponivel)
		default:
			h.log.Error("consultar: Infodesk falhou", "conversation_id", conv, "erro", descreverErro(err))
			responderRecusa(w, "infodesk_indisponivel", txtConsultarIndisponivel)
		}
		return
	}

	regra := h.posse(ctx, conv, tel, tk)
	if regra == "" {
		h.log.Info("consultar: posse não verificada", "conversation_id", conv, "numero", n)
		responderRecusa(w, "posse_nao_verificada", txtPosseNaoVerificada)
		return
	}
	h.log.Info("consultar: posse verificada", "conversation_id", conv, "numero", n, "regra", regra)

	nome := statusLegivel(tk)
	// isClosed/isCanceled mantêm a linha local em dia (se o ticket for nosso).
	if _, err := h.d.Store.AtualizarStatusTicket(ctx, n, nome, tk.IsClosed || tk.Status.IsClose || tk.Status.IsCanceled, h.agora()); err != nil {
		h.log.Error("consultar: atualizar status local", "numero", n, "err", err)
	}

	var atualizado any
	if v := textoExterno(str(tk.Bruto, "updatedAt"), 40); v != "" {
		atualizado = v
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":        true,
		"numero":        strconv.Itoa(n),
		"status_ticket": nome,
		"titulo":        textoExterno(str(tk.Bruto, "title"), 200),
		"aberto_em":     textoExterno(str(tk.Bruto, "createdAt"), 40),
		"atualizado_em": atualizado,
		"mesa":          refOut{ID: textoExterno(tk.Desk.ID, 80), Nome: textoExterno(tk.Desk.Nome, 80)},
	})
}

// posse devolve a regra que provou que o ticket é do cliente da sessão, na
// ordem do plano (docs/plano/01, A4), ou "" se nenhuma provou:
//
//	(a) a tabela local tem esse número com o MESMO telefone (extensão
//	    ConsultaTickets do store; sem ela, esta regra é pulada);
//	(b) a conversa de origem do ticket pertence a este telefone;
//	(c) contactPhone ou requester.phone é este telefone (E.164, tolerando o 9º dígito);
//	(d) requester.id é o requester_id que o lookup achou para este telefone.
func (h *Handler) posse(ctx context.Context, conv, tel string, tk infodesk.Ticket) string {
	if ext, ok := h.d.Store.(ConsultaTickets); ok {
		if t, err := ext.TicketPorNumero(ctx, tk.Number); err != nil {
			h.log.Error("posse: ler ticket local", "err", err)
		} else if t != nil && mesmoTelefone(t.Telefone, tel) {
			return "tabela_local"
		}
	}
	if tk.ConversationID != "" {
		if tk.ConversationID == conv {
			return "conversa_da_sessao"
		}
		if c, err := h.d.Store.ObterConversa(ctx, tk.ConversationID); err != nil {
			h.log.Error("posse: ler conversa do ticket", "err", err)
		} else if c != nil && mesmoTelefone(c.Telefone, tel) {
			return "conversa_do_telefone"
		}
	}
	if tk.ContactPhone != "" && mesmoTelefone(tk.ContactPhone, tel) {
		return "telefone_do_ticket"
	}
	if tk.Requester != nil {
		if tk.Requester.Phone != "" && mesmoTelefone(tk.Requester.Phone, tel) {
			return "telefone_do_solicitante"
		}
		if tk.Requester.ID != "" {
			if c := h.contatoIdentificado(ctx, tel); c != nil && c.RequesterID == tk.Requester.ID {
				return "solicitante"
			}
		}
	}
	return ""
}

// statusLegivel é o `status.name` LITERAL do Infodesk (os nomes são por mesa —
// nunca inventar). Sem nome, cai nos booleanos; sem nada legível,
// "desconhecido".
func statusLegivel(tk infodesk.Ticket) string {
	if n := textoExterno(tk.Status.Name, 60); n != "" {
		return n
	}
	switch {
	case tk.Status.IsCanceled:
		return "cancelado"
	case tk.IsClosed || tk.Status.IsClose:
		return "encerrado"
	case tk.Status.IsOpen:
		return "em andamento"
	}
	return "desconhecido"
}

type chamadoListado struct {
	Numero       string  `json:"numero"`
	Titulo       string  `json:"titulo"`
	Categoria    *string `json:"categoria"`
	StatusTicket string  `json:"status_ticket"`
	AbertoEm     string  `json:"aberto_em"`
}

const (
	obsInfodeskELocal = "abertos no Infodesk + abertos por aqui"
	obsSoLocal        = "só os abertos por aqui (Infodesk indisponível)"
	obsSoInfodesk     = "só os abertos no Infodesk"
	janelaLocalListar = 90 * 24 * time.Hour
)

type reqListar struct {
	Telefone       string `json:"telefone"`
	ConversationID string `json:"conversation_id"`
}

// handleListar — listar_meus_chamados (A5): os abertos do telefone no Infodesk
// ∪ os que a Sofia abriu nos últimos 90 dias (extensão ConsultaTickets do
// store; sem ela só o lado do Infodesk), sem duplicar por número. Posse é o
// filtro `phone`: nada de outro telefone entra.
func (h *Handler) handleListar(w http.ResponseWriter, r *http.Request) {
	var req reqListar
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	conv, tel, ok := h.sessao(ctx, req.ConversationID, req.Telefone)
	if !ok {
		responderRecusa(w, "chamados_indisponiveis", txtChamadosIndisponiveis)
		return
	}

	var lista []chamadoListado
	posicao := map[string]int{}
	add := func(c chamadoListado) {
		if i, ok := posicao[c.Numero]; ok {
			if lista[i].Categoria == nil {
				lista[i].Categoria = c.Categoria
			}
			return
		}
		posicao[c.Numero] = len(lista)
		lista = append(lista, c)
	}

	remotos, errRemoto := h.ticketsAbertosNoInfodesk(ctx, tel)
	if errRemoto != nil {
		h.log.Warn("listar: Infodesk falhou", "conversation_id", conv, "erro", descreverErro(errRemoto))
	}
	for _, tk := range remotos {
		add(chamadoListado{
			Numero: strconv.Itoa(tk.Number), Titulo: textoExterno(str(tk.Bruto, "title"), 200),
			StatusTicket: statusLegivel(tk), AbertoEm: textoExterno(str(tk.Bruto, "createdAt"), 40),
		})
	}

	localOK := false
	if ext, ok := h.d.Store.(ConsultaTickets); ok {
		locais, err := ext.TicketsDoTelefone(ctx, tel, h.agora().Add(-janelaLocalListar))
		if err != nil {
			h.log.Error("listar: ler tickets locais", "conversation_id", conv, "err", err)
		} else {
			localOK = true
			for _, t := range locais {
				if t.Numero <= 0 {
					continue
				}
				cat := t.Categoria
				status := t.StatusInfodesk
				if status == "" {
					status = "desconhecido"
				}
				add(chamadoListado{
					Numero: strconv.Itoa(t.Numero), Categoria: &cat,
					StatusTicket: textoExterno(status, 60), AbertoEm: t.CreatedAt.Format(time.RFC3339),
				})
			}
		}
	}
	for i := range lista {
		if lista[i].Titulo == "" {
			lista[i].Titulo = "chamado nº " + lista[i].Numero
		}
	}

	var obs string
	switch {
	case errRemoto == nil && localOK:
		obs = obsInfodeskELocal
	case errRemoto == nil:
		obs = obsSoInfodesk
	case localOK:
		obs = obsSoLocal
	default:
		responderRecusa(w, "chamados_indisponiveis", txtChamadosIndisponiveis)
		return
	}
	if lista == nil {
		lista = []chamadoListado{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": true, "total": len(lista), "chamados": lista, "observacao": obs})
}
