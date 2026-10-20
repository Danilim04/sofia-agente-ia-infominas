// Package store define a persistência do gateway: deduplicação de eventos
// (webhook e mensagens) e ocorrências de transferência (`falhas`).
//
// Dois backends implementam a mesma interface: SQLite (arquivo local, default
// de dev) e Postgres (PG_URL — o mesmo Postgres do RAG/pgvector, em schema
// próprio `gateway`). A escolha é feita em main.go; o resto do código só vê
// a interface. Toda mudança aqui vale para os DOIS backends e ganha caso em
// store_test.go (regra 7 do CLAUDE.md).
//
// TODO(E2.1): tabelas `conversas`, `contatos` e `tickets`
// (docs/plano/01-ferramentas.md, seção C).
package store

import (
	"context"
	"time"
)

// Falha é uma ocorrência registrada quando a conversa é transferida a um
// atendente por falha (cérebro, disjuntor) ou a pedido do cliente/mídia.
// O ID (`SOF-AAAAMMDD-HHMM-NN`) é o MESMO do log `sofia_falha` do gateway,
// do alerta e — quando a falha veio do cérebro — do log `chat_erro` do
// Python: é por ele que o atendente e o responsável se acham.
type Falha struct {
	ID             string
	ConversationID string // id da conversa no transporte (Infodesk: uuid)
	Origem         string // cerebro | disjuntor | cliente | anexo
	Classe         string // transitoria | definitiva | "" (não se aplica)
	Causa          string // linguagem simples (vai no alerta)
	Detalhe        string // técnico, sem dado pessoal (só log/tabela)
	CreatedAt      time.Time
}

// Store é a interface de persistência usada pelo resto da aplicação.
type Store interface {
	// MarkProcessed registra uma chave de dedup (id de entrega do webhook,
	// "msg:<id>" da mensagem); devolve true se for nova.
	MarkProcessed(ctx context.Context, deliveryID string) (bool, error)

	// RegistrarFalha grava uma ocorrência (tabela `falhas`). CreatedAt vazio =
	// agora.
	RegistrarFalha(ctx context.Context, f *Falha) error
	// UltimaFalha devolve a ocorrência mais recente da conversa, ou nil.
	UltimaFalha(ctx context.Context, convID string) (*Falha, error)

	Close() error
}
