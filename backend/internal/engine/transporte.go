package engine

import (
	"context"
	"time"

	"bot-infominas/internal/infodesk"
)

// Transporte é o que a engine usa do canal de atendimento. É satisfeito pelo
// cliente do Infodesk (internal/infodesk) através de NovoTransporteInfodesk;
// os testes usam um fakeInfodesk. A engine nunca vê o envelope da API: só
// estas operações.
type Transporte interface {
	// EnviarMensagem entrega UMA bolha de texto ao cliente na conversa
	// (`POST /conversations/:id/messages {text}`).
	EnviarMensagem(ctx context.Context, convID, texto string) error
	// Handoff passa a conversa para humanos (`POST /conversations/:id/handoff`,
	// ADR 0007): é o ÚNICO caminho de transferência. deptID vazio = sem
	// departamento (distribuição automática do Infodesk); note é a nota
	// interna, escrita por código.
	Handoff(ctx context.Context, convID, deptID, note string) error
	// LerConversa devolve a conversa crua (DADO externo, nunca vai ao
	// cliente nem ao prompt). Reservado para a checagem "bot ainda conduz?"
	// quando um evento reentregue é mais velho que o estado local (W9) e para
	// o reconciliador (E3.3); a engine ainda não a chama.
	LerConversa(ctx context.Context, convID string) (map[string]any, error)
	// Lookup identifica o telefone (`GET /contacts/lookup?phone=`).
	Lookup(ctx context.Context, telefone string) (infodesk.Lookup, error)
}

// Inbound é uma mensagem do cliente já neutra — sem o envelope do
// transporte. Quem recebe o webhook (internal/webhook, E2.1) monta uma e
// chama Engine.HandleMensagem; a engine não conhece o formato do Infodesk.
type Inbound struct {
	ConversationID string
	Telefone       string // E.164 — PII: só mascarado em log
	Nome           string // nome do perfil do contato (pode ser apelido) — PII
	Texto          string
	MsgID          string    // id da mensagem no transporte: segunda chave de dedup
	TemMidia       bool      // áudio/imagem/arquivo: o bot não lê, transfere com aviso
	CriadoEm       time.Time // quando o cliente mandou; base do teto do turno
}

// mascararTelefone deixa só os 4 últimos dígitos visíveis (regra 5 do
// CLAUDE.md: PII mascarada em log INFO).
func mascararTelefone(s string) string {
	r := []rune(s)
	if len(r) <= 4 {
		return "****"
	}
	return "****" + string(r[len(r)-4:])
}
