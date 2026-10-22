// Package alerta decide QUANDO avisar o responsável pelo bot (ZC-7) e entrega
// o aviso por um Notificador. O canal definitivo (WhatsApp, e-mail, webhook
// de chat...) ainda não foi decidido: a implementação padrão é o log
// estruturado `sofia_alerta` (EventoSofiaAlerta) + um webhook HTTP opcional (ALERTA_WEBHOOK_URL,
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
	TipoFalhaDefinitiva = "falha_definitiva" // uma falha que repetir não resolve (402, chave, bug)
	TipoTaxaDeFalhas    = "taxa_de_falhas"   // >5% dos turnos com falha em 15 min (mín. 3)
	TipoDisjuntorAberto = "disjuntor_aberto" // o gateway parou de chamar o cérebro

	// Tipos do gateway Infodesk (E2.1; docs/plano/01-ferramentas.md, seção B).
	TipoWebhookTimestamp      = "webhook_timestamp_fora_da_janela" // entrega autêntica fora da janela: replay ou relógio errado
	TipoChaveSemEscopo        = "chave_sem_escopo"                 // GET /me no boot: falta escopo ou a chave não conduz conversas
	TipoHandoffFalhou         = "handoff_falhou"                   // o Infodesk não aceitou o handoff; o bot cala mesmo assim
	TipoProducaoAberta        = "producao_aberta"                  // boot em produção aberta: sem lista de testadores, todo cliente fala com a Sofia
	TipoEventoIlegivel        = "evento_ilegivel"                  // Data.Erro != nil: objeto presente mas não decodificável
	TipoLookupIlegivel        = "lookup_ilegivel"                  // Lookup.Erro != nil: cai no fallback CPF, mas o motivo precisa aparecer
	TipoOrganizacaoDivergente = "organizacao_divergente"           // organizationId do webhook ≠ GET /me
)

// Métricas (contadores em memória; o log `sofia_metrica` as acompanha).
const MetricaHumanoAssumiuSemHandoff = "humano_assumiu_sem_handoff"

// Alerta é o aviso em si: texto pronto para humano + campos para correlação.
type Alerta struct {
	Tipo           string
	Titulo         string
	Texto          string
	IDOcorrencia   string
	ConversationID string
	Link           string // link da conversa no painel de atendimento (quando há; vazio até E2.1)
	Quando         time.Time
}

// Notificador entrega um alerta. Implementações não devem bloquear por muito
// tempo nem lançar pânico — a engine chama no caminho do turno.
type Notificador interface {
	Notificar(ctx context.Context, a Alerta)
}

// Log escreve o alerta como log estruturado `sofia_alerta` (sempre ligado;
// campos em CamposDoEvento). Título e texto passam por MascararPII: o contrato
// é não levarem PII, e esta é a rede de segurança para texto montado de erro
// externo.
type Log struct{ L *slog.Logger }

func (n Log) Notificar(_ context.Context, a Alerta) {
	l := n.L
	if l == nil {
		l = slog.Default()
	}
	l.Error(EventoSofiaAlerta, "tipo", a.Tipo, "titulo", MascararPII(a.Titulo), "texto", MascararPII(a.Texto),
		"id_ocorrencia", a.IDOcorrencia, "conversation_id", a.ConversationID, "link", a.Link)
}

// Webhook faz POST JSON `{"text": "...", ...}` numa URL (Slack/Discord/n8n
// aceitam esse mínimo). Falha de entrega só loga — alerta nunca
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
		"text":            fmt.Sprintf("[Sofia] %s\n%s", MascararPII(a.Titulo), MascararPII(a.Texto)),
		"tipo":            a.Tipo,
		"titulo":          MascararPII(a.Titulo),
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
