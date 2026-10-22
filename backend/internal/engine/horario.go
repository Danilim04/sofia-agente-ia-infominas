package engine

import (
	"context"
	"fmt"
	"strings"
	"unicode"
)

// Horário humano (E5.5, P7). Fora do horário configurado (HORARIO_HUMANO, fuso
// HORARIO_FUSO, FERIADOS) a Sofia segue atendendo e abrindo chamado; só a
// TRANSFERÊNCIA muda: o handoff continua (a conversa cai na fila para a equipe
// ver no próximo expediente), mas o aviso ao cliente diz que a equipe atende no
// horário configurado e que a conversa fica registrada. A mensagem sai do horário
// configurado — nenhuma hora inventada e nenhum prazo prometido.
// FORA_DO_HORARIO_MODO=ignorar mantém o aviso normal.

// notaForaDoHorario é a marca na nota interna do handoff.
const notaForaDoHorario = "fora do horário"

// msgForaDoHorarioBase monta o corpo do aviso. `horario` é a descrição do
// horário configurado ("de segunda a sexta, das 08:00 às 18:00") e `feriados` diz
// se há feriados configurados.
func msgForaDoHorarioBase(horario string, feriados bool) string {
	var b strings.Builder
	if horario == "" {
		b.WriteString("Agora a nossa equipe de atendimento não está disponível.")
	} else {
		b.WriteString("Agora a nossa equipe de atendimento não está disponível: ela atende " + horario)
		if feriados {
			b.WriteString(" (exceto feriados)")
		}
		b.WriteString(".")
	}
	b.WriteString(" Sua conversa fica registrada por aqui e a equipe retoma com você quando o atendimento voltar. 🙏")
	return b.String()
}

// msgForaDoHorario escolhe o aviso de fora do horário da transferência. Mantém na
// frente a PRIMEIRA frase da mensagem própria da origem quando ela informa algo ao
// cliente além de "vou te encaminhar" (o número do chamado aberto, que não dá para
// ver anexo, que o chamado não foi confirmado, que a empresa não foi localizada) e
// troca o resto pelo aviso de fora do horário. As mensagens genéricas de
// transferência (uma frase só) e as vazias não deixam prefixo.
func (e *Engine) msgForaDoHorario(ctx context.Context, convID string, t Transferencia) string {
	corpo := msgForaDoHorarioBase(e.cfg.HorarioDescricao, e.cfg.Horario.TemFeriados())
	if p := primeiraFrase(t.Mensagem); p != "" {
		return p + " " + corpo
	}
	if t.Origem == OrigemTicketAberto { // sem mensagem própria: o número vem da conversa
		if c, err := e.st.ObterConversa(ctx, convID); err == nil && c != nil && c.TicketNumber > 0 {
			return fmt.Sprintf("Pronto, abri o chamado nº %d para você. %s", c.TicketNumber, corpo)
		}
	}
	return corpo
}

// primeiraFrase devolve a primeira frase de uma mensagem de duas ou mais frases
// (até o primeiro ". "); vazio se a mensagem tem uma frase só ou nenhuma.
func primeiraFrase(msg string) string {
	msg = strings.TrimSpace(msg)
	i := strings.Index(msg, ". ")
	if i < 0 {
		return ""
	}
	for _, r := range msg[i+2:] { // a "segunda frase" precisa ter texto (não só um emoji)
		if unicode.IsLetter(r) {
			return msg[:i+1]
		}
	}
	return ""
}
