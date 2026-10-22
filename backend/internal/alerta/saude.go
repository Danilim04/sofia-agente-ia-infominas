package alerta

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// Estados do feed do reconciliador no /health.
const (
	FeedSemLeitura = "sem_leitura" // o reconciliador ainda não leu o feed neste processo
	FeedOK         = "ok"
	FeedSemEscopo  = "sem_escopo" // GET /events respondeu 403: a chave não tem events:read
	FeedFalhando   = "falhando"   // a última leitura falhou (rede, 5xx…)
)

// CarenciaFeed: depois deste tempo de processo no ar, o reconciliador ligado
// que ainda não leu o feed nenhuma vez (loop morto) deixa de ser "ok".
const CarenciaFeed = 5 * time.Minute

// Motivos de "degradado" no /health (vocabulário fechado; sem texto livre, sem PII).
const (
	MotivoReferenciaNaoCarregada = "referencia_nao_carregada"
	MotivoTaxonomiaNaoCarregada  = "taxonomia_nao_carregada"
	MotivoTaxonomiaInvalida      = "taxonomia_invalida"
	MotivoTaxonomiaNaoVerificada = "taxonomia_nao_verificada" // carregou depois do boot: divergências desconhecidas
	MotivoFeedSemLeitura         = "feed_sem_leitura"         // reconciliador ligado e nenhuma leitura do feed após a carência
	MotivoFeedSemEscopo          = "feed_sem_escopo"
	MotivoFeedFalhando           = "feed_falhando"
	MotivoReconciliadorDesligado = "reconciliador_desligado"
	MotivoStoreIndisponivel      = "store_indisponivel"
)

// Sonda guarda o resultado da ÚLTIMA chamada de um componente externo (leitura
// da referência do Infodesk, leitura do feed). Segura para uso concorrente.
type Sonda struct {
	mu         sync.Mutex
	agora      func() time.Time
	ultimoOK   time.Time
	ultimoErro time.Time
	ok         bool   // resultado da ÚLTIMA chamada (não depende de comparar instantes)
	classe     string // classe do último erro ("HTTP 403 FORBIDDEN", "falha de rede…"); nunca o corpo
	estado     string // estado resumido pelo chamador (FeedOK, FeedSemEscopo…)
}

// NovaSonda cria uma sonda; agora nil usa time.Now.
func NovaSonda(agora func() time.Time) *Sonda {
	if agora == nil {
		agora = time.Now
	}
	return &Sonda{agora: agora}
}

// OK registra uma chamada bem-sucedida.
func (s *Sonda) OK() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.ultimoOK, s.estado, s.ok = s.agora(), FeedOK, true
	s.mu.Unlock()
}

// Falha registra uma chamada com erro: estado e classe resumidos pelo chamador
// (sem corpo de resposta, sem PII).
func (s *Sonda) Falha(estado, classe string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.ultimoErro, s.estado, s.classe, s.ok = s.agora(), estado, classe, false
	s.mu.Unlock()
}

type fotoSonda struct {
	ultimoOK, ultimoErro time.Time
	classe, estado       string
	ok                   bool
}

func (s *Sonda) foto() fotoSonda {
	if s == nil {
		return fotoSonda{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return fotoSonda{s.ultimoOK, s.ultimoErro, s.classe, s.estado, s.ok}
}

// carregada: houve leitura bem-sucedida e nenhuma falha depois dela.
func (f fotoSonda) carregada() bool {
	return f.ok
}

// StatusTaxonomia é o que o /health sabe da taxonomia (foto do carregamento).
type StatusTaxonomia struct {
	Habilitada  bool // a API de tools (e com ela a taxonomia) está ligada
	Carregada   bool
	Placeholder bool // categorias/mesas ainda são hipótese de trabalho (P1)
	Problemas   int  // divergências com o Infodesk no último carregamento
	// Inferida: o boot não carregou e a referência foi lida depois; as
	// divergências dessa carga não são conhecidas, então a taxonomia não pode
	// ser declarada válida.
	Inferida bool
}

// Saude monta a resposta de GET /health do gateway. Tudo é injetado (relógio,
// sondas, foto da taxonomia, leitura dos cursores): o pacote não importa
// infodesk, store nem taxonomia. A resposta não leva segredo, URL, nome nem
// telefone — só flags, contagens, instantes e números de seq.
type Saude struct {
	Agora      func() time.Time
	Iniciado   time.Time
	Referencia *Sonda
	Feed       *Sonda
	// Taxonomia devolve a foto da taxonomia; nil = não informado.
	Taxonomia func() StatusTaxonomia
	// ReconciliaAtivo é RECONCILIA_ATIVO.
	ReconciliaAtivo bool
	// Seq lê o último seq processado do feed (cursor do reconciliador) e o do
	// webhook; erro = store indisponível.
	Seq func(ctx context.Context) (feed, webhook int64, err error)
}

type respostaReferencia struct {
	Carregada bool    `json:"carregada"`
	UltimoOK  *string `json:"ultima_leitura_ok"`
	UltimoErr *string `json:"ultimo_erro_em"`
	Classe    string  `json:"ultimo_erro,omitempty"`
}

type respostaTaxonomia struct {
	Habilitada  bool `json:"habilitada"`
	Carregada   bool `json:"carregada"`
	Valida      bool `json:"valida"`
	Placeholder bool `json:"placeholder"`
	Problemas   int  `json:"problemas"`
	// Inferida: "carregada" foi deduzida da leitura da referência após um boot
	// sem carga; "valida" fica false porque ninguém conferiu as divergências.
	Inferida bool `json:"inferida"`
}

type respostaFeed struct {
	Estado    string  `json:"estado"`
	UltimoOK  *string `json:"ultima_leitura_ok"`
	UltimoErr *string `json:"ultimo_erro_em"`
	Classe    string  `json:"ultimo_erro,omitempty"`
}

type respostaReconciliador struct {
	Ativo            bool         `json:"ativo"`
	UltimoSeq        *int64       `json:"ultimo_seq"`
	UltimoSeqWebhook *int64       `json:"ultimo_seq_webhook"`
	Feed             respostaFeed `json:"feed"`
}

// RespostaSaude é o corpo JSON do /health.
type RespostaSaude struct {
	// Status: "ok" ou "degradado" (veja Motivos). O HTTP é 200 nos dois casos:
	// o healthcheck do compose mede "o processo atende", e reiniciar o gateway
	// não conserta Infodesk fora do ar.
	Status        string                `json:"status"`
	Motivos       []string              `json:"motivos"`
	Quando        string                `json:"quando"`
	UptimeSeg     int64                 `json:"uptime_s"`
	Referencia    respostaReferencia    `json:"referencia"`
	Taxonomia     respostaTaxonomia     `json:"taxonomia"`
	Reconciliador respostaReconciliador `json:"reconciliador"`
}

func instante(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

// Relatorio calcula a resposta do /health.
func (s *Saude) Relatorio(ctx context.Context) RespostaSaude {
	agora := time.Now
	if s.Agora != nil {
		agora = s.Agora
	}
	r := RespostaSaude{Status: "ok", Motivos: []string{}, Quando: agora().UTC().Format(time.RFC3339)}
	if !s.Iniciado.IsZero() {
		r.UptimeSeg = int64(agora().Sub(s.Iniciado).Seconds())
	}
	degradar := func(motivo string) {
		r.Status = "degradado"
		r.Motivos = append(r.Motivos, motivo)
	}

	ref := s.Referencia.foto()
	r.Referencia = respostaReferencia{
		Carregada: ref.carregada(), UltimoOK: instante(ref.ultimoOK), UltimoErr: instante(ref.ultimoErro),
	}
	if !ref.ultimoErro.IsZero() {
		r.Referencia.Classe = ref.classe
	}

	var tx StatusTaxonomia
	if s.Taxonomia != nil {
		tx = s.Taxonomia()
	}
	r.Taxonomia = respostaTaxonomia{
		Habilitada: tx.Habilitada, Carregada: tx.Carregada, Placeholder: tx.Placeholder, Problemas: tx.Problemas,
		Valida:   tx.Habilitada && tx.Carregada && tx.Problemas == 0 && !tx.Inferida,
		Inferida: tx.Inferida,
	}
	// A taxonomia só entra na conta quando a API de tools está ligada: sem ela
	// não há abertura de ticket e a referência do Infodesk nem é lida.
	if tx.Habilitada {
		if !r.Referencia.Carregada {
			degradar(MotivoReferenciaNaoCarregada)
		}
		switch {
		case !tx.Carregada:
			degradar(MotivoTaxonomiaNaoCarregada)
		case tx.Problemas > 0:
			degradar(MotivoTaxonomiaInvalida)
		case tx.Inferida:
			degradar(MotivoTaxonomiaNaoVerificada)
		}
	}

	feed := s.Feed.foto()
	estado := feed.estado
	if estado == "" {
		estado = FeedSemLeitura
	}
	r.Reconciliador = respostaReconciliador{
		Ativo: s.ReconciliaAtivo,
		Feed:  respostaFeed{Estado: estado, UltimoOK: instante(feed.ultimoOK), UltimoErr: instante(feed.ultimoErro)},
	}
	if estado != FeedOK && estado != FeedSemLeitura {
		r.Reconciliador.Feed.Classe = feed.classe
	}
	switch {
	case !s.ReconciliaAtivo:
		degradar(MotivoReconciliadorDesligado)
	case estado == FeedSemEscopo:
		degradar(MotivoFeedSemEscopo)
	case estado == FeedFalhando:
		degradar(MotivoFeedFalhando)
	case estado == FeedSemLeitura && time.Duration(r.UptimeSeg)*time.Second > CarenciaFeed:
		degradar(MotivoFeedSemLeitura)
	}

	if s.Seq != nil {
		if f, w, err := s.Seq(ctx); err != nil {
			degradar(MotivoStoreIndisponivel)
		} else {
			r.Reconciliador.UltimoSeq, r.Reconciliador.UltimoSeqWebhook = &f, &w
		}
	}
	return r
}

// Handler serve GET /health (HEAD também).
func (s *Saude) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		if req.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(s.Relatorio(ctx))
		}
	}
}

// Carregada informa se a última chamada registrada deu certo (e houve alguma).
func (s *Sonda) Carregada() bool { return s.foto().carregada() }
