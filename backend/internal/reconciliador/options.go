package reconciliador

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"
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
// Options literal com Ativo=false desliga; use OptionsDoAmbiente para o
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

// OptionsDoAmbiente lê as variáveis RECONCILIA_ATIVO, RECONCILIA_INTERVALO,
// RECONCILIA_RETROATIVO, RECONCILIA_ATRASO, RECONCILIA_BACKOFF_MAX,
// RECONCILIA_SKEW, TICKET_PENDENTE_TTL e BOT_TIMEOUT_HANDOFF. Valor ausente = default;
// valor inválido = default com aviso no log (não derruba o boot).
//
// TODO(P3): mover para internal/config quando a zona dessa pasta permitir
// (hoje a E3.3 não a toca); nenhuma delas é segredo.
func OptionsDoAmbiente(log *slog.Logger) Options {
	if log == nil {
		log = slog.Default()
	}
	o := Options{Ativo: true}
	if v, ok := os.LookupEnv("RECONCILIA_ATIVO"); ok && strings.TrimSpace(v) != "" {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "0", "false", "f", "nao", "não", "no", "off":
			o.Ativo = false
		case "1", "true", "t", "sim", "yes", "on":
		default:
			log.Warn("RECONCILIA_ATIVO inválido; usando true", "valor", v)
		}
	}
	for _, d := range []struct {
		dst *time.Duration
		env string
	}{
		{&o.Intervalo, "RECONCILIA_INTERVALO"},
		{&o.Retroativo, "RECONCILIA_RETROATIVO"},
		{&o.Atraso, "RECONCILIA_ATRASO"},
		{&o.BackoffMax, "RECONCILIA_BACKOFF_MAX"},
		{&o.Skew, "RECONCILIA_SKEW"},
		{&o.TicketPendenteTTL, "TICKET_PENDENTE_TTL"},
		{&o.BotTimeoutHandoff, "BOT_TIMEOUT_HANDOFF"},
	} {
		v := strings.TrimSpace(os.Getenv(d.env))
		if v == "" {
			continue
		}
		dur, err := time.ParseDuration(v)
		if err != nil || dur <= 0 {
			log.Warn("duração inválida; usando o default", "variavel", d.env, "valor", v)
			continue
		}
		*d.dst = dur
	}
	return o
}
