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
	db *sql.DB
}

const pgSchema = `
CREATE SCHEMA IF NOT EXISTS gateway;

CREATE TABLE IF NOT EXISTS gateway.processed_events (
    delivery_id TEXT PRIMARY KEY,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS gateway.gate_state (
    conversation_id BIGINT PRIMARY KEY,
    state           TEXT NOT NULL DEFAULT '',
    data            TEXT NOT NULL DEFAULT '',
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS gateway.identities (
    phone      TEXT PRIMARY KEY,
    login      TEXT NOT NULL,
    perfil     TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
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
	if _, err := db.ExecContext(pingCtx, pgSchema); err != nil {
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
	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) MarkProcessed(ctx context.Context, deliveryID string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO gateway.processed_events (delivery_id, created_at) VALUES ($1, $2)
		 ON CONFLICT (delivery_id) DO NOTHING`,
		deliveryID, time.Now())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *PostgresStore) GetGate(ctx context.Context, convID int64) (*GateState, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT conversation_id, state, data, updated_at
		   FROM gateway.gate_state WHERE conversation_id = $1`, convID)

	var gs GateState
	err := row.Scan(&gs.ConversationID, &gs.State, &gs.Data, &gs.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &gs, nil
}

func (s *PostgresStore) SetGate(ctx context.Context, gs *GateState) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO gateway.gate_state (conversation_id, state, data, updated_at)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (conversation_id) DO UPDATE SET
		     state      = EXCLUDED.state,
		     data       = EXCLUDED.data,
		     updated_at = EXCLUDED.updated_at`,
		gs.ConversationID, gs.State, gs.Data, time.Now())
	return err
}

func (s *PostgresStore) DeleteGate(ctx context.Context, convID int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM gateway.gate_state WHERE conversation_id = $1`, convID)
	return err
}

func (s *PostgresStore) GetIdentity(ctx context.Context, phone string) (*CachedIdentity, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT phone, login, perfil, expires_at
		   FROM gateway.identities WHERE phone = $1 AND expires_at > $2`,
		phone, time.Now())

	var ci CachedIdentity
	err := row.Scan(&ci.Phone, &ci.Login, &ci.Perfil, &ci.ExpiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ci, nil
}

func (s *PostgresStore) PutIdentity(ctx context.Context, ci *CachedIdentity) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO gateway.identities (phone, login, perfil, expires_at)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (phone) DO UPDATE SET
		     login      = EXCLUDED.login,
		     perfil     = EXCLUDED.perfil,
		     expires_at = EXCLUDED.expires_at`,
		ci.Phone, ci.Login, ci.Perfil, ci.ExpiresAt)
	return err
}

func (s *PostgresStore) Close() error { return s.db.Close() }
