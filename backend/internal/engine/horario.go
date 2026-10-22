package engine

import (
	"context"
	"fmt"
	"strings"
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

// msgForaDoHorario escolhe o aviso de fora do horário da transferência. Mantém o
// que a mensagem normal tinha de informação própria para o cliente (o número do
// chamado aberto; que não dá para ver anexo; que o chamado não foi confirmado) e
// troca o resto pelo aviso de fora do horário.
func (e *Engine) msgForaDoHorario(ctx context.Context, convID string, t Transferencia) string {
	corpo := msgForaDoHorarioBase(e.cfg.HorarioDescricao, e.cfg.Horario.TemFeriados())
	switch t.Origem {
	case OrigemAnexo:
		return "Por aqui eu ainda não consigo ver áudio, imagem ou arquivo. " + corpo
	case OrigemTicketPendente:
		return "Não consegui confirmar o seu chamado agora. " + corpo
	case OrigemTicketAberto:
		if c, err := e.st.ObterConversa(ctx, convID); err == nil && c != nil && c.TicketNumber > 0 {
			return fmt.Sprintf("Pronto, abri o chamado nº %d para você. %s", c.TicketNumber, corpo)
		}
	}
	return corpo
}
