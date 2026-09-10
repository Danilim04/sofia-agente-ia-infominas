package engine

import (
	"context"
	"testing"
	"time"
)

func engineDeTeste(debounce, teto time.Duration) *Engine {
	return &Engine{
		baseCtx:      context.Background(),
		debounce:     debounce,
		debounceTeto: teto,
		filas:        make(map[int64]chan inbound),
	}
}

func TestColetarRajadaJuntaMensagensDentroDaJanela(t *testing.T) {
	e := engineDeTeste(60*time.Millisecond, time.Second)
	ch := make(chan inbound, 8)
	ch <- inbound{content: "b"}
	ch <- inbound{content: "c"}

	lote := e.coletarRajada(ch, inbound{content: "a"})
	if len(lote) != 3 {
		t.Fatalf("esperava lote de 3, veio %d", len(lote))
	}
	if lote[0].content != "a" || lote[2].content != "c" {
		t.Fatalf("ordem errada: %+v", lote)
	}
}

func TestColetarRajadaFechaAposSilencio(t *testing.T) {
	e := engineDeTeste(50*time.Millisecond, time.Second)
	ch := make(chan inbound, 8)

	inicio := time.Now()
	lote := e.coletarRajada(ch, inbound{content: "única"})
	if len(lote) != 1 {
		t.Fatalf("esperava lote de 1, veio %d", len(lote))
	}
	if dur := time.Since(inicio); dur < 40*time.Millisecond {
		t.Fatalf("fechou antes da janela de silêncio: %v", dur)
	}
}

func TestColetarRajadaRespeitaTeto(t *testing.T) {
	// Mensagens pingando mais rápido que a janela renovariam para sempre;
	// o teto acumulado força o fechamento.
	e := engineDeTeste(80*time.Millisecond, 200*time.Millisecond)
	ch := make(chan inbound, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			ch <- inbound{content: "ping"}
			time.Sleep(40 * time.Millisecond)
		}
	}()

	inicio := time.Now()
	lote := e.coletarRajada(ch, inbound{content: "primeira"})
	dur := time.Since(inicio)
	if dur > 500*time.Millisecond {
		t.Fatalf("teto não fechou a rajada: %v", dur)
	}
	if len(lote) < 2 {
		t.Fatalf("esperava acumular mensagens até o teto, veio %d", len(lote))
	}
	<-done
}

func TestColetarRajadaSemDebounceDevolveDireto(t *testing.T) {
	e := engineDeTeste(0, 0)
	ch := make(chan inbound, 8)
	ch <- inbound{content: "ignorada nesta chamada"}
	lote := e.coletarRajada(ch, inbound{content: "a"})
	if len(lote) != 1 || lote[0].content != "a" {
		t.Fatalf("debounce=0 deveria devolver só a primeira: %+v", lote)
	}
}
