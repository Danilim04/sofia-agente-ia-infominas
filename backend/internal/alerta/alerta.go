// Package alerta decide QUANDO avisar o responsável pelo bot (ZC-7) e entrega
// o aviso por um Notificador. O canal definitivo (WhatsApp, e-mail, webhook
// de chat...) ainda não foi decidido: a implementação padrão é o log
// estruturado `zapin_alerta` + um webhook HTTP opcional (ALERTA_WEBHOOK_URL,
// vazio por padrão — mesmo contrato `{"text": ...}` do SYNC_ALERTA_WEBHOOK).
package alerta

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Tipos de alerta.
const (
	TipoFalhaDefinitiva = "falha_definitiva" // uma falha que repetir não resolve (402, chave, bug, cadastro ilegível)
	TipoTaxaDeFalhas    = "taxa_de_falhas"   // >5% dos turnos com falha em 15 min (mín. 3)
	TipoDisjuntorAberto = "disjuntor_aberto" // o gateway parou de chamar o cérebro
	TipoReportado       = "reportar_zapin"   // atendente marcou a conversa com a etiqueta de reporte
)

// Alerta é o aviso em si: texto pronto para humano + campos para correlação.
type Alerta struct {
	Tipo           string
	Titulo         string
	Texto          string
	IDOcorrencia   string
	ConversationID int64
	Link           string // link da conversa no Chatwoot (quando há)
	Quando         time.Time
}

// Notificador entrega um alerta. Implementações não devem bloquear por muito
// tempo nem lançar pânico — a engine chama no caminho do turno.
type Notificador interface {
	Notificar(ctx context.Context, a Alerta)
}

// Log escreve o alerta como log estruturado `zapin_alerta` (sempre ligado).
type Log struct{ L *slog.Logger }

func (n Log) Notificar(_ context.Context, a Alerta) {
	l := n.L
	if l == nil {
		l = slog.Default()
	}
	l.Error("zapin_alerta", "tipo", a.Tipo, "titulo", a.Titulo, "texto", a.Texto,
		"id_ocorrencia", a.IDOcorrencia, "conversation_id", a.ConversationID, "link", a.Link)
}

// Webhook faz POST JSON `{"text": "...", ...}` numa URL (Slack/Discord/n8n/
// Chatwoot aceitam esse mínimo). Falha de entrega só loga — alerta nunca
// derruba o turno.
type Webhook struct {
	URL  string
	HTTP *http.Client
	L    *slog.Logger
}

func (n Webhook) Notificar(ctx context.Context, a Alerta) {
	if n.URL == "" {
		return
	}
	cli := n.HTTP
	if cli == nil {
		cli = &http.Client{Timeout: 5 * time.Second}
	}
	corpo, _ := json.Marshal(map[string]any{
		"text":            fmt.Sprintf("[Zapin] %s\n%s", a.Titulo, a.Texto),
		"tipo":            a.Tipo,
		"titulo":          a.Titulo,
		"id_ocorrencia":   a.IDOcorrencia,
		"conversation_id": a.ConversationID,
		"link":            a.Link,
		"quando":          a.Quando.Format(time.RFC3339),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL, bytes.NewReader(corpo))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := cli.Do(req)
	if err != nil {
		n.logar().Warn("alerta: webhook falhou", "tipo", a.Tipo, "err", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		n.logar().Warn("alerta: webhook recusou", "tipo", a.Tipo, "status", resp.StatusCode)
	}
}

func (n Webhook) logar() *slog.Logger {
	if n.L != nil {
		return n.L
	}
	return slog.Default()
}

// Multi encadeia notificadores.
type Multi []Notificador

func (m Multi) Notificar(ctx context.Context, a Alerta) {
	for _, n := range m {
		if n != nil {
			n.Notificar(ctx, a)
		}
	}
}

// Padrao monta o notificador de produção: log sempre; webhook se houver URL.
func Padrao(webhookURL string, log *slog.Logger) Notificador {
	m := Multi{Log{L: log}}
	if webhookURL != "" {
		m = append(m, Webhook{URL: webhookURL, L: log})
	}
	return m
}
