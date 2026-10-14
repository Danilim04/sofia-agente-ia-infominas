package alerta

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeNotificador struct {
	mu      sync.Mutex
	alertas []Alerta
}

func (f *fakeNotificador) Notificar(_ context.Context, a Alerta) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alertas = append(f.alertas, a)
}

func (f *fakeNotificador) tipos() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, a := range f.alertas {
		out = append(out, a.Tipo)
	}
	return out
}

func vigiaDeTeste() (*Vigia, *fakeNotificador, *time.Time) {
	agora := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	n := &fakeNotificador{}
	v := NovaVigia(n, func() time.Time { return agora })
	return v, n, &agora
}

// Regra 1: falha definitiva alerta na hora; transitória não.
func TestFalhaDefinitivaAlertaNaHora(t *testing.T) {
	v, n, _ := vigiaDeTeste()
	v.FalhaRegistrada(Falha{ID: "ZPF-1", ConversationID: 7, Origem: "cerebro", Classe: "transitoria", Causa: "timeout"})
	if len(n.alertas) != 0 {
		t.Fatalf("transitória não alerta: %+v", n.alertas)
	}
	v.FalhaRegistrada(Falha{ID: "ZPF-2", ConversationID: 7, Origem: "cerebro", Classe: "definitiva", Causa: "saldo (402)", Link: "http://cw/7"})
	if len(n.alertas) != 1 || n.alertas[0].Tipo != TipoFalhaDefinitiva || n.alertas[0].IDOcorrencia != "ZPF-2" {
		t.Fatalf("definitiva devia alertar uma vez com o ID: %+v", n.alertas)
	}
	if !strings.Contains(n.alertas[0].Texto, "saldo (402)") || n.alertas[0].Link != "http://cw/7" {
		t.Fatalf("alerta sem causa/link: %+v", n.alertas[0])
	}
	// Falha técnica do Mongo também é "precisa de ação".
	v.FalhaRegistrada(Falha{ID: "ZPF-3", ConversationID: 8, Origem: "mongo", Causa: "cadastro ilegível"})
	if len(n.alertas) != 2 {
		t.Fatalf("falha técnica do Mongo devia alertar: %+v", n.alertas)
	}
}

// Regra 2: >5% dos turnos com falha em 15 min, mínimo 3 falhas; não repete
// dentro do silêncio; falhas antigas saem da janela.
func TestTaxaDeFalhasAlertaComMinimoEJanela(t *testing.T) {
	v, n, agora := vigiaDeTeste()
	// 2 falhas em 2 turnos = 100%, mas abaixo do mínimo de 3 falhas.
	v.Turno(false)
	v.Turno(false)
	if len(n.alertas) != 0 {
		t.Fatalf("abaixo do mínimo de 3 falhas não alerta: %+v", n.alertas)
	}
	// 3ª falha: 3/3 → alerta.
	v.Turno(false)
	if len(n.alertas) != 1 || n.alertas[0].Tipo != TipoTaxaDeFalhas {
		t.Fatalf("esperava 1 alerta de taxa, veio %+v", n.alertas)
	}
	// Mais falhas logo depois: silêncio (não repete).
	v.Turno(false)
	if len(n.alertas) != 1 {
		t.Fatalf("não repete dentro do silêncio: %+v", n.alertas)
	}
	// 16 min depois tudo saiu da janela; 3 falhas em 100 turnos = 3% → não alerta.
	*agora = agora.Add(16 * time.Minute)
	for i := 0; i < 97; i++ {
		v.Turno(true)
	}
	v.Turno(false)
	v.Turno(false)
	v.Turno(false)
	if len(n.alertas) != 1 {
		t.Fatalf("3%% não passa de 5%%: %+v", n.tipos())
	}
	// 3 falhas a mais: 6/103 ≈ 5,8% → alerta (silêncio já venceu).
	v.Turno(false)
	v.Turno(false)
	v.Turno(false)
	if len(n.alertas) != 2 || !strings.Contains(n.alertas[1].Texto, "6 de 103") {
		t.Fatalf("esperava 2º alerta de taxa (6 de 103): %+v", n.alertas)
	}
}

// Regra 3: disjuntor aberto alerta na hora.
func TestDisjuntorAbertoAlerta(t *testing.T) {
	v, n, agora := vigiaDeTeste()
	v.DisjuntorAberto("definitiva|500|saldo (402)", "saldo (402)", agora.Add(10*time.Minute))
	if len(n.alertas) != 1 || n.alertas[0].Tipo != TipoDisjuntorAberto || !strings.Contains(n.alertas[0].Texto, "saldo (402)") {
		t.Fatalf("esperava alerta de disjuntor: %+v", n.alertas)
	}
}

// Vigia nula ou sem notificador é um no-op seguro (dev sem alertas).
func TestVigiaSemNotificadorNaoQuebra(t *testing.T) {
	var nula *Vigia
	nula.Turno(false)
	nula.FalhaRegistrada(Falha{Classe: "definitiva"})
	v := NovaVigia(nil, nil)
	v.Turno(false)
	v.DisjuntorAberto("k", "c", time.Now())
}

// O webhook padrão manda {"text": ...} + campos de correlação.
func TestWebhookEnviaJSONComTexto(t *testing.T) {
	var recebido map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &recebido)
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	n := Padrao(srv.URL, nil)
	n.Notificar(context.Background(), Alerta{Tipo: TipoFalhaDefinitiva, Titulo: "T", Texto: "X", IDOcorrencia: "ZPF-9", ConversationID: 5})
	if recebido == nil || !strings.Contains(recebido["text"].(string), "T") || recebido["id_ocorrencia"] != "ZPF-9" {
		t.Fatalf("webhook recebeu %+v", recebido)
	}
	// Sem URL, só o log — nada a enviar, nada a quebrar.
	Padrao("", nil).Notificar(context.Background(), Alerta{Tipo: TipoTaxaDeFalhas})
}
