package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// ---------------------------------------------------------------------------
// E2.1: eventos, cursor, conversas, contatos, tickets
// ---------------------------------------------------------------------------

// TestStoreMarkEventDedupPorIDDoEvento: a chave é o `id` do EVENTO (evt_…); o
// mesmo id com outro seq/entrega é duplicata; Desmarcar libera a reentrega.
func TestStoreMarkEventDedupPorIDDoEvento(t *testing.T) {
	for nome, st := range backends(t) {
		t.Run(nome, func(t *testing.T) {
			ctx := context.Background()
			id := unico("evt")
			if fresh, err := st.MarkEvent(ctx, id, 10); err != nil || !fresh {
				t.Fatalf("1ª vez: fresh=%v err=%v", fresh, err)
			}
			if fresh, err := st.MarkEvent(ctx, id, 10); err != nil || fresh {
				t.Fatalf("reentrega do mesmo evento devia ser duplicata: fresh=%v err=%v", fresh, err)
			}
			if err := st.DesmarcarEvento(ctx, id); err != nil {
				t.Fatalf("desmarcar: %v", err)
			}
			if fresh, err := st.MarkEvent(ctx, id, 10); err != nil || !fresh {
				t.Fatalf("depois de desmarcar devia ser novo: fresh=%v err=%v", fresh, err)
			}
		})
	}
}

// TestStoreCursorSoAvanca: eventos_cursor guarda o MAIOR seq; um seq menor
// (entrega fora de ordem) nunca o faz retroceder.
func TestStoreCursorSoAvanca(t *testing.T) {
	for nome, st := range backends(t) {
		t.Run(nome, func(t *testing.T) {
			ctx := context.Background()
			c := unico("cursor")
			if seq, err := st.Cursor(ctx, c); err != nil || seq != 0 {
				t.Fatalf("cursor novo: seq=%d err=%v", seq, err)
			}
			for _, seq := range []int64{5, 9, 7} {
				if err := st.AvancarCursor(ctx, c, seq); err != nil {
					t.Fatalf("avançar %d: %v", seq, err)
				}
			}
			if seq, _ := st.Cursor(ctx, c); seq != 9 {
				t.Fatalf("esperava o maior seq (9), veio %d", seq)
			}
		})
	}
}

// TestStoreConversaAtualizarCriaAtualizaEIgnora cobre a tabela `conversas`:
// criação, atualização, fn que não grava e persistência de todas as colunas.
func TestStoreConversaAtualizarCriaAtualizaEIgnora(t *testing.T) {
	for nome, st := range backends(t) {
		t.Run(nome, func(t *testing.T) {
			ctx := context.Background()
			id := unico("conv")
			if c, err := st.ObterConversa(ctx, id); err != nil || c != nil {
				t.Fatalf("antes: %+v err=%v", c, err)
			}
			t0 := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
			c, err := st.AtualizarConversa(ctx, id, func(c *Conversa, existia bool) bool {
				if existia {
					t.Error("conversa nova não devia existir")
				}
				c.Telefone, c.Estado, c.HandledBy = "+5531990000000", EstadoBot, "EXTERNAL_BOT"
				c.SessaoIniciadaEm, c.UltimaMsgID, c.UltimaMsgEm, c.UpdatedAt = t0, "m1", t0, t0
				return true
			})
			if err != nil || c.Estado != EstadoBot {
				t.Fatalf("criar: %+v err=%v", c, err)
			}
			// fn devolvendo false não grava.
			if _, err := st.AtualizarConversa(ctx, id, func(c *Conversa, existia bool) bool {
				if !existia {
					t.Error("agora devia existir")
				}
				c.Estado = EstadoEncerrada
				return false
			}); err != nil {
				t.Fatal(err)
			}
			got, _ := st.ObterConversa(ctx, id)
			if got == nil || got.Estado != EstadoBot {
				t.Fatalf("fn=false não pode gravar: %+v", got)
			}
			// Atualização grava ticket_number, resposta e classificação.
			t1 := t0.Add(time.Minute)
			if _, err := st.AtualizarConversa(ctx, id, func(c *Conversa, _ bool) bool {
				c.Estado, c.TicketNumber, c.UltimaRespostaEm = EstadoTransferida, 4521, t1
				c.ClassificacaoJSON, c.UpdatedAt = `{"categoria":"sem_conexao"}`, t1
				return true
			}); err != nil {
				t.Fatal(err)
			}
			got, _ = st.ObterConversa(ctx, id)
			if got == nil || got.Estado != EstadoTransferida || got.TicketNumber != 4521 ||
				!got.UltimaRespostaEm.Equal(t1) || !got.SessaoIniciadaEm.Equal(t0) || got.Telefone != "+5531990000000" ||
				got.HandledBy != "EXTERNAL_BOT" || got.UltimaMsgID != "m1" || got.ClassificacaoJSON == "" || !got.UpdatedAt.Equal(t1) {
				t.Fatalf("colunas não persistiram: %+v", got)
			}
		})
	}
}

// TestStoreContatoComTTL: contato expirado some; o CPF em claro não existe
// no modelo (só hash + mascarado); upsert por telefone.
func TestStoreContatoComTTL(t *testing.T) {
	for nome, st := range backends(t) {
		t.Run(nome, func(t *testing.T) {
			ctx := context.Background()
			tel := unico("+55319")
			agora := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
			if c, err := st.ObterContato(ctx, tel, agora); err != nil || c != nil {
				t.Fatalf("antes: %+v err=%v", c, err)
			}
			err := st.SalvarContato(ctx, &Contato{
				Telefone: tel, Origem: OrigemLookup, Forma: FormaTelefone, ValorMascarado: "Maria S.",
				RequesterID: "r1", ClienteID: "k1", NomeCadastro: "Maria Souza", ExpiresAt: agora.Add(24 * time.Hour),
			})
			if err != nil {
				t.Fatal(err)
			}
			c, err := st.ObterContato(ctx, tel, agora)
			if err != nil || c == nil || c.RequesterID != "r1" || c.Forma != FormaTelefone || c.Origem != OrigemLookup || c.NomeCadastro != "Maria Souza" {
				t.Fatalf("contato: %+v err=%v", c, err)
			}
			// upsert: o CPF informado depois substitui a forma, sem duplicar a linha.
			if err := st.SalvarContato(ctx, &Contato{
				Telefone: tel, Origem: OrigemCPF, Forma: FormaCPF, ValorMascarado: "***.***.789-**", ValorHash: "abc",
				RequesterID: "r1", ExpiresAt: agora.Add(24 * time.Hour),
			}); err != nil {
				t.Fatal(err)
			}
			if c, _ := st.ObterContato(ctx, tel, agora); c == nil || c.Forma != FormaCPF || c.ValorHash != "abc" {
				t.Fatalf("upsert: %+v", c)
			}
			if c, _ := st.ObterContato(ctx, tel, agora.Add(25*time.Hour)); c != nil {
				t.Fatalf("contato expirado devia sumir: %+v", c)
			}
		})
	}
}

// TestStoreTicketIdempotenciaEStatus: a chave única devolve o ticket já
// existente; o número é inteiro; ticket.* atualiza status_infodesk/encerrado
// só para números nossos e nunca com evento mais velho.
func TestStoreTicketIdempotenciaEStatus(t *testing.T) {
	for nome, st := range backends(t) {
		t.Run(nome, func(t *testing.T) {
			ctx := context.Background()
			chave := unico("chave")
			conv := unico("conv")
			novo := &Ticket{Chave: chave, ConversationID: conv, Telefone: "+5531990000000", Categoria: "sem_conexao"}
			t1, criado, err := st.InserirTicketPendente(ctx, novo)
			if err != nil || !criado || t1.Status != TicketPendente || t1.Numero != 0 {
				t.Fatalf("1ª inserção: %+v criado=%v err=%v", t1, criado, err)
			}
			t2, criado, err := st.InserirTicketPendente(ctx, &Ticket{Chave: chave, Categoria: "outra"})
			if err != nil || criado || t2.Categoria != "sem_conexao" {
				t.Fatalf("mesma chave devia devolver o existente: %+v criado=%v err=%v", t2, criado, err)
			}
			numero := int(time.Now().UnixNano()%1_000_000_000) + 1
			if err := st.ConcluirTicket(ctx, chave, numero, `{"number":1}`, true); err != nil {
				t.Fatal(err)
			}
			got, _ := st.ObterTicket(ctx, chave)
			if got == nil || got.Status != TicketAberto || got.Numero != numero || !got.Reconciliado || got.RawResposta == "" {
				t.Fatalf("concluir: %+v", got)
			}
			depois := time.Now().Add(time.Minute)
			if ok, err := st.AtualizarStatusTicket(ctx, numero, "Resolvido", true, depois); err != nil || !ok {
				t.Fatalf("ticket.closed: ok=%v err=%v", ok, err)
			}
			got, _ = st.ObterTicket(ctx, chave)
			if !got.Encerrado || got.StatusInfodesk != "Resolvido" {
				t.Fatalf("status: %+v", got)
			}
			// Evento reentregue mais velho (reabriu antes de fechar) não desfaz.
			if _, err := st.AtualizarStatusTicket(ctx, numero, "Aberto", false, depois.Add(-time.Hour)); err != nil {
				t.Fatal(err)
			}
			if got, _ = st.ObterTicket(ctx, chave); !got.Encerrado {
				t.Fatalf("evento velho não pode reabrir: %+v", got)
			}
			if ok, _ := st.AtualizarStatusTicket(ctx, numero+7, "x", true, depois); ok {
				t.Fatal("ticket que não é nosso não pode ser atualizado")
			}
		})
	}
}

// TestStoreSQLiteMigraDeliveryIDParaEventID: um processed_events anterior à
// E2.1 (delivery_id, sem seq) é renomeado para event_id, ganha seq e continua
// deduplicando o que já tinha.
func TestStoreSQLiteMigraDeliveryIDParaEventID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	antigo, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := antigo.ExecContext(context.Background(),
		`CREATE TABLE processed_events (delivery_id TEXT PRIMARY KEY, created_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := antigo.ExecContext(context.Background(),
		`INSERT INTO processed_events VALUES ('msg:antiga', ?)`, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	_ = antigo.Close()

	sq, err := NewSQLite(path)
	if err != nil {
		t.Fatalf("abrir banco antigo: %v", err)
	}
	defer sq.Close()
	cols := tabelasDe(t, sq.db, `SELECT name FROM pragma_table_info('processed_events')`)
	if cols["delivery_id"] || !cols["event_id"] || !cols["seq"] {
		t.Fatalf("colunas após migração: %v", cols)
	}
	ctx := context.Background()
	if fresh, _ := sq.MarkProcessed(ctx, "msg:antiga"); fresh {
		t.Fatal("linha antiga devia continuar deduplicando")
	}
	if fresh, err := sq.MarkEvent(ctx, "evt_000000000123", 123); err != nil || !fresh {
		t.Fatalf("evento novo: fresh=%v err=%v", fresh, err)
	}
	for _, tabela := range []string{"conversas", "contatos", "tickets", "eventos_cursor"} {
		if !tabelasDe(t, sq.db, `SELECT name FROM sqlite_master WHERE type = 'table'`)[tabela] {
			t.Errorf("tabela %s não criada", tabela)
		}
	}
	// Reabrir (idempotência da migração).
	_ = sq.Close()
	sq2, err := NewSQLite(path)
	if err != nil {
		t.Fatalf("reabrir: %v", err)
	}
	_ = sq2.Close()
}

// TestStorePostgresMigraDeliveryIDParaEventID: mesma prova no Postgres (só
// com PG_TEST_URL).
func TestStorePostgresMigraDeliveryIDParaEventID(t *testing.T) {
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
		INSERT INTO gateway.processed_events (delivery_id) VALUES ('msg:antiga');`); err != nil {
		t.Fatalf("montar schema antigo: %v", err)
	}
	_ = antigo.Close()

	pg, err := NewPostgres(ctx, url)
	if err != nil {
		t.Fatalf("abrir banco antigo: %v", err)
	}
	defer pg.Close()
	cols := tabelasDe(t, pg.db, `SELECT column_name FROM information_schema.columns WHERE table_schema = 'gateway' AND table_name = 'processed_events'`)
	if cols["delivery_id"] || !cols["event_id"] || !cols["seq"] {
		t.Fatalf("colunas após migração: %v", cols)
	}
	if fresh, _ := pg.MarkProcessed(ctx, "msg:antiga"); fresh {
		t.Fatal("linha antiga devia continuar deduplicando")
	}
	if fresh, err := pg.MarkEvent(ctx, "evt_000000000123", 123); err != nil || !fresh {
		t.Fatalf("evento novo: fresh=%v err=%v", fresh, err)
	}
}

// TestStoreAtualizarConversaConcorrenteNaoPerdeEscrita: N atualizações
// simultâneas de uma conversa NOVA (o caso em que SELECT FOR UPDATE não
// trava) somam todas — nenhuma sobrescreve a outra.
func TestStoreAtualizarConversaConcorrenteNaoPerdeEscrita(t *testing.T) {
	for nome, st := range backends(t) {
		t.Run(nome, func(t *testing.T) {
			ctx := context.Background()
			id := unico("conc")
			const n = 12
			var wg sync.WaitGroup
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := st.AtualizarConversa(ctx, id, func(c *Conversa, _ bool) bool {
						c.TicketNumber++
						return true
					})
					if err != nil {
						t.Errorf("atualizar: %v", err)
					}
				}()
			}
			wg.Wait()
			got, _ := st.ObterConversa(ctx, id)
			if got == nil || got.TicketNumber != n {
				t.Fatalf("esperava %d incrementos, veio %+v", n, got)
			}
			// fn que não grava numa conversa inexistente não deixa marcador.
			outra := unico("fantasma")
			if _, err := st.AtualizarConversa(ctx, outra, func(*Conversa, bool) bool { return false }); err != nil {
				t.Fatal(err)
			}
			if c, _ := st.ObterConversa(ctx, outra); c != nil {
				t.Fatalf("não podia ficar linha: %+v", c)
			}
		})
	}
}

// TestStoreDesmarcarEventosDepois: a recuperação de crash do reconciliador
// desfaz o dedup só dos eventos com seq acima do ponto dado; os de seq menor e
// as chaves genéricas (seq 0, "msg:<id>") ficam.
func TestStoreDesmarcarEventosDepois(t *testing.T) {
	for nome, st := range backends(t) {
		t.Run(nome, func(t *testing.T) {
			ctx := context.Background()
			// seqs altos: o Postgres de teste pode ser compartilhado.
			base := time.Now().UnixNano()
			antes, depois, msg := unico("evt-antes"), unico("evt-depois"), unico("msg")
			for id, seq := range map[string]int64{antes: base + 1, depois: base + 2} {
				if fresh, err := st.MarkEvent(ctx, id, seq); err != nil || !fresh {
					t.Fatalf("marcar %s: fresh=%v err=%v", id, fresh, err)
				}
			}
			if fresh, err := st.MarkProcessed(ctx, msg); err != nil || !fresh {
				t.Fatalf("marcar chave genérica: fresh=%v err=%v", fresh, err)
			}
			n, err := st.DesmarcarEventosDepois(ctx, base+1)
			if err != nil || n < 1 {
				t.Fatalf("desmarcar: n=%d err=%v", n, err)
			}
			if fresh, _ := st.MarkEvent(ctx, depois, base+2); !fresh {
				t.Fatal("o evento de seq acima do ponto devia voltar a ser novo")
			}
			if fresh, _ := st.MarkEvent(ctx, antes, base+1); fresh {
				t.Fatal("o evento de seq no ponto (ou abaixo) devia continuar visto")
			}
			if fresh, _ := st.MarkProcessed(ctx, msg); fresh {
				t.Fatal("chave genérica (seq 0) não pode ser desmarcada")
			}
		})
	}
}

// TestStoreTicketsPendentesEFalhar: TicketsPendentes lista só os pendentes,
// do mais antigo ao mais novo; FalharTicket só age sobre pendente.
func TestStoreTicketsPendentesEFalhar(t *testing.T) {
	for nome, st := range backends(t) {
		t.Run(nome, func(t *testing.T) {
			ctx := context.Background()
			a, b, c := unico("pend-a"), unico("pend-b"), unico("pend-c")
			for _, chave := range []string{a, b, c} {
				if _, _, err := st.InserirTicketPendente(ctx, &Ticket{Chave: chave, ConversationID: "conv-" + chave}); err != nil {
					t.Fatal(err)
				}
				time.Sleep(2 * time.Millisecond) // created_at em ms: ordem estável
			}
			numero := int(time.Now().UnixNano()%1_000_000_000) + 1
			if err := st.ConcluirTicket(ctx, b, numero, "{}", true); err != nil {
				t.Fatal(err)
			}
			pend, err := st.TicketsPendentes(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var chaves []string
			for _, p := range pend {
				if p.Chave == a || p.Chave == b || p.Chave == c {
					chaves = append(chaves, p.Chave)
				}
			}
			if len(chaves) != 2 || chaves[0] != a || chaves[1] != c {
				t.Fatalf("pendentes (a, c) em ordem de criação, vieram %v", chaves)
			}
			if ok, err := st.FalharTicket(ctx, a); err != nil || !ok {
				t.Fatalf("falhar pendente: ok=%v err=%v", ok, err)
			}
			if ok, _ := st.FalharTicket(ctx, a); ok {
				t.Fatal("falhar de novo não pode agir")
			}
			if ok, _ := st.FalharTicket(ctx, b); ok {
				t.Fatal("ticket já aberto não pode virar falha")
			}
			if got, _ := st.ObterTicket(ctx, a); got == nil || got.Status != TicketFalha {
				t.Fatalf("a devia estar em falha: %+v", got)
			}
			if got, _ := st.ObterTicket(ctx, b); got == nil || got.Status != TicketAberto {
				t.Fatalf("b devia continuar aberto: %+v", got)
			}
		})
	}
}

// TestStoreConversasSemResposta: só entra no vigia a conversa do bot externo,
// em estado bot, com a mensagem do cliente mais velha que o corte e sem
// resposta nossa depois dela.
func TestStoreConversasSemResposta(t *testing.T) {
	for nome, st := range backends(t) {
		t.Run(nome, func(t *testing.T) {
			ctx := context.Background()
			agora := time.Now()
			msg := agora.Add(-10 * time.Minute)
			corte := agora.Add(-5 * time.Minute)
			novas := map[string]func(c *Conversa){
				unico("sem-resposta"): func(c *Conversa) { c.UltimaMsgEm = msg },
				unico("respondida"):   func(c *Conversa) { c.UltimaMsgEm = msg; c.UltimaRespostaEm = msg.Add(time.Minute) },
				unico("resp-antiga"):  func(c *Conversa) { c.UltimaMsgEm = msg; c.UltimaRespostaEm = msg.Add(-2 * time.Minute) },
				// skew: resposta nossa "antes" da mensagem por diferença de relógio, dentro da folga
				unico("skew"):          func(c *Conversa) { c.UltimaMsgEm = msg; c.UltimaRespostaEm = msg.Add(-30 * time.Second) },
				unico("recente"):       func(c *Conversa) { c.UltimaMsgEm = agora.Add(-time.Minute) },
				unico("sem-msg"):       func(*Conversa) {},
				unico("transferida"):   func(c *Conversa) { c.UltimaMsgEm = msg; c.Estado = EstadoTransferida },
				unico("humano-conduz"): func(c *Conversa) { c.UltimaMsgEm = msg; c.HandledBy = "AGENT" },
			}
			for id, ajusta := range novas {
				if _, err := st.AtualizarConversa(ctx, id, func(c *Conversa, _ bool) bool {
					c.Estado, c.HandledBy = EstadoBot, HandledByBotExterno
					ajusta(c)
					return true
				}); err != nil {
					t.Fatal(err)
				}
			}
			got, err := st.ConversasSemResposta(ctx, corte, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			achadas := map[string]bool{}
			for _, c := range got {
				if _, nossa := novas[c.ID]; nossa {
					achadas[c.ID] = true
				}
			}
			for id := range achadas {
				if !strings.HasPrefix(id, "sem-resposta") && !strings.HasPrefix(id, "resp-antiga") {
					t.Fatalf("conversa %s não devia ser candidata do vigia", id)
				}
			}
			if len(achadas) != 2 {
				t.Fatalf("esperava sem-resposta e resp-antiga, vieram %v", achadas)
			}
		})
	}
}

// TestStoreHandoffPendente: a marca é por conversa (upsert), só aparece quando
// a próxima tentativa venceu, Adiar registra a tentativa e Limpar apaga.
func TestStoreHandoffPendente(t *testing.T) {
	for nome, st := range backends(t) {
		t.Run(nome, func(t *testing.T) {
			ctx := context.Background()
			a, b := unico("hp-a"), unico("hp-b")
			base := time.Now().Truncate(time.Millisecond)
			achar := func(ate time.Time, conv string) *HandoffPendente {
				t.Helper()
				hs, err := st.HandoffsPendentes(ctx, ate)
				if err != nil {
					t.Fatal(err)
				}
				for i := range hs {
					if hs[i].ConversationID == conv {
						return &hs[i]
					}
				}
				return nil
			}

			if err := st.MarcarHandoffPendente(ctx, &HandoffPendente{
				ConversationID: a, Origem: "cerebro", IDOcorrencia: "SOF-1", DepartamentoID: "dept-1", Nota: "nota a",
				CreatedAt: base, ProximaEm: base.Add(time.Minute),
			}); err != nil {
				t.Fatal(err)
			}
			// ProximaEm e CreatedAt vazios = agora (venceu).
			if err := st.MarcarHandoffPendente(ctx, &HandoffPendente{ConversationID: b, Origem: "vigia"}); err != nil {
				t.Fatal(err)
			}
			if achar(base, a) != nil {
				t.Fatal("a só vence em +1 min")
			}
			if achar(time.Now().Add(time.Second), b) == nil {
				t.Fatal("b (sem ProximaEm) devia estar vencida")
			}
			got := achar(base.Add(time.Minute), a)
			if got == nil || got.Origem != "cerebro" || got.IDOcorrencia != "SOF-1" || got.DepartamentoID != "dept-1" ||
				got.Nota != "nota a" || got.Tentativas != 0 || !got.CreatedAt.Equal(base) {
				t.Fatalf("a lida errado: %+v", got)
			}

			// upsert: marcar de novo substitui (outro episódio, outra nota).
			if err := st.MarcarHandoffPendente(ctx, &HandoffPendente{ConversationID: a, Origem: "cliente", Nota: "nota nova", CreatedAt: base, ProximaEm: base}); err != nil {
				t.Fatal(err)
			}
			if got := achar(base, a); got == nil || got.Origem != "cliente" || got.Nota != "nota nova" {
				t.Fatalf("upsert não substituiu: %+v", got)
			}

			if err := st.AdiarHandoffPendente(ctx, a, 2, base.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			if achar(base.Add(time.Minute), a) != nil {
				t.Fatal("adiada para +1 h não pode vencer antes")
			}
			if got := achar(base.Add(time.Hour), a); got == nil || got.Tentativas != 2 {
				t.Fatalf("tentativas não gravadas: %+v", got)
			}

			if ok, err := st.LimparHandoffPendente(ctx, a); err != nil || !ok {
				t.Fatalf("limpar a: ok=%v err=%v", ok, err)
			}
			if ok, _ := st.LimparHandoffPendente(ctx, a); ok {
				t.Fatal("limpar de novo não havia o que apagar")
			}
			if achar(base.Add(24*time.Hour), a) != nil {
				t.Fatal("a limpa não pode reaparecer")
			}
			if achar(time.Now().Add(time.Second), b) == nil {
				t.Fatal("limpar a não pode tocar em b")
			}
			_, _ = st.LimparHandoffPendente(ctx, b)
		})
	}
}

// TestStoreCadastroPropostaIdempotenciaETentativas (E5.3, ADR 0009): a proposta
// é renovada enquanto for `proposta`, uma linha `feito` nunca muda e o contador
// de tentativas de empresa soma e zera — nos dois backends.
func TestStoreCadastroPropostaIdempotenciaETentativas(t *testing.T) {
	for nome, st := range backends(t) {
		t.Run(nome, func(t *testing.T) {
			ctx := context.Background()
			chave := unico("cad")
			conv := unico("conv-cad")

			if c, err := st.ObterCadastro(ctx, chave); err != nil || c != nil {
				t.Fatalf("antes: esperava nil, veio %+v (err=%v)", c, err)
			}
			c, err := st.GravarCadastroProposta(ctx, &Cadastro{
				Chave: chave, ConversationID: conv, Telefone: "+5531999990001",
				Nome: "Maria Souza", Email: "maria@acme.com.br", ClienteID: "k1", ClienteNome: "ACME Ltda",
			})
			if err != nil || c == nil || c.Status != CadastroProposta || c.ClienteID != "k1" || c.Criado || c.RequesterID != "" {
				t.Fatalf("1ª gravação: %+v err=%v", c, err)
			}
			primeira := c.CreatedAt

			// Regravar enquanto é proposta renova os dados e o relógio da validade.
			time.Sleep(5 * time.Millisecond)
			c, err = st.GravarCadastroProposta(ctx, &Cadastro{
				Chave: chave, ConversationID: conv, Telefone: "+5531999990001",
				Nome: "Maria Souza", Email: "maria@acme.com.br", ClienteID: "k1", ClienteNome: "ACME Ltda (renovado)",
			})
			if err != nil || c.ClienteNome != "ACME Ltda (renovado)" || !c.CreatedAt.After(primeira) {
				t.Fatalf("renovação: %+v err=%v", c, err)
			}

			if err := st.ConcluirCadastro(ctx, chave, "r1", true); err != nil {
				t.Fatalf("concluir: %v", err)
			}
			c, _ = st.ObterCadastro(ctx, chave)
			if c.Status != CadastroFeito || c.RequesterID != "r1" || !c.Criado {
				t.Fatalf("depois de concluir: %+v", c)
			}
			// `feito` é imutável: regravar a proposta não desfaz nem troca o cliente.
			c, err = st.GravarCadastroProposta(ctx, &Cadastro{Chave: chave, ConversationID: conv, Nome: "Outro Nome", ClienteID: "k2"})
			if err != nil || c.Status != CadastroFeito || c.ClienteID != "k1" || c.Nome != "Maria Souza" || c.RequesterID != "r1" {
				t.Fatalf("linha feito foi alterada: %+v err=%v", c, err)
			}
			if err := st.ConcluirCadastro(ctx, unico("nao-existe"), "r1", false); err == nil {
				t.Error("concluir cadastro inexistente deveria falhar")
			}
			if err := st.ConcluirCadastro(ctx, chave, "", false); err == nil {
				t.Error("concluir sem requester_id deveria falhar")
			}
			if _, err := st.GravarCadastroProposta(ctx, &Cadastro{ConversationID: conv}); err == nil {
				t.Error("proposta sem chave deveria falhar")
			}

			for i := 1; i <= 3; i++ {
				n, err := st.IncrementarTentativasCadastro(ctx, conv)
				if err != nil || n != i {
					t.Fatalf("tentativa %d: n=%d err=%v", i, n, err)
				}
			}
			if err := st.ZerarTentativasCadastro(ctx, conv); err != nil {
				t.Fatalf("zerar: %v", err)
			}
			if n, err := st.IncrementarTentativasCadastro(ctx, conv); err != nil || n != 1 {
				t.Fatalf("depois de zerar: n=%d err=%v", n, err)
			}
			// outra conversa não compartilha o contador
			if n, _ := st.IncrementarTentativasCadastro(ctx, conv+"-b"); n != 1 {
				t.Errorf("contador vazou entre conversas: %d", n)
			}
		})
	}
}
