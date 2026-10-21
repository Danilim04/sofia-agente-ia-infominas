package reconciliador

import (
	"context"
	"time"

	"bot-infominas/internal/config"
)

// Options configura o reconciliador. Os defaults são hipóteses de trabalho
// configuráveis (nada disto é regra de negócio fechada).
type Options struct {
	// Ativo liga o loop (RECONCILIA_ATIVO; default true — nasce ligado).
	Ativo bool
	// Intervalo entre ciclos (RECONCILIA_INTERVALO; 60 s, como recomenda o
	// docs/infodesk/05, seção 5.5).
	Intervalo time.Duration
	// Retroativo: na 1ª execução SEM nenhum cursor, eventos com createdAt mais
	// velho que now−Retroativo são descartados (RECONCILIA_RETROATIVO; 1 h).
	Retroativo time.Duration
	// Limite de eventos por página do feed (500, o máximo documentado).
	Limite int
	// Atraso: o cursor `infodesk` persistido anda este tempo atrás do relógio
	// (RECONCILIA_ATRASO; 2 min), contado a partir do maior entre o createdAt do
	// evento e o instante em que este processo o entregou ao webhook. Os eventos dentro da janela são relidos no
	// ciclo seguinte (duplicata = no-op) e é isso que torna recuperável o que
	// estava na fila em memória num crash. Hipótese de trabalho, sem fonte no
	// Infodesk: ver docs/infodesk/07 item W9 (reentrega/ordem) e o ADR 0007.
	Atraso time.Duration // negativo desliga o atraso (só testes)
	// TicketPendenteTTL: prazo para um ticket `pendente` aparecer na listagem
	// antes de virar `falha` (TICKET_PENDENTE_TTL; 10 min, ADR 0005).
	TicketPendenteTTL time.Duration
	// BotTimeoutHandoff: conversa do bot sem resposta há mais que isto vai a um
	// humano (BOT_TIMEOUT_HANDOFF; 5 min). // ver docs/infodesk/07 item N1.
	BotTimeoutHandoff time.Duration
	// Skew: tolerância entre o relógio do Infodesk (`ultima_msg_em`) e o nosso
	// (`ultima_resposta_em`) ao decidir que uma mensagem ficou sem resposta
	// (RECONCILIA_SKEW; 60 s). Hipótese de trabalho. // ver docs/infodesk/07 item W9.
	Skew time.Duration
	// BackoffMax: teto da espera após RATE_LIMITED (RECONCILIA_BACKOFF_MAX;
	// 5 min). // ver docs/infodesk/07 item A4 (300 req/min).
	BackoffMax time.Duration

	// Now e Dormir são injetáveis nos testes (nil = relógio e espera reais).
	Now    func() time.Time
	Dormir func(ctx context.Context, d time.Duration) bool
}

// comPadroes completa os campos zerados com os defaults (Ativo não: um
// Options literal com Ativo=false desliga; use OptionsDeConfig para o
// default true).
func (o Options) comPadroes() Options {
	pad := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	pad(&o.Intervalo, 60*time.Second)
	pad(&o.Retroativo, time.Hour)
	if o.Atraso == 0 {
		o.Atraso = 2 * time.Minute
	}
	pad(&o.TicketPendenteTTL, 10*time.Minute)
	pad(&o.BotTimeoutHandoff, 5*time.Minute)
	pad(&o.BackoffMax, 5*time.Minute)
	pad(&o.Skew, time.Minute)
	if o.Limite <= 0 {
		o.Limite = 500
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Dormir == nil {
		o.Dormir = dormirDeVerdade
	}
	return o
}

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

// OptionsDeConfig monta as Options a partir da configuração única do gateway
// (internal/config: RECONCILIA_*, TICKET_PENDENTE_TTL, BOT_TIMEOUT_HANDOFF;
// defaults e validação moram lá).
func OptionsDeConfig(cfg *config.Config) Options {
	return Options{
		Ativo:             cfg.ReconciliaAtivo,
		Intervalo:         cfg.ReconciliaIntervalo,
		Retroativo:        cfg.ReconciliaRetroativo,
		Atraso:            cfg.ReconciliaAtraso,
		BackoffMax:        cfg.ReconciliaBackoffMax,
		Skew:              cfg.ReconciliaSkew,
		TicketPendenteTTL: cfg.TicketPendenteTTL,
		BotTimeoutHandoff: cfg.BotTimeoutHandoff,
	}
}
