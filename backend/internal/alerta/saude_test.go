package alerta

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)

// relogio devolve um relógio que anda 1 s a cada leitura (ordem dos instantes previsível).
func relogio() func() time.Time {
	t := t0
	return func() time.Time { t = t.Add(time.Second); return t }
}

func saudeSaudavel() (*Saude, *Sonda, *Sonda) {
	ref, feed := NovaSonda(relogio()), NovaSonda(relogio())
	ref.OK()
	feed.OK()
	return &Saude{
		Agora:           func() time.Time { return t0.Add(time.Hour) },
		Iniciado:        t0,
		Referencia:      ref,
		Feed:            feed,
		ReconciliaAtivo: true,
		Taxonomia: func() StatusTaxonomia {
			return StatusTaxonomia{Habilitada: true, Carregada: true, Placeholder: true}
		},
		Seq: func(context.Context) (int64, int64, error) { return 120, 125, nil },
	}, ref, feed
}

func TestSaudeTudoCarregadoEstaOK(t *testing.T) {
	s, _, _ := saudeSaudavel()
	r := s.Relatorio(context.Background())
	if r.Status != "ok" || len(r.Motivos) != 0 {
		t.Fatalf("esperava ok sem motivos: %+v", r)
	}
	if !r.Referencia.Carregada || r.Referencia.UltimoOK == nil {
		t.Fatalf("referência deveria estar carregada: %+v", r.Referencia)
	}
	if !r.Taxonomia.Valida || !r.Taxonomia.Placeholder || r.Taxonomia.Problemas != 0 {
		t.Fatalf("taxonomia válida e placeholder: %+v", r.Taxonomia)
	}
	rc := r.Reconciliador
	if !rc.Ativo || rc.Feed.Estado != FeedOK || rc.UltimoSeq == nil || *rc.UltimoSeq != 120 || *rc.UltimoSeqWebhook != 125 {
		t.Fatalf("reconciliador: %+v", rc)
	}
	if r.UptimeSeg != 3600 {
		t.Fatalf("uptime = %d, quer 3600", r.UptimeSeg)
	}
}

func TestSaudeCasosDeDegradacao(t *testing.T) {
	casos := []struct {
		nome   string
		mexer  func(*Saude, *Sonda, *Sonda)
		motivo string
	}{
		{"referência nunca lida", func(s *Saude, _, _ *Sonda) { s.Referencia = NovaSonda(nil) }, MotivoReferenciaNaoCarregada},
		{"última leitura da referência falhou", func(_ *Saude, ref, _ *Sonda) { ref.Falha("falha", "HTTP 503 UNAVAILABLE") }, MotivoReferenciaNaoCarregada},
		{"taxonomia não carregada", func(s *Saude, _, _ *Sonda) {
			s.Taxonomia = func() StatusTaxonomia { return StatusTaxonomia{Habilitada: true} }
		}, MotivoTaxonomiaNaoCarregada},
		{"taxonomia diverge do Infodesk", func(s *Saude, _, _ *Sonda) {
			s.Taxonomia = func() StatusTaxonomia { return StatusTaxonomia{Habilitada: true, Carregada: true, Problemas: 2} }
		}, MotivoTaxonomiaInvalida},
		{"403 no feed", func(_ *Saude, _, feed *Sonda) { feed.Falha(FeedSemEscopo, "HTTP 403 FORBIDDEN") }, MotivoFeedSemEscopo},
		{"feed falhando", func(_ *Saude, _, feed *Sonda) { feed.Falha(FeedFalhando, "falha de rede ou 5xx") }, MotivoFeedFalhando},
		{"reconciliador desligado", func(s *Saude, _, _ *Sonda) { s.ReconciliaAtivo = false }, MotivoReconciliadorDesligado},
		{"store fora", func(s *Saude, _, _ *Sonda) {
			s.Seq = func(context.Context) (int64, int64, error) { return 0, 0, errors.New("db fechado") }
		}, MotivoStoreIndisponivel},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			s, ref, feed := saudeSaudavel()
			c.mexer(s, ref, feed)
			r := s.Relatorio(context.Background())
			if r.Status != "degradado" || len(r.Motivos) != 1 || r.Motivos[0] != c.motivo {
				t.Fatalf("esperava degradado por %q: status=%s motivos=%v", c.motivo, r.Status, r.Motivos)
			}
		})
	}
}

func TestSaudeReferenciaRecuperaDepoisDeFalhar(t *testing.T) {
	s, ref, _ := saudeSaudavel()
	ref.Falha("falha", "HTTP 503 UNAVAILABLE")
	if r := s.Relatorio(context.Background()); r.Referencia.Carregada || r.Referencia.Classe != "HTTP 503 UNAVAILABLE" {
		t.Fatalf("falha deveria aparecer: %+v", r.Referencia)
	}
	ref.OK()
	if r := s.Relatorio(context.Background()); !r.Referencia.Carregada || r.Status != "ok" {
		t.Fatalf("voltou a carregar: %+v", r)
	}
}

func TestSaudeSemToolsAPINaoCobraReferenciaNemTaxonomia(t *testing.T) {
	s, _, _ := saudeSaudavel()
	s.Referencia = NovaSonda(nil)
	s.Taxonomia = func() StatusTaxonomia { return StatusTaxonomia{} } // TOOLS_API_TOKEN vazio
	r := s.Relatorio(context.Background())
	if r.Status != "ok" || r.Taxonomia.Habilitada || r.Taxonomia.Valida {
		t.Fatalf("API de tools desligada não degrada: %+v", r)
	}
}

func TestSaudeFeedAindaSemLeituraNaCarenciaNaoDegrada(t *testing.T) {
	s, _, _ := saudeSaudavel()
	s.Feed = NovaSonda(nil)
	s.Iniciado = s.Agora().Add(-time.Minute) // acabou de subir
	r := s.Relatorio(context.Background())
	if r.Status != "ok" || r.Reconciliador.Feed.Estado != FeedSemLeitura || r.Reconciliador.Feed.UltimoOK != nil {
		t.Fatalf("sem leitura ainda: %+v", r.Reconciliador.Feed)
	}
}

// Loop do reconciliador morto: ligado, no ar há horas e nunca leu o feed.
func TestSaudeFeedSemLeituraDepoisDaCarenciaDegrada(t *testing.T) {
	s, _, _ := saudeSaudavel()
	s.Feed = NovaSonda(nil)
	r := s.Relatorio(context.Background())
	if r.Status != "degradado" || len(r.Motivos) != 1 || r.Motivos[0] != MotivoFeedSemLeitura {
		t.Fatalf("esperava feed_sem_leitura: %+v", r)
	}
	s.ReconciliaAtivo = false // desligado é outro motivo, não este
	if r := s.Relatorio(context.Background()); r.Motivos[0] != MotivoReconciliadorDesligado || len(r.Motivos) != 1 {
		t.Fatalf("desligado: %+v", r.Motivos)
	}
}

// Taxonomia que só carregou depois do boot (carga sob demanda): ninguém viu as divergências.
func TestSaudeTaxonomiaInferidaNaoEValida(t *testing.T) {
	s, _, _ := saudeSaudavel()
	s.Taxonomia = func() StatusTaxonomia {
		return StatusTaxonomia{Habilitada: true, Carregada: true, Inferida: true}
	}
	r := s.Relatorio(context.Background())
	if r.Taxonomia.Valida || !r.Taxonomia.Inferida || r.Status != "degradado" || r.Motivos[0] != MotivoTaxonomiaNaoVerificada {
		t.Fatalf("inferida não é válida: %+v", r)
	}
}

// Relógio que não anda: a falha logo depois do OK tem o MESMO instante e ainda assim vale.
func TestSondaFalhaNoMesmoInstanteDoOKNaoContaComoCarregada(t *testing.T) {
	s := NovaSonda(func() time.Time { return t0 })
	s.OK()
	s.Falha("falha", "HTTP 503 UNAVAILABLE")
	if s.Carregada() {
		t.Fatal("a última chamada falhou")
	}
}

func TestSaudeStoreForaDeixaSeqNulo(t *testing.T) {
	s, _, _ := saudeSaudavel()
	s.Seq = func(context.Context) (int64, int64, error) { return 0, 0, errors.New("x") }
	r := s.Relatorio(context.Background())
	if r.Reconciliador.UltimoSeq != nil || r.Reconciliador.UltimoSeqWebhook != nil {
		t.Fatalf("seq deveria ser nulo: %+v", r.Reconciliador)
	}
}

func chaves(m map[string]any) string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return strings.Join(ks, " ")
}

// O JSON tem só as chaves documentadas em docs/plano/operacao.md: sem segredo,
// URL, nome, telefone — qualquer chave nova precisa passar por aqui (e pela doc).
func TestSaudeJSONTemSoAsChavesDocumentadas(t *testing.T) {
	s, ref, feed := saudeSaudavel()
	ref.Falha("falha", "HTTP 503 UNAVAILABLE")
	feed.Falha(FeedSemEscopo, "HTTP 403 FORBIDDEN")
	corpo, err := json.Marshal(s.Relatorio(context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(corpo, &m); err != nil {
		t.Fatal(err)
	}
	if got, quer := chaves(m), "motivos piloto quando reconciliador referencia status taxonomia uptime_s"; got != quer {
		t.Fatalf("chaves de topo = %q, quer %q", got, quer)
	}
	quer := map[string]string{
		"referencia":    "carregada ultima_leitura_ok ultimo_erro ultimo_erro_em",
		"taxonomia":     "carregada habilitada inferida placeholder problemas valida",
		"reconciliador": "ativo feed ultimo_seq ultimo_seq_webhook",
		"piloto":        "ativo modo numeros",
	}
	for k, v := range quer {
		if got := chaves(m[k].(map[string]any)); got != v {
			t.Errorf("%s: chaves = %q, quer %q", k, got, v)
		}
	}
	feedJSON := m["reconciliador"].(map[string]any)["feed"].(map[string]any)
	if got, v := chaves(feedJSON), "estado ultima_leitura_ok ultimo_erro ultimo_erro_em"; got != v {
		t.Errorf("feed: chaves = %q, quer %q", got, v)
	}
}

func TestSaudeHandler(t *testing.T) {
	s, _, feed := saudeSaudavel()
	feed.Falha(FeedFalhando, "falha de rede ou 5xx") // degradado continua 200
	h := s.Handler()

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("GET: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var r RespostaSaude
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil || r.Status != "degradado" {
		t.Fatalf("corpo: %v %s", err, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequestWithContext(context.Background(), http.MethodHead, "/health", nil))
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("HEAD: %d corpo=%q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/health", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: %d", rec.Code)
	}
}

func TestSondaNilEhSegura(t *testing.T) {
	var s *Sonda
	s.OK()
	s.Falha("x", "y")
	if s.Carregada() {
		t.Fatal("sonda nil não está carregada")
	}
}

// A lista de testadores (E3.9) aparece só como ligada/contagem; desligada por
// padrão, e ligá-la não degrada o status (é uma escolha, não uma falha).
func TestSaudePilotoLigadoEDesligado(t *testing.T) {
	s, _, _ := saudeSaudavel()
	r := s.Relatorio(context.Background())
	if r.Piloto.Ativo || r.Piloto.Numeros != 0 || r.Piloto.Modo != ModoAberto {
		t.Fatalf("desligado por padrão: %+v", r.Piloto)
	}
	s.Testadores = 3
	r = s.Relatorio(context.Background())
	if !r.Piloto.Ativo || r.Piloto.Numeros != 3 || r.Piloto.Modo != ModoPiloto || r.Status != "ok" {
		t.Fatalf("ligado com 3: %+v status=%s", r.Piloto, r.Status)
	}
}
