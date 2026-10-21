package toolsapi

import (
	"context"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bot-infominas/internal/config"
	"bot-infominas/internal/engine"
	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
	"bot-infominas/internal/taxonomia"
)

// Tipos de alerta emitidos aqui (o pacote alerta é folha e não os conhece;
// `Aviso` aceita qualquer string). Os que já existem lá (ex.:
// alerta.TipoChaveSemEscopo) são usados pela constante de lá.
const (
	AlertaTicketPendente = "ticket_pendente"       // POST sem resposta e a listagem não achou: pode haver ticket sem número
	AlertaConfiguracao   = "configuracao_infodesk" // 400 VALIDATION_ERROR / INVALID_REFERENCE: campo exigido pela organização ou taxonomia velha
	AlertaTaxonomia      = taxonomia.AlertaInvalida
)

// InfodeskAPI é o que o toolsapi usa do cliente do Infodesk (satisfeito por
// *infodesk.Client; os testes usam um fake). Tools Python nunca chegam aqui:
// só o gateway fala com o Infodesk.
type InfodeskAPI interface {
	CriarTicketDaConversa(ctx context.Context, convID string, t infodesk.NovoTicketDaConversa) (infodesk.Ticket, error)
	ListarTickets(ctx context.Context, f infodesk.FiltroTickets) (infodesk.Pagina[infodesk.Ticket], error)
	Ticket(ctx context.Context, number int) (infodesk.Ticket, error)
	VincularSolicitante(ctx context.Context, convID, requesterID string) error
}

// Resolvedor é a taxonomia (satisfeito por *taxonomia.Taxonomia).
type Resolvedor interface {
	Resolver(ctx context.Context, categoria, prioridade string) (taxonomia.Resolvida, error)
	Recarregar(ctx context.Context) error
	DepartamentoNome(id string) string
}

// Transferidor é o CAMINHO ÚNICO de transferência para humano (ADR 0007):
// *engine.Engine. Devolve o ID de ocorrência, ou "" quando a conversa já não
// era do bot (nada foi feito).
type Transferidor interface {
	TransferirParaHumano(ctx context.Context, convID string, t engine.Transferencia) string
}

// Avisador entrega alertas ao responsável (*alerta.Vigia). O texto NÃO pode
// ter PII.
type Avisador interface {
	Aviso(tipo, titulo, texto, convID string) bool
}

// ConsultaTickets é uma extensão de LEITURA do store que ainda não existe em
// store.Store (a zona da E3.1 não toca o store): quando o store passa a
// implementá-la o toolsapi a usa por type assertion — dedup local por
// telefone + categoria, posse (a) por número e a parte local de "meus
// chamados". Sem ela, essas três partes degradam (veja cada uso) e o resto
// funciona só com a tabela por chave, `conversas` e o Infodesk.
// TODO(E3.1-store): PR de store com TicketsDoTelefone/TicketPorNumero nos dois
// backends + caso em store_test.go.
type ConsultaTickets interface {
	// TicketsDoTelefone devolve os tickets locais do telefone criados desde
	// `desde`, mais novos primeiro.
	TicketsDoTelefone(ctx context.Context, telefone string, desde time.Time) ([]store.Ticket, error)
	// TicketPorNumero devolve o ticket local de `numero`, ou nil.
	TicketPorNumero(ctx context.Context, numero int) (*store.Ticket, error)
}

// Deps agrupa as dependências do Handler.
type Deps struct {
	Token        string
	Log          *slog.Logger
	Store        store.Store
	Infodesk     InfodeskAPI
	Taxonomia    Resolvedor
	Transferidor Transferidor
	Alertas      Avisador // nil = sem alertas
	Opcoes       Opcoes
	Agora        func() time.Time // nil = time.Now
}

// Opcoes são as hipóteses de trabalho configuráveis desta etapa. Cada uma
// referencia o item de docs/infodesk/07 que a decide.
type Opcoes struct {
	// TicketFinishConversa (TICKET_FINISH_CONVERSA, default false): `finish` do
	// POST /conversations/:id/ticket. true encerra a conversa e o Desk manda a
	// mensagem configurada. TODO(C6): decisão de negócio.
	TicketFinishConversa bool
	// TicketRodape (TICKET_RODAPE, default true): anexa o rodapé
	// `[Aberto pela Sofia · …]` à descrição. 07 T8.
	TicketRodape *bool
	// TransferirAposTicket (TRANSFERIR_APOS_TICKET, default true): handoff
	// depois do ticket aberto. TODO(P11/C6): revisar.
	TransferirAposTicket *bool
	// DedupJanela (TICKET_DEDUP_JANELA, default 24h): mesma categoria + mesmo
	// telefone dentro da janela conta como chamado já aberto.
	DedupJanela time.Duration
	// DedupConsultaInfodesk (DEDUP_CONSULTA_INFODESK, default true): consulta
	// GET /tickets?phone=&status=open no preparar. 07 T5.
	DedupConsultaInfodesk *bool
	// TicketPendenteTTL (TICKET_PENDENTE_TTL, default 10m): até quando um ticket
	// `pendente` ainda conta como "em andamento" num segundo `abrir`.
	TicketPendenteTTL time.Duration
	// IdentTTL (IDENT_TTL, default 24h), IdentContratoRE (IDENT_CONTRATO_RE,
	// default ^\d{4,12}$) e IdentSal (IDENT_HASH_SAL; vazio = o token da API de
	// tools): identificação por CPF/contrato.
	IdentTTL        time.Duration
	IdentContratoRE string
	IdentSal        string
	// DepartamentoHumanoPadrao (DEPARTAMENTO_HUMANO_PADRAO, P11/D6): o
	// departamento que o caminho único da engine usa no handoff; aqui só serve
	// para o NOME dele na resposta de `transferir`.
	DepartamentoHumanoPadrao string

	contratoRE *regexp.Regexp
}

func ptrBool(b bool) *bool { return &b }

func (o Opcoes) comPadroes() Opcoes {
	if o.TicketRodape == nil {
		o.TicketRodape = ptrBool(true)
	}
	if o.TransferirAposTicket == nil {
		o.TransferirAposTicket = ptrBool(true)
	}
	if o.DedupConsultaInfodesk == nil {
		o.DedupConsultaInfodesk = ptrBool(true)
	}
	if o.DedupJanela <= 0 {
		o.DedupJanela = 24 * time.Hour
	}
	if o.TicketPendenteTTL <= 0 {
		o.TicketPendenteTTL = 10 * time.Minute
	}
	if o.IdentTTL <= 0 {
		o.IdentTTL = 24 * time.Hour
	}
	if o.IdentContratoRE == "" {
		o.IdentContratoRE = `^\d{4,12}$`
	}
	re, err := regexp.Compile(o.IdentContratoRE)
	if err != nil {
		re = regexp.MustCompile(`^\d{4,12}$`)
	}
	o.contratoRE = re
	return o
}

// OpcoesDoAmbiente lê as hipóteses desta etapa do ambiente (a zona da E3.1 não
// inclui internal/config; o que já está lá vem de cfg).
func OpcoesDoAmbiente(cfg *config.Config) Opcoes {
	return Opcoes{
		TicketFinishConversa:     envBool("TICKET_FINISH_CONVERSA", false),
		TicketRodape:             ptrBool(envBool("TICKET_RODAPE", true)),
		TransferirAposTicket:     ptrBool(envBool("TRANSFERIR_APOS_TICKET", true)),
		DedupConsultaInfodesk:    ptrBool(envBool("DEDUP_CONSULTA_INFODESK", true)),
		DedupJanela:              envDur("TICKET_DEDUP_JANELA", 24*time.Hour),
		TicketPendenteTTL:        envDur("TICKET_PENDENTE_TTL", 10*time.Minute),
		IdentContratoRE:          os.Getenv("IDENT_CONTRATO_RE"),
		IdentSal:                 os.Getenv("IDENT_HASH_SAL"),
		IdentTTL:                 cfg.IdentTTL,
		DepartamentoHumanoPadrao: cfg.DepartamentoHumanoPadrao,
	}
}

func envBool(chave string, def bool) bool {
	if v, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(chave))); err == nil {
		return v
	}
	return def
}

func envDur(chave string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(os.Getenv(chave))); err == nil && d > 0 {
		return d
	}
	return def
}
