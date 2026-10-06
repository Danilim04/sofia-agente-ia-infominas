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

	sq, err := NewSQLite(filepath.Join(t.TempDir(), "gate.db"))
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
			id := unico("waid")
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

func TestStoreGateCicloDeVida(t *testing.T) {
	for nome, st := range backends(t) {
		t.Run(nome, func(t *testing.T) {
			ctx := context.Background()
			conv := time.Now().UnixNano() % 1_000_000_000

			gs, err := st.GetGate(ctx, conv)
			if err != nil || gs != nil {
				t.Fatalf("antes: esperava nil, veio %+v (err=%v)", gs, err)
			}
			if err := st.SetGate(ctx, &GateState{ConversationID: conv, State: GateFalha, Data: `{"x":1}`}); err != nil {
				t.Fatalf("set: %v", err)
			}
			gs, err = st.GetGate(ctx, conv)
			if err != nil || gs == nil {
				t.Fatalf("depois do set: esperava estado, veio %+v (err=%v)", gs, err)
			}
			if gs.State != GateFalha || gs.Data != `{"x":1}` {
				t.Fatalf("estado gravado errado: %+v", gs)
			}
			if d := time.Since(gs.UpdatedAt); d < 0 || d > time.Minute {
				t.Fatalf("updated_at fora do esperado: %v (Δ=%v)", gs.UpdatedAt, d)
			}
			// Upsert: mesmo id substitui.
			if err := st.SetGate(ctx, &GateState{ConversationID: conv, State: GateIdentificado, Data: ""}); err != nil {
				t.Fatalf("set 2: %v", err)
			}
			if gs, _ = st.GetGate(ctx, conv); gs == nil || gs.State != GateIdentificado {
				t.Fatalf("upsert não substituiu: %+v", gs)
			}
			// Apagar: some; apagar de novo não é erro.
			if err := st.DeleteGate(ctx, conv); err != nil {
				t.Fatalf("delete: %v", err)
			}
			if gs, _ = st.GetGate(ctx, conv); gs != nil {
				t.Fatalf("depois do delete: esperava nil, veio %+v", gs)
			}
			if err := st.DeleteGate(ctx, conv); err != nil {
				t.Fatalf("delete idempotente: %v", err)
			}
		})
	}
}

func TestStoreIdentityRespeitaTTL(t *testing.T) {
	for nome, st := range backends(t) {
		t.Run(nome, func(t *testing.T) {
			ctx := context.Background()
			phone := unico("55119")

			if err := st.PutIdentity(ctx, &CachedIdentity{
				Phone: phone, Login: "joao", Perfil: `{"encontrado":true}`,
				ExpiresAt: time.Now().Add(time.Hour),
			}); err != nil {
				t.Fatalf("put: %v", err)
			}
			ci, err := st.GetIdentity(ctx, phone)
			if err != nil || ci == nil || ci.Login != "joao" {
				t.Fatalf("válido: esperava joao, veio %+v (err=%v)", ci, err)
			}
			// Vencido: o get não devolve (e o upsert por telefone substitui).
			if err := st.PutIdentity(ctx, &CachedIdentity{
				Phone: phone, Login: "joao", Perfil: `{"encontrado":true}`,
				ExpiresAt: time.Now().Add(-time.Minute),
			}); err != nil {
				t.Fatalf("put vencido: %v", err)
			}
			if ci, _ = st.GetIdentity(ctx, phone); ci != nil {
				t.Fatalf("vencido: esperava nil, veio %+v", ci)
			}
		})
	}
}

// TestStoreFalhasRegistraEDevolveAUltima (ZC-3): a ocorrência gravada na
// transferência é recuperável pela conversa — a mais recente primeiro.
func TestStoreFalhasRegistraEDevolveAUltima(t *testing.T) {
	for nome, st := range backends(t) {
		t.Run(nome, func(t *testing.T) {
			ctx := context.Background()
			conv := time.Now().UnixNano() % 1_000_000_000

			if f, err := st.UltimaFalha(ctx, conv); err != nil || f != nil {
				t.Fatalf("antes: esperava nil, veio %+v (err=%v)", f, err)
			}
			if err := st.RegistrarFalha(ctx, &Falha{
				ID: "ZPF-20261002-0911-55", ConversationID: conv, Origem: "cerebro",
				Classe: "definitiva", Causa: "saldo do OpenRouter insuficiente (402)", Detalhe: "status 500",
			}); err != nil {
				t.Fatalf("registrar 1: %v", err)
			}
			if err := st.RegistrarFalha(ctx, &Falha{
				ID: "ZPF-20261002-0915-07", ConversationID: conv, Origem: "disjuntor",
				Classe: "definitiva", Causa: "disjuntor aberto",
			}); err != nil {
				t.Fatalf("registrar 2: %v", err)
			}
			f, err := st.UltimaFalha(ctx, conv)
			if err != nil || f == nil {
				t.Fatalf("depois: esperava falha, veio %+v (err=%v)", f, err)
			}
			if f.ID != "ZPF-20261002-0915-07" || f.Origem != "disjuntor" || f.ConversationID != conv {
				t.Fatalf("esperava a ÚLTIMA ocorrência da conversa, veio %+v", f)
			}
			if d := time.Since(f.CreatedAt); d < 0 || d > time.Minute {
				t.Fatalf("created_at fora do esperado: %v (Δ=%v)", f.CreatedAt, d)
			}
			// Outra conversa não enxerga esta ocorrência.
			if f, _ := st.UltimaFalha(ctx, conv+1); f != nil {
				t.Fatalf("conversa vizinha não podia ter falha: %+v", f)
			}
		})
	}
}
