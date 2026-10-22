package toolsapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
	"bot-infominas/internal/taxonomia"
)

const (
	tituloMax    = 120
	descricaoMax = 4000
	// chamadosAbertosMax limita a lista devolvida no `chamado_aberto` (o LLM
	// só precisa de alguns para perguntar "quer acompanhar o existente?").
	chamadosAbertosMax = 5
	listagemLimite     = 20
)

type reqPreparar struct {
	Telefone       string `json:"telefone"`
	ConversationID string `json:"conversation_id"`
	Titulo         string `json:"titulo"`
	Descricao      string `json:"descricao"`
	Categoria      string `json:"categoria"`
	Prioridade     string `json:"prioridade"`
	// `identificacao` (visão mascarada do estado do cérebro) é ignorada de
	// propósito: quem identifica é a tabela `contatos` pelo telefone da sessão.
}

type refOut struct {
	ID   string `json:"id"`
	Nome string `json:"nome"`
}

type prioridadeOut struct {
	Nome string  `json:"nome"`
	ID   *string `json:"id"`
}

type propostaOut struct {
	Titulo                 string        `json:"titulo"`
	Descricao              string        `json:"descricao"`
	Categoria              string        `json:"categoria"`
	Mesa                   refOut        `json:"mesa"`
	Departamento           refOut        `json:"departamento"`
	Prioridade             prioridadeOut `json:"prioridade"`
	IdentificacaoMascarada string        `json:"identificacao_mascarada"`
	Rodape                 string        `json:"rodape"`
}

type chamadoAberto struct {
	Numero    string  `json:"numero"`
	Titulo    string  `json:"titulo"`
	Categoria *string `json:"categoria"`
	AbertoEm  string  `json:"aberto_em"`
}

// handlePreparar — preparar_abertura_chamado (A2). Dry-run: valida, resolve o
// destino pela taxonomia, faz o dedup e devolve a proposta canônica. Não cria
// nada (I-nenhuma).
func (h *Handler) handlePreparar(w http.ResponseWriter, r *http.Request) {
	var req reqPreparar
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	conv, tel, ok := h.sessaoEstrita(ctx, req.ConversationID, req.Telefone)
	if !ok {
		responderRecusa(w, "campo_invalido", txtCampoInvalidoPreparar)
		return
	}
	titulo, _ := sanitizarTexto(req.Titulo, tituloMax, true)
	descricao, cortou := sanitizarTexto(req.Descricao, descricaoMax, false)
	if titulo == "" || descricao == "" {
		responderRecusa(w, "campo_invalido", txtCampoInvalidoPreparar)
		return
	}
	if cortou {
		h.log.Info("preparar: descrição cortada no teto", "conversation_id", conv)
	}

	contato := h.contatoIdentificado(ctx, tel)
	if contato == nil {
		responderRecusa(w, "nao_identificado", txtNaoIdentificadoPreparar)
		return
	}

	prioridade := req.Prioridade
	if !taxonomia.PrioridadeValida(prioridade) {
		prioridade = "" // a taxonomia usa o padrão da categoria
	}
	res, motivo, erro := h.resolverDestino(ctx, conv, req.Categoria, prioridade)
	if motivo != "" {
		responderRecusa(w, motivo, erro)
		return
	}

	if abertos := h.chamadosAbertos(ctx, conv, tel, req.Categoria); len(abertos) > 0 {
		h.log.Info("preparar: chamado já aberto", "conversation_id", conv, "categoria", req.Categoria, "chamados", len(abertos))
		out := recusa("chamado_aberto", txtChamadoAberto)
		out["chamados_abertos"] = abertos
		writeJSON(w, http.StatusOK, out)
		return
	}

	h.avisarDegradacao(res, conv)
	// Sela a proposta (E4.5, M8): o `abrir` só cria o ticket de uma proposta que o
	// gateway preparou. Falhar em gravar não bloqueia o cliente aqui, mas o
	// `abrir` vai recusar (sem_proposta) e o cérebro prepara de novo.
	if err := h.d.Store.GravarPropostaTicket(ctx, conv, hashProposta(conv, titulo, descricao, res.Categoria, res.PrioridadeNome), h.agora()); err != nil {
		h.log.Error("preparar: gravar o selo da proposta", "conversation_id", conv, "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": true,
		"proposta": propostaOut{
			Titulo: titulo, Descricao: descricao, Categoria: res.Categoria,
			Mesa:                   refOut{ID: res.Mesa.ID, Nome: res.Mesa.Nome},
			Departamento:           refOut{ID: res.Departamento.ID, Nome: res.Departamento.Nome},
			Prioridade:             prioridadeOut{Nome: res.PrioridadeNome, ID: res.PriorityID},
			IdentificacaoMascarada: contato.ValorMascarado,
			Rodape:                 h.rodape(conv, contato),
		},
	})
}

// resolverDestino consulta a taxonomia e traduz o erro para o motivo do
// contrato (motivo "" = ok). categoria_sem_mapa alerta o responsável.
func (h *Handler) resolverDestino(ctx context.Context, conv, categoria, prioridade string) (taxonomia.Resolvida, string, string) {
	if !reCategoria.MatchString(categoria) {
		h.alertar(AlertaTaxonomia, "Categoria sem mapa na taxonomia",
			"A tool recebeu uma categoria fora do formato esperado; confira o cérebro e config/taxonomia.yaml.", conv)
		return taxonomia.Resolvida{}, "categoria_sem_mapa", txtCategoriaSemMapa
	}
	res, err := h.d.Taxonomia.Resolver(ctx, categoria, prioridade)
	switch {
	case err == nil:
		return res, "", ""
	case errors.Is(err, taxonomia.ErrReferenciaIndisponivel):
		return taxonomia.Resolvida{}, "referencia_indisponivel", txtReferenciaIndisponivel
	default:
		h.log.Error("categoria sem mapa", "categoria", categoria, "conversation_id", conv)
		h.alertar(AlertaTaxonomia, "Categoria sem mapa na taxonomia",
			"A categoria "+categoria+" não tem mesa/departamento resolvidos no Infodesk; o cliente foi orientado a falar com um atendente. Confira config/taxonomia.yaml.", conv)
		return taxonomia.Resolvida{}, "categoria_sem_mapa", txtCategoriaSemMapa
	}
}

// avisarDegradacao alerta o que a resolução precisou degradar (prioridade sem
// UUID na mesa, item de catálogo ausente): o ticket sai, mas sem esse campo.
func (h *Handler) avisarDegradacao(res taxonomia.Resolvida, conv string) {
	if len(res.Avisos) == 0 {
		return
	}
	h.log.Warn("taxonomia degradada na resolução", "categoria", res.Categoria, "avisos", res.Avisos)
	h.alertar(AlertaTaxonomia, "Taxonomia diverge do Infodesk",
		strings.Join(res.Avisos, "; ")+". O chamado foi aberto sem esse campo; confira config/taxonomia.yaml.", conv)
}

// rodape é o texto fixo anexado à descrição (`TICKET_RODAPE`): origem, conversa
// e a identificação SÓ mascarada.
func (h *Handler) rodape(conv string, c *store.Contato) string {
	if !*h.op.TicketRodape {
		return ""
	}
	partes := []string{"Aberto pela Sofia", "conversa " + conv}
	switch c.Forma {
	case store.FormaTelefone:
		if c.ValorMascarado != "" {
			partes = append(partes, "solicitante "+c.ValorMascarado)
		}
	case store.FormaCPF:
		partes = append(partes, "CPF "+c.ValorMascarado)
	case store.FormaContrato:
		partes = append(partes, "contrato "+c.ValorMascarado)
	}
	return "[" + strings.Join(partes, " · ") + "]"
}

// chamadosAbertos reúne o que impede abrir outro chamado: (1) o chamado já
// vinculado à conversa; (2) tickets locais abertos da mesma conversa ou do
// mesmo telefone + categoria na janela (só com a extensão ConsultaTickets do
// store); (3) os tickets abertos do telefone no Infodesk (DEDUP_CONSULTA_INFODESK;
// falha = só o dedup local + log, nunca bloqueia). Ticket `pendente` não entra:
// ainda não tem número (a chave de idempotência do `abrir` cobre o retry).
func (h *Handler) chamadosAbertos(ctx context.Context, conv, tel, categoria string) []chamadoAberto {
	var out []chamadoAberto
	visto := map[string]int{} // número → posição em out
	add := func(c chamadoAberto) {
		if i, ok := visto[c.Numero]; ok {
			if out[i].Titulo == "" {
				out[i].Titulo = c.Titulo
			}
			if out[i].Categoria == nil {
				out[i].Categoria = c.Categoria
			}
			if out[i].AbertoEm == "" {
				out[i].AbertoEm = c.AbertoEm
			}
			return
		}
		visto[c.Numero] = len(out)
		out = append(out, c)
	}

	if c, err := h.d.Store.ObterConversa(ctx, conv); err != nil {
		h.log.Error("dedup: ler conversa", "conversation_id", conv, "err", err)
	} else if c != nil && c.TicketNumber > 0 {
		add(chamadoAberto{Numero: strconv.Itoa(c.TicketNumber)})
	}

	if ext, ok := h.d.Store.(ConsultaTickets); ok {
		desde := h.agora().Add(-h.op.DedupJanela)
		locais, err := ext.TicketsDoTelefone(ctx, tel, desde)
		if err != nil {
			h.log.Error("dedup: ler tickets locais", "conversation_id", conv, "err", err)
		}
		for _, t := range locais {
			if t.Status != store.TicketAberto || t.Encerrado || t.Numero <= 0 {
				continue
			}
			if t.ConversationID != conv && t.Categoria != categoria {
				continue
			}
			cat := t.Categoria
			add(chamadoAberto{Numero: strconv.Itoa(t.Numero), Categoria: &cat, AbertoEm: t.CreatedAt.Format(time.RFC3339)})
		}
	}

	if *h.op.DedupConsultaInfodesk {
		tickets, err := h.ticketsAbertosNoInfodesk(ctx, tel)
		if err != nil {
			h.log.Warn("dedup: consulta ao Infodesk falhou; só o dedup local vale", "conversation_id", conv, "erro", descreverErro(err))
		}
		for _, tk := range tickets {
			add(chamadoAberto{
				Numero: strconv.Itoa(tk.Number), Titulo: textoExterno(str(tk.Bruto, "title"), 120),
				AbertoEm: textoExterno(str(tk.Bruto, "createdAt"), 40),
			})
		}
	}

	for i := range out {
		if out[i].Titulo == "" {
			out[i].Titulo = "chamado nº " + out[i].Numero
		}
	}
	if len(out) > chamadosAbertosMax {
		out = out[:chamadosAbertosMax]
	}
	return out
}

// ticketsAbertosNoInfodesk lista os tickets abertos do telefone (uma página;
// o limite é suficiente para o dedup e para "meus chamados").
func (h *Handler) ticketsAbertosNoInfodesk(ctx context.Context, tel string) ([]infodesk.Ticket, error) {
	pg, err := h.d.Infodesk.ListarTickets(ctx, infodesk.FiltroTickets{Phone: tel, Status: "open", Limit: listagemLimite})
	if err != nil {
		return nil, err
	}
	out := make([]infodesk.Ticket, 0, len(pg.Data))
	for _, tk := range pg.Data {
		if tk.Number > 0 {
			out = append(out, tk)
		}
	}
	return out, nil
}
