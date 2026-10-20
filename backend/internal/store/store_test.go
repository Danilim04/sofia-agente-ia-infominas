package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// backends devolve os stores a exercitar pelos MESMOS testes: o SQLite sempre
// (arquivo temporário); o Postgres só com PG_TEST_URL no ambiente — é o teste
// de integração da migração (ex.: PG_TEST_URL=postgresql://rag:x@localhost/rag).
func backends(t *testing.T) map[string]Store {
	t.Helper()
	out := map[string]Store{}

	sq, err := NewSQLite(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	t.Cleanup(func() { _ = sq.Close() })
	out["sqlite"] = sq

	if url := os.Getenv("PG_TEST_URL"); url != "" {
		pg, err := NewPostgres(context.Background(), url)
		if err != nil {
			t.Fatalf("postgres (PG_TEST_URL): %v", err)
		}
		t.Cleanup(func() { _ = pg.Close() })
		out["postgres"] = pg
	}
	return out
}

// chave única por execução: o Postgres de teste pode ser compartilhado.
func unico(prefixo string) string {
	return fmt.Sprintf("%s-%d", prefixo, time.Now().UnixNano())
}

func TestStoreMarkProcessedDedup(t *testing.T) {
	for nome, st := range backends(t) {
		t.Run(nome, func(t *testing.T) {
			ctx := context.Background()
			id := unico("msg")
			fresh, err := st.MarkProcessed(ctx, id)
			if err != nil || !fresh {
				t.Fatalf("1ª vez: esperava fresh=true, veio %v (err=%v)", fresh, err)
			}
			fresh, err = st.MarkProcessed(ctx, id)
			if err != nil || fresh {
				t.Fatalf("2ª vez: esperava fresh=false, veio %v (err=%v)", fresh, err)
			}
		})
	}
}

// TestStoreFalhasRegistraEDevolveAUltima (ZC-3): a ocorrência gravada na
// transferência é recuperável pela conversa — a mais recente primeiro. O id
// da conversa é string (uuid do Infodesk).
func TestStoreFalhasRegistraEDevolveAUltima(t *testing.T) {
	for nome, st := range backends(t) {
		t.Run(nome, func(t *testing.T) {
			ctx := context.Background()
			conv := unico("3f2b9c1e-conv")

			if f, err := st.UltimaFalha(ctx, conv); err != nil || f != nil {
				t.Fatalf("antes: esperava nil, veio %+v (err=%v)", f, err)
			}
			if err := st.RegistrarFalha(ctx, &Falha{
				ID: "SOF-20261002-0911-55", ConversationID: conv, Origem: "cerebro",
				Classe: "definitiva", Causa: "saldo do OpenRouter insuficiente (402)", Detalhe: "status 500",
			}); err != nil {
				t.Fatalf("registrar 1: %v", err)
			}
			if err := st.RegistrarFalha(ctx, &Falha{
				ID: "SOF-20261002-0915-07", ConversationID: conv, Origem: "disjuntor",
				Classe: "definitiva", Causa: "disjuntor aberto",
			}); err != nil {
				t.Fatalf("registrar 2: %v", err)
			}
			f, err := st.UltimaFalha(ctx, conv)
			if err != nil || f == nil {
				t.Fatalf("depois: esperava falha, veio %+v (err=%v)", f, err)
			}
			if f.ID != "SOF-20261002-0915-07" || f.Origem != "disjuntor" || f.ConversationID != conv {
				t.Fatalf("esperava a ÚLTIMA ocorrência da conversa, veio %+v", f)
			}
			if d := time.Since(f.CreatedAt); d < 0 || d > time.Minute {
				t.Fatalf("created_at fora do esperado: %v (Δ=%v)", f.CreatedAt, d)
			}
			// Outra conversa não enxerga esta ocorrência.
			if f, _ := st.UltimaFalha(ctx, conv+"-outra"); f != nil {
				t.Fatalf("conversa vizinha não podia ter falha: %+v", f)
			}
		})
	}
}

// tabelasDe lista as tabelas visíveis pela consulta dada (uma coluna: nome).
func tabelasDe(t *testing.T, db *sql.DB, consulta string) map[string]bool {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), consulta)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	tabelas := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tabelas[n] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return tabelas
}

// TestStoreSQLiteMigraSchemaAntigo: um gateway.db criado pelo domínio antigo
// (gate_state, identities, falhas com conversation_id INTEGER) abre, perde as
// tabelas do gate e aceita conversation_id string.
func TestStoreSQLiteMigraSchemaAntigo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	antigo, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := antigo.ExecContext(context.Background(), `
		CREATE TABLE processed_events (delivery_id TEXT PRIMARY KEY, created_at INTEGER NOT NULL);
		CREATE TABLE gate_state (conversation_id INTEGER PRIMARY KEY, state TEXT NOT NULL DEFAULT '', data TEXT NOT NULL DEFAULT '', updated_at INTEGER NOT NULL);
		CREATE TABLE identities (phone TEXT PRIMARY KEY, login TEXT NOT NULL, perfil TEXT NOT NULL, expires_at INTEGER NOT NULL);
		CREATE TABLE falhas (seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL, conversation_id INTEGER NOT NULL,
			origem TEXT NOT NULL DEFAULT '', classe TEXT NOT NULL DEFAULT '', causa TEXT NOT NULL DEFAULT '',
			detalhe TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL);
		INSERT INTO gate_state VALUES (41, 'falha', '{}', 0);
		INSERT INTO identities VALUES ('5511999', 'joao', '{}', 0);`); err != nil {
		t.Fatal(err)
	}
	_ = antigo.Close()

	sq, err := NewSQLite(path)
	if err != nil {
		t.Fatalf("abrir banco antigo: %v", err)
	}
	defer sq.Close()
	tabelas := tabelasDe(t, sq.db, `SELECT name FROM sqlite_master WHERE type = 'table'`)
	if tabelas["gate_state"] || tabelas["identities"] {
		t.Fatalf("tabelas do gate deviam ter sido apagadas: %v", tabelas)
	}
	if !tabelas["processed_events"] || !tabelas["falhas"] {
		t.Fatalf("tabelas esperadas ausentes: %v", tabelas)
	}
	ctx := context.Background()
	if err := sq.RegistrarFalha(ctx, &Falha{ID: "SOF-1", ConversationID: "3f2b-uuid", Origem: "cliente"}); err != nil {
		t.Fatalf("registrar com conversation_id string na tabela antiga: %v", err)
	}
	if f, _ := sq.UltimaFalha(ctx, "3f2b-uuid"); f == nil || f.ConversationID != "3f2b-uuid" {
		t.Fatalf("conversation_id string devia voltar igual: %+v", f)
	}
}

// TestStorePostgresMigraSchemaAntigo: mesma prova no Postgres (só com
// PG_TEST_URL — o CI tem). Recria o schema `gateway` como o domínio antigo o
// deixava (falhas.conversation_id BIGINT + tabelas do gate), abre o store e
// confere a conversão da coluna e o sumiço das tabelas.
func TestStorePostgresMigraSchemaAntigo(t *testing.T) {
	url := os.Getenv("PG_TEST_URL")
	if url == "" {
		t.Skip("PG_TEST_URL não definido")
	}
	ctx := context.Background()
	antigo, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := antigo.ExecContext(ctx, `
		DROP SCHEMA IF EXISTS gateway CASCADE;
		CREATE SCHEMA gateway;
		CREATE TABLE gateway.processed_events (delivery_id TEXT PRIMARY KEY, created_at TIMESTAMPTZ NOT NULL DEFAULT now());
		CREATE TABLE gateway.gate_state (conversation_id BIGINT PRIMARY KEY, state TEXT NOT NULL DEFAULT '', data TEXT NOT NULL DEFAULT '', updated_at TIMESTAMPTZ NOT NULL DEFAULT now());
		CREATE TABLE gateway.identities (phone TEXT PRIMARY KEY, login TEXT NOT NULL, perfil TEXT NOT NULL, expires_at TIMESTAMPTZ NOT NULL);
		CREATE TABLE gateway.falhas (seq BIGSERIAL PRIMARY KEY, id TEXT NOT NULL, conversation_id BIGINT NOT NULL,
			origem TEXT NOT NULL DEFAULT '', classe TEXT NOT NULL DEFAULT '', causa TEXT NOT NULL DEFAULT '',
			detalhe TEXT NOT NULL DEFAULT '', created_at TIMESTAMPTZ NOT NULL DEFAULT now());
		CREATE INDEX falhas_conversa ON gateway.falhas (conversation_id, seq);
		INSERT INTO gateway.falhas (id, conversation_id, origem) VALUES ('SOF-0', 41, 'cerebro');
		INSERT INTO gateway.gate_state (conversation_id, state) VALUES (41, 'falha');`); err != nil {
		t.Fatalf("montar schema antigo: %v", err)
	}
	_ = antigo.Close()

	pg, err := NewPostgres(ctx, url)
	if err != nil {
		t.Fatalf("abrir banco antigo: %v", err)
	}
	defer pg.Close()
	tabelas := tabelasDe(t, pg.db, `SELECT table_name FROM information_schema.tables WHERE table_schema = 'gateway'`)
	if tabelas["gate_state"] || tabelas["identities"] {
		t.Fatalf("tabelas do gate deviam ter sido apagadas: %v", tabelas)
	}
	var tipo string
	if err := pg.db.QueryRowContext(ctx, `SELECT data_type FROM information_schema.columns WHERE table_schema = 'gateway' AND table_name = 'falhas' AND column_name = 'conversation_id'`).Scan(&tipo); err != nil {
		t.Fatal(err)
	}
	if tipo != "text" {
		t.Fatalf("conversation_id devia ter virado text, veio %q", tipo)
	}
	// A linha antiga (BIGINT 41) sobrevive como "41"; uuid novo entra.
	if f, _ := pg.UltimaFalha(ctx, "41"); f == nil || f.ID != "SOF-0" {
		t.Fatalf("linha antiga devia ser legível como string: %+v", f)
	}
	if err := pg.RegistrarFalha(ctx, &Falha{ID: "SOF-1", ConversationID: "3f2b-uuid", Origem: "cliente"}); err != nil {
		t.Fatalf("registrar com uuid: %v", err)
	}
	if f, _ := pg.UltimaFalha(ctx, "3f2b-uuid"); f == nil || f.ID != "SOF-1" {
		t.Fatalf("uuid devia voltar igual: %+v", f)
	}
}
