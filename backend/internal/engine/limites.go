package engine

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"bot-infominas/internal/config"
)

// Limites de abuso (E4.5, M4). Dois tetos configuráveis, com defaults folgados
// para o uso normal de atendimento:
//
//   - LOTE_MAX_CHARS: o texto coalescido de uma rajada que vai ao cérebro é
//     cortado neste tamanho (custo de LLM e superfície de injeção por mensagem);
//   - TURNOS_POR_HORA_MAX: turnos que chegam ao cérebro por telefone numa janela
//     deslizante de 1 h; acima disso a conversa vai a um atendente (uma vez; o
//     estado `transferida` cala as mensagens seguintes do episódio).
const (
	loteMaxCharsPadrao      = 4000
	turnosPorHoraMaxPadrao  = 60
	janelaTurnos            = time.Hour
	limiteTurnosVarreduraAt = 10000 // acima disto o mapa de telefones é varrido
)

// OrigemLimiteTurnos: o telefone passou de TURNOS_POR_HORA_MAX.
const OrigemLimiteTurnos = "limite_turnos"

func (e *Engine) loteMaxChars() int {
	if e.cfg.LoteMaxChars > 0 {
		return e.cfg.LoteMaxChars
	}
	return loteMaxCharsPadrao
}

func (e *Engine) turnosPorHoraMax() int {
	if e.cfg.TurnosPorHoraMax > 0 {
		return e.cfg.TurnosPorHoraMax
	}
	return turnosPorHoraMaxPadrao
}

// limitarLote corta o texto coalescido no teto (em caracteres, sem partir um
// rune) e loga o corte.
func (e *Engine) limitarLote(convID, texto string) string {
	max := e.loteMaxChars()
	r := []rune(texto)
	if len(r) <= max {
		return texto
	}
	e.log.Warn("lote coalescido cortado no teto (LOTE_MAX_CHARS)",
		"conversation_id", convID, "chars", len(r), "teto", max)
	return strings.TrimSpace(string(r[:max]))
}

// contadorTurnos conta, por telefone, os turnos da última hora (em memória: um
// reinício zera, o que só afrouxa o limite por uma hora).
type contadorTurnos struct {
	mu   sync.Mutex
	hist map[string][]time.Time
}

// registrar anota um turno de `tel` em `agora` e devolve quantos turnos ele
// tem na janela (incluindo este).
func (c *contadorTurnos) registrar(tel string, agora time.Time) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hist == nil {
		c.hist = map[string][]time.Time{}
	}
	corte := agora.Add(-janelaTurnos)
	ts := c.hist[tel]
	i := 0
	for i < len(ts) && !ts[i].After(corte) {
		i++
	}
	ts = append(ts[i:], agora)
	c.hist[tel] = ts
	if len(c.hist) > limiteTurnosVarreduraAt {
		for k, v := range c.hist {
			if len(v) == 0 || !v[len(v)-1].After(corte) {
				delete(c.hist, k)
			}
		}
	}
	return len(ts)
}

// passouDoLimiteDeTurnos registra o turno e transfere a conversa (uma vez) se o
// telefone passou de TURNOS_POR_HORA_MAX. Devolve true quando transferiu — o
// turno NÃO vai ao cérebro. Sem telefone não há como contar: segue.
func (e *Engine) passouDoLimiteDeTurnos(ctx context.Context, convID, telefone string) bool {
	if telefone == "" {
		return false
	}
	// Chave normalizada: +55…, 55… e sem o 9º dígito são o mesmo telefone.
	chave := telefone
	if k, ok := config.ChaveTelefone(telefone); ok {
		chave = k
	}
	n := e.turnos.registrar(chave, e.agora())
	if n <= e.turnosPorHoraMax() {
		return false
	}
	e.log.Warn("limite de turnos por hora excedido; transferindo",
		"conversation_id", convID, "telefone", mascararTelefone(telefone), "turnos", n, "teto", e.turnosPorHoraMax())
	e.TransferirParaHumano(ctx, convID, Transferencia{
		Origem:  OrigemLimiteTurnos,
		Causa:   "o telefone passou do limite de turnos por hora (TURNOS_POR_HORA_MAX)",
		Detalhe: "turnos na última hora: " + strconv.Itoa(n),
	})
	return true
}
