// Package engine orquestra: webhook → higiene/dedup → fila por conversa
// (coalescência) → gate de identidade → (quando identificado) cérebro Python →
// formatação WhatsApp → resposta no Chatwoot.
package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"bot-azapfy/internal/brain"
	"bot-azapfy/internal/chatwoot"
	"bot-azapfy/internal/config"
	"bot-azapfy/internal/identity"
	"bot-azapfy/internal/store"
)

// ChatwootAPI é o que a engine usa do cliente Chatwoot (satisfeito por
// *chatwoot.Client; interface para os testes usarem um Chatwoot falso).
type ChatwootAPI interface {
	SetLabels(ctx context.Context, convID int64, labels []string) error
	SendMessage(ctx context.Context, convID int64, content string, private bool) error
	ToggleStatus(ctx context.Context, convID int64, status string) error
	AssignTeam(ctx context.Context, convID int64, teamID int64) error
	ToggleTyping(ctx context.Context, convID int64, on bool) error
}

// BrainAPI é o que a engine usa do cérebro (satisfeito por *brain.Client).
type BrainAPI interface {
	Chat(ctx context.Context, req brain.ChatRequest) (*brain.ChatResponse, error)
}

// Engine reúne as dependências do orquestrador.
type Engine struct {
	cfg   *config.Config
	cw    ChatwootAPI
	gate  *identity.Gate
	brain BrainAPI
	st    store.Store
	log   *slog.Logger
	agora func() time.Time // relógio injetável (IDs de ocorrência, notas, testes)

	// Resiliência ao cérebro (ZC-5): esperas entre novas tentativas de falha
	// TRANSITÓRIA, espera injetável (testes não dormem) e o disjuntor.
	esperasRetry []time.Duration
	dormir       func(ctx context.Context, d time.Duration) bool
	disjuntor    *disjuntor

	// Coalescência/serialização por conversa (F11) — ver coalescer.go.
	baseCtx      context.Context
	debounce     time.Duration
	debounceTeto time.Duration
	mu           sync.Mutex
	filas        map[int64]chan inbound
}

// New constrói a engine. Chame Start(ctx) antes de processar mensagens.
func New(cfg *config.Config, cw ChatwootAPI, gate *identity.Gate, brainClient BrainAPI, st store.Store, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{
		cfg:          cfg,
		cw:           cw,
		gate:         gate,
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
		filas:        make(map[int64]chan inbound),
	}
}

// Start registra o contexto-raiz dos workers por conversa (cancelado no
// shutdown). Os workers em si nascem sob demanda, um por conversa ativa.
func (e *Engine) Start(ctx context.Context) {
	e.baseCtx = ctx
}

// HandleMessageCreated valida/higieniza a mensagem de entrada e a entrega à
// fila da conversa. O processamento de verdade acontece no worker (um lote
// por vez por conversa — ver coalescer.go).
func (e *Engine) HandleMessageCreated(ctx context.Context, msg *chatwoot.MessageCreated) {
	convID := msg.Conversation.ID

	// Filtro de borda: só mensagens de entrada, do contato, não privadas.
	if !msg.MessageType.IsIncoming() || !msg.Sender.IsContact() || msg.Private {
		return
	}
	// Grupo de WhatsApp: o bot não responde (não há cliente a identificar e a
	// resposta iria para todos os participantes). Fica antes da adoção de
	// etiqueta para a conversa nem entrar na fila do bot.
	if msg.EhGrupo() {
		e.log.Info("mensagem de grupo ignorada", "conversation_id", convID)
		return
	}
	// Gate de etiqueta (opcional): só atua na fila do bot. Vazio = processa tudo.
	if e.cfg.LabelBot != "" && !chatwoot.HasLabel(msg.Conversation.Labels, e.cfg.LabelBot) {
		// O Chatwoot REABRE conversa resolvida quando o contato volta a
		// escrever — não há conversation_created. Conversa da caixa do bot sem
		// NENHUMA etiqueta de fila é adotada aqui mesmo; com fila-humano (ou
		// fora da caixa), continua ignorada.
		if !e.adotarConversa(ctx, &msg.Conversation) {
			e.log.Info("mensagem ignorada: conversa sem a etiqueta do bot",
				"conversation_id", convID, "label_bot", e.cfg.LabelBot)
			return
		}
		e.log.Info("conversa adotada pela fila do bot (reaberta/sem etiqueta)",
			"conversation_id", convID)
	}

	// Higiene de envelope (F2): o gate/cérebro veem a fala, não a assinatura
	// que o relay grudou ("**Fulano:**\n...").
	content := StripAssinatura(msg.Content)
	if content == "" {
		return
	}

	// Descarta eventos velhos (F10): re-sync do relay recria mensagens antigas
	// no Chatwoot — responder a elas gera "resposta-fantasma".
	if e.cfg.EventoIdadeMax > 0 && !msg.CreatedAt.IsZero() {
		if idade := time.Since(msg.CreatedAt.Time); idade > e.cfg.EventoIdadeMax {
			e.log.Warn("mensagem antiga descartada",
				"conversation_id", convID, "idade", idade.String(), "source_id", msg.SourceID)
			return
		}
	}

	// Idempotência por WAID (F10): o dedup por delivery-id do webhook não pega
	// a MESMA mensagem de WhatsApp recriada com outro id no Chatwoot.
	if msg.SourceID != "" {
		fresh, err := e.st.MarkProcessed(ctx, "waid:"+msg.SourceID)
		if err != nil {
			e.log.Error("dedup por waid", "source_id", msg.SourceID, "err", err)
		} else if !fresh {
			e.log.Info("mensagem duplicada (waid) ignorada",
				"conversation_id", convID, "source_id", msg.SourceID)
			return
		}
	}

	recebido := msg.CreatedAt.Time
	if recebido.IsZero() {
		recebido = e.agora()
	}
	e.enfileirar(convID, inbound{
		conv:       msg.Conversation,
		phone:      msg.Phone(),
		content:    content,
		recebidoEm: recebido,
	})
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

// processarLote roda UM turno para a rajada coletada: junta os textos, avança
// o gate e, se identificado, chama o cérebro. Chamado apenas pelo worker da
// conversa — nunca em paralelo para o mesmo conversation_id.
func (e *Engine) processarLote(ctx context.Context, lote []inbound) {
	if len(lote) == 0 {
		return
	}
	ultimo := lote[len(lote)-1]
	convID := ultimo.conv.ID

	textos := make([]string, 0, len(lote))
	for _, m := range lote {
		textos = append(textos, m.content)
	}
	content := strings.TrimSpace(strings.Join(textos, "\n"))
	if content == "" {
		return
	}
	if len(lote) > 1 {
		e.log.Info("rajada coalescida", "conversation_id", convID, "mensagens", len(lote))
	}

	res := e.gate.Process(ctx, convID, ultimo.phone, content)
	e.log.Info("gate", "conversation_id", convID, "acao", res.Acao)

	switch res.Acao {
	case identity.AcaoPerguntar, identity.AcaoSaudar:
		e.send(ctx, convID, res.Reply)
	case identity.AcaoRotearHumano:
		// O gate já escolheu a mensagem (persona); a transferência em si é a
		// mesma de toda falha: etiqueta, equipe, nota privada com ID, registro.
		origem := OrigemGate
		if res.Origem != "" {
			origem = res.Origem // ex.: falha técnica do Mongo (identity.OrigemMongo)
		}
		e.TransferirParaHumano(ctx, &ultimo.conv, Transferencia{
			Origem: origem, Causa: res.Motivo, Mensagem: res.Reply,
		})
	case identity.AcaoEncaminhar:
		// O teto do turno conta desde a PRIMEIRA mensagem da rajada.
		inicio := lote[0].recebidoEm
		if inicio.IsZero() {
			inicio = e.agora()
		}
		e.encaminhar(ctx, &ultimo.conv, ultimo.phone, content, res, inicio)
	case identity.AcaoIgnorar:
		// já roteado para humano — nada a fazer.
	}
}

// HandleConversationUpdated reage a UMA mudança: a conversa foi RESOLVIDA no
// Chatwoot. Mudanças de etiqueta continuam sem ação (a identificação é dirigida
// pela mensagem do cliente).
func (e *Engine) HandleConversationUpdated(ctx context.Context, ev *chatwoot.ConversationUpdated) {
	if !ev.StatusChangedTo(chatwoot.StatusResolved) {
		e.log.Debug("conversation_updated sem resolução (sem ação)", "conversation_id", ev.ID)
		return
	}
	e.encerrarEpisodio(ctx, ev.ID)
}

// HandleConversationStatusChanged é o mesmo tratamento pelo evento dedicado do
// Chatwoot (só chega se o webhook assinar conversation_status_changed).
func (e *Engine) HandleConversationStatusChanged(ctx context.Context, ev *chatwoot.ConversationStatusChanged) {
	if ev.Status != chatwoot.StatusResolved {
		return
	}
	e.encerrarEpisodio(ctx, ev.ID)
}

// encerrarEpisodio apaga o estado do gate da conversa resolvida. Resolver é o
// fim do episódio: o Chatwoot REUTILIZA a conversa quando o contato volta a
// escrever, e sem isto uma conversa em GateFalha ("roteada") ficaria muda
// até o GATE_FALHA_TTL vencer — cliente com chamado novo sem resposta. O cache
// telefone→perfil (`identities`) NÃO é tocado: quem já se identificou volta
// direto ao cérebro na próxima mensagem, sem novo login.
func (e *Engine) encerrarEpisodio(ctx context.Context, convID int64) {
	if err := e.st.DeleteGate(ctx, convID); err != nil {
		e.log.Error("apagar estado do gate ao resolver", "conversation_id", convID, "err", err)
		return
	}
	e.log.Info("conversa resolvida: estado do gate apagado", "conversation_id", convID)
}

// HandleConversationCreated coloca conversas novas na fila do bot: aplica a
// LabelBot no conversation_created, escopado pela caixa INBOX_ID (0 = todas).
// É o próprio bot que etiqueta — mesmo padrão do triage-bot do omni-route —
// porque automation rule nativa do Chatwoot não é gerenciada pelo omni-route
// (viraria configuração fantasma, invisível no painel do produto).
func (e *Engine) HandleConversationCreated(ctx context.Context, ev *chatwoot.ConversationCreated) {
	if e.adotarConversa(ctx, &ev.Conversation) {
		e.log.Info("conversa nova na fila do bot",
			"conversation_id", ev.ID, "inbox_id", ev.InboxID, "label", e.cfg.LabelBot)
	}
}

// adotarConversa aplica a LabelBot se a conversa for elegível: na caixa do bot
// (INBOX_ID; 0 = todas), de um contato individual (grupo nunca é adotado) e
// sem NENHUMA etiqueta de fila — nem bot (nada a fazer) nem humano (a
// conversa é da equipe). Devolve true se etiquetou.
func (e *Engine) adotarConversa(ctx context.Context, conv *chatwoot.Conversation) bool {
	if e.cfg.LabelBot == "" {
		return false
	}
	if e.cfg.InboxID != 0 && conv.InboxID != e.cfg.InboxID {
		e.log.Debug("conversa fora da caixa do bot",
			"conversation_id", conv.ID, "inbox_id", conv.InboxID)
		return false
	}
	if conv.EhGrupo() {
		e.log.Info("conversa de grupo não adotada pela fila do bot", "conversation_id", conv.ID)
		return false
	}
	if chatwoot.HasLabel(conv.Labels, e.cfg.LabelBot) ||
		(e.cfg.LabelHumano != "" && chatwoot.HasLabel(conv.Labels, e.cfg.LabelHumano)) {
		return false
	}
	// SetLabels SUBSTITUI o conjunto inteiro — preserva as existentes.
	labels := append(append([]string{}, conv.Labels...), e.cfg.LabelBot)
	if err := e.cw.SetLabels(ctx, conv.ID, labels); err != nil {
		e.log.Error("aplicar etiqueta do bot",
			"conversation_id", conv.ID, "err", err)
		return false
	}
	return true
}

// encaminhar chama o cérebro com a política de resiliência (ZC-5):
//
//   - disjuntor aberto → não chama; transfere direto (origem disjuntor);
//   - falha DEFINITIVA → não repete; transfere e alimenta o disjuntor;
//   - falha TRANSITÓRIA → até len(esperasRetry) novas tentativas silenciosas
//     (2 s e 5 s), com "digitando" ligado, enquanto couber no teto do turno
//     (TURNO_TETO desde a mensagem do cliente); esgotou → transfere.
//
// A chamada ao cérebro recebe o prazo do teto como deadline do contexto: o
// cérebro nunca trabalha além do que o cliente espera.
func (e *Engine) encaminhar(ctx context.Context, conv *chatwoot.Conversation, phone, content string, res identity.Resultado, inicio time.Time) {
	var login string
	if res.Perfil != nil {
		login = res.Perfil.Login
	}
	e.log.Debug("encaminhando ao cérebro",
		"conversation_id", conv.ID, "telefone", phone, "login", login, "mensagem", content)

	if !e.disjuntor.permite(e.agora()) {
		_, causa := e.disjuntor.estado()
		e.TransferirParaHumano(ctx, conv, Transferencia{
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
		ConversationID: strconv.FormatInt(conv.ID, 10),
		Canal:          "whatsapp",
		Mensagem:       content,
		Identidade:     res.Perfil,
		Telefone:       phone,
	}

	var resp *brain.ChatResponse
	for tentativa := 0; ; tentativa++ {
		cctx, cancel := context.WithDeadline(ctx, prazo)
		r, err := e.brain.Chat(cctx, req)
		cancel()
		if err == nil {
			resp = r
			e.disjuntor.sucesso()
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
			e.TransferirParaHumano(ctx, conv, t)
			return
		}
		e.disjuntor.falhaTransitoria()
		e.log.Warn("falha transitória do cérebro", "conversation_id", conv.ID, "tentativa", tentativa+1, "err", err)
		if tentativa >= len(e.esperasRetry) {
			t.Causa = fmt.Sprintf("%s — %d tentativas sem sucesso", t.Causa, tentativa+1)
			e.TransferirParaHumano(ctx, conv, t)
			return
		}
		espera := e.esperasRetry[tentativa]
		if e.agora().Add(espera).After(prazo) {
			t.Causa = fmt.Sprintf("%s — sem tempo para nova tentativa (teto de %s)", t.Causa, teto)
			e.TransferirParaHumano(ctx, conv, t)
			return
		}
		// O cliente vê "digitando" enquanto o bot tenta de novo em silêncio.
		if err := e.cw.ToggleTyping(ctx, conv.ID, true); err != nil {
			e.log.Debug("indicador digitando", "conversation_id", conv.ID, "err", err)
		}
		if !e.dormir(ctx, espera) {
			return
		}
	}
	reply := strings.TrimSpace(resp.Reply)
	if reply == "" {
		reply = "Desculpe, não consegui formular uma resposta agora. Pode reformular?"
	}
	e.log.Debug("resposta do cérebro",
		"conversation_id", conv.ID, "fontes", resp.Fontes, "reply", reply)
	e.send(ctx, conv.ID, reply)
}

// send divide a resposta nas bolhas marcadas pelo cérebro (`---`), formata
// cada uma para o WhatsApp (F3) e quebra as que ainda passarem do limite
// (F12), entregando cada parte como mensagem separada — com uma pausa curta
// entre elas para imitar o ritmo de quem digita.
func (e *Engine) send(ctx context.Context, convID int64, content string) {
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
			if err := e.cw.SendMessage(ctx, convID, parte, false); err != nil {
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
