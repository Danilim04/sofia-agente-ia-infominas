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
	OrigemCadastro = "cadastro" // solicitante cadastrado pela Sofia (ADR 0009)

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

// TentativasCadastroJanela: depois deste tempo sem tentativa, o contador de
// empresas não resolvidas recomeça (um telefone não fica bloqueado para sempre).
const TentativasCadastroJanela = 24 * time.Hour

// Status de `cadastros.status`.
const (
	CadastroProposta = "proposta" // preparado pelo gateway; ainda não foi para o Infodesk
	CadastroFeito    = "feito"    // POST /requesters concluído
)

// Cadastro é uma linha de `cadastros` (ADR 0009): a proposta de cadastro de
// solicitante que o gateway preparou e a idempotência local do POST
// /requesters. O gateway só executa o que gravou aqui. Nome e Email são PII.
type Cadastro struct {
	Chave          string // sha256 de conversa + telefone + cliente + nome + e-mail (PK)
	ConversationID string
	Telefone       string
	Nome           string
	Email          string
	ClienteID      string // escolhido pelo gateway entre os candidatos de GET /clients
	ClienteNome    string
	Status         string // CadastroProposta | CadastroFeito
	RequesterID    string // preenchido em CadastroFeito
	Criado         bool   // `created` do POST /requesters (false = já existia)
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// HandoffPendente é uma linha de `handoffs_pendentes`: a transferência para
// humano que a engine não conseguiu confirmar no Infodesk (E3.6). Guarda o que
// o handoff precisa para ser repetido (departamento e nota por código, sem PII).
type HandoffPendente struct {
	ConversationID string
	Origem         string // Transferencia.Origem de quem pediu
	IDOcorrencia   string // SOF-… da ocorrência original
	DepartamentoID string // "" = sem departamento (distribuição automática)
	Nota           string // nota interna montada por código
	Tentativas     int    // repetições do vigia que falharam (a da engine não conta)
	ProximaEm      time.Time
	CreatedAt      time.Time
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

	// MarcarHandoffPendente grava (upsert por conversa) que o handoff dessa
	// conversa NÃO foi confirmado pelo Infodesk: o estado local já é
	// `transferida` e o bot calou, mas nenhum atendente foi avisado. É o que o
	// vigia do reconciliador repete (E3.6; docs/plano/01, seção B).
	MarcarHandoffPendente(ctx context.Context, h *HandoffPendente) error
	// HandoffsPendentes devolve as marcas cuja próxima tentativa já venceu
	// (ProximaEm ≤ ate), da mais antiga à mais nova.
	HandoffsPendentes(ctx context.Context, ate time.Time) ([]HandoffPendente, error)
	// AdiarHandoffPendente registra uma tentativa que falhou: grava o total de
	// tentativas e quando tentar de novo.
	AdiarHandoffPendente(ctx context.Context, convID string, tentativas int, proximaEm time.Time) error
	// LimparHandoffPendente apaga a marca (handoff confirmado, ou a conversa
	// deixou de precisar dele); false se não havia.
	LimparHandoffPendente(ctx context.Context, convID string) (bool, error)

	// GravarPropostaTicket guarda o hash da última proposta de chamado que o
	// `preparar` devolveu para a conversa (uma por conversa; a nova substitui a
	// anterior). O `abrir` só executa uma proposta cujo hash confere (E4.5, M8).
	GravarPropostaTicket(ctx context.Context, convID, hash string, criadaEm time.Time) error
	// ObterPropostaTicket devolve o hash e o instante da última proposta preparada
	// da conversa; hash "" = nenhuma.
	ObterPropostaTicket(ctx context.Context, convID string) (string, time.Time, error)

	// GravarCadastroProposta grava (ou renova, se ainda for `proposta`) a
	// proposta de cadastro e devolve a linha como ficou. Uma linha `feito` NUNCA
	// é alterada. CreatedAt passa a ser "agora" ao renovar (validade de 15 min).
	GravarCadastroProposta(ctx context.Context, c *Cadastro) (*Cadastro, error)
	// ObterCadastro devolve o cadastro pela chave ou nil.
	ObterCadastro(ctx context.Context, chave string) (*Cadastro, error)
	// ConcluirCadastro marca como `feito` com o solicitante devolvido pelo
	// Infodesk; erro se a chave não existe. Zera `nome` e `email` da linha (PII
	// que só servia para montar o POST): depois de `feito` ela guarda só o
	// resultado (requester_id, cliente, telefone — o telefone fica, ADR 0008).
	ConcluirCadastro(ctx context.Context, chave, requesterID string, criado bool) error
	// ExpirarCadastros apaga as propostas (`proposta`) criadas antes de `antes` —
	// nunca foram para o Infodesk e carregam nome e e-mail em claro — e limpa
	// nome/e-mail de qualquer linha `feito` que ainda os tenha (linhas anteriores
	// a E4.5). Devolve quantas propostas apagou.
	ExpirarCadastros(ctx context.Context, antes time.Time) (int, error)
	// IncrementarTentativasCadastro soma 1 às vezes em que a empresa não foi
	// resolvida e devolve o total (CADASTRO_MAX_TENTATIVAS). `chave` identifica
	// QUEM tenta (o toolsapi passa o hash do telefone: trocar de conversa não zera
	// o contador). Sem tentativa há mais de TentativasCadastroJanela, recomeça em 1.
	IncrementarTentativasCadastro(ctx context.Context, chave string) (int, error)
	// ZerarTentativasCadastro apaga o contador de `chave`.
	ZerarTentativasCadastro(ctx context.Context, chave string) error

	// RegistrarFalha grava uma ocorrência (tabela `falhas`). CreatedAt vazio =
	// agora.
	RegistrarFalha(ctx context.Context, f *Falha) error
	// UltimaFalha devolve a ocorrência mais recente da conversa, ou nil.
	UltimaFalha(ctx context.Context, convID string) (*Falha, error)

	Close() error
}
