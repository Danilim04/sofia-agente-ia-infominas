package engine

import (
	"context"
	"time"
)

// Transporte é o que a engine usa do canal de atendimento. A partir de E2.1
// é satisfeito pelo cliente do Infodesk (internal/infodesk); até lá o
// main.go liga um transporte nulo que só loga, e os testes usam um
// fakeTransporte. A engine nunca vê o envelope da API: só estas três
// operações.
type Transporte interface {
	// EnviarMensagem entrega UMA bolha de texto ao cliente na conversa.
	EnviarMensagem(ctx context.Context, convID string, body string) error
	// TransferirDepartamento move a conversa para o departamento humano.
	TransferirDepartamento(ctx context.Context, convID, deptID string) error
	// LerConversa devolve a conversa crua (DADO externo, nunca vai ao
	// cliente nem ao prompt). Reservado para a checagem "humano assumiu"
	// e para o reconciliador (E2.x); a engine ainda não a chama.
	LerConversa(ctx context.Context, convID string) (map[string]any, error)
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
