// Package store define a persistência: deduplicação de webhooks, estado do gate
// de identidade por conversa, e a "base própria" (cache telefone→perfil).
//
// Dois backends implementam a mesma interface: SQLite (arquivo local, default
// de dev) e Postgres (PG_URL — o mesmo Postgres do RAG/pgvector, em schema
// próprio `gateway`). A escolha é feita em main.go; o resto do código só vê
// a interface.
package store

import (
	"context"
	"time"
)

// Estados do gate de identidade.
const (
	GateNovo          = ""                       // sem estado: primeira interação
	GateAguardLogin   = "aguardando_login"       // pedimos o login, aguardando resposta
	GateAguardConfirm = "aguardando_confirmacao" // pedimos confirmação de um dado
	GateIdentificado  = "identificado"           // identidade resolvida
	GateFalha         = "falha"                  // desistimos; roteado para humano
)

// GateState é o estado persistido do gate de uma conversa. Data guarda um JSON
// de trabalho (login em curso, tentativas, perfil pendente de confirmação).
type GateState struct {
	ConversationID int64
	State          string
	Data           string
	UpdatedAt      time.Time
}

// CachedIdentity é uma entrada da base própria: telefone → perfil mínimo já
// resolvido e confirmado, com validade (TTL).
type CachedIdentity struct {
	Phone     string
	Login     string
	Perfil    string // JSON do perfil mínimo (mesmo formato do Contrato A)
	ExpiresAt time.Time
}

// Falha é uma ocorrência registrada quando a conversa é transferida à fila
// humana por falha (cérebro, Mongo, disjuntor, gate) ou a pedido do cliente.
// O ID (`ZPF-AAAAMMDD-HHMM-NN`) é o MESMO da nota privada na conversa, do log
// `zapin_falha` do gateway e — quando a falha veio do cérebro — do log
// `chat_erro` do Python: é por ele que o atendente e o responsável se acham.
type Falha struct {
	ID             string
	ConversationID int64
	Origem         string // gate | cerebro | mongo | disjuntor | cliente
	Classe         string // transitoria | definitiva | "" (não se aplica)
	Causa          string // linguagem simples (vai na nota privada e no alerta)
	Detalhe        string // técnico, sem dado pessoal (só log/tabela)
	CreatedAt      time.Time
}

// Store é a interface de persistência usada pelo resto da aplicação.
type Store interface {
	// MarkProcessed registra um delivery_id; devolve true se for novo.
	MarkProcessed(ctx context.Context, deliveryID string) (bool, error)

	// GetGate devolve o estado do gate da conversa, ou nil se inexistente.
	GetGate(ctx context.Context, convID int64) (*GateState, error)
	// SetGate insere ou atualiza o estado do gate.
	SetGate(ctx context.Context, gs *GateState) error
	// DeleteGate apaga o estado do gate da conversa (idempotente: conversa sem
	// estado não é erro). Usado quando a conversa é RESOLVIDA no Chatwoot — o
	// episódio acabou e a próxima mensagem começa do zero (cache de identidade
	// à parte, que é por telefone).
	DeleteGate(ctx context.Context, convID int64) error

	// GetIdentity devolve o perfil em cache para o telefone, ou nil se ausente
	// ou expirado.
	GetIdentity(ctx context.Context, phone string) (*CachedIdentity, error)
	// PutIdentity grava/atualiza o cache telefone→perfil.
	PutIdentity(ctx context.Context, ci *CachedIdentity) error

	// RegistrarFalha grava uma ocorrência (tabela `falhas`). CreatedAt vazio =
	// agora.
	RegistrarFalha(ctx context.Context, f *Falha) error
	// UltimaFalha devolve a ocorrência mais recente da conversa, ou nil.
	UltimaFalha(ctx context.Context, convID int64) (*Falha, error)

	Close() error
}
