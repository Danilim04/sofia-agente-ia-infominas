package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // driver "pgx" para database/sql
)

// PostgresStore implementa Store sobre Postgres (pgx via database/sql).
//
// Vive no MESMO Postgres do RAG (pgvector, Contrato B), num schema próprio
// (`gateway`) para não se misturar com as tabelas rag_* do cérebro. O schema
// é criado/migrado no boot (idempotente), como o SQLite faz.
type PostgresStore struct {
	*ops
	db *sql.DB
}

// pgSchema é idempotente. `falhas.conversation_id` virou TEXT (o Infodesk usa
// uuid); um banco criado pelo domínio antigo (BIGINT) é convertido no boot.
// As tabelas do gate de identidade (gate_state, identities) são apagadas:
// não há mais login (ADR 0003).
const pgSchema = `
CREATE SCHEMA IF NOT EXISTS gateway;

CREATE TABLE IF NOT EXISTS gateway.processed_events (
    event_id   TEXT PRIMARY KEY,
    seq        BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Anterior à E2.1: a chave era o id da entrega (delivery_id), sem seq.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
                WHERE table_schema = 'gateway' AND table_name = 'processed_events' AND column_name = 'delivery_id') THEN
        ALTER TABLE gateway.processed_events RENAME COLUMN delivery_id TO event_id;
    END IF;
END $$;
ALTER TABLE gateway.processed_events ADD COLUMN IF NOT EXISTS seq BIGINT NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS gateway.falhas (
    seq             BIGSERIAL PRIMARY KEY,
    id              TEXT   NOT NULL,
    conversation_id TEXT   NOT NULL,
    origem          TEXT   NOT NULL DEFAULT '',
    classe          TEXT   NOT NULL DEFAULT '',
    causa           TEXT   NOT NULL DEFAULT '',
    detalhe         TEXT   NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE gateway.falhas ALTER COLUMN conversation_id TYPE TEXT USING conversation_id::text;
CREATE INDEX IF NOT EXISTS falhas_conversa ON gateway.falhas (conversation_id, seq);

DROP TABLE IF EXISTS gateway.gate_state;
DROP TABLE IF EXISTS gateway.identities;
`

// NewPostgres abre o pool, valida a conexão (fail-fast no boot) e aplica o
// schema. `url` é uma URL libpq (postgresql://user:pass@host:5432/db).
func NewPostgres(ctx context.Context, url string) (*PostgresStore, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, fmt.Errorf("abrir postgres: %w", err)
	}
	// O gateway é pouco concorrente (um worker por conversa + webhook); um
	// pool pequeno basta e não disputa conexões com o sync do RAG.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("conectar ao postgres: %w", err)
	}
	// Sem argumentos o pgx usa o protocolo simples, que aceita várias
	// instruções numa chamada só.
	o := &ops{db: db, pg: true}
	if _, err := db.ExecContext(pingCtx, pgSchema+o.rebind(ddlComum)); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrar schema gateway: %w", err)
	}
	// Higiene: mesma regra do SQLite — dedup mais velho que 7 dias não tem valor.
	if _, err := db.ExecContext(pingCtx,
		`DELETE FROM gateway.processed_events WHERE created_at < $1`,
		time.Now().Add(-7*24*time.Hour),
	); err != nil {
		db.Close()
		return nil, fmt.Errorf("limpar processed_events: %w", err)
	}
	return &PostgresStore{ops: o, db: db}, nil
}

func (s *PostgresStore) RegistrarFalha(ctx context.Context, f *Falha) error {
	quando := f.CreatedAt
	if quando.IsZero() {
		quando = time.Now()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO gateway.falhas (id, conversation_id, origem, classe, causa, detalhe, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		f.ID, f.ConversationID, f.Origem, f.Classe, f.Causa, f.Detalhe, quando)
	return err
}

func (s *PostgresStore) UltimaFalha(ctx context.Context, convID string) (*Falha, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, conversation_id, origem, classe, causa, detalhe, created_at
		   FROM gateway.falhas WHERE conversation_id = $1 ORDER BY seq DESC LIMIT 1`, convID)
	var f Falha
	err := row.Scan(&f.ID, &f.ConversationID, &f.Origem, &f.Classe, &f.Causa, &f.Detalhe, &f.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func (s *PostgresStore) Close() error { return s.db.Close() }
