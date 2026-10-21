package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // driver "sqlite" (puro Go, sem CGO)
)

// SQLiteStore implementa Store sobre SQLite via modernc.org/sqlite. O que é
// portável (dedup de eventos, cursor, conversas, contatos, tickets) está em
// ops.go; aqui ficam o schema, a migração e as `falhas`.
type SQLiteStore struct {
	*ops
	db *sql.DB
}

// schema é idempotente. As tabelas do gate de identidade do domínio antigo
// (gate_state, identities) são apagadas: não há mais login (ADR 0003).
const schema = `
CREATE TABLE IF NOT EXISTS processed_events (
    event_id   TEXT PRIMARY KEY,
    seq        INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS falhas (
    seq             INTEGER PRIMARY KEY AUTOINCREMENT,
    id              TEXT    NOT NULL,
    conversation_id TEXT    NOT NULL,
    origem          TEXT    NOT NULL DEFAULT '',
    classe          TEXT    NOT NULL DEFAULT '',
    causa           TEXT    NOT NULL DEFAULT '',
    detalhe         TEXT    NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS falhas_conversa ON falhas (conversation_id, seq);

DROP TABLE IF EXISTS gate_state;
DROP TABLE IF EXISTS identities;
`

// migrarProcessedEvents leva um `processed_events` anterior à E2.1
// (delivery_id, sem seq) ao formato novo: a chave passa a ser o id do EVENTO
// (`event_id`), não o da entrega. As linhas antigas ficam (7 dias de dedup).
func migrarProcessedEvents(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info('processed_events')`)
	if err != nil {
		return err
	}
	cols := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			_ = rows.Close()
			return err
		}
		cols[n] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(cols) == 0 {
		return nil // tabela nova: o schema cria já no formato certo
	}
	if cols["delivery_id"] && !cols["event_id"] {
		if _, err := db.ExecContext(ctx, `ALTER TABLE processed_events RENAME COLUMN delivery_id TO event_id`); err != nil {
			return err
		}
	}
	if !cols["seq"] {
		if _, err := db.ExecContext(ctx, `ALTER TABLE processed_events ADD COLUMN seq INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	return nil
}

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

	o := &ops{db: db}
	if err := migrarProcessedEvents(context.Background(), db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrar processed_events: %w", err)
	}
	if _, err := db.ExecContext(context.Background(), schema+o.rebind(ddlComum)); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrar schema: %w", err)
	}
	// Higiene: eventos processados (dedup por entrega/mensagem) mais velhos
	// que 7 dias não têm mais valor — sem isto a tabela cresce para sempre.
	if _, err := db.ExecContext(context.Background(),
		`DELETE FROM processed_events WHERE created_at < ?`,
		time.Now().Add(-7*24*time.Hour).Unix(),
	); err != nil {
		db.Close()
		return nil, fmt.Errorf("limpar processed_events: %w", err)
	}
	return &SQLiteStore{ops: o, db: db}, nil
}

func (s *SQLiteStore) RegistrarFalha(ctx context.Context, f *Falha) error {
	quando := f.CreatedAt
	if quando.IsZero() {
		quando = time.Now()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO falhas (id, conversation_id, origem, classe, causa, detalhe, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		f.ID, f.ConversationID, f.Origem, f.Classe, f.Causa, f.Detalhe, quando.Unix())
	return err
}

func (s *SQLiteStore) UltimaFalha(ctx context.Context, convID string) (*Falha, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, conversation_id, origem, classe, causa, detalhe, created_at
		   FROM falhas WHERE conversation_id = ? ORDER BY seq DESC LIMIT 1`, convID)
	var f Falha
	var created int64
	err := row.Scan(&f.ID, &f.ConversationID, &f.Origem, &f.Classe, &f.Causa, &f.Detalhe, &created)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	f.CreatedAt = time.Unix(created, 0)
	return &f, nil
}

func (s *SQLiteStore) Close() error { return s.db.Close() }
