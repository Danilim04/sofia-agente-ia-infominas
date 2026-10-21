// Package store define a persistência do gateway: deduplicação de eventos
// (webhook e mensagens), cursor do feed de eventos, estado das conversas,
// identificação por telefone, tickets e ocorrências de transferência
// (`falhas`) — docs/plano/01-ferramentas.md, seção C.
//
// Dois backends implementam a mesma interface: SQLite (arquivo local, default
// de dev) e Postgres (PG_URL — o mesmo Postgres do RAG/pgvector, em schema
// próprio `gateway`). A escolha é feita em main.go; o resto do código só vê
// a interface. Toda mudança aqui vale para os DOIS backends e ganha caso em
// store_test.go (regra 7 do CLAUDE.md).
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
	Origem         string // cerebro | disjuntor | cliente | anexo | ...
	Classe         string // transitoria | definitiva | "" (não se aplica)
	Causa          string // linguagem simples (vai no alerta)
	Detalhe        string // técnico, sem dado pessoal (só log/tabela)
	CreatedAt      time.Time
}

// Estados da conversa no gateway (`conversas.estado`). O estado é NOSSO;
// `HandledBy` é o do Infodesk — a divergência entre os dois é a métrica
// `humano_assumiu_sem_handoff`.
const (
	EstadoBot         = "bot"         // a Sofia conduz
	EstadoTransferida = "transferida" // humano assumiu/recebeu: o bot cala até `conversation.finished`
	EstadoEncerrada   = "encerrada"   // episódio terminou
)

// Nomes de `eventos_cursor`. O do reconciliador (feed GET /events, E3.3) só
// avança depois de processar a página; o do webhook guarda o maior `seq` que
// o webhook viu — serve de ponto de partida do reconciliador na 1ª execução e
// NUNCA o faz pular um intervalo perdido.
const (
	NomeCursorInfodesk = "infodesk"
	NomeCursorWebhook  = "infodesk_webhook"
)

// HandledByBotExterno é o `handledBy` de uma conversa conduzida pelo nosso bot
// (mesmo valor de infodesk.ConduzidaPorBotExterno; o store não importa o infodesk).
const HandledByBotExterno = "EXTERNAL_BOT"

// Conversa é uma linha de `conversas`. Zero values significam "nulo".
type Conversa struct {
	ID                string
	Telefone          string // E.164 — PII: só mascarado em log
	Estado            string // EstadoBot | EstadoTransferida | EstadoEncerrada
	HandledBy         string // EXTERNAL_BOT | INTERNAL_BOT | AGENT | QUEUE (último visto)
	TicketNumber      int    // 0 = sem chamado vinculado
	SessaoIniciadaEm  time.Time
	UltimaMsgID       string
	UltimaMsgEm       time.Time
	UltimaRespostaEm  time.Time // nossa última resposta ao cliente (vigia N1, E3.3)
	ClassificacaoJSON string    // última `classificacao` do cérebro (auditoria)
	// UpdatedAt é o relógio do ÚLTIMO fato aplicado (createdAt do evento, ou
	// "agora" nas mudanças locais): um evento reentregue mais velho que isto
	// não pode rebaixar o estado (docs/infodesk/07, W9).
	UpdatedAt time.Time
}

// Origens de `contatos.origem` e valores de `contatos.forma`.
const (
	OrigemLookup   = "lookup"
	OrigemCPF      = "cpf"
	OrigemContrato = "contrato"

	FormaTelefone = "telefone"
	FormaCPF      = "cpf"
	FormaContrato = "contrato"
)

// Contato é uma linha de `contatos`: a identificação do telefone, com TTL.
// O CPF em claro NUNCA é persistido — só ValorHash (sha256 + sal) e
// ValorMascarado (ADR 0004).
type Contato struct {
	Telefone       string // PK, E.164
	Origem         string // OrigemLookup | OrigemCPF | OrigemContrato
	Forma          string // FormaTelefone | FormaCPF | FormaContrato | "" (lookup guardado, sem identificação suficiente)
	ValorMascarado string // "***.***.789-**"; para FormaTelefone, o nome do cadastro abreviado
	ValorHash      string
	RequesterID    string
	ClienteID      string
	NomeCadastro   string // PII
	ExpiresAt      time.Time
}

// Status de `tickets.status` (local).
const (
	TicketPendente = "pendente"
	TicketAberto   = "aberto"
	TicketFalha    = "falha"
)

// Ticket é uma linha de `tickets` (idempotência, dedup, listagem local).
type Ticket struct {
	Chave          string // chave_idempotencia (PK)
	ConversationID string
	Telefone       string
	Numero         int // 0 = ainda sem número (pendente)
	Categoria      string
	MesaID         string
	DepartamentoID string
	Prioridade     string
	Status         string // TicketPendente | TicketAberto | TicketFalha
	StatusInfodesk string // `status.name` do último evento ticket.*
	Encerrado      bool
	Reconciliado   bool
	RawResposta    string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Store é a interface de persistência usada pelo resto da aplicação.
type Store interface {
	// MarkProcessed registra uma chave de dedup genérica ("msg:<id>" da
	// mensagem); devolve true se for nova.
	MarkProcessed(ctx context.Context, chave string) (bool, error)
	// MarkEvent registra o `id` do EVENTO do Infodesk (`evt_…`, estável entre
	// reentregas) com o seu `seq`; devolve true se for novo. Retenção de 7 dias.
	MarkEvent(ctx context.Context, eventID string, seq int64) (bool, error)
	// DesmarcarEvento desfaz MarkEvent (o evento não pôde ser enfileirado e
	// precisa poder ser reentregue).
	DesmarcarEvento(ctx context.Context, eventID string) error

	// DesmarcarEventosDepois desfaz MarkEvent de todo evento com seq > `seq`
	// (as chaves genéricas de MarkProcessed, seq 0, ficam) e devolve quantos
	// eram. É a recuperação de crash do reconciliador (E3.3): na subida, o que
	// o webhook marcou como visto mas a fila em memória perdeu volta a poder
	// ser entregue pelo feed.
	DesmarcarEventosDepois(ctx context.Context, seq int64) (int, error)

	// Cursor devolve o maior `seq` do feed já visto/processado (0 = nenhum).
	Cursor(ctx context.Context, nome string) (int64, error)
	// AvancarCursor grava seq só se for maior que o atual (nunca retrocede).
	AvancarCursor(ctx context.Context, nome string, seq int64) error

	// ObterConversa devolve a conversa ou nil.
	ObterConversa(ctx context.Context, id string) (*Conversa, error)
	// AtualizarConversa lê a conversa (ou uma vazia, com existia=false) e
	// aplica fn dentro de uma transação; grava se fn devolver true. fn NÃO
	// pode chamar o store (a transação segura a conexão). Devolve a conversa
	// como ficou. A mudança não precisa preencher UpdatedAt: fn decide.
	AtualizarConversa(ctx context.Context, id string, fn func(c *Conversa, existia bool) bool) (Conversa, error)

	// ConversasSemResposta devolve as conversas em que a Sofia conduz
	// (estado `bot`, handled_by EXTERNAL_BOT) e a última mensagem do cliente
	// é anterior a `antes` e não foi respondida (`ultima_resposta_em` vazio ou
	// anterior à mensagem menos `folga`: `ultima_msg_em` vem do relógio do
	// Infodesk e `ultima_resposta_em` do nosso, e a diferença não pode fazer uma
	// conversa respondida parecer parada): a entrada do vigia BOT_TIMEOUT_HANDOFF (E3.3).
	ConversasSemResposta(ctx context.Context, antes time.Time, folga time.Duration) ([]Conversa, error)

	// ObterContato devolve o contato do telefone se ainda não expirou em
	// `agora`; expirado ou ausente = nil.
	ObterContato(ctx context.Context, telefone string, agora time.Time) (*Contato, error)
	// SalvarContato faz upsert por telefone.
	SalvarContato(ctx context.Context, c *Contato) error

	// InserirTicketPendente grava o ticket com a chave de idempotência; se a
	// chave já existe devolve o existente e false (nada é alterado).
	InserirTicketPendente(ctx context.Context, t *Ticket) (*Ticket, bool, error)
	// ObterTicket devolve o ticket pela chave ou nil.
	ObterTicket(ctx context.Context, chave string) (*Ticket, error)
	// ConcluirTicket marca o ticket como aberto com o número devolvido pelo Infodesk.
	ConcluirTicket(ctx context.Context, chave string, numero int, raw string, reconciliado bool) error
	// TicketsPendentes devolve os tickets com status `pendente`, do mais
	// antigo ao mais novo (reconciliação por listagem, ADR 0005; E3.3).
	TicketsPendentes(ctx context.Context) ([]Ticket, error)
	// FalharTicket marca como `falha` um ticket que ainda está `pendente`
	// (prazo de TICKET_PENDENTE_TTL esgotado); false se já não estava pendente.
	FalharTicket(ctx context.Context, chave string) (bool, error)
	// AtualizarStatusTicket aplica um evento ticket.* ao ticket local de
	// `numero`; false se o número não é nosso.
	AtualizarStatusTicket(ctx context.Context, numero int, statusInfodesk string, encerrado bool, quando time.Time) (bool, error)

	// RegistrarFalha grava uma ocorrência (tabela `falhas`). CreatedAt vazio =
	// agora.
	RegistrarFalha(ctx context.Context, f *Falha) error
	// UltimaFalha devolve a ocorrência mais recente da conversa, ou nil.
	UltimaFalha(ctx context.Context, convID string) (*Falha, error)

	Close() error
}
