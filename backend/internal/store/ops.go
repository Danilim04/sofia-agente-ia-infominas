package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ops é a parte do Store escrita UMA vez para os dois backends: SQL portável
// (SQLite ≥ 3.35 e Postgres). As diferenças ficam em três pontos:
//
//   - o prefixo de schema — nas consultas as tabelas aparecem como `gw.tabela`,
//     trocado por "" (SQLite) ou "gateway." (Postgres);
//   - os placeholders — escritos `?`, reescritos para `$n` no Postgres;
//   - os instantes das tabelas novas são BIGINT em milissegundos Unix nos dois
//     (SQLite dá afinidade INTEGER a BIGINT), então não há conversão de tipo.
//
// As tabelas `processed_events` e `falhas` (anteriores a E2.1) mantêm o tipo
// de tempo próprio de cada backend e por isso têm DDL e leitura em
// sqlite.go/postgres.go.
type ops struct {
	db *sql.DB
	pg bool
}

// rebind aplica o prefixo de schema e os placeholders do backend.
func (o *ops) rebind(q string) string {
	if !o.pg {
		return strings.ReplaceAll(q, "gw.", "")
	}
	q = strings.ReplaceAll(q, "gw.", "gateway.")
	var b strings.Builder
	n := 0
	for _, r := range q {
		if r == '?' {
			n++
			b.WriteString("$" + strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// tempoLegado converte um instante para a coluna de `processed_events`
// (INTEGER segundos no SQLite, TIMESTAMPTZ no Postgres).
func (o *ops) tempoLegado(t time.Time) any {
	if o.pg {
		return t
	}
	return t.Unix()
}

func ms(t time.Time) sql.NullInt64 {
	if t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UnixMilli(), Valid: true}
}

func deMs(v sql.NullInt64) time.Time {
	if !v.Valid || v.Int64 == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v.Int64)
}

func inteiroOuNulo(n int) sql.NullInt64 {
	if n <= 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(n), Valid: true}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// Dedup de eventos e cursor do feed
// ---------------------------------------------------------------------------

func (o *ops) MarkProcessed(ctx context.Context, chave string) (bool, error) {
	return o.MarkEvent(ctx, chave, 0)
}

func (o *ops) MarkEvent(ctx context.Context, eventID string, seq int64) (bool, error) {
	res, err := o.db.ExecContext(ctx, o.rebind(
		`INSERT INTO gw.processed_events (event_id, seq, created_at) VALUES (?, ?, ?)
		 ON CONFLICT (event_id) DO NOTHING`),
		eventID, seq, o.tempoLegado(time.Now()))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (o *ops) DesmarcarEvento(ctx context.Context, eventID string) error {
	_, err := o.db.ExecContext(ctx, o.rebind(`DELETE FROM gw.processed_events WHERE event_id = ?`), eventID)
	return err
}

func (o *ops) Cursor(ctx context.Context, nome string) (int64, error) {
	var seq int64
	err := o.db.QueryRowContext(ctx, o.rebind(`SELECT ultimo_seq FROM gw.eventos_cursor WHERE nome = ?`), nome).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return seq, err
}

func (o *ops) AvancarCursor(ctx context.Context, nome string, seq int64) error {
	_, err := o.db.ExecContext(ctx, o.rebind(
		`INSERT INTO gw.eventos_cursor AS c (nome, ultimo_seq, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (nome) DO UPDATE SET ultimo_seq = excluded.ultimo_seq, updated_at = excluded.updated_at
		 WHERE excluded.ultimo_seq > c.ultimo_seq`),
		nome, seq, time.Now().UnixMilli())
	return err
}

// ---------------------------------------------------------------------------
// Conversas
// ---------------------------------------------------------------------------

const colunasConversa = `conversation_id, telefone, estado, handled_by, ticket_number, sessao_iniciada_em,
	ultima_msg_id, ultima_msg_em, ultima_resposta_em, classificacao_json, updated_at`

type linha interface{ Scan(dest ...any) error }

func lerConversa(r linha) (*Conversa, error) {
	var c Conversa
	var ticket, sessao, msgEm, respEm, upd sql.NullInt64
	if err := r.Scan(&c.ID, &c.Telefone, &c.Estado, &c.HandledBy, &ticket, &sessao,
		&c.UltimaMsgID, &msgEm, &respEm, &c.ClassificacaoJSON, &upd); err != nil {
		return nil, err
	}
	c.TicketNumber = int(ticket.Int64)
	c.SessaoIniciadaEm, c.UltimaMsgEm, c.UltimaRespostaEm, c.UpdatedAt = deMs(sessao), deMs(msgEm), deMs(respEm), deMs(upd)
	return &c, nil
}

func (o *ops) ObterConversa(ctx context.Context, id string) (*Conversa, error) {
	c, err := lerConversa(o.db.QueryRowContext(ctx,
		o.rebind(`SELECT `+colunasConversa+` FROM gw.conversas WHERE conversation_id = ?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

func (o *ops) AtualizarConversa(ctx context.Context, id string, fn func(c *Conversa, existia bool) bool) (Conversa, error) {
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return Conversa{}, err
	}
	defer func() { _ = tx.Rollback() }()

	consulta := `SELECT ` + colunasConversa + ` FROM gw.conversas WHERE conversation_id = ?`
	if o.pg {
		consulta += ` FOR UPDATE`
	}
	atual, err := lerConversa(tx.QueryRowContext(ctx, o.rebind(consulta), id))
	existia := err == nil
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return Conversa{}, err
		}
		atual = &Conversa{ID: id}
	}
	if !fn(atual, existia) {
		return *atual, nil
	}
	atual.ID = id
	if _, err := tx.ExecContext(ctx, o.rebind(
		`INSERT INTO gw.conversas (`+colunasConversa+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (conversation_id) DO UPDATE SET
		   telefone = excluded.telefone, estado = excluded.estado, handled_by = excluded.handled_by,
		   ticket_number = excluded.ticket_number, sessao_iniciada_em = excluded.sessao_iniciada_em,
		   ultima_msg_id = excluded.ultima_msg_id, ultima_msg_em = excluded.ultima_msg_em,
		   ultima_resposta_em = excluded.ultima_resposta_em, classificacao_json = excluded.classificacao_json,
		   updated_at = excluded.updated_at`),
		atual.ID, atual.Telefone, atual.Estado, atual.HandledBy, inteiroOuNulo(atual.TicketNumber), ms(atual.SessaoIniciadaEm),
		atual.UltimaMsgID, ms(atual.UltimaMsgEm), ms(atual.UltimaRespostaEm), atual.ClassificacaoJSON, ms(atual.UpdatedAt),
	); err != nil {
		return Conversa{}, err
	}
	if err := tx.Commit(); err != nil {
		return Conversa{}, err
	}
	return *atual, nil
}

// ---------------------------------------------------------------------------
// Contatos
// ---------------------------------------------------------------------------

func (o *ops) ObterContato(ctx context.Context, telefone string, agora time.Time) (*Contato, error) {
	var c Contato
	var exp sql.NullInt64
	err := o.db.QueryRowContext(ctx, o.rebind(
		`SELECT telefone, origem, forma, valor_mascarado, valor_hash, requester_id, cliente_id, nome_cadastro, expires_at
		   FROM gw.contatos WHERE telefone = ?`), telefone).
		Scan(&c.Telefone, &c.Origem, &c.Forma, &c.ValorMascarado, &c.ValorHash, &c.RequesterID, &c.ClienteID, &c.NomeCadastro, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.ExpiresAt = deMs(exp)
	if !c.ExpiresAt.IsZero() && !c.ExpiresAt.After(agora) {
		return nil, nil
	}
	return &c, nil
}

func (o *ops) SalvarContato(ctx context.Context, c *Contato) error {
	if strings.TrimSpace(c.Telefone) == "" {
		return errors.New("store: contato sem telefone")
	}
	_, err := o.db.ExecContext(ctx, o.rebind(
		`INSERT INTO gw.contatos (telefone, origem, forma, valor_mascarado, valor_hash, requester_id, cliente_id, nome_cadastro, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (telefone) DO UPDATE SET
		   origem = excluded.origem, forma = excluded.forma, valor_mascarado = excluded.valor_mascarado,
		   valor_hash = excluded.valor_hash, requester_id = excluded.requester_id, cliente_id = excluded.cliente_id,
		   nome_cadastro = excluded.nome_cadastro, expires_at = excluded.expires_at`),
		c.Telefone, c.Origem, c.Forma, c.ValorMascarado, c.ValorHash, c.RequesterID, c.ClienteID, c.NomeCadastro, ms(c.ExpiresAt))
	return err
}

// ---------------------------------------------------------------------------
// Tickets
// ---------------------------------------------------------------------------

const colunasTicket = `chave_idempotencia, conversation_id, telefone, numero, categoria, mesa_id, departamento_id,
	prioridade, status, status_infodesk, encerrado, reconciliado, raw_resposta, created_at, updated_at`

func lerTicket(r linha) (*Ticket, error) {
	var t Ticket
	var numero, criado, atualizado sql.NullInt64
	var encerrado, reconciliado int
	if err := r.Scan(&t.Chave, &t.ConversationID, &t.Telefone, &numero, &t.Categoria, &t.MesaID, &t.DepartamentoID,
		&t.Prioridade, &t.Status, &t.StatusInfodesk, &encerrado, &reconciliado, &t.RawResposta, &criado, &atualizado); err != nil {
		return nil, err
	}
	t.Numero = int(numero.Int64)
	t.Encerrado, t.Reconciliado = encerrado != 0, reconciliado != 0
	t.CreatedAt, t.UpdatedAt = deMs(criado), deMs(atualizado)
	return &t, nil
}

func (o *ops) ObterTicket(ctx context.Context, chave string) (*Ticket, error) {
	t, err := lerTicket(o.db.QueryRowContext(ctx,
		o.rebind(`SELECT `+colunasTicket+` FROM gw.tickets WHERE chave_idempotencia = ?`), chave))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

func (o *ops) InserirTicketPendente(ctx context.Context, t *Ticket) (*Ticket, bool, error) {
	if strings.TrimSpace(t.Chave) == "" {
		return nil, false, errors.New("store: ticket sem chave de idempotência")
	}
	agora := time.Now()
	status := t.Status
	if status == "" {
		status = TicketPendente
	}
	res, err := o.db.ExecContext(ctx, o.rebind(
		`INSERT INTO gw.tickets (`+colunasTicket+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (chave_idempotencia) DO NOTHING`),
		t.Chave, t.ConversationID, t.Telefone, inteiroOuNulo(t.Numero), t.Categoria, t.MesaID, t.DepartamentoID,
		t.Prioridade, status, t.StatusInfodesk, boolInt(t.Encerrado), boolInt(t.Reconciliado), t.RawResposta,
		agora.UnixMilli(), agora.UnixMilli())
	if err != nil {
		return nil, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	atual, err := o.ObterTicket(ctx, t.Chave)
	if err != nil {
		return nil, false, err
	}
	return atual, n > 0, nil
}

func (o *ops) ConcluirTicket(ctx context.Context, chave string, numero int, raw string, reconciliado bool) error {
	if numero <= 0 {
		return fmt.Errorf("store: número de ticket inválido (%d)", numero)
	}
	res, err := o.db.ExecContext(ctx, o.rebind(
		`UPDATE gw.tickets SET numero = ?, status = ?, raw_resposta = ?, reconciliado = ?, updated_at = ?
		  WHERE chave_idempotencia = ?`),
		numero, TicketAberto, raw, boolInt(reconciliado), time.Now().UnixMilli(), chave)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("store: ticket %q não existe", chave)
	}
	return nil
}

func (o *ops) AtualizarStatusTicket(ctx context.Context, numero int, statusInfodesk string, encerrado bool, quando time.Time) (bool, error) {
	if quando.IsZero() {
		quando = time.Now()
	}
	res, err := o.db.ExecContext(ctx, o.rebind(
		`UPDATE gw.tickets SET status_infodesk = ?, encerrado = ?, updated_at = ?
		  WHERE numero = ? AND (updated_at IS NULL OR updated_at <= ?)`),
		statusInfodesk, boolInt(encerrado), quando.UnixMilli(), numero, quando.UnixMilli())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ddlComum é o schema das tabelas novas (E2.1), idêntico nos dois backends
// (ver o comentário de ops). `gw.` vira o prefixo do backend.
const ddlComum = `
CREATE TABLE IF NOT EXISTS gw.eventos_cursor (
    nome        TEXT PRIMARY KEY,
    ultimo_seq  BIGINT NOT NULL DEFAULT 0,
    updated_at  BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS gw.conversas (
    conversation_id    TEXT PRIMARY KEY,
    telefone           TEXT NOT NULL DEFAULT '',
    estado             TEXT NOT NULL DEFAULT 'bot',
    handled_by         TEXT NOT NULL DEFAULT '',
    ticket_number      BIGINT,
    sessao_iniciada_em BIGINT,
    ultima_msg_id      TEXT NOT NULL DEFAULT '',
    ultima_msg_em      BIGINT,
    ultima_resposta_em BIGINT,
    classificacao_json TEXT NOT NULL DEFAULT '',
    updated_at         BIGINT
);
CREATE INDEX IF NOT EXISTS conversas_telefone ON gw.conversas (telefone);

CREATE TABLE IF NOT EXISTS gw.contatos (
    telefone        TEXT PRIMARY KEY,
    origem          TEXT NOT NULL DEFAULT '',
    forma           TEXT NOT NULL DEFAULT '',
    valor_mascarado TEXT NOT NULL DEFAULT '',
    valor_hash      TEXT NOT NULL DEFAULT '',
    requester_id    TEXT NOT NULL DEFAULT '',
    cliente_id      TEXT NOT NULL DEFAULT '',
    nome_cadastro   TEXT NOT NULL DEFAULT '',
    expires_at      BIGINT
);

CREATE TABLE IF NOT EXISTS gw.tickets (
    chave_idempotencia TEXT PRIMARY KEY,
    conversation_id    TEXT NOT NULL DEFAULT '',
    telefone           TEXT NOT NULL DEFAULT '',
    numero             BIGINT,
    categoria          TEXT NOT NULL DEFAULT '',
    mesa_id            TEXT NOT NULL DEFAULT '',
    departamento_id    TEXT NOT NULL DEFAULT '',
    prioridade         TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL DEFAULT 'pendente',
    status_infodesk    TEXT NOT NULL DEFAULT '',
    encerrado          INTEGER NOT NULL DEFAULT 0,
    reconciliado       INTEGER NOT NULL DEFAULT 0,
    raw_resposta       TEXT NOT NULL DEFAULT '',
    created_at         BIGINT,
    updated_at         BIGINT
);
CREATE INDEX IF NOT EXISTS tickets_numero ON gw.tickets (numero);
CREATE INDEX IF NOT EXISTS tickets_conversa ON gw.tickets (conversation_id);
CREATE INDEX IF NOT EXISTS tickets_telefone ON gw.tickets (telefone);
`
