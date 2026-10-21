package infodesk

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Testes que fecham os achados da revisão do PR (B1, I1–I5, S1–S6).

// B1: o *url.Error do net/http traz a URL com a query; o telefone não pode
// sobreviver no texto do ErroRede.
func TestClient_TelefoneNaoVazaEmErroDeRede(t *testing.T) {
	s := novoServidor(t, responderJSON(http.StatusOK, `{}`))
	url := s.srv.URL
	s.srv.Close()
	c := New(Options{BaseURL: url, APIKey: "ik_live_teste"})
	ctx := context.Background()

	_, errLookup := c.Lookup(ctx, "+5531990000000")
	_, errTickets := c.ListarTickets(ctx, FiltroTickets{Phone: "+5531990000000"})
	_, errConversas := c.Conversas(ctx, FiltroConversas{Phone: "+5531990000000"})
	for nome, err := range map[string]error{"Lookup": errLookup, "ListarTickets": errTickets, "Conversas": errConversas} {
		var er *ErroRede
		if !errors.As(err, &er) {
			t.Fatalf("%s: esperado *ErroRede, veio %T: %v", nome, err, err)
		}
		if !Transitorio(err) {
			t.Errorf("%s: conexão recusada deve continuar transitória", nome)
		}
		for _, vazou := range []string{"5531990000000", "%2B55", "phone=", "http://"} {
			if strings.Contains(err.Error(), vazou) {
				t.Errorf("%s: %q vazou no erro de rede: %q", nome, vazou, err.Error())
			}
		}
	}

	t.Run("cancelamentoContinuaIdentificavel", func(t *testing.T) {
		s := novoServidor(t, responderJSON(http.StatusOK, `{}`))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := s.cliente().Lookup(ctx, "+5531990000000")
		if !errors.Is(err, context.Canceled) || Transitorio(err) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("timeoutContinuaTransitorio", func(t *testing.T) {
		s := novoServidor(t, func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(300 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		})
		c := s.cliente(func(o *Options) { o.HTTP = &http.Client{Timeout: 50 * time.Millisecond} })
		_, err := c.Lookup(context.Background(), "+5531990000000")
		if !Transitorio(err) || strings.Contains(err.Error(), "5531990000000") {
			t.Errorf("err = %v", err)
		}
	})
}

// I1: ilegível não pode ser confundido com ausente.
func TestDadosEvento_IlegivelFicaObservavel(t *testing.T) {
	ev, err := DecodificarEvento([]byte(`{"id":"e","seq":1,"event":"message.received","data":{
	  "conversation":{"id":"c1","botActive":"true","handledBy":"EXTERNAL_BOT"},
	  "message":{"id":"m","sender":"CLIENT","body":"oi"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Data.Conversation != nil {
		t.Error("conversation ilegível deve ficar nil")
	}
	if ev.Data.Erro == nil || !strings.Contains(ev.Data.Erro.Error(), `"conversation"`) {
		t.Fatalf("Erro = %v; a falha não pode ser silenciosa", ev.Data.Erro)
	}
	if strings.Contains(ev.Data.Erro.Error(), "EXTERNAL_BOT") || strings.Contains(ev.Data.Erro.Error(), "oi") {
		t.Errorf("o erro não pode citar valores do payload: %v", ev.Data.Erro)
	}
	if ev.Data.Message == nil {
		t.Error("message legível não deve ser afetada")
	}

	ok, _ := DecodificarEvento([]byte(eventoMensagemPDF))
	if ok.Data.Erro != nil {
		t.Errorf("payload íntegro não deve ter Erro: %v", ok.Data.Erro)
	}
	ping, _ := DecodificarEvento([]byte(`{"id":"e","event":"ping","data":"x"}`))
	if ping.Data.Erro != nil {
		t.Errorf("ping não é erro: %v", ping.Data.Erro)
	}
}

func TestLookup_IlegivelFicaObservavel(t *testing.T) {
	var l Lookup
	if err := json.Unmarshal([]byte(`{"phone":"+5531","requesters":"nenhum","conversation":{"botActive":"sim"},"clientIds":["k1"]}`), &l); err != nil {
		t.Fatal(err)
	}
	if l.Erro == nil || !strings.Contains(l.Erro.Error(), `"requesters"`) || !strings.Contains(l.Erro.Error(), `"conversation"`) {
		t.Errorf("Erro = %v", l.Erro)
	}
	if len(l.Requesters) != 0 || l.Conversation != nil || len(l.ClientIDs) != 1 {
		t.Errorf("lookup = %+v", l)
	}
	var bom Lookup
	_ = json.Unmarshal([]byte(`{"phone":"+5531","requesters":[],"openTickets":[],"conversation":null}`), &bom)
	if bom.Erro != nil {
		t.Errorf("Erro = %v", bom.Erro)
	}
}

// I2: tipo estranho em createdAt/seq não derruba a página do feed; id/event
// de tipo errado derrubam (o cursor não pode pular um evento).
func TestEvento_EnvelopeTolerante(t *testing.T) {
	for corpo, quer := range map[string]struct {
		seq  int64
		zero bool
	}{
		`{"id":"e","event":"x","seq":7,"createdAt":1760875200}`:               {7, true},
		`{"id":"e","event":"x","seq":"8","createdAt":"2026-10-19T12:00:00Z"}`: {8, false},
		`{"id":"e","event":"x","seq":"oito","createdAt":null}`:                {0, true},
		`{"id":"e","event":"x","seq":{"a":1},"createdAt":{"a":1}}`:            {0, true},
		`{"id":"e","event":"x","seq":9.0}`:                                    {9, true},
	} {
		ev, err := DecodificarEvento([]byte(corpo))
		if err != nil || ev.Seq != quer.seq || ev.CreatedAt.IsZero() != quer.zero {
			t.Errorf("%s: ev = %+v, err = %v", corpo, ev, err)
		}
	}
	if _, err := DecodificarEvento([]byte(`{"id":5,"event":"x"}`)); err == nil {
		t.Error("id de tipo errado deveria falhar")
	}

	s := novoServidor(t, responderJSON(http.StatusOK, `{"data":[{"id":"e1","seq":1,"event":"x","createdAt":12345},{"id":"e2","seq":2,"event":"x"}],"nextAfter":2,"hasMore":false}`))
	feed, err := s.cliente().Eventos(context.Background(), 0, 10)
	if err != nil || len(feed.Data) != 2 || feed.NextAfter != 2 {
		t.Fatalf("feed = %+v, err = %v", feed, err)
	}
	s2 := novoServidor(t, responderJSON(http.StatusOK, `{"data":[{"id":7,"seq":1,"event":"x"}],"nextAfter":1,"hasMore":false}`))
	var er *ErroResposta
	if _, err := s2.cliente().Eventos(context.Background(), 0, 10); !errors.As(err, &er) {
		t.Errorf("id inválido deve derrubar a página com erro claro: %v", err)
	}
}

// I3: o cache devolve cópias PROFUNDAS, inclusive dentro de Bruto.
func TestMesa_CacheClonaProfundo(t *testing.T) {
	s := novoServidor(t, responderJSON(http.StatusOK, `{"id":"m1","name":"Suporte","priorities":[{"id":"p1","name":"Alta"}],"extra":{"lista":[1,2],"obj":{"k":"v"}}}`))
	c := s.cliente()
	ctx := context.Background()

	a, _ := c.Mesa(ctx, "m1")
	a.Bruto["priorities"].([]any)[0].(map[string]any)["name"] = "contaminado"
	a.Bruto["extra"].(map[string]any)["obj"].(map[string]any)["k"] = "contaminado"
	a.Bruto["extra"].(map[string]any)["lista"].([]any)[0] = "contaminado"

	b, _ := c.Mesa(ctx, "m1")
	if s.chamadas.Load() != 1 {
		t.Fatalf("segunda leitura deveria vir do cache (%d chamadas)", s.chamadas.Load())
	}
	if b.Bruto["priorities"].([]any)[0].(map[string]any)["name"] != "Alta" ||
		b.Bruto["extra"].(map[string]any)["obj"].(map[string]any)["k"] != "v" ||
		b.Bruto["extra"].(map[string]any)["lista"].([]any)[0] == "contaminado" {
		t.Errorf("cache contaminado por quem chamou: %v", b.Bruto)
	}
}

// I5: o servidor pode ecoar a chave ou o segredo na message.
func TestHigienizar_MascaraSegredosEcoados(t *testing.T) {
	for corpo, naoTem := range map[string]string{ //nolint:gosec // segredos FICTÍCIOS: o teste prova que são mascarados
		`{"statusCode":401,"message":"Chave ik_live_AbC123xyz inválida"}`:           "AbC123xyz",
		`{"statusCode":401,"message":"Authorization: Bearer abc.def-ghi inválido"}`: "abc.def-ghi",
		`{"statusCode":401,"message":"segredo whsec_Segredo99 não confere"}`:        "Segredo99",
		`{"statusCode":401,"message":"BEARER TokenQualquer"}`:                       "TokenQualquer",
		`{"statusCode":401,"message":"chave sk_live_zzz9 revogada"}`:                "zzz9",
	} {
		got := higienizarCorpo([]byte(corpo))
		if strings.Contains(got, naoTem) || !strings.Contains(got, "[segredo]") {
			t.Errorf("%s → %q", corpo, got)
		}
	}
	// Texto comum com "ik" ou "bearer" soltos (sem _ e sem espaço+token) não é mascarado.
	if got := higienizarTexto("o ik é um prefixo; urso bearer? não"); got != "o ik é um prefixo; urso bearer? não" {
		t.Errorf("mascarou texto comum: %q", got)
	}
}

func TestClient_ChaveEcoadaPeloServidorNaoVai(t *testing.T) {
	const chave = "ik_live_teste"
	s := novoServidor(t, responderJSON(http.StatusUnauthorized, `{"statusCode":401,"code":"UNAUTHORIZED","message":"A chave `+chave+` foi revogada"}`))
	err := s.cliente().Encerrar(context.Background(), "c1")
	var he *HTTPError
	if !errors.As(err, &he) || strings.Contains(err.Error(), "teste") || !strings.Contains(he.Mensagem, "[segredo]") {
		t.Errorf("err = %v", err)
	}
	// Chave fora do formato ik_… também é removida (cinto e suspensório).
	s2 := novoServidor(t, responderJSON(http.StatusUnauthorized, `{"message":"chave minha-chave-estranha revogada"}`))
	err = New(Options{BaseURL: s2.srv.URL, APIKey: "minha-chave-estranha"}).Encerrar(context.Background(), "c1") //nolint:gosec // chave fictícia de teste
	if err == nil || strings.Contains(err.Error(), "estranha") {
		t.Errorf("err = %v", err)
	}
}

// S1: timestamp absurdo não satura a comparação e passa.
func TestVerificar_TimestampAbsurdo(t *testing.T) {
	raw := []byte(corpoTeste)
	for _, ts := range []string{"99999999999", "9223372036854775807", "1"} {
		h := Assinar(segredoTeste, ts, raw)
		if err := verificadorTeste().Verificar(ts, raw, h); !errors.Is(err, ErrTimestampForaDaJanela) {
			t.Errorf("ts=%s: err = %v, quer ErrTimestampForaDaJanela", ts, err)
		}
	}
}

// S2: Retry-After enorme não estoura time.Duration.
func TestAnalisarRetryAfter_Teto(t *testing.T) {
	agora := time.Now()
	for _, v := range []string{"99999999999", strconv.Itoa(1 << 62), "86400"} {
		d := analisarRetryAfter(v, agora)
		if d <= 0 || d > retryAfterMax {
			t.Errorf("%s → %v", v, d)
		}
	}
}

// S3: o array `errors` de VALIDATION_ERROR entra, higienizado, na Mensagem.
func TestHigienizarCorpo_ErrorsDoValidationError(t *testing.T) {
	got := higienizarCorpo([]byte(`{"statusCode":400,"code":"VALIDATION_ERROR","message":"Validação falhou",
	  "errors":[{"field":"requesterId","message":"obrigatório"},{"field":"contactPhone","value":"+5531990000000"}]}`))
	if !strings.Contains(got, "Validação falhou") || !strings.Contains(got, "requesterId") || !strings.Contains(got, "obrigatório") {
		t.Errorf("got %q", got)
	}
	if strings.Contains(got, "5531990000000") {
		t.Errorf("PII vazou: %q", got)
	}
	if got := higienizarCorpo([]byte(`{"message":"x","errors":null}`)); got != "x" {
		t.Errorf("errors null: %q", got)
	}
}

// S4: 3xx não seguido vira erro (não "sucesso silencioso").
func TestClient_Resposta3xxEErro(t *testing.T) {
	s := novoServidor(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotModified) })
	err := s.cliente().Encerrar(context.Background(), "c1")
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusNotModified || he.Transitorio() {
		t.Errorf("err = %v", err)
	}
}

// S5: hasMore sem avançar o cursor giraria para sempre quem pagina.
func TestClient_Eventos_HasMoreSemAvancoEErro(t *testing.T) {
	for _, corpo := range []string{
		`{"data":[],"nextAfter":10,"hasMore":true}`, // after == nextAfter
		`{"data":[],"nextAfter":3,"hasMore":true}`,  // regrediu
		`{"data":[],"hasMore":true}`,                // sem nextAfter
	} {
		s := novoServidor(t, responderJSON(http.StatusOK, corpo))
		_, err := s.cliente().Eventos(context.Background(), 10, 500)
		var er *ErroResposta
		if !errors.As(err, &er) {
			t.Errorf("%s: esperado *ErroResposta, veio %v", corpo, err)
		}
	}
	s := novoServidor(t, responderJSON(http.StatusOK, `{"data":[],"nextAfter":11,"hasMore":true}`))
	if feed, err := s.cliente().Eventos(context.Background(), 10, 500); err != nil || !feed.HasMore || feed.NextAfter != 11 {
		t.Errorf("feed = %+v, err = %v", feed, err)
	}
}

// S6: ponteiro opcional para "" não vai ao corpo.
func TestClient_PonteiroParaVazioEhRecusado(t *testing.T) {
	s := novoServidor(t, responderJSON(http.StatusOK, `{"number":1}`))
	c := s.cliente()
	ctx := context.Background()
	vazio, espaco := "", "  "
	if err := c.Handoff(ctx, "c1", Handoff{DepartmentID: &vazio}); !errors.Is(err, ErrIDVazio) {
		t.Errorf("Handoff.DepartmentID: %v", err)
	}
	if err := c.Handoff(ctx, "c1", Handoff{UserID: &espaco}); !errors.Is(err, ErrIDVazio) {
		t.Errorf("Handoff.UserID: %v", err)
	}
	if _, err := c.CriarTicket(ctx, NovoTicket{DeskID: "m", Title: "t", PriorityID: &vazio}); !errors.Is(err, ErrIDVazio) {
		t.Errorf("CriarTicket.PriorityID: %v", err)
	}
	if _, err := c.CriarTicket(ctx, NovoTicket{DeskID: "m", Title: "t", RequesterID: &espaco}); !errors.Is(err, ErrIDVazio) {
		t.Errorf("CriarTicket.RequesterID: %v", err)
	}
	if _, err := c.CriarTicketDaConversa(ctx, "c1", NovoTicketDaConversa{DeskID: "m", Title: "t", CatalogItemID: &vazio}); !errors.Is(err, ErrIDVazio) {
		t.Errorf("CriarTicketDaConversa.CatalogItemID: %v", err)
	}
	if s.chamadas.Load() != 0 {
		t.Errorf("validação local bateu no servidor %d vezes", s.chamadas.Load())
	}
	// nil continua valendo (campo omitido).
	if _, err := c.CriarTicket(ctx, NovoTicket{DeskID: "m", Title: "t"}); err != nil {
		t.Errorf("nil deve passar: %v", err)
	}
}

func TestClient_RespostaSemSaidaEDrenada(t *testing.T) {
	// 2xx com corpo grande e saida == nil: não deve falhar nem travar.
	s := novoServidor(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, strings.Repeat("x", 200<<10))
	})
	if err := s.cliente().Encerrar(context.Background(), "c1"); err != nil {
		t.Errorf("err = %v", err)
	}
}
