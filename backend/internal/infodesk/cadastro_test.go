package infodesk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestClient_Clientes_ArrayPuroEQuery(t *testing.T) {
	s := novoServidor(t, responderJSON(http.StatusOK, `[{"id":"k1","name":"ACME Ltda","document":"12.345.678/0001-90"},{"id":"k2","name":"ACME Brasil S.A."}]`))
	cs, err := s.cliente().Clientes(context.Background(), "  ACME ")
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || cs[0].ID != "k1" || cs[0].Nome != "ACME Ltda" || cs[1].Nome != "ACME Brasil S.A." {
		t.Errorf("clientes = %+v", cs)
	}
	r := s.ultima.Load()
	if r.metodo != http.MethodGet || r.caminho != PrefixoRotas+"/clients" {
		t.Errorf("rota: %s %s", r.metodo, r.caminho)
	}
	if !strings.Contains(r.query, "q=ACME") || strings.Contains(r.query, "q=+ACME") || !strings.Contains(r.query, "limit=20") {
		t.Errorf("query = %q (q aparado e limit=20)", r.query)
	}
}

func TestClient_Clientes_EnvelopeDataELista(t *testing.T) {
	s := novoServidor(t, responderJSON(http.StatusOK, `{"data":[{"id":"k1","name":"ACME Ltda"}],"nextCursor":null}`))
	cs, err := s.cliente().Clientes(context.Background(), "ACME")
	if err != nil || len(cs) != 1 || cs[0].ID != "k1" {
		t.Fatalf("clientes = %+v err=%v", cs, err)
	}
}

func TestClient_Clientes_QVazioNaoChamaAAPI(t *testing.T) {
	var chamadas atomic.Int32
	s := novoServidor(t, func(w http.ResponseWriter, r *http.Request) {
		chamadas.Add(1)
		responderJSON(http.StatusOK, `[]`)(w, r)
	})
	if _, err := s.cliente().Clientes(context.Background(), "   "); !errors.Is(err, ErrCampoObrigatorio) {
		t.Errorf("err = %v, esperava ErrCampoObrigatorio", err)
	}
	if chamadas.Load() != 0 {
		t.Error("q vazio não pode chamar a API")
	}
}

func TestClient_Clientes_TextoDoClienteNaoVazaNoErro(t *testing.T) {
	s := novoServidor(t, responderJSON(http.StatusForbidden, `{"code":"FORBIDDEN","message":"sem escopo"}`))
	_, err := s.cliente().Clientes(context.Background(), "Empresa Secreta Ltda")
	if err == nil || strings.Contains(err.Error(), "Secreta") {
		t.Errorf("o texto do cliente vazou no erro: %v", err)
	}
}

func TestClient_CriarSolicitante(t *testing.T) {
	s := novoServidor(t, responderJSON(http.StatusOK, `{"id":"r1","name":"Maria Souza","email":"maria@acme.com.br","phone":"+5531999990001",
	  "clientId":"k1","client":{"id":"k1","name":"ACME Ltda"},"created":true,"openTickets":0}`))
	got, err := s.cliente().CriarSolicitante(context.Background(), NovoSolicitante{
		Name: "Maria Souza", ClientID: "k1", Phone: "+5531999990001", Email: "maria@acme.com.br", ConversationID: "c1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "r1" || !got.Created || got.ClientID != "k1" || got.Client == nil || got.Client.Nome != "ACME Ltda" {
		t.Errorf("solicitante = %+v", got)
	}
	r := s.ultima.Load()
	if r.metodo != http.MethodPost || r.caminho != PrefixoRotas+"/requesters" {
		t.Errorf("rota: %s %s", r.metodo, r.caminho)
	}
	var corpo map[string]any
	if err := json.Unmarshal([]byte(r.corpo), &corpo); err != nil {
		t.Fatalf("corpo: %v", err)
	}
	want := map[string]any{"name": "Maria Souza", "clientId": "k1", "phone": "+5531999990001", "email": "maria@acme.com.br", "conversationId": "c1"}
	for k, v := range want {
		if corpo[k] != v {
			t.Errorf("corpo[%s] = %v, esperava %v", k, corpo[k], v)
		}
	}
}

func TestClient_CriarSolicitante_JaExistiaCreatedFalse(t *testing.T) {
	s := novoServidor(t, responderJSON(http.StatusOK, `{"id":"r9","name":"Maria Souza","clientId":"k1","created":false}`))
	got, err := s.cliente().CriarSolicitante(context.Background(), NovoSolicitante{Name: "Maria Souza", ClientID: "k1", Phone: "+5531999990001"})
	if err != nil || got.ID != "r9" || got.Created {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if strings.Contains(s.ultima.Load().corpo, "email") || strings.Contains(s.ultima.Load().corpo, "conversationId") {
		t.Errorf("campos opcionais vazios devem ser omitidos: %s", s.ultima.Load().corpo)
	}
}

func TestClient_CriarSolicitante_ExigeNomeClienteETelefone(t *testing.T) {
	var chamadas atomic.Int32
	s := novoServidor(t, func(w http.ResponseWriter, r *http.Request) {
		chamadas.Add(1)
		responderJSON(http.StatusOK, `{"id":"r1"}`)(w, r)
	})
	c := s.cliente()
	for nome, n := range map[string]NovoSolicitante{
		"sem nome":    {ClientID: "k1", Phone: "+5531999990001"},
		"sem cliente": {Name: "Maria Souza", Phone: "+5531999990001"},
		"sem fone":    {Name: "Maria Souza", ClientID: "k1"},
	} {
		if _, err := c.CriarSolicitante(context.Background(), n); !errors.Is(err, ErrCampoObrigatorio) {
			t.Errorf("%s: err = %v", nome, err)
		}
	}
	if chamadas.Load() != 0 {
		t.Error("nunca cadastra sem clientId/nome/telefone: nenhuma chamada deveria sair")
	}
}

func TestClient_CriarSolicitante_SemEscopoENuncaRepete(t *testing.T) {
	var chamadas atomic.Int32
	s := novoServidor(t, func(w http.ResponseWriter, r *http.Request) {
		chamadas.Add(1)
		responderJSON(http.StatusForbidden, `{"code":"FORBIDDEN","message":"falta contacts:write"}`)(w, r)
	})
	_, err := s.cliente().CriarSolicitante(context.Background(), NovoSolicitante{Name: "Maria Souza", ClientID: "k1", Phone: "+5531999990001"})
	var he *HTTPError
	if !errors.As(err, &he) || he.Code != CodeForbidden {
		t.Fatalf("err = %v, esperava HTTPError FORBIDDEN", err)
	}
	if chamadas.Load() != 1 {
		t.Errorf("chamadas = %d, o POST não pode ser repetido", chamadas.Load())
	}
}

func TestClient_CriarSolicitante_RespostaSemIDEErroDeResposta(t *testing.T) {
	s := novoServidor(t, responderJSON(http.StatusOK, `{"name":"Maria Souza","created":true}`))
	_, err := s.cliente().CriarSolicitante(context.Background(), NovoSolicitante{Name: "Maria Souza", ClientID: "k1", Phone: "+5531999990001"})
	var er *ErroResposta
	if !errors.As(err, &er) {
		t.Fatalf("err = %v, esperava ErroResposta", err)
	}
}
