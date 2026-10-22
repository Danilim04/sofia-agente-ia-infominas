package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"bot-infominas/internal/alerta"
	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
)

// infodeskFalso responde só o que o /health observa: mesas e o feed.
type infodeskFalso struct {
	mesasStatus  int
	eventosState int
}

func (f *infodeskFalso) servir(t *testing.T) (*infodesk.Client, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/desks"):
			w.WriteHeader(f.mesasStatus)
			if f.mesasStatus == http.StatusOK {
				_, _ = w.Write([]byte(`[{"id":"d1","name":"Abertura de Ticket"}]`))
				return
			}
			_, _ = w.Write([]byte(`{"code":"INTERNAL","message":"corpo que não pode ir ao health"}`))
		case strings.HasSuffix(r.URL.Path, "/events"):
			w.WriteHeader(f.eventosState)
			if f.eventosState == http.StatusOK {
				_, _ = w.Write([]byte(`{"data":[],"nextAfter":0,"hasMore":false}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":"FORBIDDEN","message":"falta events:read"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	cli := infodesk.New(infodesk.Options{BaseURL: srv.URL, APIKey: "ik_test_segredo", HTTP: srv.Client()})
	return cli, srv.Close
}

func storeDeTeste(t *testing.T) store.Store {
	t.Helper()
	st, err := store.NewSQLite(filepath.Join(t.TempDir(), "saude.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func consultar(t *testing.T, s *alerta.Saude) (alerta.RespostaSaude, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler()(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/health = %d, quer 200 sempre", rec.Code)
	}
	var r alerta.RespostaSaude
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("corpo inválido: %v", err)
	}
	return r, rec.Body.String()
}

func TestHealthComCacheDeReferenciaNaoCarregado(t *testing.T) {
	st := storeDeTeste(t)
	sondaRef, sondaFeed, tax := alerta.NovaSonda(nil), alerta.NovaSonda(nil), &fotoTaxonomia{}
	tax.definir(alerta.StatusTaxonomia{Habilitada: true, Placeholder: true}) // o boot não conseguiu carregar
	r, _ := consultar(t, novaSaude(st, sondaRef, sondaFeed, tax, true))
	if r.Referencia.Carregada || r.Taxonomia.Carregada || r.Taxonomia.Valida {
		t.Fatalf("nada carregado: %+v %+v", r.Referencia, r.Taxonomia)
	}
	if r.Status != "degradado" || len(r.Motivos) != 2 {
		t.Fatalf("esperava degradado por referência e taxonomia: %+v", r)
	}
	if !r.Taxonomia.Placeholder {
		t.Fatal("placeholder precisa aparecer mesmo sem carregar")
	}
}

func TestHealthComCacheDeReferenciaCarregado(t *testing.T) {
	st := storeDeTeste(t)
	idk := &infodeskFalso{mesasStatus: http.StatusOK, eventosState: http.StatusOK}
	cli, fechar := idk.servir(t)
	defer fechar()
	sondaRef, sondaFeed, tax := alerta.NovaSonda(nil), alerta.NovaSonda(nil), &fotoTaxonomia{}

	fonte := fonteSondada{Client: cli, sonda: sondaRef}
	if _, err := fonte.Mesas(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := (infodeskDoFeed{Client: cli, sonda: sondaFeed}).Eventos(context.Background(), 0, 10); err != nil {
		t.Fatal(err)
	}
	tax.definir(alerta.StatusTaxonomia{Habilitada: true, Carregada: true, Placeholder: true})
	if err := st.AvancarCursor(context.Background(), store.NomeCursorInfodesk, 120); err != nil {
		t.Fatal(err)
	}
	if err := st.AvancarCursor(context.Background(), store.NomeCursorWebhook, 125); err != nil {
		t.Fatal(err)
	}

	r, corpo := consultar(t, novaSaude(st, sondaRef, sondaFeed, tax, true))
	if r.Status != "ok" || !r.Referencia.Carregada || !r.Taxonomia.Valida || !r.Taxonomia.Placeholder {
		t.Fatalf("esperava ok e carregado: %s", corpo)
	}
	if rc := r.Reconciliador; !rc.Ativo || rc.Feed.Estado != "ok" || *rc.UltimoSeq != 120 || *rc.UltimoSeqWebhook != 125 {
		t.Fatalf("reconciliador: %+v", rc)
	}
	for _, proibido := range []string{"ik_test_segredo", "Abertura de Ticket", "http://"} {
		if strings.Contains(corpo, proibido) {
			t.Fatalf("o health vazou %q: %s", proibido, corpo)
		}
	}
}

func TestHealthReferenciaFalhandoNaoVazaCorpoDoErro(t *testing.T) {
	st := storeDeTeste(t)
	idk := &infodeskFalso{mesasStatus: http.StatusBadRequest, eventosState: http.StatusOK}
	cli, fechar := idk.servir(t)
	defer fechar()
	sondaRef, sondaFeed, tax := alerta.NovaSonda(nil), alerta.NovaSonda(nil), &fotoTaxonomia{}
	tax.definir(alerta.StatusTaxonomia{Habilitada: true, Carregada: true})
	if _, err := (fonteSondada{Client: cli, sonda: sondaRef}).Mesas(context.Background()); err == nil {
		t.Fatal("esperava erro do Infodesk")
	}
	r, corpo := consultar(t, novaSaude(st, sondaRef, sondaFeed, tax, true))
	if r.Referencia.Carregada || !strings.Contains(corpo, "HTTP 400") {
		t.Fatalf("classe do erro ausente: %s", corpo)
	}
	if strings.Contains(corpo, "corpo que não pode ir ao health") {
		t.Fatalf("corpo da resposta do Infodesk no health: %s", corpo)
	}
}

func TestHealthFeed403VirasemEscopo(t *testing.T) {
	st := storeDeTeste(t)
	idk := &infodeskFalso{mesasStatus: http.StatusOK, eventosState: http.StatusForbidden}
	cli, fechar := idk.servir(t)
	defer fechar()
	sondaRef, sondaFeed, tax := alerta.NovaSonda(nil), alerta.NovaSonda(nil), &fotoTaxonomia{}
	if _, err := (infodeskDoFeed{Client: cli, sonda: sondaFeed}).Eventos(context.Background(), 0, 10); err == nil {
		t.Fatal("esperava 403")
	}
	r, _ := consultar(t, novaSaude(st, sondaRef, sondaFeed, tax, true))
	if r.Reconciliador.Feed.Estado != alerta.FeedSemEscopo || r.Status != "degradado" {
		t.Fatalf("feed 403: %+v", r)
	}
	if r.Reconciliador.Feed.Classe != "HTTP 403 FORBIDDEN" {
		t.Fatalf("classe = %q", r.Reconciliador.Feed.Classe)
	}
}

func TestHealthSemToolsAPINaoCobraTaxonomia(t *testing.T) {
	st := storeDeTeste(t)
	r, _ := consultar(t, novaSaude(st, alerta.NovaSonda(nil), alerta.NovaSonda(nil), &fotoTaxonomia{}, true))
	if r.Status != "ok" || r.Taxonomia.Habilitada {
		t.Fatalf("TOOLS_API_TOKEN vazio: %+v", r)
	}
}

// Boot sem Infodesk, depois a carga sob demanda deu certo: a taxonomia conta
// como carregada pela leitura da referência.
func TestFotoTaxonomiaConsideraACargaSobDemanda(t *testing.T) {
	var f fotoTaxonomia
	f.definir(alerta.StatusTaxonomia{Habilitada: true, Placeholder: true})
	if f.status(false).Carregada {
		t.Fatal("sem leitura da referência, segue não carregada")
	}
	if s := f.status(true); !s.Carregada || !s.Placeholder || !s.Inferida {
		t.Fatalf("referência lida depois do boot: %+v", s)
	}
	f.definir(alerta.StatusTaxonomia{})
	if f.status(true).Carregada {
		t.Fatal("API de tools desligada nunca fica carregada")
	}
}

func TestHealthTaxonomiaInferidaNaoSeDizValida(t *testing.T) {
	st := storeDeTeste(t)
	sondaRef, sondaFeed, tax := alerta.NovaSonda(nil), alerta.NovaSonda(nil), &fotoTaxonomia{}
	tax.definir(alerta.StatusTaxonomia{Habilitada: true}) // o boot falhou…
	sondaRef.OK()                                         // …e a referência foi lida depois
	sondaFeed.OK()
	r, _ := consultar(t, novaSaude(st, sondaRef, sondaFeed, tax, true))
	if r.Taxonomia.Valida || !r.Taxonomia.Carregada || !r.Taxonomia.Inferida || r.Status != "degradado" {
		t.Fatalf("taxonomia inferida: %+v", r)
	}
}

func TestClasseDoErroSoExpoeCodigoSeguro(t *testing.T) {
	if got := classeDoErro(&infodesk.HTTPError{Status: 400, Code: "INVALID_REFERENCE"}); got != "HTTP 400 INVALID_REFERENCE" {
		t.Fatalf("got %q", got)
	}
	if got := classeDoErro(&infodesk.HTTPError{Status: 400, Code: "cpf 529.982.247-25 inválido"}); got != "HTTP 400" {
		t.Fatalf("code do corpo vazou: %q", got)
	}
}
