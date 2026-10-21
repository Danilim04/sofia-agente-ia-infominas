package infodesk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Erros sentinela do pacote. Use errors.Is para testá-los.
var (
	// ErrTransitorio marca falhas que vale a pena repetir mais tarde (429, 5xx,
	// rede, timeout). *HTTPError e *ErroRede respondem a errors.Is(err,
	// ErrTransitorio) conforme Transitorio(). Quem decide SE repete é o chamador
	// (nunca em POST /tickets — ADR 0005).
	ErrTransitorio = errors.New("infodesk: falha transitória")
	// ErrSemBaseURL: o cliente foi criado sem Options.BaseURL.
	ErrSemBaseURL = errors.New("infodesk: BaseURL não configurada")
	// ErrIDVazio: identificador (conversa, ticket, departamento, usuário) vazio.
	ErrIDVazio = errors.New("infodesk: identificador vazio")
	// ErrCorpoVazio: tentativa de enviar mensagem/resposta sem texto.
	ErrCorpoVazio = errors.New("infodesk: corpo da mensagem vazio")
	// ErrNumeroInvalido: número de chamado <= 0 (o número é inteiro positivo).
	ErrNumeroInvalido = errors.New("infodesk: número de chamado inválido")
	// ErrCampoObrigatorio: NovoTicket sem deskId ou title (03-tickets.md).
	ErrCampoObrigatorio = errors.New("infodesk: campo obrigatório ausente")
	// ErrTicketSemNumero: o Infodesk aceitou o POST /tickets (2xx) mas a
	// resposta não trouxe `number` reconhecível. O ticket PODE ter sido
	// criado: o chamador deve tratar como "pendente" (ADR 0005), nunca repetir.
	ErrTicketSemNumero = errors.New("infodesk: ticket criado sem número na resposta")
)

// Códigos de erro estáveis da API v1 (docs/infodesk/06-erros.md): "decida pelo
// `code`; a `message` é para leitura humana".
const (
	CodeValidationError        = "VALIDATION_ERROR"
	CodeInvalidReference       = "INVALID_REFERENCE"
	CodeUnauthorized           = "UNAUTHORIZED"
	CodeForbidden              = "FORBIDDEN"
	CodeNotFound               = "NOT_FOUND"
	CodeTemplateNotFound       = "TEMPLATE_NOT_FOUND"
	CodeConflict               = "CONFLICT"
	CodeWhatsAppWindowClosed   = "WHATSAPP_WINDOW_CLOSED"
	CodeWhatsAppNotConfigured  = "WHATSAPP_NOT_CONFIGURED"
	CodeTemplateNotApproved    = "TEMPLATE_NOT_APPROVED"
	CodeTemplateParamsMismatch = "TEMPLATE_PARAMS_MISMATCH"
	CodeMediaURLRequired       = "MEDIA_URL_REQUIRED"
	CodeMediaDownloadFailed    = "MEDIA_DOWNLOAD_FAILED"
	CodeRateLimited            = "RATE_LIMITED"
	CodeInternalError          = "INTERNAL_ERROR"
)

// transitorioPorCode classifica os códigos documentados: true = vale repetir
// (só em operação idempotente); false = definitivo ou erro de configuração.
// Código fora da tabela cai no fallback por status HTTP.
var transitorioPorCode = map[string]bool{
	CodeRateLimited:            true,
	CodeInternalError:          true,
	CodeUnauthorized:           false,
	CodeForbidden:              false,
	CodeNotFound:               false,
	CodeValidationError:        false,
	CodeInvalidReference:       false,
	CodeConflict:               false,
	CodeWhatsAppWindowClosed:   false,
	CodeWhatsAppNotConfigured:  false,
	CodeTemplateNotFound:       false,
	CodeTemplateNotApproved:    false,
	CodeTemplateParamsMismatch: false,
	CodeMediaURLRequired:       false,
	CodeMediaDownloadFailed:    false,
}

// HTTPError é uma resposta >= 400 do Infodesk. Mensagem já vem higienizada
// (sem e-mail, sem sequências de 7+ dígitos, truncada): pode ir ao log,
// nunca ao cliente final nem ao prompt do LLM. Limite conhecido: NOMES não
// são detectáveis por padrão — um 4xx de validação que ecoe `title`,
// `description` ou `contact.name` pode trazê-los; quem loga trata Mensagem
// como "sem identificadores", não como "sem PII".
type HTTPError struct {
	Status     int
	Code       string // `code` estável da API (CodeXxx); "" quando ausente ou irreconhecível
	Mensagem   string
	Rota       string        // rota relativa (sem base, sem query)
	RetryAfter time.Duration // do header Retry-After, quando presente (0 = ausente; ver docs/infodesk/07 item A4)
}

func (e *HTTPError) Error() string {
	cod := ""
	if e.Code != "" {
		cod = " [" + e.Code + "]"
	}
	if e.Mensagem == "" {
		return fmt.Sprintf("infodesk %s: status http %d%s", e.Rota, e.Status, cod)
	}
	return fmt.Sprintf("infodesk %s: status http %d%s: %s", e.Rota, e.Status, cod, e.Mensagem)
}

// Transitorio informa se vale repetir. Decide pelo `code` (06-erros.md):
// RATE_LIMITED e INTERNAL_ERROR são transitórios; UNAUTHORIZED, FORBIDDEN,
// NOT_FOUND, VALIDATION_ERROR, INVALID_REFERENCE, CONFLICT,
// WHATSAPP_WINDOW_CLOSED (e os demais códigos documentados) são definitivos.
// Sem `code` — ou com um código que não conhecemos — cai no status HTTP:
// 429 e 5xx (inclusive 502/503/504, não documentados) são transitórios, o
// resto é definitivo. Transitório não autoriza repetir POST /tickets nem
// POST /conversations/:id/ticket (ADR 0005).
func (e *HTTPError) Transitorio() bool {
	if t, ok := transitorioPorCode[e.Code]; ok {
		return t
	}
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// Is permite errors.Is(err, ErrTransitorio).
func (e *HTTPError) Is(alvo error) bool {
	return alvo == ErrTransitorio && e.Transitorio()
}

// ErroRede embrulha falha antes de haver resposta HTTP (DNS, conexão recusada,
// timeout, corpo interrompido). É transitório, exceto quando o próprio
// chamador cancelou o contexto.
type ErroRede struct {
	Rota string
	Err  error
}

func (e *ErroRede) Error() string { return fmt.Sprintf("infodesk %s: rede: %v", e.Rota, e.Err) }
func (e *ErroRede) Unwrap() error { return e.Err }

// Transitorio é falso só para cancelamento explícito do contexto.
func (e *ErroRede) Transitorio() bool { return !errors.Is(e.Err, context.Canceled) }

// Is permite errors.Is(err, ErrTransitorio).
func (e *ErroRede) Is(alvo error) bool {
	return alvo == ErrTransitorio && e.Transitorio()
}

// ErroResposta: o Infodesk respondeu 2xx com corpo vazio ou JSON inválido.
// Definitivo (repetir não ajuda); o corpo nunca entra no erro.
type ErroResposta struct {
	Rota   string
	Motivo string
}

func (e *ErroResposta) Error() string {
	return fmt.Sprintf("infodesk %s: resposta inválida: %s", e.Rota, e.Motivo)
}

// Transitorio é o atalho para errors.Is(err, ErrTransitorio).
func Transitorio(err error) bool { return errors.Is(err, ErrTransitorio) }

// Tetos do texto de erro do Infodesk que entra no erro (e, por ele, no log).
const (
	corpoErroLeituraMax = 8 << 10 // bytes lidos da resposta de erro
	corpoErroMax        = 500     // runas do trecho higienizado
	retryAfterMax       = 24 * time.Hour
)

var (
	// reEmail e reNumero mascaram o que parecer dado pessoal num texto vindo do
	// Infodesk: e-mails e números longos (telefone, CPF/CNPJ — 7+ dígitos, com
	// ou sem separadores), mesmo colados a letras (`contact5531…`). Códigos
	// HTTP (3 dígitos) e UUIDs com segmentos curtos continuam legíveis.
	reEmail  = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	reNumero = regexp.MustCompile(`\d(?:[\s().\-/]{0,2}\d){6,}`)
	// reSegredo mascara chave de API (`ik_live_…`), segredo de webhook
	// (`whsec_…`) e credencial `Bearer …` que o servidor eventualmente ecoe
	// na mensagem (comum em 401).
	reSegredo = regexp.MustCompile(`(?i)\b(?:ik|sk|whsec)_[A-Za-z0-9_]+|\bbearer\s+\S+`)
)

// reCode é o formato aceito para `code`: caixa alta com _ (SCREAMING_SNAKE).
// Qualquer outra coisa vira "" — o valor vem de fora e vai parar em log e
// em Error().
var reCode = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)

// extrairCode lê `code` do envelope de erro {statusCode, code, message}
// (06-erros.md). Ausente, não-string ou fora do formato = "".
func extrairCode(raw []byte) string {
	var env struct {
		Code any `json:"code"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return ""
	}
	if s, ok := env.Code.(string); ok && reCode.MatchString(s) {
		return s
	}
	return ""
}

// higienizarCorpo extrai a `message` do envelope {statusCode, code, message}
// (06-erros.md) — ou usa o corpo cru quando não é esse formato — e devolve o
// texto higienizado para log. O formato do array `errors` de VALIDATION_ERROR
// não está documentado (docs/infodesk/06): `message` pode ser string ou lista;
// ambos são aceitos, e o array `errors` (formato livre) entra compactado e
// higienizado no fim do texto — é o que permite investigar um 400 em produção
// (docs/infodesk/07 item T10).
func higienizarCorpo(raw []byte) string {
	texto := string(raw)
	var env struct {
		Message any             `json:"message"`
		Errors  json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err == nil {
		if env.Message != nil {
			if m := mensagemComoTexto(env.Message); m != "" {
				texto = m
			}
		}
		if e := bytes.TrimSpace(env.Errors); len(e) > 0 && !bytes.Equal(e, []byte("null")) {
			texto += " [errors: " + string(e) + "]"
		}
	}
	return higienizarTexto(texto)
}

func mensagemComoTexto(v any) string {
	switch m := v.(type) {
	case string:
		return m
	case []any:
		partes := make([]string, 0, len(m))
		for _, p := range m {
			if s, ok := p.(string); ok {
				partes = append(partes, s)
			}
		}
		return strings.Join(partes, "; ")
	default:
		return ""
	}
}

// higienizarTexto mascara segredos, e-mails e números longos, achata
// controle/espaços numa linha e corta em corpoErroMax runas.
func higienizarTexto(s string) string {
	s = reSegredo.ReplaceAllLiteralString(s, "[segredo]")
	s = reEmail.ReplaceAllLiteralString(s, "[email]")
	s = reNumero.ReplaceAllLiteralString(s, "[numero]")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	return truncarRunas(s, corpoErroMax)
}

func truncarRunas(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "… [truncado]"
}

// analisarRetryAfter aceita segundos inteiros ou data HTTP (RFC 7231). Valor
// irreconhecível ou no passado devolve 0. O cliente nunca dorme: o chamador
// decide o que fazer com o prazo.
func analisarRetryAfter(valor string, agora time.Time) time.Duration {
	valor = strings.TrimSpace(valor)
	if valor == "" {
		return 0
	}
	if seg, err := strconv.Atoi(valor); err == nil {
		if seg < 0 {
			return 0
		}
		// Teto só para não estourar time.Duration com valor absurdo.
		return time.Duration(min(seg, int(retryAfterMax/time.Second))) * time.Second
	}
	if quando, err := http.ParseTime(valor); err == nil {
		if d := quando.Sub(agora); d > 0 {
			return d
		}
	}
	return 0
}
