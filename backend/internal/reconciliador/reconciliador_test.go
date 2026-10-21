package reconciliador

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
	"bot-infominas/internal/webhook"
)

var atrasado = agora.Add(-30 * time.Minute) // fora de qualquer Atraso e dentro do Retroativo

// Evento já visto (e tratado) pelo webhook = no-op no feed; o cursor do
// reconciliador avança mesmo assim.
func TestEventoJaVistoPeloWebhookENoop(t *testing.T) {
	a := novoAmbiente(t, nil, opcoesDeTeste(), true, 16)
	ev := eventoMsg(t, 11, "conv-1", "ola", atrasado)
	a.idk.eventos = []infodesk.Evento{ev}

	if got := a.srv.Receber(context.Background(), ev, "entrega-webhook"); got != webhook.Enfileirado {
		t.Fatalf("webhook devia enfileirar, veio %v", got)
	}
	espera(t, "o webhook tratar o turno", func() bool { return len(a.eng.textos()) == 1 })

	a.rec.Executar(context.Background())

	if got := a.eng.textos(); len(got) != 1 {
		t.Fatalf("o feed não podia gerar um 2º turno: %v", got)
	}
	if got := cursor(t, a.st, store.NomeCursorInfodesk); got != 11 {
		t.Fatalf("cursor infodesk = %d, queria 11", got)
	}
}

// Evento que o webhook nunca recebeu vira turno pelo feed.
func TestEventoPerdidoPeloWebhookViraTurno(t *testing.T) {
	a := novoAmbiente(t, nil, opcoesDeTeste(), true, 16)
	a.idk.eventos = []infodesk.Evento{eventoMsg(t, 21, "conv-2", "perdido", atrasado)}

	a.rec.Executar(context.Background())

	espera(t, "o turno do evento perdido", func() bool { return len(a.eng.textos()) == 1 })
	if got := a.eng.textos()[0]; got != "perdido" {
		t.Fatalf("turno = %q", got)
	}
	if got := cursor(t, a.st, store.NomeCursorInfodesk); got != 21 {
		t.Fatalf("cursor infodesk = %d, queria 21", got)
	}
}

// Visto pelo webhook mas perdido num crash (kill -9 com o evento na fila em
// memória): sem Recuperar o dedup o esconde; com Recuperar o feed o reentrega.
func TestEventoVistoMasNaoProcessadoAntesDoCrashERecuperado(t *testing.T) {
	ctx := context.Background()
	ev := eventoMsg(t, 101, "conv-3", "crash", atrasado)

	// crashado devolve um store no estado em que um kill -9 o deixa: o webhook
	// marcou o evento como visto e o enfileirou em memória, sem os workers
	// terem rodado (o processo morreu).
	crashado := func(t *testing.T) store.Store {
		st := novoStore(t)
		if err := st.AvancarCursor(ctx, store.NomeCursorInfodesk, 100); err != nil {
			t.Fatal(err)
		}
		antes := novoAmbiente(t, st, opcoesDeTeste(), false, 16)
		if got := antes.srv.Receber(ctx, ev, "entrega"); got != webhook.Enfileirado {
			t.Fatalf("processo 1 devia enfileirar, veio %v", got)
		}
		return st
	}

	t.Run("sem recuperar o dedup esconde o evento", func(t *testing.T) {
		depois := novoAmbiente(t, crashado(t), opcoesDeTeste(), true, 16)
		depois.idk.eventos = []infodesk.Evento{ev}
		depois.rec.Executar(ctx)
		time.Sleep(50 * time.Millisecond)
		if got := depois.eng.textos(); len(got) != 0 {
			t.Fatalf("controle: o evento duplicado não devia virar turno: %v", got)
		}
	})

	t.Run("recuperar reentrega o evento", func(t *testing.T) {
		depois := novoAmbiente(t, crashado(t), opcoesDeTeste(), true, 16)
		depois.idk.eventos = []infodesk.Evento{ev}
		if err := depois.rec.Recuperar(ctx); err != nil {
			t.Fatal(err)
		}
		depois.rec.Executar(ctx)
		espera(t, "o turno recuperado", func() bool { return len(depois.eng.textos()) == 1 })
		if got := depois.eng.textos()[0]; got != "crash" {
			t.Fatalf("turno = %q", got)
		}
	})

	// Ponta a ponta: o processo 1 também roda o reconciliador (que enfileira o
	// evento do feed e morre antes do worker). O Atraso impede que o cursor
	// passe dele; o processo 2 recupera.
	t.Run("crash depois do ciclo do reconciliador", func(t *testing.T) {
		st := novoStore(t)
		recente := eventoMsg(t, 201, "conv-3b", "crash-feed", agora.Add(-20*time.Second))
		o := opcoesDeTeste()
		o.Atraso = 2 * time.Minute
		p1 := novoAmbiente(t, st, o, false, 16) // sem workers: morre com o evento na fila
		p1.idk.eventos = []infodesk.Evento{recente}
		p1.rec.Executar(ctx)
		if got := cursor(t, st, store.NomeCursorInfodesk); got >= 201 {
			t.Fatalf("o cursor (%d) não pode passar do evento dentro do Atraso", got)
		}

		p2 := novoAmbiente(t, st, o, true, 16)
		p2.idk.eventos = []infodesk.Evento{recente}
		if err := p2.rec.Recuperar(ctx); err != nil {
			t.Fatal(err)
		}
		p2.rec.Executar(ctx)
		espera(t, "o turno recuperado no processo 2", func() bool { return len(p2.eng.textos()) == 1 })
	})
}

// O cursor persistido anda Atraso atrás do relógio: o evento dentro da janela
// é entregue, mas o cursor não passa dele (é o que torna o crash recuperável).
func TestCursorNaoPassaDaJanelaDeAtraso(t *testing.T) {
	o := opcoesDeTeste()
	o.Atraso = 2 * time.Minute
	a := novoAmbiente(t, nil, o, true, 16)
	a.idk.eventos = []infodesk.Evento{
		eventoMsg(t, 31, "conv-4", "velho", agora.Add(-10*time.Minute)),
		eventoMsg(t, 32, "conv-4", "novo", agora.Add(-30*time.Second)),
	}
	a.rec.Executar(context.Background())

	espera(t, "os dois turnos", func() bool { return len(a.eng.textos()) == 2 })
	if got := cursor(t, a.st, store.NomeCursorInfodesk); got != 31 {
		t.Fatalf("cursor = %d, queria 31 (o evento 32 está dentro do Atraso)", got)
	}
}

// Falha numa página não avança o cursor além do que já foi entregue; o ciclo
// seguinte recomeça do cursor.
func TestCursorNaoAvancaEmFalha(t *testing.T) {
	o := opcoesDeTeste()
	o.Limite = 2
	a := novoAmbiente(t, nil, o, true, 16)
	for i, txt := range []string{"a", "b", "c", "d"} {
		a.idk.eventos = append(a.idk.eventos, eventoMsg(t, int64(41+i), "conv-5", txt, atrasado))
	}
	// 1ª página ok, 2ª falha.
	a.idk.errosFeed = []error{nil, &infodesk.HTTPError{Status: 500, Code: infodesk.CodeInternalError, Rota: "/events"}}

	a.rec.Executar(context.Background())
	espera(t, "a 1ª página", func() bool { return len(a.eng.textos()) == 2 })
	if got := cursor(t, a.st, store.NomeCursorInfodesk); got != 42 {
		t.Fatalf("cursor = %d, queria 42 (fim da página processada)", got)
	}

	a.rec.Executar(context.Background())
	espera(t, "a 2ª página no ciclo seguinte", func() bool { return len(a.eng.textos()) == 4 })
	if got := cursor(t, a.st, store.NomeCursorInfodesk); got != 44 {
		t.Fatalf("cursor = %d, queria 44", got)
	}
	if got, want := a.idk.chamadasAoFeed(), []int64{0, 42, 42}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after de cada chamada = %v, queria %v", got, want)
	}
}

// Fila do webhook cheia: o cursor para antes do evento recusado.
func TestFilaCheiaParaOCursor(t *testing.T) {
	a := novoAmbiente(t, nil, opcoesDeTeste(), false, 1) // sem workers: a fila de 1 enche
	a.idk.eventos = []infodesk.Evento{
		eventoMsg(t, 51, "conv-6", "um", atrasado),
		eventoMsg(t, 52, "conv-6", "dois", atrasado),
		eventoMsg(t, 53, "conv-6", "tres", atrasado),
	}
	a.rec.Executar(context.Background())
	if got := cursor(t, a.st, store.NomeCursorInfodesk); got != 51 {
		t.Fatalf("cursor = %d, queria 51 (o 52 foi recusado pela fila)", got)
	}
	// O recusado foi desmarcado pelo webhook: continua entregável.
	if fresh, _ := a.st.MarkEvent(context.Background(), "evt_dois", 52); !fresh {
		t.Fatal("o evento recusado devia ter sido desmarcado")
	}
}

// Pagina enquanto hasMore.
func TestPaginaEnquantoHasMore(t *testing.T) {
	o := opcoesDeTeste()
	o.Limite = 2
	a := novoAmbiente(t, nil, o, true, 16)
	for i, txt := range []string{"p1", "p2", "p3", "p4", "p5"} {
		a.idk.eventos = append(a.idk.eventos, eventoMsg(t, int64(61+i), "conv-7", txt, atrasado))
	}
	a.rec.Executar(context.Background())
	espera(t, "5 turnos", func() bool { return len(a.eng.textos()) == 5 })
	if got, want := a.idk.chamadasAoFeed(), []int64{0, 62, 64}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after de cada página = %v, queria %v", got, want)
	}
	if got := cursor(t, a.st, store.NomeCursorInfodesk); got != 65 {
		t.Fatalf("cursor = %d, queria 65", got)
	}
}

// 1ª execução: parte do cursor que o webhook gravou.
func TestPrimeiraExecucaoParteDoCursorDoWebhook(t *testing.T) {
	a := novoAmbiente(t, nil, opcoesDeTeste(), true, 16)
	if err := a.st.AvancarCursor(context.Background(), store.NomeCursorWebhook, 70); err != nil {
		t.Fatal(err)
	}
	a.idk.eventos = []infodesk.Evento{
		eventoMsg(t, 70, "conv-8", "ja-visto", atrasado.Add(-3*time.Hour)),
		eventoMsg(t, 71, "conv-8", "novo", atrasado),
	}
	a.rec.Executar(context.Background())
	espera(t, "o turno do seq 71", func() bool { return len(a.eng.textos()) == 1 })
	if got := a.idk.chamadasAoFeed()[0]; got != 70 {
		t.Fatalf("1º after = %d, queria 70 (cursor do webhook)", got)
	}
	if got := cursor(t, a.st, store.NomeCursorInfodesk); got != 71 {
		t.Fatalf("cursor infodesk = %d, queria 71", got)
	}
}

// 1ª execução sem nenhum cursor: descarta o que é mais velho que RECONCILIA_RETROATIVO.
func TestPrimeiraExecucaoSemCursorDescartaRetroativoAntigo(t *testing.T) {
	a := novoAmbiente(t, nil, opcoesDeTeste(), true, 16)
	a.idk.eventos = []infodesk.Evento{
		eventoMsg(t, 81, "conv-9", "antigo", agora.Add(-3*time.Hour)),
		eventoMsg(t, 82, "conv-9", "recente", atrasado),
	}
	a.rec.Executar(context.Background())
	espera(t, "o turno recente", func() bool { return len(a.eng.textos()) == 1 })
	time.Sleep(30 * time.Millisecond)
	if got := a.eng.textos(); len(got) != 1 || got[0] != "recente" {
		t.Fatalf("turnos = %v, queria só o recente", got)
	}
	if got := a.idk.chamadasAoFeed()[0]; got != 0 {
		t.Fatalf("sem cursor o feed é lido do começo, after = %d", got)
	}
	if got := cursor(t, a.st, store.NomeCursorInfodesk); got != 82 {
		t.Fatalf("cursor = %d, queria 82 (o antigo também foi consumido)", got)
	}
}

// A 1ª execução NÃO mexe no cursor do webhook (é só o ponto de partida).
func TestFeedNaoAvancaOCursorDoWebhook(t *testing.T) {
	a := novoAmbiente(t, nil, opcoesDeTeste(), false, 16)
	a.idk.eventos = []infodesk.Evento{eventoMsg(t, 91, "conv-10", "x", atrasado)}
	a.rec.Executar(context.Background())
	if got := cursor(t, a.st, store.NomeCursorWebhook); got != 0 {
		t.Fatalf("o reconciliador não avança infodesk_webhook, mas ele = %d", got)
	}
}

// ---------------------------------------------------------------------------
// 403 e RATE_LIMITED
// ---------------------------------------------------------------------------

func erroHTTP(status int, code string, retry time.Duration) error {
	return &infodesk.HTTPError{Status: status, Code: code, Rota: "/events", RetryAfter: retry}
}

// 403 no feed: um alerta de configuração, e o ciclo segue com (a) e (b).
func TestFeed403AlertaEMantemTicketsEVigia(t *testing.T) {
	a := novoAmbiente(t, nil, opcoesDeTeste(), true, 16)
	ctx := context.Background()
	a.idk.errosFeed = []error{erroHTTP(403, infodesk.CodeForbidden, 0), erroHTTP(403, infodesk.CodeForbidden, 0)}

	// (a) um ticket pendente que existe no Infodesk; (b) uma conversa parada.
	if _, _, err := a.st.InserirTicketPendente(ctx, &store.Ticket{Chave: "k1", ConversationID: "conv-t", Telefone: telefone}); err != nil {
		t.Fatal(err)
	}
	a.idk.tickets[telefone] = []infodesk.Ticket{{Number: 777, ConversationID: "conv-t"}}
	conversaDoBot(t, a.st, "conv-parada", agora.Add(-20*time.Minute), time.Time{})

	if espera := a.rec.Executar(ctx); espera != time.Minute {
		t.Fatalf("403 não é backoff: espera = %v", espera)
	}
	a.rec.Executar(ctx) // 2º 403: não repete o alerta

	if got := a.alertas.doTipo(TipoFeedSemEscopo); len(got) != 1 {
		t.Fatalf("alertas de escopo = %d, queria 1", len(got))
	}
	if tk, _ := a.st.ObterTicket(ctx, "k1"); tk == nil || tk.Status != store.TicketAberto || tk.Numero != 777 {
		t.Fatalf("(a) devia seguir rodando: %+v", tk)
	}
	if got := a.eng.transferidas(); len(got) != 1 || got[0] != "conv-parada" {
		t.Fatalf("(b) devia seguir rodando: %v", got)
	}
}

// RATE_LIMITED: backoff exponencial com teto, Retry-After respeitado, e o
// ciclo para (não gasta mais requisições).
func TestRateLimitedFazBackoff(t *testing.T) {
	o := opcoesDeTeste()
	o.BackoffMax = 5 * time.Minute
	a := novoAmbiente(t, nil, o, false, 16)
	ctx := context.Background()
	limite := erroHTTP(429, infodesk.CodeRateLimited, 0)
	a.idk.errosFeed = []error{limite, limite, limite, limite, erroHTTP(429, infodesk.CodeRateLimited, 4*time.Minute+30*time.Second)}
	conversaDoBot(t, a.st, "conv-parada", agora.Add(-20*time.Minute), time.Time{})

	var esperas []time.Duration
	for i := 0; i < 5; i++ {
		esperas = append(esperas, a.rec.Executar(ctx))
	}
	want := []time.Duration{2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute, 5 * time.Minute}
	if !reflect.DeepEqual(esperas, want) {
		t.Fatalf("esperas = %v, queria %v", esperas, want)
	}
	if got := a.eng.transferidas(); len(got) != 0 {
		t.Fatalf("com RATE_LIMITED o ciclo para antes do vigia: %v", got)
	}
	// Respondeu: zera o backoff e o ciclo completo roda.
	if espera := a.rec.Executar(ctx); espera != time.Minute {
		t.Fatalf("após sucesso a espera volta ao intervalo, veio %v", espera)
	}
	if got := a.eng.transferidas(); len(got) != 1 {
		t.Fatalf("o vigia devia rodar no ciclo bem-sucedido: %v", got)
	}
}

func TestRetryAfterMaiorQueOBackoffMandaMasRespeitaOTeto(t *testing.T) {
	o := opcoesDeTeste()
	o.BackoffMax = 3 * time.Minute
	a := novoAmbiente(t, nil, o, false, 16)
	a.idk.errosFeed = []error{erroHTTP(429, infodesk.CodeRateLimited, 2*time.Minute+30*time.Second), erroHTTP(429, infodesk.CodeRateLimited, 10*time.Minute)}
	if got := a.rec.Executar(context.Background()); got != 2*time.Minute+30*time.Second {
		t.Fatalf("Retry-After de 2m30 devia valer, veio %v", got)
	}
	if got := a.rec.Executar(context.Background()); got != 3*time.Minute {
		t.Fatalf("o teto do backoff vale, veio %v", got)
	}
}

// Feed falhando por várias execuções seguidas: alerta, uma vez.
func TestFeedFalhandoAlertaUmaVez(t *testing.T) {
	a := novoAmbiente(t, nil, opcoesDeTeste(), false, 16)
	for i := 0; i < 8; i++ {
		a.idk.errosFeed = append(a.idk.errosFeed, errors.New("conexão recusada"))
	}
	for i := 0; i < 8; i++ {
		a.rec.Executar(context.Background())
	}
	if got := a.alertas.doTipo(TipoFeedFalhando); len(got) != 1 {
		t.Fatalf("alertas de feed falhando = %d, queria 1", len(got))
	}
}

// ---------------------------------------------------------------------------
// (a) tickets pendentes
// ---------------------------------------------------------------------------

func pendente(t *testing.T, st store.Store, chave, conv string) {
	t.Helper()
	if _, _, err := st.InserirTicketPendente(context.Background(), &store.Ticket{Chave: chave, ConversationID: conv, Telefone: telefone}); err != nil {
		t.Fatal(err)
	}
}

// O ticket é criado com o relógio real do store; o do reconciliador anda.
func relogioQueAnda(a *ambiente, depois time.Duration) {
	a.rec.opt.Now = func() time.Time { return time.Now().Add(depois) }
}

func TestTicketPendenteReconciliadoViraAberto(t *testing.T) {
	a := novoAmbiente(t, nil, opcoesDeTeste(), false, 16)
	pendente(t, a.st, "k-ok", "conv-t1")
	a.idk.tickets[telefone] = []infodesk.Ticket{{Number: 5, ConversationID: "outra"}, {Number: 4321, ConversationID: "conv-t1"}}
	relogioQueAnda(a, 0)

	a.rec.Executar(context.Background())

	tk, _ := a.st.ObterTicket(context.Background(), "k-ok")
	if tk == nil || tk.Status != store.TicketAberto || tk.Numero != 4321 || !tk.Reconciliado {
		t.Fatalf("ticket = %+v", tk)
	}
}

func TestTicketPendenteAntesDoTTLContinuaPendente(t *testing.T) {
	a := novoAmbiente(t, nil, opcoesDeTeste(), false, 16)
	pendente(t, a.st, "k-cedo", "conv-t2")
	relogioQueAnda(a, 9*time.Minute)

	a.rec.Executar(context.Background())

	if tk, _ := a.st.ObterTicket(context.Background(), "k-cedo"); tk.Status != store.TicketPendente {
		t.Fatalf("antes do TTL continua pendente: %+v", tk)
	}
	if got := a.alertas.doTipo(TipoTicketPendenteFalh); len(got) != 0 {
		t.Fatalf("sem alerta antes do TTL: %v", got)
	}
}

func TestTicketPendenteApósOTTLViraFalhaComAlerta(t *testing.T) {
	a := novoAmbiente(t, nil, opcoesDeTeste(), false, 16)
	pendente(t, a.st, "k-falha", "conv-t3")
	relogioQueAnda(a, 11*time.Minute)

	a.rec.Executar(context.Background())
	a.rec.Executar(context.Background())

	if tk, _ := a.st.ObterTicket(context.Background(), "k-falha"); tk.Status != store.TicketFalha {
		t.Fatalf("depois do TTL e sem aparecer vira falha: %+v", tk)
	}
	if got := a.alertas.doTipo(TipoTicketPendenteFalh); len(got) != 1 || got[0].ConversationID != "conv-t3" {
		t.Fatalf("queria 1 alerta da conversa conv-t3, veio %+v", got)
	}
}

// Prazo vencido mas a listagem falhou: não dá para afirmar que o chamado não
// existe — fica pendente, com alerta próprio.
func TestTicketPendenteComListagemFalhandoNaoViraFalha(t *testing.T) {
	a := novoAmbiente(t, nil, opcoesDeTeste(), false, 16)
	pendente(t, a.st, "k-sem-lista", "conv-t4")
	a.idk.errTickets = &infodesk.HTTPError{Status: 503, Rota: "/tickets"}
	relogioQueAnda(a, 11*time.Minute)

	a.rec.Executar(context.Background())

	if tk, _ := a.st.ObterTicket(context.Background(), "k-sem-lista"); tk.Status != store.TicketPendente {
		t.Fatalf("sem conseguir olhar não pode virar falha: %+v", tk)
	}
	if got := a.alertas.doTipo(TipoTicketNaoChecado); len(got) != 1 {
		t.Fatalf("queria o alerta de não checado, veio %v", got)
	}
}

// ---------------------------------------------------------------------------
// (b) vigia BOT_TIMEOUT_HANDOFF
// ---------------------------------------------------------------------------

func TestVigiaDisparaUmaVezPorEpisodio(t *testing.T) {
	a := novoAmbiente(t, nil, opcoesDeTeste(), false, 16)
	conversaDoBot(t, a.st, "conv-v1", agora.Add(-6*time.Minute), time.Time{})                       // sem resposta há 6 min
	conversaDoBot(t, a.st, "conv-recente", agora.Add(-2*time.Minute), time.Time{})                  // ainda dentro do prazo
	conversaDoBot(t, a.st, "conv-respondida", agora.Add(-9*time.Minute), agora.Add(-8*time.Minute)) // já respondida

	a.rec.Executar(context.Background())
	a.rec.Executar(context.Background())
	a.rec.Executar(context.Background())

	if got := a.eng.transferidas(); !reflect.DeepEqual(got, []string{"conv-v1"}) {
		t.Fatalf("transferidas = %v, queria só conv-v1, uma vez", got)
	}
	al := a.alertas.doTipo(TipoBotSemResposta)
	if len(al) != 1 || al[0].ConversationID != "conv-v1" {
		t.Fatalf("alertas do vigia = %+v", al)
	}
	if c, _ := a.st.ObterConversa(context.Background(), "conv-v1"); c.Estado != store.EstadoTransferida {
		t.Fatalf("estado = %s", c.Estado)
	}
}

// Se o Infodesk diz que um humano já conduz, o vigia não faz handoff.
func TestVigiaNaoTransfereConversaQueOHumanoAssumiu(t *testing.T) {
	a := novoAmbiente(t, nil, opcoesDeTeste(), false, 16)
	conversaDoBot(t, a.st, "conv-v2", agora.Add(-20*time.Minute), time.Time{})
	a.idk.conversas["conv-v2"] = infodesk.Conversa{ID: "conv-v2", HandledBy: infodesk.ConduzidaPorAtendente}

	a.rec.Executar(context.Background())

	if got := a.eng.transferidas(); len(got) != 0 {
		t.Fatalf("não podia transferir: %v", got)
	}
}

// O timeout vem da configuração (BOT_TIMEOUT_HANDOFF).
func TestVigiaRespeitaOTimeoutConfigurado(t *testing.T) {
	o := opcoesDeTeste()
	o.BotTimeoutHandoff = 30 * time.Minute
	a := novoAmbiente(t, nil, o, false, 16)
	conversaDoBot(t, a.st, "conv-v3", agora.Add(-20*time.Minute), time.Time{})
	a.rec.Executar(context.Background())
	if got := a.eng.transferidas(); len(got) != 0 {
		t.Fatalf("20 min < 30 min: %v", got)
	}
}

// ---------------------------------------------------------------------------
// Run e configuração
// ---------------------------------------------------------------------------

func TestRunDesligadoNaoFazNada(t *testing.T) {
	o := opcoesDeTeste()
	o.Ativo = false
	a := novoAmbiente(t, nil, o, false, 16)
	a.idk.eventos = []infodesk.Evento{eventoMsg(t, 1, "c", "x", atrasado)}
	a.rec.Run(context.Background())
	if n := len(a.idk.chamadasAoFeed()); n != 0 {
		t.Fatalf("desligado não lê o feed: %d chamadas", n)
	}
}

func TestRunRepeteACadaIntervaloAteCancelar(t *testing.T) {
	o := opcoesDeTeste()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var esperas []time.Duration
	o.Dormir = func(_ context.Context, d time.Duration) bool {
		esperas = append(esperas, d)
		if len(esperas) == 3 {
			cancel()
			return false
		}
		return true
	}
	a := novoAmbiente(t, nil, o, false, 16)
	a.rec.Run(ctx)
	if len(esperas) != 3 || esperas[0] != time.Minute {
		t.Fatalf("esperas = %v", esperas)
	}
	if n := len(a.idk.chamadasAoFeed()); n != 3 {
		t.Fatalf("o feed devia ser lido a cada ciclo: %d", n)
	}
}

func TestOptionsDoAmbiente(t *testing.T) {
	t.Setenv("RECONCILIA_ATIVO", "")
	o := OptionsDoAmbiente(nil)
	if !o.Ativo {
		t.Fatal("nasce ligado")
	}
	o = o.comPadroes()
	if o.Intervalo != time.Minute || o.Retroativo != time.Hour || o.TicketPendenteTTL != 10*time.Minute || o.BotTimeoutHandoff != 5*time.Minute {
		t.Fatalf("defaults: %+v", o)
	}

	t.Setenv("RECONCILIA_ATIVO", "false")
	t.Setenv("RECONCILIA_INTERVALO", "30s")
	t.Setenv("BOT_TIMEOUT_HANDOFF", "invalido")
	o = OptionsDoAmbiente(nil)
	if o.Ativo || o.Intervalo != 30*time.Second || o.BotTimeoutHandoff != 0 {
		t.Fatalf("env: %+v", o)
	}
	if o.comPadroes().BotTimeoutHandoff != 5*time.Minute {
		t.Fatal("valor inválido cai no default")
	}
}
