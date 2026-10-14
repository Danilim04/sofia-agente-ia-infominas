package engine

import (
	"sync"
	"time"
)

// Defaults do disjuntor do cérebro (ZC-5): 3 falhas DEFINITIVAS iguais em
// 5 min abrem o disjuntor por 10 min — nesse período o cérebro nem é chamado
// e a conversa vai direto para a fila humana. Passados os 10 min, UMA chamada
// de teste é liberada: sucesso fecha; falha definitiva reabre por mais 10 min.
const (
	disjuntorLimiar   = 3
	disjuntorJanela   = 5 * time.Minute
	disjuntorAbertura = 10 * time.Minute
)

// disjuntor protege o cliente (e o saldo) de uma falha definitiva repetida —
// ex.: 402 de crédito do OpenRouter: sem ele, cada mensagem de cada cliente
// viraria uma chamada condenada + uma transferência. Seguro para uso
// concorrente (um worker por conversa).
type disjuntor struct {
	mu       sync.Mutex
	limiar   int
	janela   time.Duration
	abertura time.Duration

	falhas    map[string][]time.Time // chave da falha → instantes das definitivas na janela
	aberto    bool
	abertoAte time.Time
	emTeste   bool   // meia-abertura: uma chamada de teste em curso
	chave     string // falha que abriu o disjuntor
	causa     string // causa (linguagem simples) da falha que abriu

	// aoAbrir é chamado (fora do lock) quando o disjuntor ABRE — gancho dos
	// alertas (ZC-7). Pode ser nil.
	aoAbrir func(chave, causa string, ate time.Time)
}

func novoDisjuntor() *disjuntor {
	return &disjuntor{
		limiar: disjuntorLimiar, janela: disjuntorJanela, abertura: disjuntorAbertura,
		falhas: map[string][]time.Time{},
	}
}

// permite decide se o cérebro pode ser chamado agora. Fechado → sim. Aberto e
// ainda dentro do prazo → não. Aberto e prazo vencido → libera UMA chamada de
// teste (as demais esperam o resultado dela).
func (d *disjuntor) permite(agora time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.aberto {
		return true
	}
	if agora.Before(d.abertoAte) || d.emTeste {
		return false
	}
	d.emTeste = true
	return true
}

// estado devolve se está aberto e a causa que o abriu (p/ a nota/alerta).
func (d *disjuntor) estado() (aberto bool, causa string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.aberto, d.causa
}

// sucesso: chamada ao cérebro deu certo. Se estava em teste (meia-abertura),
// fecha o disjuntor e esquece o histórico.
func (d *disjuntor) sucesso() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.aberto {
		d.aberto, d.emTeste = false, false
		d.chave, d.causa = "", ""
		d.falhas = map[string][]time.Time{}
	}
}

// falhaTransitoria: em meia-abertura, a chamada de teste não concluiu — libera
// outro teste (não é a falha que abriu o disjuntor). Fechado: nada a fazer.
func (d *disjuntor) falhaTransitoria() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.aberto {
		d.emTeste = false
	}
}

// falhaDefinitiva registra uma falha definitiva. Devolve true quando ESTA
// falha abriu (ou reabriu) o disjuntor — o chamador dispara o alerta.
func (d *disjuntor) falhaDefinitiva(chave, causa string, agora time.Time) (abriu bool) {
	d.mu.Lock()
	if d.aberto {
		// Chamada de teste falhou de novo: reabre por mais um período.
		d.abertoAte = agora.Add(d.abertura)
		d.emTeste = false
		d.chave, d.causa = chave, causa
		d.mu.Unlock()
		d.notificar(chave, causa, agora.Add(d.abertura))
		return true
	}
	hist := d.falhas[chave]
	corte := agora.Add(-d.janela)
	vivas := hist[:0]
	for _, t := range hist {
		if t.After(corte) {
			vivas = append(vivas, t)
		}
	}
	vivas = append(vivas, agora)
	d.falhas[chave] = vivas
	if len(vivas) < d.limiar {
		d.mu.Unlock()
		return false
	}
	d.aberto, d.emTeste = true, false
	d.abertoAte = agora.Add(d.abertura)
	d.chave, d.causa = chave, causa
	d.mu.Unlock()
	d.notificar(chave, causa, agora.Add(d.abertura))
	return true
}

func (d *disjuntor) notificar(chave, causa string, ate time.Time) {
	if d.aoAbrir != nil {
		d.aoAbrir(chave, causa, ate)
	}
}
