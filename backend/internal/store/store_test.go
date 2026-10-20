package store

import (
	"context"
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

// TestStoreSemTabelasDoGate: o schema não cria (e apaga, se existirem) as
// tabelas do gate de identidade do domínio antigo. Só SQLite: no Postgres a
// prova é a mesma instrução DROP do pgSchema.
func TestStoreSemTabelasDoGate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	sq, err := NewSQLite(path)
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	defer sq.Close()
	rows, err := sq.db.QueryContext(context.Background(), `SELECT name FROM sqlite_master WHERE type = 'table'`)
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
	if tabelas["gate_state"] || tabelas["identities"] {
		t.Fatalf("tabelas do gate não podiam existir: %v", tabelas)
	}
	if !tabelas["processed_events"] || !tabelas["falhas"] {
		t.Fatalf("tabelas esperadas ausentes: %v", tabelas)
	}
}
