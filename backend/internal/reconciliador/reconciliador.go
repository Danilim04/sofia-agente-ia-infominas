// Package reconciliador garante que o gateway não perde nada do Infodesk
// (docs/infodesk/05, seção 5.5; docs/plano/01-ferramentas.md, seção B):
//
//   - feed: no boot e a cada RECONCILIA_INTERVALO lê GET /events a partir do
//     cursor `infodesk` e entrega cada envelope ao MESMO handler do webhook
//     (webhook.Server.Receber: dedup por id do evento, filtro de turno, fila
//     por conversa). O cursor só avança depois da página processada;
//   - (a) tickets `pendente` são procurados por listagem até
//     TICKET_PENDENTE_TTL (ADR 0005);
//   - (b) o vigia BOT_TIMEOUT_HANDOFF transfere a conversa que o cliente
//     escreveu e a Sofia não respondeu (docs/infodesk/07, N1).
//   - (c) o handoff que a engine não conseguiu confirmar (marca no store) é
//     repetido com backoff e alerta até o Infodesk aceitar (docs/plano/01, B).
//
// Nasce ligado (RECONCILIA_ATIVO=true). Chave sem `events:read` (403) não
// derruba nada: alerta de configuração e o loop segue só com (a), (b) e (c).
package reconciliador

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"bot-infominas/internal/alerta"
	"bot-infominas/internal/engine"
	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
	"bot-infominas/internal/webhook"
)

// Tipos de alerta do reconciliador (strings livres para alerta.Vigia.Aviso).
const (
	TipoFeedSemEscopo        = "reconciliador_sem_escopo"    // 403 no GET /events: chave sem events:read
	TipoFeedFalhando         = "reconciliador_feed_falhando" // o feed falha há várias execuções seguidas
	TipoTicketPendenteFalh   = "ticket_pendente_falhou"      // pendente passou do TTL sem aparecer no Infodesk
	TipoTicketNaoChecado     = "ticket_pendente_nao_checado" // TTL vencido e a listagem não respondeu
	TipoBotSemResposta       = "bot_sem_resposta_handoff"    // vigia N1 transferiu uma conversa sem resposta
	TipoHandoffNaoConfirmado = "handoff_nao_confirmado"      // o handoff repetido pelo vigia continua falhando
	OrigemVigia              = engine.OrigemVigia            // Transferencia.Origem do vigia N1
	entregaReconciliador     = "reconciliador"               // "delivery" informativo dos eventos do feed
	falhasParaAlertarFeed    = 5                             // execuções seguidas com erro no feed antes do alerta
	maxPaginasListaTickets   = 5                             // páginas de GET /tickets por ticket pendente
	limiteListaTickets       = 100                           // tamanho da página de GET /tickets (máx. da API)
)

// Infodesk é o que o reconciliador usa do cliente da API (*infodesk.Client).
type Infodesk interface {
	Eventos(ctx context.Context, after int64, limit int) (infodesk.FeedEventos, error)
	ListarTickets(ctx context.Context, f infodesk.FiltroTickets) (infodesk.Pagina[infodesk.Ticket], error)
	Conversa(ctx context.Context, id string) (infodesk.Conversa, error)
}

// Receptor é a entrada comum de webhook e feed (*webhook.Server).
type Receptor interface {
	Receber(ctx context.Context, ev infodesk.Evento, entrega string) webhook.Recebimento
}

// Transferidor é o caminho único para o atendimento humano (ADR 0007;
// *engine.Engine).
type Transferidor interface {
	TransferirParaHumano(ctx context.Context, convID string, t engine.Transferencia) string
	// ReenviarHandoff repete UMA vez o handoff de uma marca de "handoff não
	// confirmado" e a apaga se der certo.
	ReenviarHandoff(ctx context.Context, h store.HandoffPendente) error
}

// Reconciliador roda o feed, a reconciliação de tickets e o vigia.
type Reconciliador struct {
	opt    Options
	idk    Infodesk
	rec    Receptor
	transf Transferidor
	st     store.Store
	vigia  *alerta.Vigia
	log    *slog.Logger

	recuperado       bool          // Recuperar já rodou
	backoff          time.Duration // espera atual após RATE_LIMITED (0 = sem backoff)
	feedSemEscopo    bool          // o último GET /events foi 403
	falhasDoFeed     int           // execuções seguidas com erro no feed
	alertouFalhaFd   bool
	cicloSemEscopo   bool // o feed deste ciclo foi 403
	partidaFeita     bool // pontoDePartida já decidiu a partida deste processo
	partidaSemCursor bool
	vistos           map[int64]time.Time // seq → 1ª entrega por este processo (janela de Atraso)
}

// New cria o reconciliador. vigia e log podem ser nil.
func New(o Options, idk Infodesk, rec Receptor, transf Transferidor, st store.Store, vigia *alerta.Vigia, log *slog.Logger) *Reconciliador {
	o = o.comPadroes()
	if log == nil {
		log = slog.Default()
	}
	return &Reconciliador{opt: o, idk: idk, rec: rec, transf: transf, st: st, vigia: vigia, log: log.With("componente", "reconciliador"), vistos: map[int64]time.Time{}}
}

// Run bloqueia até ctx ser cancelado: recupera o que um crash pode ter
// perdido (se main.go ainda não chamou Recuperar) e executa um ciclo agora e
// a cada Intervalo (mais o backoff de RATE_LIMITED).
func (r *Reconciliador) Run(ctx context.Context) {
	if !r.opt.Ativo {
		r.log.Warn("reconciliador desligado (RECONCILIA_ATIVO=false): eventos perdidos pelo webhook, tickets pendentes e conversas sem resposta NÃO serão recuperados")
		return
	}
	if err := r.Recuperar(ctx); err != nil {
		r.log.Error("recuperação de crash falhou; o feed segue só com o dedup atual", "err", err)
	}
	for {
		espera := r.executarSeguro(ctx)
		if !r.opt.Dormir(ctx, espera) {
			return
		}
	}
}

func (r *Reconciliador) executarSeguro(ctx context.Context) (espera time.Duration) {
	defer func() {
		if rec := recover(); rec != nil {
			r.log.Error("panic no ciclo do reconciliador", "recover", fmt.Sprint(rec))
			espera = r.opt.Intervalo
		}
	}()
	return r.Executar(ctx)
}

// Recuperar desfaz o dedup dos eventos que o webhook marcou como vistos depois
// do ponto de partida do feed: se o gateway morreu (kill -9) com eventos na
// fila em memória, eles já constavam como vistos e o feed os trataria como
// duplicata. Reprocessar o que de fato já foi tratado é seguro — a engine
// deduplica o turno por `msg:<id>`, o estado da conversa não é rebaixado por
// evento mais velho (W9) e ticket.* é idempotente.
//
// LIMITE: recupera o que estava na fila `jobs` do webhook. O turno que já
// passou por Engine.HandleMensagem (que grava `msg:<id>` antes de coalescer em
// memória) NÃO é recuperado: o replay é descartado pelo dedup da engine e a
// conversa cai no vigia BOT_TIMEOUT_HANDOFF. Desmarcar `msg:` aqui arriscaria
// responder duas vezes uma mensagem já respondida.
//
// Deve rodar ANTES de o webhook começar a receber (main.go chama
// sincronamente); Run chama se ninguém chamou. Só vale como rede de
// segurança porque o cursor persistido anda ATRASO atrás do relógio (ver
// Options.Atraso): o que estava na fila no crash ainda está depois dele.
func (r *Reconciliador) Recuperar(ctx context.Context) error {
	if r.recuperado || !r.opt.Ativo {
		return nil
	}
	r.recuperado = true
	inicio, _, err := r.pontoDePartida(ctx)
	if err != nil {
		return err
	}
	n, err := r.st.DesmarcarEventosDepois(ctx, inicio)
	if err != nil {
		return err
	}
	r.log.Info("recuperação de crash: dedup desfeito para reentrega pelo feed", "apos_seq", inicio, "eventos", n)
	return nil
}

// Executar roda UM ciclo (feed, depois (a), depois (b)) e devolve quanto
// esperar até o próximo: Intervalo, ou o backoff quando o Infodesk respondeu
// RATE_LIMITED (docs/infodesk/07, A4).
func (r *Reconciliador) Executar(ctx context.Context) time.Duration {
	limitado := false
	passos := []struct {
		nome string
		fn   func(context.Context) error
	}{
		{"feed", r.feed},
		{"tickets_pendentes", r.ticketsPendentes},
		{"handoffs_pendentes", r.handoffsPendentes},
		{"vigia", r.vigiaSemResposta},
		{"cadastros_vencidos", r.cadastrosVencidos},
	}
	for _, p := range passos {
		if ctx.Err() != nil {
			break
		}
		err := p.fn(ctx)
		if err == nil {
			continue
		}
		var limite *rateLimited
		if errors.As(err, &limite) {
			limitado = true
			r.esperarMais(limite.depois)
			r.log.Warn("Infodesk limitou as requisições (RATE_LIMITED); ciclo interrompido", "passo", p.nome, "espera", r.backoff.String())
			break
		}
		r.log.Error("passo do reconciliador falhou", "passo", p.nome, "err", err)
	}
	if limitado {
		return r.backoff
	}
	r.backoff = 0
	return r.opt.Intervalo
}

// rateLimited sinaliza RATE_LIMITED/429 (com o Retry-After, se veio).
type rateLimited struct {
	depois time.Duration
	err    error
}

func (e *rateLimited) Error() string { return e.err.Error() }
func (e *rateLimited) Unwrap() error { return e.err }

// limitar converte o erro do Infodesk em *rateLimited quando for o caso.
func limitar(err error) error {
	var he *infodesk.HTTPError
	if errors.As(err, &he) && (he.Code == infodesk.CodeRateLimited || he.Status == 429) {
		return &rateLimited{depois: he.RetryAfter, err: err}
	}
	return err
}

// esperarMais dobra o backoff (a partir de 2×Intervalo), respeitando o
// Retry-After e o teto BackoffMax.
func (r *Reconciliador) esperarMais(retryAfter time.Duration) {
	prox := r.backoff * 2
	if prox == 0 {
		prox = r.opt.Intervalo * 2
	}
	if retryAfter > prox {
		prox = retryAfter
	}
	if prox > r.opt.BackoffMax {
		prox = r.opt.BackoffMax
	}
	r.backoff = prox
}

// avisar alerta sem nunca derrubar o ciclo.
func (r *Reconciliador) avisar(tipo, titulo, texto, convID string) {
	r.vigia.Aviso(tipo, titulo, texto, convID)
}
