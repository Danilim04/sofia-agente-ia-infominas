package main

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"bot-infominas/internal/alerta"
	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
)

// Ligações do GET /health (E4.3). Os pacotes taxonomia e reconciliador não
// expõem seu estado, então o gateway observa de fora: decora o cliente do
// Infodesk que cada um recebe e anota o resultado da última chamada.

// classeDoErro descreve o erro sem expor corpo de resposta (sem PII).
func classeDoErro(err error) string {
	var he *infodesk.HTTPError
	if errors.As(err, &he) {
		return fmt.Sprintf("HTTP %d %s", he.Status, he.Code)
	}
	if infodesk.Transitorio(err) {
		return "falha de rede ou 5xx"
	}
	return "erro inesperado"
}

func anotarReferencia(s *alerta.Sonda, err error) {
	if err != nil {
		s.Falha("falha", classeDoErro(err))
		return
	}
	s.OK()
}

// fonteSondada é a Fonte da taxonomia (mesas, catálogo, departamentos) com a
// sonda da referência: "cache de referência carregado" = a última leitura deu certo.
type fonteSondada struct {
	*infodesk.Client
	sonda *alerta.Sonda
}

func (f fonteSondada) Mesas(ctx context.Context) ([]infodesk.Referencia, error) {
	v, err := f.Client.Mesas(ctx)
	anotarReferencia(f.sonda, err)
	return v, err
}

func (f fonteSondada) Mesa(ctx context.Context, id string) (infodesk.Mesa, error) {
	v, err := f.Client.Mesa(ctx, id)
	anotarReferencia(f.sonda, err)
	return v, err
}

func (f fonteSondada) Catalogo(ctx context.Context, deskID string) ([]infodesk.Referencia, error) {
	v, err := f.Client.Catalogo(ctx, deskID)
	anotarReferencia(f.sonda, err)
	return v, err
}

func (f fonteSondada) Departamentos(ctx context.Context) ([]infodesk.Referencia, error) {
	v, err := f.Client.Departamentos(ctx)
	anotarReferencia(f.sonda, err)
	return v, err
}

// infodeskDoFeed é o Infodesk do reconciliador com a sonda do feed: só o
// GET /events conta (tickets pendentes e vigia têm alertas próprios).
type infodeskDoFeed struct {
	*infodesk.Client
	sonda *alerta.Sonda
}

func (i infodeskDoFeed) Eventos(ctx context.Context, after int64, limit int) (infodesk.FeedEventos, error) {
	v, err := i.Client.Eventos(ctx, after, limit)
	switch {
	case err == nil:
		i.sonda.OK()
	case semEscopo(err):
		i.sonda.Falha(alerta.FeedSemEscopo, classeDoErro(err))
	default:
		i.sonda.Falha(alerta.FeedFalhando, classeDoErro(err))
	}
	return v, err
}

func semEscopo(err error) bool {
	var he *infodesk.HTTPError
	return errors.As(err, &he) && (he.Code == infodesk.CodeForbidden || he.Status == 403)
}

// fotoTaxonomia guarda o que o boot soube da taxonomia (o pacote não tem
// leitura viva do estado): carregada ou não, quantas divergências, placeholder.
type fotoTaxonomia struct {
	mu sync.RWMutex
	v  alerta.StatusTaxonomia
}

func (f *fotoTaxonomia) definir(v alerta.StatusTaxonomia) {
	f.mu.Lock()
	f.v = v
	f.mu.Unlock()
}

// status devolve a foto. Se o boot não conseguiu carregar mas a referência foi
// lida com sucesso depois (carga sob demanda do Resolver), a taxonomia conta
// como carregada; as divergências dessa carga só aparecem no alerta
// `taxonomia_invalida`.
func (f *fotoTaxonomia) status(referenciaLida bool) alerta.StatusTaxonomia {
	f.mu.RLock()
	v := f.v
	f.mu.RUnlock()
	if v.Habilitada && !v.Carregada && referenciaLida {
		v.Carregada = true
	}
	return v
}

// novaSaude monta o /health do gateway.
func novaSaude(st store.Store, sondaRef, sondaFeed *alerta.Sonda, tax *fotoTaxonomia, reconciliaAtivo bool) *alerta.Saude {
	return &alerta.Saude{
		Referencia:      sondaRef,
		Feed:            sondaFeed,
		ReconciliaAtivo: reconciliaAtivo,
		Taxonomia:       func() alerta.StatusTaxonomia { return tax.status(sondaRef.Carregada()) },
		Seq: func(ctx context.Context) (int64, int64, error) {
			feed, err := st.Cursor(ctx, store.NomeCursorInfodesk)
			if err != nil {
				return 0, 0, err
			}
			webhook, err := st.Cursor(ctx, store.NomeCursorWebhook)
			return feed, webhook, err
		},
	}
}
