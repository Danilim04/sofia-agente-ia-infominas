package reconciliador

import (
	"context"
	"errors"
	"fmt"

	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
	"bot-infominas/internal/webhook"
)

// pontoDePartida devolve o `after` do feed: o cursor `infodesk` (do
// reconciliador) ou, na 1ª execução, o maior seq que o webhook viu
// (`infodesk_webhook`). semCursor=true quando não há nenhum dos dois — aí o
// feed é lido do começo e os eventos mais velhos que Retroativo são
// descartados.
func (r *Reconciliador) pontoDePartida(ctx context.Context) (after int64, semCursor bool, err error) {
	after, err = r.st.Cursor(ctx, store.NomeCursorInfodesk)
	if err != nil {
		return 0, false, fmt.Errorf("ler cursor infodesk: %w", err)
	}
	if after > 0 {
		return after, false, nil
	}
	after, err = r.st.Cursor(ctx, store.NomeCursorWebhook)
	if err != nil {
		return 0, false, fmt.Errorf("ler cursor infodesk_webhook: %w", err)
	}
	return after, after == 0, nil
}

// feed lê GET /events a partir do cursor, página a página enquanto hasMore, e
// entrega cada envelope a Receber (o handler do webhook). O cursor persistido
// avança só até o último evento do prefixo contíguo já entregue e fora da
// janela de Atraso: um erro de página ou uma fila cheia param o avanço no
// ponto exato e o ciclo seguinte recomeça dali.
func (r *Reconciliador) feed(ctx context.Context) error {
	cursor, semCursor, err := r.pontoDePartida(ctx)
	if err != nil {
		return err
	}
	if cursor > 0 {
		// Fixa a partida da 1ª execução no cursor do reconciliador: dali em
		// diante só ele manda (AvancarCursor nunca retrocede).
		if err := r.st.AvancarCursor(ctx, store.NomeCursorInfodesk, cursor); err != nil {
			return fmt.Errorf("gravar cursor infodesk: %w", err)
		}
	}
	agora := r.opt.Now()
	corteRetroativo := agora.Add(-r.opt.Retroativo)
	corteAtraso := agora.Add(-r.opt.Atraso)

	after := cursor
	prefixoFechado := false // um evento dentro do Atraso já impede o cursor de passar
	for {
		pag, err := r.idk.Eventos(ctx, after, r.opt.Limite)
		if err != nil {
			return r.erroDoFeed(err)
		}
		r.feedOK()
		var persistir int64
		for _, ev := range pag.Data {
			descartar := semCursor && !ev.CreatedAt.IsZero() && ev.CreatedAt.Before(corteRetroativo)
			if descartar {
				r.log.Debug("evento antigo descartado na 1ª execução", "event_id", ev.ID, "seq", ev.Seq)
			} else if r.rec.Receber(ctx, ev, entregaReconciliador) == webhook.FilaCheia {
				if persistir > 0 {
					if err := r.st.AvancarCursor(ctx, store.NomeCursorInfodesk, persistir); err != nil {
						return fmt.Errorf("gravar cursor infodesk: %w", err)
					}
				}
				r.log.Warn("fila do webhook cheia; o feed recomeça deste ponto no próximo ciclo", "seq", ev.Seq)
				return nil
			}
			if !prefixoFechado && (ev.CreatedAt.IsZero() || !ev.CreatedAt.After(corteAtraso)) {
				persistir = ev.Seq
			} else {
				prefixoFechado = true
			}
		}
		if persistir > 0 {
			if err := r.st.AvancarCursor(ctx, store.NomeCursorInfodesk, persistir); err != nil {
				return fmt.Errorf("gravar cursor infodesk: %w", err)
			}
		}
		if !pag.HasMore {
			return nil
		}
		// A paginação segue o nextAfter do feed (mesmo com o cursor persistido
		// atrasado); o cliente já recusa hasMore sem avanço.
		after = pag.NextAfter
	}
}

// erroDoFeed trata o erro de GET /events: 403 vira alerta de configuração (e
// não é erro do ciclo — (a) e (b) seguem); 429 vira backoff; o resto conta
// para o alerta de feed falhando.
func (r *Reconciliador) erroDoFeed(err error) error {
	var he *infodesk.HTTPError
	if errors.As(err, &he) && (he.Code == infodesk.CodeForbidden || he.Status == 403) {
		if !r.feedSemEscopo {
			r.log.Error("feed /events recusado (403): a chave não tem o escopo events:read", "err", err)
			r.avisar(TipoFeedSemEscopo, "Reconciliador sem acesso ao feed de eventos",
				"GET /events respondeu 403 FORBIDDEN: a chave do Infodesk não tem o escopo events:read. O reconciliador segue só com tickets pendentes e o vigia de conversa sem resposta; eventos que o webhook perder NÃO serão recuperados. Inclua events:read na chave.", "")
		}
		r.feedSemEscopo = true
		return nil
	}
	if lim := limitar(err); isRateLimited(lim) {
		return lim
	}
	r.falhasDoFeed++
	if r.falhasDoFeed >= falhasParaAlertarFeed && !r.alertouFalhaFd {
		r.alertouFalhaFd = true
		r.avisar(TipoFeedFalhando, "Reconciliador não consegue ler o feed de eventos",
			fmt.Sprintf("GET /events falhou em %d execuções seguidas (%s). Eventos que o webhook perder não estão sendo recuperados.", r.falhasDoFeed, classeDoErro(err)), "")
	}
	return err
}

func isRateLimited(err error) bool {
	var rl *rateLimited
	return errors.As(err, &rl)
}

// feedOK zera os contadores de falha quando o feed responde.
func (r *Reconciliador) feedOK() {
	if r.feedSemEscopo {
		r.log.Info("feed /events voltou a responder")
	}
	r.feedSemEscopo, r.falhasDoFeed, r.alertouFalhaFd = false, 0, false
}

// classeDoErro descreve o erro sem expor corpo da resposta (sem PII).
func classeDoErro(err error) string {
	var he *infodesk.HTTPError
	if errors.As(err, &he) {
		return fmt.Sprintf("HTTP %d %s", he.Status, he.Code)
	}
	if infodesk.Transitorio(err) {
		return "falha de rede ou 5xx"
	}
	return "erro inesperado"
}
