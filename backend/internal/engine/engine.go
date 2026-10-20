// Package engine orquestra: mensagem neutra do webhook → higiene/dedup →
// fila por conversa (coalescência) → cérebro Python (Contrato A) →
// formatação WhatsApp → resposta pelo Transporte. Não há gate de login: a
// identificação do cliente acontece na conversa, dentro do cérebro, com
// validação por código (ADR 0003).
package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"bot-infominas/internal/alerta"
	"bot-infominas/internal/brain"
	"bot-infominas/internal/config"
	"bot-infominas/internal/store"
)

// BrainAPI é o que a engine usa do cérebro (satisfeito por *brain.Client).
type BrainAPI interface {
	Chat(ctx context.Context, req brain.ChatRequest) (*brain.ChatResponse, error)
	// EncerrarEpisodio limpa a proposta de chamado pendente da conversa
	// encerrada (ZB-1).
	EncerrarEpisodio(ctx context.Context, conversationID string) error
}

// prazoEncerrarEpisodio limita a chamada ao cérebro no fim do episódio: é
// limpeza best-effort num worker de webhook, não pode segurar a fila.
const prazoEncerrarEpisodio = 5 * time.Second

// Engine reúne as dependências do orquestrador.
type Engine struct {
	cfg   *config.Config
	tr    Transporte
	brain BrainAPI
	st    store.Store
	log   *slog.Logger
	agora func() time.Time // relógio injetável (IDs de ocorrência, testes)

	// Resiliência ao cérebro (ZC-5): esperas entre novas tentativas de falha
	// TRANSITÓRIA, espera injetável (testes não dormem) e o disjuntor.
	esperasRetry []time.Duration
	dormir       func(ctx context.Context, d time.Duration) bool
	disjuntor    *disjuntor
	// vigia decide quando alertar o responsável (ZC-7) — pode ser nil.
	vigia *alerta.Vigia

	// Coalescência/serialização por conversa (F11) — ver coalescer.go.
	baseCtx      context.Context
	debounce     time.Duration
	debounceTeto time.Duration
	mu           sync.Mutex
	filas        map[string]chan Inbound
}

// New constrói a engine. Chame Start(ctx) antes de processar mensagens.
func New(cfg *config.Config, tr Transporte, brainClient BrainAPI, st store.Store, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	e := &Engine{
		cfg:          cfg,
		tr:           tr,
		brain:        brainClient,
		st:           st,
		log:          log,
		agora:        time.Now,
		esperasRetry: []time.Duration{2 * time.Second, 5 * time.Second},
		dormir:       dormirDeVerdade,
		disjuntor:    novoDisjuntor(),
		baseCtx:      context.Background(),
		debounce:     cfg.DebounceJanela,
		debounceTeto: cfg.DebounceTeto,
		filas:        make(map[string]chan Inbound),
	}
	e.ligarVigia(alerta.NovaVigia(alerta.Padrao(cfg.AlertaWebhookURL, log), nil))
	return e
}

// ligarVigia instala a vigia de alertas (ZC-7) e o gancho do disjuntor. Os
// testes trocam por uma vigia com notificador falso.
func (e *Engine) ligarVigia(v *alerta.Vigia) {
	e.vigia = v
	if v != nil && e.agora != nil {
		v.Agora = e.agora
	}
	e.disjuntor.aoAbrir = func(chave, causa string, ate time.Time) {
		e.vigia.DisjuntorAberto(chave, causa, ate)
	}
}

// Start registra o contexto-raiz dos workers por conversa (cancelado no
// shutdown). Os workers em si nascem sob demanda, um por conversa ativa.
func (e *Engine) Start(ctx context.Context) {
	e.baseCtx = ctx
}

// HandleMensagem valida/higieniza a mensagem de entrada e a entrega à fila
// da conversa. O processamento de verdade acontece no worker (um lote por
// vez por conversa — ver coalescer.go). Quem chama já filtrou o que não é do
// cliente (sender != CLIENT, organização errada) — isso é do webhook.
func (e *Engine) HandleMensagem(ctx context.Context, m Inbound) {
	m.ConversationID = strings.TrimSpace(m.ConversationID)
	m.Texto = strings.TrimSpace(m.Texto)
	if m.ConversationID == "" {
		e.log.Warn("mensagem sem conversation_id ignorada", "msg_id", m.MsgID)
		return
	}
	// Mensagem só com mídia NÃO é descartada: segue para a fila e vira aviso
	// + transferência no worker. Vazia de verdade (sem texto e sem mídia)
	// continua ignorada.
	if m.Texto == "" && !m.TemMidia {
		return
	}
	// Descarta eventos velhos (F10): reentrega/reconciliação tardia não pode
	// gerar "resposta-fantasma".
	if e.cfg.EventoIdadeMax > 0 && !m.CriadoEm.IsZero() {
		if idade := e.agora().Sub(m.CriadoEm); idade > e.cfg.EventoIdadeMax {
			e.log.Warn("mensagem antiga descartada",
				"conversation_id", m.ConversationID, "idade", idade.String(), "msg_id", m.MsgID)
			return
		}
	}
	// Idempotência pelo id da mensagem (F10): o dedup por id de entrega do
	// webhook não pega a MESMA mensagem reentregue com outro id de entrega.
	if m.MsgID != "" {
		fresh, err := e.st.MarkProcessed(ctx, "msg:"+m.MsgID)
		if err != nil {
			e.log.Error("dedup por msg_id", "msg_id", m.MsgID, "err", err)
		} else if !fresh {
			e.log.Info("mensagem duplicada (msg_id) ignorada",
				"conversation_id", m.ConversationID, "msg_id", m.MsgID)
			return
		}
	}
	if m.CriadoEm.IsZero() {
		m.CriadoEm = e.agora()
	}
	e.enfileirar(m)
}

// EncerrarEpisodio é o fim do episódio da conversa (`conversation.finished`
// no Infodesk): manda o cérebro limpar a proposta de chamado pendente dela
// (ZB-1), best-effort — a validade da proposta no cérebro (15 min) é a rede
// de segurança quando esta limpeza não chega lá.
//
// TODO(E2.1): limpar também `conversas.estado` (transferida → encerrada)
// quando a tabela existir.
func (e *Engine) EncerrarEpisodio(ctx context.Context, convID string) {
	if e.brain == nil {
		return
	}
	bctx, cancel := context.WithTimeout(ctx, prazoEncerrarEpisodio)
	defer cancel()
	if err := e.brain.EncerrarEpisodio(bctx, convID); err != nil {
		e.log.Warn("conversa encerrada: cérebro não limpou a proposta de chamado",
			"conversation_id", convID, "err", err)
		return
	}
	e.log.Info("conversa encerrada: proposta de chamado pendente limpa no cérebro", "conversation_id", convID)
}

// dormirDeVerdade espera d ou o cancelamento do ctx (false). Os testes trocam
// por um relógio falso.
func dormirDeVerdade(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// processarLote roda UM turno para a rajada coletada: junta os textos e chama
// o cérebro (ou transfere, quando é mídia ou pedido de atendente). Chamado
// apenas pelo worker da conversa — nunca em paralelo para o mesmo
// conversation_id.
func (e *Engine) processarLote(ctx context.Context, lote []Inbound) {
	if len(lote) == 0 {
		return
	}
	ultimo := lote[len(lote)-1]
	convID := ultimo.ConversationID

	// Mídia na rajada: o bot não lê áudio/imagem/arquivo — avisa o cliente e
	// transfere, sem cérebro.
	// TODO(E2.1): conversa já `transferida` (tabela conversas) não transfere
	// de novo — hoje o webhook ainda não filtra esse estado.
	for _, m := range lote {
		if m.TemMidia {
			e.transferirPorMidia(ctx, convID)
			return
		}
	}

	textos := make([]string, 0, len(lote))
	for _, m := range lote {
		textos = append(textos, m.Texto)
	}
	texto := strings.TrimSpace(strings.Join(textos, "\n"))
	if texto == "" {
		return
	}
	if len(lote) > 1 {
		e.log.Info("rajada coalescida", "conversation_id", convID, "mensagens", len(lote))
	}

	// Pedido explícito por um humano (palavra de escape): não vai ao cérebro.
	if PedeAtendente(texto) {
		e.log.Info("cliente pediu atendente; transferindo", "conversation_id", convID)
		e.TransferirParaHumano(ctx, convID, Transferencia{
			Origem: OrigemCliente, Causa: motivoPedidoAtendente, Mensagem: MsgPedidoAtendente,
		})
		return
	}

	// O teto do turno conta desde a PRIMEIRA mensagem da rajada.
	inicio := lote[0].CriadoEm
	if inicio.IsZero() {
		inicio = e.agora()
	}
	e.encaminhar(ctx, ultimo, texto, inicio)
}

// encaminhar chama o cérebro com a política de resiliência (ZC-5):
//
//   - disjuntor aberto → não chama; transfere direto (origem disjuntor);
//   - falha DEFINITIVA → não repete; transfere e alimenta o disjuntor;
//   - falha TRANSITÓRIA → até len(esperasRetry) novas tentativas silenciosas
//     (2 s e 5 s), enquanto couber no teto do turno (TURNO_TETO desde a
//     mensagem do cliente); esgotou → transfere.
//
// A chamada ao cérebro recebe o prazo do teto como deadline do contexto: o
// cérebro nunca trabalha além do que o cliente espera.
func (e *Engine) encaminhar(ctx context.Context, m Inbound, texto string, inicio time.Time) {
	convID := m.ConversationID
	e.log.Debug("encaminhando ao cérebro",
		"conversation_id", convID, "telefone", mascararTelefone(m.Telefone), "chars", len(texto))

	if !e.disjuntor.permite(e.agora()) {
		_, causa := e.disjuntor.estado()
		e.TransferirParaHumano(ctx, convID, Transferencia{
			Origem: OrigemDisjuntor, Classe: brain.ClasseDefinitiva,
			Causa:   "cérebro em pausa por falhas repetidas (" + causa + ")",
			Detalhe: "disjuntor aberto; cérebro não foi chamado",
		})
		return
	}

	teto := e.cfg.TurnoTeto
	if teto <= 0 {
		teto = 45 * time.Second
	}
	prazo := inicio.Add(teto)
	req := brain.ChatRequest{
		ConversationID: convID,
		Canal:          "whatsapp",
		Mensagem:       texto,
		Telefone:       m.Telefone,
	}

	var resp *brain.ChatResponse
	for tentativa := 0; ; tentativa++ {
		cctx, cancel := context.WithDeadline(ctx, prazo)
		r, err := e.brain.Chat(cctx, req)
		cancel()
		if err == nil {
			resp = r
			e.disjuntor.sucesso()
			e.vigia.Turno(true)
			break
		}
		t := transferenciaPorErroDoCerebro(err)
		if t.Classe != brain.ClasseTransitoria {
			// Definitiva: repetir não resolve. Transfere e conta no disjuntor.
			chave := t.Classe + "|" + t.Causa
			if be, ok := brain.ComoErro(err); ok {
				chave = be.Chave()
			}
			if e.disjuntor.falhaDefinitiva(chave, t.Causa, e.agora()) {
				e.log.Error("disjuntor do cérebro ABERTO", "chave", chave, "causa", t.Causa, "por", disjuntorAbertura.String())
			}
			e.TransferirParaHumano(ctx, convID, t)
			return
		}
		e.disjuntor.falhaTransitoria()
		e.log.Warn("falha transitória do cérebro", "conversation_id", convID, "tentativa", tentativa+1, "err", err)
		if tentativa >= len(e.esperasRetry) {
			t.Causa = fmt.Sprintf("%s — %d tentativas sem sucesso", t.Causa, tentativa+1)
			e.TransferirParaHumano(ctx, convID, t)
			return
		}
		espera := e.esperasRetry[tentativa]
		if e.agora().Add(espera).After(prazo) {
			t.Causa = fmt.Sprintf("%s — sem tempo para nova tentativa (teto de %s)", t.Causa, teto)
			e.TransferirParaHumano(ctx, convID, t)
			return
		}
		if !e.dormir(ctx, espera) {
			return
		}
	}
	reply := strings.TrimSpace(resp.Reply)
	if reply == "" {
		reply = MsgSemResposta
	}
	e.log.Debug("resposta do cérebro",
		"conversation_id", convID, "fontes", resp.Fontes, "chars", len(reply))
	// TODO(E2.1): executar `resp.Acoes` (transferir) pelo caminho único e
	// gravar `resp.Classificacao` na tabela conversas.
	e.send(ctx, convID, reply)
}

// send divide a resposta nas bolhas marcadas pelo cérebro (`---`), formata
// cada uma para o WhatsApp (F3) e quebra as que ainda passarem do limite
// (F12), entregando cada parte como mensagem separada — com uma pausa curta
// entre elas para imitar o ritmo de quem digita.
//
// TODO(E2.1): antes da primeira bolha, reconferir no store se a conversa
// continua com o bot (estado `transferida` = humano assumiu, ZC-6).
func (e *Engine) send(ctx context.Context, convID, content string) {
	if content == "" {
		return
	}
	primeira := true
	for _, bolha := range DividirBolhas(content) {
		for _, parte := range QuebrarMensagem(FormatWhatsApp(bolha), e.cfg.ReplyMaxChars) {
			if !primeira {
				e.pausaEntreBolhas(ctx)
			}
			primeira = false
			if err := e.tr.EnviarMensagem(ctx, convID, parte); err != nil {
				e.log.Error("enviar mensagem", "conversation_id", convID, "err", err)
				return
			}
		}
	}
}

// pausaEntreBolhas espera BolhaPausa entre mensagens consecutivas do mesmo
// turno. O worker é por conversa, então a espera não atrasa outras conversas.
func (e *Engine) pausaEntreBolhas(ctx context.Context) {
	if e.cfg.BolhaPausa <= 0 {
		return
	}
	t := time.NewTimer(e.cfg.BolhaPausa)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
