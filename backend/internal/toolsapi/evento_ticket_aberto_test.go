package toolsapi

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"bot-infominas/internal/alerta"
	"bot-infominas/internal/store"
)

// E4.3: o ticket aberto sai no log com o nome `ticket_aberto` e os campos do
// contrato (alerta.CamposDoEvento), com `conversation_id` e sem PII.
func TestAbrir_LogaOEventoTicketAbertoDoContratoSemPII(t *testing.T) {
	a := novoAmb(t)
	a.conversa(convTeste, telTeste)
	a.contato(store.Contato{Telefone: telTeste, Origem: store.OrigemLookup, Forma: store.FormaTelefone, ValorMascarado: "Maria S.", RequesterID: "r-1", NomeCadastro: "Maria Souza"})
	if out, _ := a.abrir("sem_conexao"); out["status"] != true {
		t.Fatalf("abrir: %+v", out)
	}

	var achados []map[string]string
	for _, linha := range strings.Split(a.log(), "\n") {
		if !strings.Contains(linha, "msg=ticket_aberto ") {
			continue
		}
		m := map[string]string{}
		for _, par := range strings.Fields(linha) {
			if k, v, ok := strings.Cut(par, "="); ok {
				m[k] = strings.Trim(v, `"`)
			}
		}
		achados = append(achados, m)
	}
	if len(achados) != 1 {
		t.Fatalf("esperava 1 evento ticket_aberto, achei %d:\n%s", len(achados), a.log())
	}
	var campos []string
	for k := range achados[0] {
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
	if achados[0]["numero"] != "48213" || achados[0]["conversation_id"] != convTeste || achados[0]["reconciliado"] != "false" {
		t.Fatalf("valores: %v", achados[0])
	}
	raw, _ := json.Marshal(achados[0])
	for _, pii := range []string{telTeste, "Maria"} {
		if strings.Contains(string(raw), pii) {
			t.Fatalf("PII %q no evento: %s", pii, raw)
		}
	}
}
