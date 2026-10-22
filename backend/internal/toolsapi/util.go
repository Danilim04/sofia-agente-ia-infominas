package toolsapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"time"

	"bot-infominas/internal/store"
)

// Textos fixos de erro (voz da Sofia, pt-BR; o LLM os explica ao cliente).
// Iguais aos de contratos/tools-infodesk/**/erro-*.json — o teste de contrato
// compara.
const (
	txtCampoInvalidoPreparar   = "título e descrição são obrigatórios (título até 120 caracteres, descrição até 4000)"
	txtCategoriaSemMapa        = "a categoria não tem mesa configurada; ofereça um atendente"
	txtChamadoAberto           = "já existe um chamado aberto para este assunto; pergunte se o cliente quer acompanhar o existente"
	txtNaoIdentificadoPreparar = "o cliente ainda não se identificou; peça o CPF ou o número do contrato antes"
	txtReferenciaIndisponivel  = "não consegui consultar as mesas de atendimento agora; ofereça um atendente"
	txtSemProposta             = "não há proposta de chamado preparada; prepare o chamado antes de abrir"
	txtEmAndamento             = "este chamado já está sendo aberto; não abra outro e aguarde a confirmação"
	txtAbrirIndisponivel       = "não consegui registrar o chamado agora; diga que um atendente vai assumir"
	txtEscopoInsuficiente      = "não foi possível abrir o chamado por aqui; ofereça um atendente"
	txtConsultarNaoEncontrado  = "não encontrei um chamado com esse número; peça para o cliente conferir"
	txtPosseNaoVerificada      = "não consigo confirmar que esse chamado é do cliente por aqui; ofereça um atendente"
	txtConsultarIndisponivel   = "não consegui consultar o chamado agora; peça para tentar mais tarde ou ofereça um atendente"
	txtChamadosIndisponiveis   = "não consegui consultar seus chamados agora; peça para tentar mais tarde"
	txtJaTransferida           = "a conversa já está com um atendente; não responda mais"
	txtTransferirIndisponivel  = "não consegui transferir agora; diga que um atendente vai assumir"
	txtIdentCPFInvalido        = "CPF inválido; peça para o cliente conferir os 11 dígitos"
	txtIdentContratoInvalido   = "número de contrato inválido; peça para o cliente conferir"
	txtIdentNenhum             = "nenhum dado informado; peça o CPF ou o número do contrato"
	txtSessaoInvalida          = "dados da sessão ausentes; não foi possível concluir"
)

// MsgFalhaAoAbrir é o aviso do GATEWAY ao cliente quando o chamado não pôde ser
// registrado e a conversa vai para um atendente (nunca erro técnico, sem
// prometer prazo, ≤ 1 emoji).
const MsgFalhaAoAbrir = "Não consegui registrar o seu chamado agora. Vou te encaminhar para um de nossos atendentes, que segue com você por aqui. 🙏"

var (
	reCategoria = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	reDigitos   = regexp.MustCompile(`^\d{1,10}$`)
)

// somenteDigitos devolve só os dígitos de s.
func somenteDigitos(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// normalizarTelefone devolve "+<dígitos>" ou "" se não parece um telefone.
func normalizarTelefone(s string) string {
	d := somenteDigitos(s)
	if len(d) < 8 || len(d) > 15 {
		return ""
	}
	return "+" + d
}

// formaBR devolve o telefone em dígitos, com o 9º dígito inserido nos números
// BR de 12 dígitos (55 + DDD + 8): +55 31 9999-0001 == +55 31 99999-0001
// (docs/plano/01, A4: "tolerando o 9º dígito").
func formaBR(s string) string {
	d := somenteDigitos(s)
	if strings.HasPrefix(d, "55") && len(d) == 12 && d[4] >= '6' {
		return d[:4] + "9" + d[4:]
	}
	return d
}

func mesmoTelefone(a, b string) bool {
	fa, fb := formaBR(a), formaBR(b)
	return fa != "" && fa == fb
}

// mascararTelefone deixa só os 4 últimos dígitos (regra 5: PII mascarada em log).
func mascararTelefone(s string) string {
	r := []rune(s)
	if len(r) <= 4 {
		return "****"
	}
	return "****" + string(r[len(r)-4:])
}

// str lê um campo string do `Bruto` de um objeto do Infodesk (dado externo).
func str(m map[string]any, chave string) string {
	if v, ok := m[chave].(string); ok {
		return v
	}
	return ""
}

// textoExterno limpa um texto vindo do Infodesk antes de ir ao LLM: dado
// externo é DADO (regra 4), sem controle e com teto.
func textoExterno(s string, max int) string {
	out, _ := sanitizarTexto(s, max, true)
	return out
}

func parseInstante(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// chaveIdempotencia = sha256(conversation_id + titulo + categoria + criada_em)
// em hex (ADR 0005; mesma fórmula do Python).
func chaveIdempotencia(convID, titulo, categoria, criadaEm string) string {
	sum := sha256.Sum256([]byte(convID + titulo + categoria + criadaEm))
	return hex.EncodeToString(sum[:])
}

// hashComSal devolve sha256(sal + ":" + valor) em hex (o CPF/contrato em claro
// nunca é persistido — ADR 0004).
func hashComSal(sal, valor string) string {
	sum := sha256.Sum256([]byte(sal + ":" + valor))
	return hex.EncodeToString(sum[:])
}

// cpfValido confere 11 dígitos, não todos iguais, e os dois dígitos
// verificadores. O Python já valida (src/identity/cpf.py); aqui é a defesa do
// gateway, que não confia em quem chama.
func cpfValido(d string) bool {
	if len(d) != 11 {
		return false
	}
	todosIguais := true
	for i := 1; i < 11; i++ {
		if d[i] != d[0] {
			todosIguais = false
		}
	}
	if todosIguais {
		return false
	}
	dv := func(n int) int {
		soma := 0
		for i := 0; i < n; i++ {
			soma += int(d[i]-'0') * (n + 1 - i)
		}
		r := (soma * 10) % 11
		if r == 10 {
			r = 0
		}
		return r
	}
	return dv(9) == int(d[9]-'0') && dv(10) == int(d[10]-'0')
}

func mascararCPF(d string) string {
	if len(d) != 11 {
		return "***"
	}
	return "***.***." + d[6:9] + "-**"
}

func mascararContrato(c string) string {
	r := []rune(c)
	if len(r) <= 3 {
		return strings.Repeat("*", len(r))
	}
	return strings.Repeat("*", len(r)-3) + string(r[len(r)-3:])
}

// contatoIdentificado devolve o contato do telefone se ele identifica o
// cliente (lookup, CPF ou contrato) e não expirou; senão nil.
func (h *Handler) contatoIdentificado(ctx context.Context, telefone string) *store.Contato {
	c, err := h.d.Store.ObterContato(ctx, telefone, h.agora())
	if err != nil {
		h.log.Error("ler contato", "telefone", mascararTelefone(telefone), "err", err)
		return nil
	}
	if c == nil {
		return nil
	}
	switch c.Forma {
	case store.FormaTelefone, store.FormaCPF, store.FormaContrato:
		return c
	}
	return nil
}

// sessao valida os campos de sessão e resolve o telefone: a conversa do
// store é a autoridade (o telefone veio do webhook); o corpo só vale quando o
// store ainda não tem a conversa.
func (h *Handler) sessao(ctx context.Context, convID, telCorpo string) (conv, tel string, ok bool) {
	conv = strings.TrimSpace(convID)
	if conv == "" {
		return "", "", false
	}
	tel = normalizarTelefone(telCorpo)
	if c, err := h.d.Store.ObterConversa(ctx, conv); err != nil {
		h.log.Error("ler conversa", "conversation_id", conv, "err", err)
	} else if c != nil && c.Telefone != "" {
		if tel != "" && !mesmoTelefone(tel, c.Telefone) {
			h.log.Warn("telefone do corpo diverge da conversa; usando o da conversa", "conversation_id", conv)
		}
		tel = c.Telefone
	}
	if tel == "" {
		return conv, "", false
	}
	return conv, tel, true
}

// sessaoEstrita exige que a conversa exista no store: consultar/listar expõem dado
// de ticket e a posse não pode ser decidida por um telefone vindo só do corpo.
func (h *Handler) sessaoEstrita(ctx context.Context, convID, telCorpo string) (conv, tel string, ok bool) {
	conv = strings.TrimSpace(convID)
	if conv == "" {
		return "", "", false
	}
	c, err := h.d.Store.ObterConversa(ctx, conv)
	if err != nil || c == nil || c.Telefone == "" {
		if err != nil {
			h.log.Error("ler conversa", "conversation_id", conv, "err", err)
		}
		return conv, "", false
	}
	return h.sessao(ctx, conv, telCorpo)
}

func (h *Handler) alertar(tipo, titulo, texto, convID string) {
	if h.d.Alertas == nil {
		return
	}
	h.d.Alertas.Aviso(tipo, titulo, texto, convID)
}

// propostaPreparadaValidade é por quanto tempo uma proposta preparada pode ser
// aberta: os 15 min de PROPOSTA_VALIDADE do cérebro (contados do carimbo, que é
// POSTERIOR ao preparar) mais 2 min de margem, para um "sim" no limite não ser
// aceito pelo cérebro e recusado aqui.
const propostaPreparadaValidade = 15*time.Minute + 2*time.Minute

// hashProposta é o selo da proposta de chamado: o que o `preparar` devolveu e o
// `abrir` precisa receber de volta, byte a byte, para criar o ticket (E4.5, M8).
// Cobre o que vai ao Infodesk e o que decide o destino: conversa, título e
// descrição (já sanitizados), categoria e prioridade (comparadas sem caixa).
func hashProposta(conv, titulo, descricao, categoria, prioridade string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		"proposta-ticket", conv, titulo, descricao,
		strings.ToLower(strings.TrimSpace(categoria)), strings.ToLower(strings.TrimSpace(prioridade)),
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}
