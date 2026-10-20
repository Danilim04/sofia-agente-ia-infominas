// Coalescência + serialização por conversa (F11 do laudo).
//
// WhatsApp entrega pensamentos em rajadas de mensagens curtas. Sem isso, cada
// mensagem virava um turno concorrente na MESMA conversa (corrida no
// checkpointer do cérebro → respostas mescladas/duplicadas). Aqui cada
// conversa ganha UM worker goroutine com uma fila FIFO:
//
//   - a primeira mensagem abre uma janela de coalescência (DEBOUNCE_JANELA,
//     default 8s); cada mensagem nova renova a janela, até o teto acumulado
//     (DEBOUNCE_TETO, default 20s — quem digita sem parar não adia p/ sempre);
//   - fechada a janela, o lote inteiro vira UM turno (textos unidos por \n) —
//     o agente enxerga o pensamento completo do cliente;
//   - o worker processa um lote por vez: NUNCA há dois turnos simultâneos na
//     mesma conversa, mesmo para mensagens fora da janela.
package engine

import (
	"context"
	"time"
)

// filaOciosaTTL: worker sem mensagens por esse tempo encerra e libera o slot.
const filaOciosaTTL = 15 * time.Minute

// loteTimeout limita o processamento de um lote (cérebro + envio).
const loteTimeout = 90 * time.Second

// enfileirar entrega a mensagem à fila da conversa, criando o worker na
// primeira vez. Fila cheia descarta com log (backpressure explícito).
func (e *Engine) enfileirar(msg Inbound) {
	convID := msg.ConversationID
	e.mu.Lock()
	defer e.mu.Unlock()
	ch, ok := e.filas[convID]
	if !ok {
		ch = make(chan Inbound, 64)
		e.filas[convID] = ch
		go e.trabalhadorConversa(convID, ch)
	}
	select {
	case ch <- msg:
	default:
		e.log.Error("fila da conversa cheia, mensagem descartada",
			"conversation_id", convID)
	}
}

// trabalhadorConversa consome a fila de UMA conversa: coleta a rajada, processa
// o lote, repete. Encerra (e se remove do mapa) após ociosidade prolongada.
func (e *Engine) trabalhadorConversa(convID string, ch chan Inbound) {
	for {
		select {
		case <-e.baseCtx.Done():
			return
		case primeiro := <-ch:
			lote := e.coletarRajada(ch, primeiro)
			func() {
				ctx, cancel := context.WithTimeout(e.baseCtx, loteTimeout)
				defer cancel()
				e.processarLote(ctx, lote)
			}()
		case <-time.After(filaOciosaTTL):
			e.mu.Lock()
			if len(ch) == 0 {
				delete(e.filas, convID)
				e.mu.Unlock()
				return
			}
			e.mu.Unlock() // chegou algo entre o timeout e o lock — continua
		}
	}
}

// coletarRajada junta as mensagens da rajada: renova a janela de silêncio a
// cada chegada, respeitando o teto acumulado.
func (e *Engine) coletarRajada(ch chan Inbound, primeiro Inbound) []Inbound {
	lote := []Inbound{primeiro}
	if e.debounce <= 0 {
		return lote
	}
	janela := time.NewTimer(e.debounce)
	defer janela.Stop()
	teto := e.debounceTeto
	if teto <= 0 {
		teto = 2 * e.debounce
	}
	limite := time.NewTimer(teto)
	defer limite.Stop()

	for {
		select {
		case m := <-ch:
			lote = append(lote, m)
			if !janela.Stop() {
				select {
				case <-janela.C:
				default:
				}
			}
			janela.Reset(e.debounce)
		case <-janela.C:
			return lote
		case <-limite.C:
			return lote
		case <-e.baseCtx.Done():
			return lote
		}
	}
}
