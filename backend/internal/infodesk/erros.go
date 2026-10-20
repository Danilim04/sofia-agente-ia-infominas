package infodesk

import (
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
	// ErrCampoObrigatorio: NovoTicket sem deskId ou title (03-tickets.md).
	ErrCampoObrigatorio = errors.New("infodesk: campo obrigatório ausente")
	// ErrTicketSemNumero: o Infodesk aceitou o POST /tickets (2xx) mas a
	// resposta não trouxe `number` reconhecível. O ticket PODE ter sido
	// criado: o chamador deve tratar como "pendente" (ADR 0005), nunca repetir.
	ErrTicketSemNumero = errors.New("infodesk: ticket criado sem número na resposta")
)

// HTTPError é uma resposta >= 400 do Infodesk. Mensagem já vem higienizada
// (sem e-mail, sem sequências longas de dígitos, truncada): pode ir ao log,
// nunca ao cliente final nem ao prompt do LLM.
type HTTPError struct {
	Status     int
	Mensagem   string
	Rota       string        // rota relativa (sem base, sem query)
	RetryAfter time.Duration // do header Retry-After, quando presente (0 = ausente)
}

func (e *HTTPError) Error() string {
	if e.Mensagem == "" {
		return fmt.Sprintf("infodesk %s: status http %d", e.Rota, e.Status)
	}
	return fmt.Sprintf("infodesk %s: status http %d: %s", e.Rota, e.Status, e.Mensagem)
}

// Transitorio informa se vale repetir: 429 e 5xx. 401/403 são erro de
// configuração (alerta), 404 e demais 4xx são definitivos (06-erros.md).
func (e *HTTPError) Transitorio() bool {
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
)

var (
	// reEmail e reNumero mascaram o que parecer dado pessoal num texto vindo do
	// Infodesk: e-mails e números longos (telefone, CPF/CNPJ — 7+ dígitos, com
	// ou sem separadores). Códigos HTTP (3 dígitos) continuam legíveis.
	reEmail  = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	reNumero = regexp.MustCompile(`\b\d(?:[\s().\-/]{0,2}\d){6,}\b`)
)

// higienizarCorpo extrai a `message` do envelope {statusCode, message}
// (06-erros.md) — ou usa o corpo cru quando não é esse formato — e devolve o
// texto higienizado para log. Formato de validação com vários campos não está
// documentado: `message` pode ser string ou lista; ambos são aceitos.
func higienizarCorpo(raw []byte) string {
	texto := string(raw)
	var env struct {
		Message any `json:"message"`
	}
	if err := json.Unmarshal(raw, &env); err == nil && env.Message != nil {
		if m := mensagemComoTexto(env.Message); m != "" {
			texto = m
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

// higienizarTexto mascara e-mails e números longos, achata controle/espaços
// numa linha e corta em corpoErroMax runas.
func higienizarTexto(s string) string {
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
		return time.Duration(seg) * time.Second
	}
	if quando, err := http.ParseTime(valor); err == nil {
		if d := quando.Sub(agora); d > 0 {
			return d
		}
	}
	return 0
}
