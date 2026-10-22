package toolsapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"testing"

	"bot-infominas/internal/alerta"
	"bot-infominas/internal/store"
)

// E4.3: o ticket aberto sai no log com o nome `ticket_aberto` e os campos do
// contrato (alerta.CamposDoEvento), com `conversation_id` e sem PII.
func TestAbrir_LogaOEventoTicketAbertoDoContratoSemPII(t *testing.T) {
	var buf bytes.Buffer
	a := novoAmb(t, func(d *Deps) { d.Log = slog.New(slog.NewJSONHandler(&buf, nil)) })
	a.conversa(convTeste, telTeste)
	a.contato(store.Contato{Telefone: telTeste, Origem: store.OrigemLookup, Forma: store.FormaTelefone, ValorMascarado: "Maria S.", RequesterID: "r-1", NomeCadastro: "Maria Souza"})
	if out, _ := a.abrir("sem_conexao"); out["status"] != true {
		t.Fatalf("abrir: %+v", out)
	}

	var achados []map[string]any
	for _, linha := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(linha), &m) == nil && m["msg"] == alerta.EventoTicketAberto {
			achados = append(achados, m)
		}
	}
	if len(achados) != 1 {
		t.Fatalf("esperava 1 evento ticket_aberto, achei %d:\n%s", len(achados), buf.String())
	}
	ev := achados[0]
	var campos []string
	for k := range ev {
		if k != "time" && k != "level" && k != "msg" {
			campos = append(campos, k)
		}
	}
	sort.Strings(campos)
	quer := append([]string(nil), alerta.CamposDoEvento[alerta.EventoTicketAberto]...)
	sort.Strings(quer)
	if strings.Join(campos, ",") != strings.Join(quer, ",") {
		t.Fatalf("campos = %v, contrato = %v", campos, quer)
	}
	if ev["numero"] != float64(48213) || ev["conversation_id"] != convTeste || ev["reconciliado"] != false ||
		ev["categoria"] != "sem_conexao" || ev["mesa"] != "Suporte Técnico" {
		t.Fatalf("valores: %v", ev)
	}
	raw, _ := json.Marshal(ev)
	for _, pii := range []string{telTeste, "Maria"} {
		if strings.Contains(string(raw), pii) {
			t.Fatalf("PII %q no evento: %s", pii, raw)
		}
	}
}
