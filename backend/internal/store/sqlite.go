package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // driver "sqlite" (puro Go, sem CGO)
)

// SQLiteStore implementa Store sobre SQLite via modernc.org/sqlite.
type SQLiteStore struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS processed_events (
    delivery_id TEXT PRIMARY KEY,
    created_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS gate_state (
    conversation_id INTEGER PRIMARY KEY,
    state           TEXT    NOT NULL DEFAULT '',
    data            TEXT    NOT NULL DEFAULT '',
    updated_at      INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS identities (
    phone      TEXT PRIMARY KEY,
    login      TEXT NOT NULL,
    perfil     TEXT NOT NULL,
    expires_at INTEGER NOT NULL
);
`

// NewSQLite abre (criando se necessário) o banco e aplica o schema.
func NewSQLite(path string) (*SQLiteStore, error) {
	dsn := path
	if !strings.Contains(dsn, "?") {
		dsn += "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("abrir sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrar schema: %w", err)
	}
	// Higiene: eventos processados (dedup por delivery/WAID) mais velhos que
	// 7 dias não têm mais valor — sem isto a tabela cresce para sempre.
	if _, err := db.Exec(
		`DELETE FROM processed_events WHERE created_at < ?`,
		time.Now().Add(-7*24*time.Hour).Unix(),
	); err != nil {
		db.Close()
		return nil, fmt.Errorf("limpar processed_events: %w", err)
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) MarkProcessed(ctx context.Context, deliveryID string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO processed_events (delivery_id, created_at) VALUES (?, ?)
		 ON CONFLICT(delivery_id) DO NOTHING`,
		deliveryID, time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *SQLiteStore) GetGate(ctx context.Context, convID int64) (*GateState, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT conversation_id, state, data, updated_at
		   FROM gate_state WHERE conversation_id = ?`, convID)

	var gs GateState
	var updated int64
	err := row.Scan(&gs.ConversationID, &gs.State, &gs.Data, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	gs.UpdatedAt = time.Unix(updated, 0)
	return &gs, nil
}

func (s *SQLiteStore) SetGate(ctx context.Context, gs *GateState) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO gate_state (conversation_id, state, data, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(conversation_id) DO UPDATE SET
		     state      = excluded.state,
		     data       = excluded.data,
		     updated_at = excluded.updated_at`,
		gs.ConversationID, gs.State, gs.Data, time.Now().Unix())
	return err
}

func (s *SQLiteStore) DeleteGate(ctx context.Context, convID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM gate_state WHERE conversation_id = ?`, convID)
	return err
}

func (s *SQLiteStore) GetIdentity(ctx context.Context, phone string) (*CachedIdentity, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT phone, login, perfil, expires_at
		   FROM identities WHERE phone = ? AND expires_at > ?`,
		phone, time.Now().Unix())

	var ci CachedIdentity
	var exp int64
	err := row.Scan(&ci.Phone, &ci.Login, &ci.Perfil, &exp)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ci.ExpiresAt = time.Unix(exp, 0)
	return &ci, nil
}

func (s *SQLiteStore) PutIdentity(ctx context.Context, ci *CachedIdentity) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO identities (phone, login, perfil, expires_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(phone) DO UPDATE SET
		     login      = excluded.login,
		     perfil     = excluded.perfil,
		     expires_at = excluded.expires_at`,
		ci.Phone, ci.Login, ci.Perfil, ci.ExpiresAt.Unix())
	return err
}

func (s *SQLiteStore) Close() error { return s.db.Close() }
