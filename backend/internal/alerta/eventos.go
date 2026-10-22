package alerta

import (
	"log/slog"
	"regexp"
	"strings"
)

// Nomes dos eventos de log do gateway (E4.3). O cérebro Python usa os MESMOS
// nomes de evento para o que acontece do lado dele (`chat_request`,
// `classificacao`, `tool_exec`, `transferencia`, `jev_indisponivel`); a tabela
// completa dos dois lados, com os campos, está em docs/plano/operacao.md.
//
// O gateway loga em JSON (slog): o nome do evento é a `msg`, os campos são
// atributos. Nenhum campo leva CPF, telefone ou nome do cliente — conversa e
// ticket aparecem por ID/número.
const (
	// EventoSofiaAlerta: um alerta foi emitido. Campos: tipo, titulo, texto,
	// id_ocorrencia, conversation_id, link. Nível ERROR.
	EventoSofiaAlerta = "sofia_alerta"
	// EventoTicketAberto: o ticket foi criado no Infodesk (ou reconciliado).
	// Campos: numero, conversation_id, categoria, mesa, reconciliado. Nível INFO.
	EventoTicketAberto = "ticket_aberto"
	// EventoTransferencia: a conversa foi passada ao atendimento humano.
	// Campos: id_ocorrencia, conversation_id, origem, departamento. Nível INFO.
	EventoTransferencia = "transferencia"
	// EventoMetrica: um contador em memória subiu. Campos: metrica, total. Nível WARN.
	EventoMetrica = "sofia_metrica"
)

// CamposDoEvento é o contrato de campos de cada evento (a fonte do teste e da
// tabela de operacao.md). Mudar um campo = mudar aqui, na tabela e no teste.
var CamposDoEvento = map[string][]string{
	EventoSofiaAlerta:   {"tipo", "titulo", "texto", "id_ocorrencia", "conversation_id", "link"},
	EventoTicketAberto:  {"numero", "conversation_id", "categoria", "mesa", "reconciliado"},
	EventoTransferencia: {"id_ocorrencia", "conversation_id", "origem", "departamento"},
	EventoMetrica:       {"metrica", "total"},
}

// TicketAberto são os campos do evento `ticket_aberto`.
type TicketAberto struct {
	Numero         int
	ConversationID string
	Categoria      string
	Mesa           string
	Reconciliado   bool
}

// LogTicketAberto escreve o evento `ticket_aberto` com os campos do contrato.
func LogTicketAberto(l *slog.Logger, t TicketAberto) {
	if l == nil {
		l = slog.Default()
	}
	l.Info(EventoTicketAberto, "numero", t.Numero, "conversation_id", t.ConversationID,
		"categoria", t.Categoria, "mesa", t.Mesa, "reconciliado", t.Reconciliado)
}

// Transferencia são os campos do evento `transferencia`.
type Transferencia struct {
	IDOcorrencia   string
	ConversationID string
	Origem         string
	Departamento   string
}

// LogTransferencia escreve o evento `transferencia` com os campos do contrato.
func LogTransferencia(l *slog.Logger, t Transferencia) {
	if l == nil {
		l = slog.Default()
	}
	l.Info(EventoTransferencia, "id_ocorrencia", t.IDOcorrencia, "conversation_id", t.ConversationID,
		"origem", t.Origem, "departamento", t.Departamento)
}

// ---------------------------------------------------------------------------
// Máscara de PII dos textos de alerta
// ---------------------------------------------------------------------------

var (
	reCPF      = regexp.MustCompile(`\d{3}\.?\d{3}\.?\d{3}-?\d{2}`)
	reTelefone = regexp.MustCompile(`(?:\+?55[\s-]?)?\(?\d{2}\)?[\s-]?9?\d{4}[-\s]?\d{4}`)
	reSeqLonga = regexp.MustCompile(`\d(?:[ .\-/]?\d){8,}`)
	reEmail    = regexp.MustCompile(`[\w.+-]+@[\w-]+(?:\.[\w-]+)+`)
	// IDs de correlação com cara de número longo (SOF-20261021-1530-01) não são PII.
	reIDOcorrencia = regexp.MustCompile(`SOF-\d{8}-\d{4}-\d{2,}`)
)

// MascararPII troca e-mail, CPF, telefone e números longos por <EMAIL>, <CPF>,
// <TEL> e <NUM>. Rede de segurança do alerta: o contrato é os textos nunca
// levarem PII (só IDs e causas), mas um texto montado de erro externo não pode
// vazar. IDs `SOF-…` ficam intactos. Os limites (nunca no meio de uma palavra)
// são checados à mão porque o RE2 do Go não tem lookaround.
func MascararPII(s string) string {
	if s == "" {
		return s
	}
	var out strings.Builder
	resto := s
	for resto != "" {
		loc := reIDOcorrencia.FindStringIndex(resto)
		if loc == nil {
			out.WriteString(mascararTrecho(resto))
			break
		}
		out.WriteString(mascararTrecho(resto[:loc[0]]))
		out.WriteString(resto[loc[0]:loc[1]])
		resto = resto[loc[1]:]
	}
	return out.String()
}

func mascararTrecho(s string) string {
	s = reEmail.ReplaceAllString(s, "<EMAIL>")
	s = substituirComLimite(s, reCPF, "<CPF>")
	s = substituirComLimite(s, reTelefone, "<TEL>")
	return substituirComLimite(s, reSeqLonga, "<NUM>")
}

func substituirComLimite(s string, re *regexp.Regexp, token string) string {
	idx := re.FindAllStringIndex(s, -1)
	if len(idx) == 0 {
		return s
	}
	var out strings.Builder
	ultimo := 0
	for _, m := range idx {
		if ultimo > m[0] || ehAlnum(s, m[0]-1) || ehAlnum(s, m[1]) {
			continue
		}
		out.WriteString(s[ultimo:m[0]])
		out.WriteString(token)
		ultimo = m[1]
	}
	out.WriteString(s[ultimo:])
	return out.String()
}

func ehAlnum(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
}
