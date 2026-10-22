package toolsapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bot-infominas/internal/alerta"
	"bot-infominas/internal/engine"
	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
	"bot-infominas/internal/taxonomia"
)

// abrirTeto é o prazo de TODO o `abrir` (insert, vínculo, POST, reconciliação,
// handoff). O contexto é desligado do da requisição do cérebro: se o cérebro
// desistir de esperar, o POST já enviado precisa terminar de ser registrado
// (senão o ticket existiria no Infodesk sem linha `aberto` aqui).
const abrirTeto = 25 * time.Second

// Orçamento: o cérebro espera pelo `abrir` até 35 s (25 s + a folga de 10 s do
// handoff; piso próprio da tool, acima do INFODESK_TOOLS_TIMEOUT de 30 s), dentro
// do BRAIN_TURNO_PRAZO de 70 s do turno (E3.7). Até o POST e a reconciliação
// valem os 25 s acima; o handoff final tem a sua folga (posTeto) para a conversa
// nunca ficar sem atendente por falta de prazo, e o vínculo do solicitante
// (best-effort) não pode gastar mais que vinculoTeto.
const (
	posTeto     = 10 * time.Second
	vinculoTeto = 5 * time.Second
)

// Dimensões da reconciliação por listagem.
const (
	// paginasReconciliacao × listagemMaxima = quantos tickets abertos do
	// telefone a reconciliação enxerga.
	paginasReconciliacao = 3
	listagemMaxima       = 100
	// folgaRelogio é a tolerância entre o relógio do Infodesk e o nosso ao
	// exigir "criado depois do insert" no casamento por conversationId.
	folgaRelogio = 2 * time.Minute
)

type propostaIn struct {
	Titulo     string `json:"titulo"`
	Descricao  string `json:"descricao"`
	Categoria  string `json:"categoria"`
	Prioridade struct {
		Nome string `json:"nome"`
	} `json:"prioridade"`
	CriadaEm string `json:"criada_em"`
}

type reqAbrir struct {
	Telefone          string      `json:"telefone"`
	ConversationID    string      `json:"conversation_id"`
	Proposta          *propostaIn `json:"proposta"`
	ChaveIdempotencia string      `json:"chave_idempotencia"`
}

// abertura reúne o que o fluxo de `abrir` precisa passar entre os passos.
type abertura struct {
	conv, tel string
	titulo    string // sanitizado (o que vai ao Infodesk)
	descricao string
	criadaEm  time.Time
	prop      *propostaIn
	chave     string
	res       taxonomia.Resolvida
	contato   *store.Contato
	linha     *store.Ticket
}

// handleAbrir — abrir_chamado (A3). Cria o ticket EXATAMENTE como foi
// preparado e confirmado (a confirmação é checada por código no Python). Aqui:
//
//  1. a chave de idempotência manda: `INSERT tickets … pendente` ANTES do POST
//     (unique); chave repetida devolve o resultado, nunca um segundo POST;
//  2. mesa/departamento/prioridade/catálogo são RE-resolvidos pela taxonomia
//     (a proposta vem do estado do cérebro; o gateway não confia nela);
//  3. `POST /conversations/:id/ticket` UMA vez, sem retry (ADR 0005); qualquer
//     erro depois do envio é ambíguo e é decidido por LISTAGEM
//     (`GET /tickets?phone=&status=open`, casando `conversationId`), nunca por
//     novo POST.
func (h *Handler) handleAbrir(w http.ResponseWriter, r *http.Request) {
	var req reqAbrir
	if !decode(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), abrirTeto)
	defer cancel()

	a, motivo, erro := h.validarAbertura(ctx, &req)
	if motivo != "" {
		responderRecusa(w, motivo, erro)
		return
	}

	// Chave já vista: devolve o resultado (aberto) ou reconcilia (pendente).
	if linha, err := h.d.Store.ObterTicket(ctx, a.chave); err != nil {
		h.log.Error("abrir: ler ticket por chave", "conversation_id", a.conv, "err", err)
		writeJSON(w, http.StatusOK, h.indisponivelAoAbrir(ctx, a, "falha ao ler a tabela de tickets"))
		return
	} else if linha != nil {
		a.linha = linha
		writeJSON(w, http.StatusOK, h.abrirRepetido(ctx, a))
		return
	}

	// Defesa (docs/infodesk/07 T12): a conversa já tem chamado vinculado. Não
	// abre outro e NÃO diz que abriu: recusa (sem handoff) para o LLM não
	// anunciar um chamado que o cliente não confirmou. O `preparar` já devolve
	// `chamado_aberto` com o número; chegar aqui é corrida/estado anômalo.
	// TODO(T12): quando a Infominas disser o que o Desk faz nesse caso, revisar.
	if c, err := h.d.Store.ObterConversa(ctx, a.conv); err != nil {
		h.log.Error("abrir: ler conversa", "conversation_id", a.conv, "err", err)
	} else if c != nil && c.TicketNumber > 0 {
		h.log.Warn("abrir: a conversa já tem chamado; não abre outro (T12)", "conversation_id", a.conv, "numero", c.TicketNumber)
		responderRecusa(w, "em_andamento", txtEmAndamento)
		return
	}

	res, motivo, _ := h.resolverDestino(ctx, a.conv, a.prop.Categoria, a.prop.Prioridade.Nome)
	if motivo != "" {
		// A taxonomia mudou entre o preparar e o abrir: nada foi enviado.
		h.log.Error("abrir: destino não resolvido", "conversation_id", a.conv, "motivo", motivo)
		writeJSON(w, http.StatusOK, h.indisponivelAoAbrir(ctx, a, "taxonomia sem destino para a categoria"))
		return
	}
	a.res = res
	h.avisarDegradacao(res, a.conv)

	a.contato = h.contatoIdentificado(ctx, a.tel)
	linha, inseriu, err := h.d.Store.InserirTicketPendente(ctx, &store.Ticket{
		Chave: a.chave, ConversationID: a.conv, Telefone: a.tel, Categoria: res.Categoria,
		MesaID: res.Mesa.ID, DepartamentoID: res.Departamento.ID, Prioridade: res.PrioridadeNome,
		Status: store.TicketPendente,
	})
	if err != nil {
		h.log.Error("abrir: gravar ticket pendente", "conversation_id", a.conv, "err", err)
		writeJSON(w, http.StatusOK, h.indisponivelAoAbrir(ctx, a, "falha ao gravar o ticket pendente"))
		return
	}
	a.linha = linha
	if !inseriu { // corrida: outra chamada com a mesma chave chegou primeiro
		writeJSON(w, http.StatusOK, h.abrirRepetido(ctx, a))
		return
	}

	h.vincularSolicitante(ctx, a)
	tk, err := h.d.Infodesk.CriarTicketDaConversa(ctx, a.conv, h.novoTicket(a))
	if err == nil {
		writeJSON(w, http.StatusOK, h.concluirAbertura(ctx, a, tk, false))
		return
	}
	writeJSON(w, http.StatusOK, h.falhaDoPOST(ctx, a, err))
}

// validarAbertura confere a proposta e calcula a chave. motivo != "" = recusa.
func (h *Handler) validarAbertura(ctx context.Context, req *reqAbrir) (*abertura, string, string) {
	conv, tel, ok := h.sessao(ctx, req.ConversationID, req.Telefone)
	p := req.Proposta
	if !ok || p == nil || strings.TrimSpace(p.Categoria) == "" || strings.TrimSpace(p.CriadaEm) == "" {
		return nil, "sem_proposta", txtSemProposta
	}
	titulo, _ := sanitizarTexto(p.Titulo, tituloMax, true)
	descricao, _ := sanitizarTexto(p.Descricao, descricaoMax, false)
	if titulo == "" || descricao == "" {
		return nil, "sem_proposta", txtSemProposta
	}
	// A chave é a fórmula do ADR 0005 sobre a proposta carimbada. O gateway a
	// recalcula: uma chave divergente (bug do chamador) só vale se coincidir —
	// senão a idempotência dependeria de quem chama.
	chave := chaveIdempotencia(conv, p.Titulo, p.Categoria, p.CriadaEm)
	if req.ChaveIdempotencia != chave {
		h.log.Warn("abrir: chave_idempotencia diverge da fórmula do ADR 0005; usando a calculada aqui", "conversation_id", conv)
	}
	return &abertura{
		conv: conv, tel: tel, titulo: titulo, descricao: descricao,
		criadaEm: parseInstante(p.CriadaEm), prop: p, chave: chave,
	}, "", ""
}

// novoTicket monta o corpo do POST: sem requesterId/clientId/contactPhone
// (presume-se herdados da conversa — 07 T11), `period: session` e o `finish`
// configurado (C6, default false).
func (h *Handler) novoTicket(a *abertura) infodesk.NovoTicketDaConversa {
	desc := a.descricao
	if a.contato != nil {
		if rod := h.rodape(a.conv, a.contato); rod != "" {
			desc += "\n\n" + rod
		}
	}
	return infodesk.NovoTicketDaConversa{
		DeskID: a.res.Mesa.ID, Title: a.titulo, Description: desc,
		PriorityID: a.res.PriorityID, CatalogItemID: a.res.CatalogItemID,
		Period: "session", Finish: h.op.TicketFinishConversa,
	}
}

// vincularSolicitante: quando o lookup identificou o telefone, vincula o
// solicitante à conversa ANTES do ticket (fluxo de referência do 08). Best-effort:
// falha vira log (07 T11).
func (h *Handler) vincularSolicitante(ctx context.Context, a *abertura) {
	if a.contato == nil || a.contato.RequesterID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, vinculoTeto)
	defer cancel()
	if err := h.d.Infodesk.VincularSolicitante(ctx, a.conv, a.contato.RequesterID); err != nil {
		h.log.Warn("abrir: vincular solicitante falhou; seguindo sem o vínculo", "conversation_id", a.conv, "erro", descreverErro(err))
	}
}

// concluirAbertura registra o número no store, vincula o chamado à conversa,
// faz o handoff pós-ticket e monta a resposta de sucesso.
func (h *Handler) concluirAbertura(ctx context.Context, a *abertura, tk infodesk.Ticket, reconciliado bool) map[string]any {
	if err := h.d.Store.ConcluirTicket(ctx, a.chave, tk.Number, rawMinimo(tk), reconciliado); err != nil {
		// O ticket EXISTE no Infodesk; o cliente precisa do número. O reconciliador
		// (E3.3) repara a linha pendente.
		h.log.Error("abrir: gravar número do ticket", "conversation_id", a.conv, "numero", tk.Number, "err", err)
	}
	h.log.Info("ticket_aberto", "numero", tk.Number, "conversa", a.conv, "categoria", a.res.Categoria,
		"mesa", a.res.Mesa.Nome, "reconciliado", reconciliado)
	return h.sucessoAbrir(ctx, a, tk.Number)
}

// sucessoAbrir vincula o número à conversa, faz o handoff pós-ticket
// (TRANSFERIR_APOS_TICKET) e monta o `ok` do contrato.
func (h *Handler) sucessoAbrir(ctx context.Context, a *abertura, numero int) map[string]any {
	h.gravarNumeroNaConversa(ctx, a, numero)
	transferido := false
	if *h.op.TransferirAposTicket {
		pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), posTeto)
		defer cancel()
		id := h.d.Transferidor.TransferirParaHumano(pctx, a.conv, engine.Transferencia{
			Origem:  engine.OrigemTicketAberto,
			Causa:   "chamado aberto; atendente assume (TRANSFERIR_APOS_TICKET)",
			Detalhe: "chamado nº " + strconv.Itoa(numero),
			// O LLM comunica o número e a passagem na resposta deste turno.
			SemAviso: true,
		})
		transferido = id != ""
	}
	if !transferido {
		if c, err := h.d.Store.ObterConversa(ctx, a.conv); err == nil && c != nil && c.Estado == store.EstadoTransferida {
			transferido = true
		}
	}
	if a.res.Categoria == "" { // repetição sem resolver a taxonomia: tenta agora, só para os nomes
		if res, err := h.d.Taxonomia.Resolver(ctx, a.prop.Categoria, a.prop.Prioridade.Nome); err == nil {
			a.res = res
		}
	}
	return map[string]any{
		"status": true,
		"numero": strconv.Itoa(numero),
		"mesa":   refOut{ID: a.res.Mesa.ID, Nome: a.res.Mesa.Nome},
		"departamento": refOut{
			ID: a.res.Departamento.ID, Nome: a.res.Departamento.Nome,
		},
		"prioridade":  prioridadeOut{Nome: a.res.PrioridadeNome, ID: a.res.PriorityID},
		"transferido": transferido,
	}
}

func (h *Handler) gravarNumeroNaConversa(ctx context.Context, a *abertura, numero int) {
	agora := h.agora()
	_, err := h.d.Store.AtualizarConversa(ctx, a.conv, func(c *store.Conversa, existia bool) bool {
		if !existia {
			c.Estado = store.EstadoBot
		}
		if c.Telefone == "" {
			c.Telefone = a.tel
		}
		c.TicketNumber = numero
		c.UpdatedAt = agora
		return true
	})
	if err != nil {
		h.log.Error("abrir: gravar chamado na conversa", "conversation_id", a.conv, "err", err)
	}
}

// abrirRepetido trata a chave já existente: aberto devolve o resultado;
// pendente reconcilia por listagem; falha (4xx definitivo gravado por
// falhaDoPOST, ou TTL vencido) não reabre, nem faz POST nem listagem.
func (h *Handler) abrirRepetido(ctx context.Context, a *abertura) map[string]any {
	l := a.linha
	switch {
	case l.Status == store.TicketAberto && l.Numero > 0:
		return h.sucessoAbrir(ctx, a, l.Numero)
	case l.Status == store.TicketFalha:
		return h.indisponivelAoAbrir(ctx, a, "ticket desta chave já foi dado como falha")
	}
	// pendente: a primeira chamada ainda pode estar em curso, ou o POST ficou sem resposta.
	tk, achou, err := h.reconciliar(ctx, a)
	switch {
	case achou:
		return h.concluirAbertura(ctx, a, tk, true)
	case h.agora().Sub(l.CreatedAt) < h.op.TicketPendenteTTL:
		if err != nil {
			h.log.Warn("abrir: listagem falhou durante o TTL; segue em andamento", "conversation_id", a.conv, "erro", descreverErro(err))
		}
		return recusa("em_andamento", txtEmAndamento)
	}
	h.alertarPendente(a)
	return h.indisponivelAoAbrir(ctx, a, "ticket pendente sem correspondente no Infodesk")
}

// falhaDoPOST decide o que um erro do POST significa. 4xx definitivo (exceto
// 429) = o Infodesk recusou, nada foi criado; o resto (5xx, rede, 429, resposta
// ilegível, 2xx sem número) é AMBÍGUO e se resolve por listagem — nunca por
// novo POST.
func (h *Handler) falhaDoPOST(ctx context.Context, a *abertura, err error) map[string]any {
	var he *infodesk.HTTPError
	// 409 CONFLICT pode ser "a conversa já tem ticket" (T12): o ticket PODE
	// existir, então é ambíguo e se resolve por listagem como o 5xx.
	if errors.As(err, &he) && he.Status >= 400 && he.Status < 500 &&
		he.Status != http.StatusTooManyRequests && he.Code != infodesk.CodeConflict {
		h.registrarRecusa(ctx, a)
		h.log.Error("abrir: o Infodesk recusou o ticket", "conversation_id", a.conv, "status", he.Status, "code", he.Code,
			"campos", camposCitados(he.Mensagem), "rota", he.Rota)
		switch he.Code {
		case infodesk.CodeForbidden, infodesk.CodeUnauthorized:
			h.alertar(alerta.TipoChaveSemEscopo, "Chave do Infodesk sem permissão para abrir chamado",
				"POST /conversations/:id/ticket recusado ("+he.Code+"): confira os escopos da chave (tickets:write, conversations:write).", a.conv)
			return recusa("escopo_insuficiente", txtEscopoInsuficiente)
		case infodesk.CodeInvalidReference:
			h.alertar(AlertaConfiguracao, "Taxonomia aponta para um id que o Infodesk não conhece",
				"O Infodesk respondeu INVALID_REFERENCE ao abrir o chamado (mesa, prioridade, item de catálogo ou departamento). Recarreguei a referência; confira config/taxonomia.yaml.", a.conv)
			h.recarregarTaxonomia(ctx)
		case infodesk.CodeValidationError:
			h.alertar(AlertaConfiguracao, "Infodesk exige um campo que o gateway não envia",
				"O Infodesk respondeu VALIDATION_ERROR ao abrir o chamado (campos citados: "+strings.Join(camposCitados(he.Mensagem), ", ")+
					"). Pode ser um campo exigido pela organização (docs/infodesk/07 T10): cliente, solicitante, serviço ou prioridade. Veja o log do gateway.", a.conv)
		default:
			h.alertar(AlertaConfiguracao, "Infodesk recusou a abertura do chamado",
				"POST /conversations/:id/ticket recusado ("+he.Code+", HTTP "+strconv.Itoa(he.Status)+"). Veja o log do gateway.", a.conv)
		}
		return h.indisponivelAoAbrir(ctx, a, "o Infodesk recusou a abertura")
	}

	h.log.Error("abrir: POST do ticket sem resposta confiável; reconciliando por listagem (sem novo POST)",
		"conversation_id", a.conv, "erro", descreverErro(err))
	tk, achou, errLista := h.reconciliar(ctx, a)
	if achou {
		return h.concluirAbertura(ctx, a, tk, true)
	}
	if errLista != nil {
		h.log.Error("abrir: a listagem de reconciliação também falhou", "conversation_id", a.conv, "erro", descreverErro(errLista))
	}
	h.alertarPendente(a)
	return h.indisponivelAoAbrir(ctx, a, "POST sem resposta e a listagem não achou o ticket")
}

func (h *Handler) alertarPendente(a *abertura) {
	h.alertar(AlertaTicketPendente, "Chamado possivelmente criado, mas sem número confirmado",
		"O POST do chamado ficou sem resposta e a listagem do Infodesk não o achou. A linha fica 'pendente' (chave "+
			chavePrefixo(a.chave)+") e a conversa foi passada a um atendente; o reconciliador repete a busca até o TTL.", a.conv)
}

// indisponivelAoAbrir devolve `infodesk_indisponivel`, e o cliente não fica
// sem ninguém: a conversa vai a um atendente pelo caminho único (aviso do
// gateway + handoff com nota por código). Idempotente na engine.
func (h *Handler) indisponivelAoAbrir(ctx context.Context, a *abertura, causa string) map[string]any {
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), posTeto)
	defer cancel()
	h.d.Transferidor.TransferirParaHumano(pctx, a.conv, engine.Transferencia{
		Origem:   engine.OrigemTicketPendente,
		Causa:    "não foi possível registrar o chamado: " + causa,
		Mensagem: MsgFalhaAoAbrir,
	})
	return recusa("infodesk_indisponivel", txtAbrirIndisponivel)
}

func (h *Handler) recarregarTaxonomia(ctx context.Context) {
	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := h.d.Taxonomia.Recarregar(rctx); err != nil {
		h.log.Error("recarregar taxonomia após INVALID_REFERENCE", "erro", descreverErro(err))
	}
}

// reconciliar procura, entre os tickets ABERTOS do telefone no Infodesk, o que
// este `abrir` criou: `conversationId` igual (criado depois do insert, com
// folga de relógio) ou, como segunda chance, o mesmo título criado depois da
// proposta. GET apenas — nunca POST.
func (h *Handler) reconciliar(ctx context.Context, a *abertura) (infodesk.Ticket, bool, error) {
	desde := a.linha.CreatedAt.Add(-folgaRelogio)
	cursor := ""
	for pagina := 0; pagina < paginasReconciliacao; pagina++ {
		pg, err := h.d.Infodesk.ListarTickets(ctx, infodesk.FiltroTickets{Phone: a.tel, Status: "open", Limit: listagemMaxima, Cursor: cursor})
		if err != nil {
			return infodesk.Ticket{}, false, err
		}
		for _, tk := range pg.Data {
			if casaComAbertura(tk, a, desde) {
				return tk, true, nil
			}
		}
		if pg.NextCursor == "" {
			break
		}
		cursor = pg.NextCursor
	}
	return infodesk.Ticket{}, false, nil
}

func casaComAbertura(tk infodesk.Ticket, a *abertura, desde time.Time) bool {
	if tk.Number <= 0 {
		return false
	}
	criado := parseInstante(str(tk.Bruto, "createdAt"))
	if tk.ConversationID == a.conv {
		// Sem `createdAt` não dá para distinguir de um ticket anterior da
		// mesma conversa: não casa (o reconciliador/atendente resolvem).
		return !criado.IsZero() && !criado.Before(desde)
	}
	if tk.ConversationID != "" {
		return false
	}
	mesmoTitulo := strings.Join(strings.Fields(str(tk.Bruto, "title")), " ") == a.titulo
	return mesmoTitulo && !criado.IsZero() && !a.criadaEm.IsZero() && !criado.Before(a.criadaEm)
}

// rawMinimo é o que fica em `tickets.raw_resposta`: ids e estado, sem o
// solicitante nem o texto do cliente (PII só mascarada em disco).
func rawMinimo(tk infodesk.Ticket) string {
	b, _ := json.Marshal(map[string]any{
		"number": tk.Number, "status": tk.Status.Name, "desk": tk.Desk.ID, "conversationId": tk.ConversationID,
		"createdAt": str(tk.Bruto, "createdAt"),
	})
	return string(b)
}

func chavePrefixo(chave string) string {
	if len(chave) > 12 {
		return chave[:12]
	}
	return chave
}

// camposConhecidos: nomes de campo da API que podem aparecer num 400. O texto
// do erro do Infodesk NÃO vai ao log (pode ecoar título, descrição ou nome do
// cliente): só estes nomes, que não são dado pessoal.
var camposConhecidos = regexp.MustCompile(`(?i)\b(deskId|title|description|priorityId|catalogItemId|clientId|requesterId|contactPhone|period|finish|client|requester|catalog|service|priority)\b`)

func camposCitados(mensagem string) []string {
	vistos := map[string]bool{}
	var out []string
	for _, m := range camposConhecidos.FindAllString(mensagem, -1) {
		k := strings.ToLower(m)
		if !vistos[k] {
			vistos[k] = true
			out = append(out, m)
		}
	}
	return out
}

// descreverErro resume um erro do Infodesk para log SEM a mensagem do servidor
// (que pode ecoar dado do cliente): tipo, status HTTP e `code`.
func descreverErro(err error) string {
	var he *infodesk.HTTPError
	var re *infodesk.ErroRede
	var rp *infodesk.ErroResposta
	switch {
	case errors.As(err, &he):
		return "http " + strconv.Itoa(he.Status) + " " + he.Code + " " + he.Rota
	case errors.As(err, &re):
		return "rede " + re.Rota
	case errors.As(err, &rp):
		return "resposta inválida " + rp.Rota
	case errors.Is(err, infodesk.ErrTicketSemNumero):
		return "ticket sem número na resposta"
	case errors.Is(err, context.DeadlineExceeded):
		return "prazo esgotado"
	default:
		return "erro interno"
	}
}

// registrarRecusa grava `falha` na linha do ticket quando o Infodesk recusou o
// POST de forma definitiva (4xx): nada foi criado, então um segundo `abrir` com
// a mesma chave devolve `infodesk_indisponivel` pelo caminho `falha` do
// abrirRepetido — sem POST e sem listagem, e sem perder o fato num reinício.
// Se a gravação falhar o retry cai no caminho `pendente` (listagem até o TTL):
// pior em latência, nunca em duplicar o POST.
func (h *Handler) registrarRecusa(ctx context.Context, a *abertura) {
	ok, err := h.d.Store.FalharTicket(context.WithoutCancel(ctx), a.chave)
	switch {
	case err != nil:
		h.log.Error("abrir: não consegui gravar a recusa do Infodesk no ticket; o retry reconcilia por listagem (sem POST)",
			"conversation_id", a.conv, "chave", chavePrefixo(a.chave), "err", err)
	case !ok:
		h.log.Warn("abrir: recusa do Infodesk, mas o ticket local já não estava pendente",
			"conversation_id", a.conv, "chave", chavePrefixo(a.chave))
	}
}
