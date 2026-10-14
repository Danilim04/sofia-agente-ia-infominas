package alerta

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Regras de disparo (ZC-7).
const (
	JanelaTaxa   = 15 * time.Minute // janela deslizante da taxa de falhas
	MinFalhas    = 3                // mínimo de falhas na janela para a regra de taxa valer
	LimiarTaxa   = 0.05             // >5% dos turnos com falha
	SilencioTaxa = 15 * time.Minute // não repete o alerta de taxa dentro deste intervalo
)

// Falha é o que a Vigia precisa saber de uma ocorrência (espelho mínimo de
// store.Falha, sem importar o store).
type Falha struct {
	ID             string
	ConversationID int64
	Origem         string
	Classe         string
	Causa          string
	Link           string
}

// Vigia aplica as três regras e chama o Notificador:
//
//  1. falha DEFINITIVA → alerta imediato (uma por ocorrência);
//  2. >5% dos turnos com falha nos últimos 15 min, com no mínimo 3 falhas →
//     alerta (no máximo um a cada 15 min);
//  3. disjuntor aberto → alerta imediato.
//
// Segura para uso concorrente. `Agora` é injetável (testes).
type Vigia struct {
	N     Notificador
	Agora func() time.Time

	mu            sync.Mutex
	turnos        []time.Time // turnos do cérebro (ok ou não) na janela
	falhas        []time.Time // turnos com falha na janela
	ultimoDaTaxa  time.Time
	alertouDaTaxa bool
}

// NovaVigia cria a vigia; n nil = sem alertas (no-op seguro).
func NovaVigia(n Notificador, agora func() time.Time) *Vigia {
	if agora == nil {
		agora = time.Now
	}
	return &Vigia{N: n, Agora: agora}
}

func (v *Vigia) notificar(a Alerta) {
	if v == nil || v.N == nil {
		return
	}
	if a.Quando.IsZero() {
		a.Quando = v.Agora()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	v.N.Notificar(ctx, a)
}

// Turno registra um turno do cérebro (ok=false: terminou em falha) e aplica a
// regra de taxa.
func (v *Vigia) Turno(ok bool) {
	if v == nil {
		return
	}
	agora := v.Agora()
	v.mu.Lock()
	v.turnos = podar(append(v.turnos, agora), agora.Add(-JanelaTaxa))
	if !ok {
		v.falhas = append(v.falhas, agora)
	}
	v.falhas = podar(v.falhas, agora.Add(-JanelaTaxa))
	turnos, falhas := len(v.turnos), len(v.falhas)
	dispara := falhas >= MinFalhas && turnos > 0 && float64(falhas)/float64(turnos) > LimiarTaxa &&
		(!v.alertouDaTaxa || agora.Sub(v.ultimoDaTaxa) >= SilencioTaxa)
	if dispara {
		v.alertouDaTaxa, v.ultimoDaTaxa = true, agora
	}
	v.mu.Unlock()
	if dispara {
		v.notificar(Alerta{
			Tipo:   TipoTaxaDeFalhas,
			Titulo: "Taxa de falhas alta no Zapin",
			Texto: fmt.Sprintf("%d de %d turnos falharam nos últimos %s (%.0f%%). Veja os IDs ZPF-* no log zapin_falha.",
				falhas, turnos, JanelaTaxa, 100*float64(falhas)/float64(turnos)),
		})
	}
}

// FalhaRegistrada avalia uma ocorrência registrada na transferência: falha
// definitiva (ou técnica, origem mongo) alerta na hora.
func (v *Vigia) FalhaRegistrada(f Falha) {
	if v == nil {
		return
	}
	if f.Classe != "definitiva" && f.Origem != "mongo" {
		return
	}
	v.notificar(Alerta{
		Tipo:   TipoFalhaDefinitiva,
		Titulo: "Falha definitiva no Zapin — cliente transferido ao atendente",
		Texto: fmt.Sprintf("Ocorrência %s (conversa %d, origem %s): %s. Repetir não resolve; precisa de ação.",
			f.ID, f.ConversationID, f.Origem, f.Causa),
		IDOcorrencia: f.ID, ConversationID: f.ConversationID, Link: f.Link,
	})
}

// DisjuntorAberto: o gateway parou de chamar o cérebro.
func (v *Vigia) DisjuntorAberto(chave, causa string, ate time.Time) {
	if v == nil {
		return
	}
	v.notificar(Alerta{
		Tipo:   TipoDisjuntorAberto,
		Titulo: "Disjuntor do Zapin ABERTO — clientes indo direto ao atendente",
		Texto: fmt.Sprintf("3 falhas definitivas iguais em 5 min (%s). O cérebro não será chamado até %s; uma chamada de teste decide reabrir. Chave: %s",
			causa, ate.Format("15:04"), chave),
	})
}

// Reportado: um atendente marcou a conversa com a etiqueta de reporte (ZC-8).
func (v *Vigia) Reportado(f Falha) {
	if v == nil {
		return
	}
	causa := f.Causa
	if f.ID == "" {
		causa = "sem ocorrência registrada pelo bot nesta conversa — ver a conversa"
	}
	v.notificar(Alerta{
		Tipo:   TipoReportado,
		Titulo: "Atendente reportou um problema do Zapin",
		Texto: fmt.Sprintf("Conversa %d marcada para revisão. Ocorrência: %s. Causa: %s. Link: %s",
			f.ConversationID, valorOu(f.ID, "—"), causa, valorOu(f.Link, "—")),
		IDOcorrencia: f.ID, ConversationID: f.ConversationID, Link: f.Link,
	})
}

func valorOu(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// podar descarta instantes anteriores ao corte (slice ordenado no tempo).
func podar(ts []time.Time, corte time.Time) []time.Time {
	i := 0
	for i < len(ts) && !ts[i].After(corte) {
		i++
	}
	return append([]time.Time{}, ts[i:]...)
}
