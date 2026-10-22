package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"bot-infominas/internal/alerta"
	"bot-infominas/internal/brain"
	"bot-infominas/internal/config"
	"bot-infominas/internal/store"
)

// E4.3: a transferência executada sai no log com o nome `transferencia` e os
// campos do contrato (alerta.CamposDoEvento), sem PII.
func TestTransferenciaLogaOEventoDoContratoSemPII(t *testing.T) {
	st, err := store.NewSQLite(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var buf bytes.Buffer
	e := New(&config.Config{DepartamentoHumanoPadrao: deptHumano}, novoFakeInfodesk(),
		&fakeBrain{respostas: []func() (*brain.ChatResponse, error){sempre(&brain.Erro{
			Classe: brain.ClasseDefinitiva, Causa: "erro não tratado no cérebro (status 500)", Status: 500,
		})}},
		st, slog.New(slog.NewJSONHandler(&buf, nil)))

	e.processarLote(context.Background(), []Inbound{msg("c-evt-1", "oi")})

	var achados []map[string]any
	for _, linha := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(linha), &m) == nil && m["msg"] == alerta.EventoTransferencia {
			achados = append(achados, m)
		}
	}
	if len(achados) != 1 {
		t.Fatalf("esperava 1 evento %q, achei %d: %s", alerta.EventoTransferencia, len(achados), buf.String())
	}
	var campos []string
	for k := range achados[0] {
		if k != "time" && k != "level" && k != "msg" {
			campos = append(campos, k)
		}
	}
	sort.Strings(campos)
	quer := append([]string(nil), alerta.CamposDoEvento[alerta.EventoTransferencia]...)
	sort.Strings(quer)
	if strings.Join(campos, ",") != strings.Join(quer, ",") {
		t.Fatalf("campos = %v, contrato = %v", campos, quer)
	}
	if achados[0]["conversation_id"] != "c-evt-1" || achados[0]["departamento"] != deptHumano || !reIDOcorrencia.MatchString(achados[0]["id_ocorrencia"].(string)) {
		t.Fatalf("valores: %v", achados[0])
	}
	linhaEvento, _ := json.Marshal(achados[0])
	for _, pii := range []string{telefoneCliente, "Daniel"} {
		if strings.Contains(string(linhaEvento), pii) {
			t.Fatalf("PII %q no evento: %s", pii, linhaEvento)
		}
	}
}
