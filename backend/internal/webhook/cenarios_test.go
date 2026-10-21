package webhook

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"bot-infominas/internal/alerta"
	"bot-infominas/internal/brain"
	"bot-infominas/internal/config"
	"bot-infominas/internal/engine"
	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
)

// Cenários da E2.1 de ponta a ponta: webhook assinado → Server → engine REAL →
// fakeInfodesk/fakeBrain. A engine, o store (SQLite) e a vigia são os de
// produção; só a rede (Infodesk, cérebro, alerta) é falsa.

// Rajada de mensagens do cliente vira UM turno, com os textos unidos.
func TestRajadaViraUmTurno(t *testing.T) {
	a := novoAmbiente(t, comRajadaLarga)
	a.enviar(a.recebida("c-10", "m-1", "minha internet caiu", t0))
	a.enviar(a.recebida("c-10", "m-2", "desde ontem", t0.Add(time.Second)))
	a.enviar(a.recebida("c-10", "m-3", "e o roteador está vermelho", t0.Add(2*time.Second)))

	esperar(t, "resposta da Sofia", func() bool { return len(a.idk.mensagens("c-10")) >= 1 })
	silencio()
	if a.br.total() != 1 {
		t.Fatalf("a rajada devia virar UM turno, veio %d chamadas ao cérebro", a.br.total())
	}
	req := a.br.ultimo()
	if req.Mensagem != "minha internet caiu\ndesde ontem\ne o roteador está vermelho" {
		t.Fatalf("textos da rajada: %q", req.Mensagem)
	}
	if req.ConversationID != "c-10" || req.Canal != "whatsapp" || req.Telefone != telefoneTeste ||
		req.NomeContato != "Maria" || req.EstadoConversa != "bot" {
		t.Fatalf("Contrato A: %+v", req)
	}
	if got := a.idk.mensagens("c-10"); len(got) != 1 || got[0] != "Oi! Como posso ajudar?" {
		t.Fatalf("resposta ao cliente: %q", got)
	}
	if c := a.conversa("c-10"); c == nil || c.Estado != store.EstadoBot || c.HandledBy != "EXTERNAL_BOT" ||
		c.UltimaMsgID != "m-3" || c.UltimaRespostaEm.IsZero() || c.Telefone != telefoneTeste {
		t.Fatalf("conversa gravada: %+v", c)
	}
	if seq, _ := a.st.Cursor(context.Background(), store.NomeCursorWebhook); seq != a.seq {
		t.Fatalf("eventos_cursor devia estar no maior seq (%d), veio %d", a.seq, seq)
	}
}

// Mídia chega em DUAS mensagens (rótulo "[imagem]" em TEXT + o arquivo): vira
// UM handoff, com UM aviso ao cliente, sem passar pelo cérebro.
func TestMidiaEmDuasMensagensViraUmHandoff(t *testing.T) {
	// As duas mensagens PRECISAM cair no mesmo lote: se a janela fechasse entre
	// elas, o rótulo TEXT iria sozinho ao cérebro.
	a := novoAmbiente(t, comRajadaLarga)
	a.enviar(a.recebidaDe("c-20", "m-1", "[imagem]", "EXTERNAL_BOT", "TEXT", false, t0))
	a.enviar(a.recebidaDe("c-20", "m-2", "", "EXTERNAL_BOT", "IMAGE", true, t0.Add(time.Second)))

	esperar(t, "handoff", func() bool { return len(a.idk.handoffsDe("c-20")) >= 1 })
	silencio()
	hs := a.idk.handoffsDe("c-20")
	if len(hs) != 1 {
		t.Fatalf("duas mensagens de mídia = UM handoff, veio %d", len(hs))
	}
	if hs[0].dept != deptHumanoTeste {
		t.Fatalf("handoff devia ir para DEPARTAMENTO_HUMANO_PADRAO: %+v", hs[0])
	}
	if !strings.Contains(hs[0].note, "áudio, imagem ou arquivo") || !strings.Contains(hs[0].note, "Sofia → humano") {
		t.Fatalf("nota gerada por código esperada: %q", hs[0].note)
	}
	if got := a.idk.mensagens("c-20"); len(got) != 1 || !strings.Contains(got[0], "não consigo ver áudio") {
		t.Fatalf("esperava UM aviso de mídia antes do handoff: %q", got)
	}
	if a.br.total() != 0 {
		t.Fatalf("mídia não vai ao cérebro: %d chamadas", a.br.total())
	}
	if c := a.conversa("c-20"); c.Estado != store.EstadoTransferida {
		t.Fatalf("conversa devia estar transferida: %+v", c)
	}

	// A legenda que chega depois (outra mensagem de texto) encontra a conversa muda.
	a.enviar(a.recebida("c-20", "m-3", "é o comprovante", t0.Add(2*time.Second)))
	silencio()
	if len(a.idk.handoffsDe("c-20")) != 1 || a.br.total() != 0 || len(a.idk.mensagens("c-20")) != 1 {
		t.Fatalf("depois da transferência a Sofia fica muda: handoffs=%d cérebro=%d msgs=%q",
			len(a.idk.handoffsDe("c-20")), a.br.total(), a.idk.mensagens("c-20"))
	}
}

// O rótulo "[imagem]" sozinho é TEXTO e não dispara a transferência.
func TestRotuloDeMidiaSozinhoNaoTransfere(t *testing.T) {
	a := novoAmbiente(t)
	a.enviar(a.recebidaDe("c-21", "m-1", "[imagem]", "EXTERNAL_BOT", "TEXT", false, t0))
	esperar(t, "cérebro", func() bool { return a.br.total() >= 1 })
	if len(a.idk.handoffsDe("c-21")) != 0 {
		t.Fatal("o rótulo em texto não é mídia")
	}
}

// Conversa transferida fica muda: nada mais vai ao cérebro nem ao cliente
// até conversation.finished.
func TestConversaTransferidaFicaMuda(t *testing.T) {
	a := novoAmbiente(t)
	a.enviar(a.recebida("c-30", "m-1", "quero falar com um atendente", t0))
	esperar(t, "handoff", func() bool { return len(a.idk.handoffsDe("c-30")) == 1 })
	msgs := len(a.idk.mensagens("c-30"))

	// O Infodesk confirma o handoff (QUEUE) e o cliente segue escrevendo
	// (o envelope ainda pode trazer EXTERNAL_BOT em reentrega/atraso).
	a.enviar(a.deConversa("conversation.handoff", "c-30", "QUEUE", t0.Add(time.Second)))
	a.enviar(a.recebida("c-30", "m-2", "alô?", t0.Add(2*time.Second)))
	a.enviar(a.recebida("c-30", "m-3", "tem alguém aí?", t0.Add(3*time.Second)))
	silencio()
	if a.br.total() != 0 || len(a.idk.mensagens("c-30")) != msgs || len(a.idk.handoffsDe("c-30")) != 1 {
		t.Fatalf("conversa transferida devia ficar muda: cérebro=%d msgs=%q handoffs=%d",
			a.br.total(), a.idk.mensagens("c-30"), len(a.idk.handoffsDe("c-30")))
	}
	if c := a.conversa("c-30"); c.Estado != store.EstadoTransferida {
		t.Fatalf("estado: %+v", c)
	}
	if hs := a.idk.handoffsDe("c-30"); !strings.Contains(hs[0].note, "pedido do cliente") {
		t.Fatalf("nota do handoff: %q", hs[0].note)
	}
}

// Atendente assume (conversation.assigned) SEM handoff nosso: o bot cala,
// nenhum handoff é feito e a métrica humano_assumiu_sem_handoff conta.
func TestAtendenteAssumeBotCalaSemHandoff(t *testing.T) {
	a := novoAmbiente(t)
	a.enviar(a.recebida("c-40", "m-1", "oi", t0))
	esperar(t, "1ª resposta", func() bool { return len(a.idk.mensagens("c-40")) == 1 })

	a.enviar(a.deConversa("conversation.assigned", "c-40", "AGENT", t0.Add(time.Minute)))
	if c := a.conversa("c-40"); c.Estado != store.EstadoTransferida || c.HandledBy != "AGENT" {
		t.Fatalf("assigned devia transferir: %+v", c)
	}
	if n := a.eng.Vigia().Contagem(alerta.MetricaHumanoAssumiuSemHandoff); n != 1 {
		t.Fatalf("métrica humano_assumiu_sem_handoff = %d, esperava 1", n)
	}

	// O cliente responde ao atendente: o envelope traz handledBy AGENT.
	a.enviar(a.recebidaDe("c-40", "m-2", "obrigado, pode ser agora", "AGENT", "TEXT", false, t0.Add(2*time.Minute)))
	silencio()
	if a.br.total() != 1 || len(a.idk.mensagens("c-40")) != 1 {
		t.Fatalf("a Sofia não fala por cima do atendente: cérebro=%d msgs=%q", a.br.total(), a.idk.mensagens("c-40"))
	}
	if len(a.idk.handoffsDe("c-40")) != 0 {
		t.Fatal("humano assumiu sozinho: o gateway NÃO faz handoff")
	}
	if n := a.eng.Vigia().Contagem(alerta.MetricaHumanoAssumiuSemHandoff); n != 1 {
		t.Fatalf("a métrica conta uma vez por conversa assumida, veio %d", n)
	}
}

// handledBy != EXTERNAL_BOT em QUALQUER evento (aqui message.sent de atendente)
// também marca transferida.
func TestHandledByDeAtendenteEmQualquerEventoTransfere(t *testing.T) {
	a := novoAmbiente(t)
	a.enviar(a.recebida("c-41", "m-1", "oi", t0))
	esperar(t, "resposta", func() bool { return len(a.idk.mensagens("c-41")) == 1 })
	id, seq := a.proximo()
	a.enviar(evt(id, seq, "message.sent", t0.Add(time.Minute), map[string]any{
		"conversation": convJSON("c-41", "AGENT", t0.Add(-time.Hour)),
		"message":      msgJSON("m-9", "c-41", "AGENT", "TEXT", "Bom dia, aqui é o João", t0.Add(time.Minute), false),
	}))
	if c := a.conversa("c-41"); c.Estado != store.EstadoTransferida {
		t.Fatalf("message.sent de atendente devia transferir: %+v", c)
	}
}

// Evento AGENT reentregue DEPOIS de finished não reabre nem rebaixa (W9); uma
// mensagem nova do cliente, essa sim, abre outro episódio.
func TestEventoAgentReentregueDepoisDeFinishedNaoReabre(t *testing.T) {
	a := novoAmbiente(t)
	a.enviar(a.recebida("c-50", "m-1", "oi", t0))
	esperar(t, "resposta", func() bool { return len(a.idk.mensagens("c-50")) == 1 })

	assigned := a.deConversa("conversation.assigned", "c-50", "AGENT", t0.Add(time.Minute))
	a.enviar(assigned)
	a.enviar(a.deConversa("conversation.finished", "c-50", "AGENT", t0.Add(2*time.Minute)))
	if c := a.conversa("c-50"); c.Estado != store.EstadoEncerrada {
		t.Fatalf("finished devia encerrar: %+v", c)
	}

	// 1) a reentrega do MESMO evento morre no dedup por id;
	if rec := a.entregar(assigned, "reentrega-6h"); rec.Code != http.StatusOK {
		t.Fatalf("reentrega: %d", rec.Code)
	}
	a.drenar()
	// 2) o mesmo fato com id novo (dedup de 7 dias expirado, ou vindo do feed)
	//    é mais velho que o estado local: não rebaixa.
	id, seq := a.proximo()
	a.enviar(evt(id, seq, "conversation.assigned", t0.Add(time.Minute), map[string]any{
		"conversation": convJSON("c-50", "AGENT", t0.Add(-time.Hour)),
	}))
	if c := a.conversa("c-50"); c.Estado != store.EstadoEncerrada {
		t.Fatalf("evento AGENT reentregue depois de finished NÃO reabre: %+v", c)
	}
	// A message.received antiga (EXTERNAL_BOT, anterior ao finished) também não reabre o bot.
	a.enviar(a.recebida("c-50", "m-velha", "ainda estou aqui?", t0.Add(30*time.Second)))
	silencio()
	if a.br.total() != 1 {
		t.Fatalf("mensagem anterior ao finished não gera turno: %d chamadas", a.br.total())
	}

	// Cliente volta depois: novo episódio, a Sofia responde.
	a.enviar(a.recebida("c-50", "m-nova", "oi de novo", t0.Add(3*time.Minute)))
	esperar(t, "novo episódio", func() bool { return a.br.total() == 2 })
	if c := a.conversa("c-50"); c.Estado != store.EstadoBot {
		t.Fatalf("mensagem nova depois do finished abre outro episódio: %+v", c)
	}
}

// finished limpa o episódio: estado encerrada, classificação e chamado
// zerados, cérebro avisado para limpar a proposta; a próxima conversa faz
// lookup de novo.
func TestFinishedLimpaOEpisodio(t *testing.T) {
	a := novoAmbiente(t)
	a.br.resposta = func(brain.ChatRequest) (*brain.ChatResponse, error) {
		return &brain.ChatResponse{
			Reply:         "Entendi, vou abrir um chamado.",
			Classificacao: map[string]any{"categoria": "sem_conexao", "prioridade": "alta", "confianca": 0.91, "origem": "jev"},
		}, nil
	}
	a.enviar(a.recebida("c-60", "m-1", "internet caiu", t0))
	esperar(t, "resposta", func() bool { return len(a.idk.mensagens("c-60")) == 1 })
	c := a.conversa("c-60")
	if !strings.Contains(c.ClassificacaoJSON, "sem_conexao") || c.UltimaRespostaEm.IsZero() {
		t.Fatalf("classificação e ultima_resposta_em deviam estar gravadas: %+v", c)
	}

	a.enviar(a.deConversa("conversation.finished", "c-60", "EXTERNAL_BOT", t0.Add(time.Minute)))
	c = a.conversa("c-60")
	if c.Estado != store.EstadoEncerrada || c.ClassificacaoJSON != "" || !c.UltimaRespostaEm.IsZero() || c.TicketNumber != 0 {
		t.Fatalf("finished devia limpar o episódio: %+v", c)
	}
	if got := a.br.fechados(); len(got) != 1 || got[0] != "c-60" {
		t.Fatalf("cérebro devia limpar a proposta (encerrar-episodio): %v", got)
	}
}

// Falha definitiva do cérebro: aviso neutro ao cliente, handoff com nota,
// conversa transferida, ocorrência registrada e alerta ao responsável.
func TestFalhaDefinitivaDoCerebroFazHandoffEAlerta(t *testing.T) {
	a := novoAmbiente(t)
	a.br.resposta = func(brain.ChatRequest) (*brain.ChatResponse, error) {
		return nil, &brain.Erro{Classe: brain.ClasseDefinitiva, IDOcorrencia: "SOF-20261021-1200-07",
			Causa: "saldo do OpenRouter insuficiente (402)", Status: 500}
	}
	a.enviar(a.recebida("c-70", "m-1", "como vejo minha fatura?", t0))
	esperar(t, "handoff", func() bool { return len(a.idk.handoffsDe("c-70")) == 1 })

	hs := a.idk.handoffsDe("c-70")
	if hs[0].dept != deptHumanoTeste || !strings.Contains(hs[0].note, "falha ao processar") || !strings.Contains(hs[0].note, "SOF-20261021-1200-07") {
		t.Fatalf("handoff/nota: %+v", hs[0])
	}
	if got := a.idk.mensagens("c-70"); len(got) != 1 || strings.Contains(got[0], "402") || strings.Contains(got[0], "SOF-") {
		t.Fatalf("o cliente recebe só o aviso neutro, nunca o erro técnico: %q", got)
	}
	if al := a.alertas.doTipo(alerta.TipoFalhaDefinitiva); len(al) != 1 || al[0].IDOcorrencia != "SOF-20261021-1200-07" {
		t.Fatalf("falha definitiva devia alertar o responsável: %+v", a.alertas.alertas)
	}
	if c := a.conversa("c-70"); c.Estado != store.EstadoTransferida {
		t.Fatalf("estado: %+v", c)
	}
	if f, _ := a.st.UltimaFalha(context.Background(), "c-70"); f == nil || f.ID != "SOF-20261021-1200-07" {
		t.Fatalf("ocorrência não registrada: %+v", f)
	}
}

// Handoff recusado pelo Infodesk: a Sofia cala mesmo assim e o responsável é alertado.
func TestHandoffFalhoAlertaEBotCala(t *testing.T) {
	a := novoAmbiente(t)
	a.idk.handoffErr = errors.New("infodesk fora do ar")
	a.enviar(a.recebida("c-71", "m-1", "quero um atendente", t0))
	esperar(t, "alerta", func() bool { return len(a.alertas.doTipo(alerta.TipoHandoffFalhou)) == 1 })
	if c := a.conversa("c-71"); c.Estado != store.EstadoTransferida {
		t.Fatalf("o bot cala mesmo com o handoff falho: %+v", c)
	}
	a.enviar(a.recebida("c-71", "m-2", "alô", t0.Add(time.Second)))
	silencio()
	if a.br.total() != 0 {
		t.Fatal("conversa muda depois do handoff falho")
	}
}

// `acoes: transferir` do cérebro passa pelo caminho único: a resposta do
// cérebro é o aviso (não duplica) e o handoff vai com nota por código.
func TestAcaoTransferirDoCerebroFazHandoff(t *testing.T) {
	a := novoAmbiente(t)
	a.br.resposta = func(brain.ChatRequest) (*brain.ChatResponse, error) {
		return &brain.ChatResponse{
			Reply: "Poxa, imagino o transtorno.\n---\nVou te passar para um atendente agora.",
			Acoes: []map[string]any{{"tipo": "transferir", "origem": "frustracao", "motivo": "frustração 3/3 \n com confiança 0.82"}},
			Classificacao: map[string]any{
				"categoria": "sem_conexao", "prioridade": "alta", "confianca": 0.91, "origem": "jev",
			},
		}, nil
	}
	a.enviar(a.recebida("c-80", "m-1", "ESTOU FURIOSO, ninguém resolve!", t0))
	esperar(t, "handoff", func() bool { return len(a.idk.handoffsDe("c-80")) == 1 })

	if got := a.idk.mensagens("c-80"); len(got) != 2 {
		t.Fatalf("só as bolhas do cérebro, sem aviso duplicado: %q", got)
	}
	nota := a.idk.handoffsDe("c-80")[0].note
	for _, quer := range []string{"frustração do cliente", "categoria: sem_conexao (jev 0.91)", "sem chamado"} {
		if !strings.Contains(nota, quer) {
			t.Errorf("nota sem %q: %q", quer, nota)
		}
	}
	if strings.Contains(nota, "furioso") || strings.Contains(nota, "3/3") {
		t.Fatalf("texto do cliente/LLM não entra na nota: %q", nota)
	}
	if f, _ := a.st.UltimaFalha(context.Background(), "c-80"); f == nil || f.Origem != engine.OrigemFrustracao {
		t.Fatalf("ocorrência com a origem da ação: %+v", f)
	}
}

// ---------------------------------------------------------------------------
// Identificação: lookup no primeiro turno do episódio (ADR 0004, revisão)
// ---------------------------------------------------------------------------

func solicitante(id, nome string) infodesk.Solicitante {
	return infodesk.Solicitante{ID: id, Name: nome, ClientID: "k1", Phone: telefoneTeste}
}

// Lookup com exatamente 1 solicitante dispensa o CPF: o cérebro recebe
// contato{forma: "telefone"} e `contatos` guarda origem=lookup.
func TestLookupComUmSolicitanteDispensaCPF(t *testing.T) {
	a := novoAmbiente(t)
	a.idk.lookups[telefoneTeste] = infodesk.Lookup{Requesters: []infodesk.Solicitante{solicitante("r1", "Maria Souza")}, ClientIDs: []string{"k1"}}
	a.enviar(a.recebida("c-90", "m-1", "minha impressora parou", t0))
	esperar(t, "cérebro", func() bool { return a.br.total() == 1 })

	ct := a.br.ultimo().Contato
	if ct == nil || ct.Forma != "telefone" || ct.ValorMascarado != "Maria S." ||
		ct.ClienteID == nil || *ct.ClienteID != "k1" || ct.NomeCadastro == nil || *ct.NomeCadastro != "Maria Souza" {
		t.Fatalf("contato do Contrato A: %+v", ct)
	}
	c, _ := a.st.ObterContato(context.Background(), telefoneTeste, a.now())
	if c == nil || c.Origem != store.OrigemLookup || c.RequesterID != "r1" || c.ClienteID != "k1" || c.Forma != store.FormaTelefone {
		t.Fatalf("contatos: %+v", c)
	}
	// Segundo turno do episódio: não repete o lookup, mantém o contato.
	esperar(t, "resposta", func() bool { return len(a.idk.mensagens("c-90")) == 1 })
	a.enviar(a.recebida("c-90", "m-2", "continua parada", t0.Add(time.Minute)))
	esperar(t, "2º turno", func() bool { return a.br.total() == 2 })
	if a.idk.lookupsFeitos() != 1 {
		t.Fatalf("lookup só no primeiro turno do episódio, veio %d", a.idk.lookupsFeitos())
	}
	if ct := a.br.ultimo().Contato; ct == nil || ct.Forma != "telefone" {
		t.Fatalf("o contato persiste no episódio: %+v", ct)
	}
}

// Lookup vazio: o telefone é desconhecido, contato nulo e o cérebro pede CPF.
func TestLookupVazioPedeCPF(t *testing.T) {
	a := novoAmbiente(t)
	a.enviar(a.recebida("c-91", "m-1", "oi", t0))
	esperar(t, "cérebro", func() bool { return a.br.total() == 1 })
	if a.br.ultimo().Contato != nil {
		t.Fatalf("lookup vazio = contato nulo: %+v", a.br.ultimo().Contato)
	}
	if c, _ := a.st.ObterContato(context.Background(), telefoneTeste, a.now()); c != nil {
		t.Fatalf("nada a guardar: %+v", c)
	}
}

// Mais de um solicitante (IDENT_LOOKUP_AMBIGUO=pedir_cpf): não escolhe um.
func TestLookupAmbiguoPedeCPF(t *testing.T) {
	a := novoAmbiente(t)
	a.idk.lookups[telefoneTeste] = infodesk.Lookup{Requesters: []infodesk.Solicitante{solicitante("r1", "Maria Souza"), solicitante("r2", "José Lima")}}
	a.enviar(a.recebida("c-92", "m-1", "oi", t0))
	esperar(t, "cérebro", func() bool { return a.br.total() == 1 })
	if a.br.ultimo().Contato != nil {
		t.Fatalf("ambíguo = contato nulo: %+v", a.br.ultimo().Contato)
	}
}

// IDENT_EXIGIR_CPF=true: o lookup só guarda requester_id/cliente_id; o
// cérebro continua pedindo o CPF (contato nulo).
func TestExigirCPFMantemContatoNuloMasGuardaRequester(t *testing.T) {
	a := novoAmbiente(t, func(c *config.Config) { c.IdentExigirCPF = true })
	a.idk.lookups[telefoneTeste] = infodesk.Lookup{Requesters: []infodesk.Solicitante{solicitante("r1", "Maria Souza")}}
	a.enviar(a.recebida("c-93", "m-1", "oi", t0))
	esperar(t, "cérebro", func() bool { return a.br.total() == 1 })
	if a.br.ultimo().Contato != nil {
		t.Fatalf("com IDENT_EXIGIR_CPF o telefone sozinho não identifica: %+v", a.br.ultimo().Contato)
	}
	if c, _ := a.st.ObterContato(context.Background(), telefoneTeste, a.now()); c == nil || c.RequesterID != "r1" || c.Forma != "" {
		t.Fatalf("requester_id guardado para a posse/vínculo: %+v", c)
	}
}

// Lookup com campo ilegível (Lookup.Erro != nil) NÃO é "telefone desconhecido"
// em silêncio: pede CPF (seguro) mas loga e alerta.
func TestLookupIlegivelAlerta(t *testing.T) {
	a := novoAmbiente(t)
	a.idk.lookups[telefoneTeste] = infodesk.Lookup{Erro: errors.New("requesters: tipo inesperado")}
	a.enviar(a.recebida("c-94", "m-1", "oi", t0))
	esperar(t, "cérebro", func() bool { return a.br.total() == 1 })
	if a.br.ultimo().Contato != nil {
		t.Fatalf("lookup ilegível = não identificado: %+v", a.br.ultimo().Contato)
	}
	if al := a.alertas.doTipo(alerta.TipoLookupIlegivel); len(al) != 1 || al[0].ConversationID != "c-94" {
		t.Fatalf("esperava alerta lookup_ilegivel: %+v", a.alertas.alertas)
	}
}

// ---------------------------------------------------------------------------
// Data.Erro, filtro de turno, ticket.*, eco
// ---------------------------------------------------------------------------

// Data.Erro != nil (objeto presente mas ilegível) loga e ALERTA em vez de
// virar "evento sem conversa" em silêncio.
func TestDataErroAlerta(t *testing.T) {
	a := novoAmbiente(t)
	conv := convJSON("c-100", "EXTERNAL_BOT", t0)
	conv["botActive"] = "talvez" // tipo trocado: Conversation fica nil e Data.Erro != nil
	id, seq := a.proximo()
	a.enviar(evt(id, seq, "message.received", t0, map[string]any{
		"conversation": conv,
		"message":      msgJSON("m-1", "c-100", "CLIENT", "TEXT", "oi", t0, false),
	}))
	al := a.alertas.doTipo(alerta.TipoEventoIlegivel)
	if len(al) != 1 || !strings.Contains(al[0].Texto, id) {
		t.Fatalf("esperava alerta evento_ilegivel citando o evento: %+v", a.alertas.alertas)
	}
	silencio()
	if a.br.total() != 0 {
		t.Fatal("evento ilegível não vira turno")
	}
	if seq2, _ := a.st.Cursor(context.Background(), store.NomeCursorWebhook); seq2 != seq {
		t.Fatalf("o cursor avança mesmo assim (evento já dedupado): %d", seq2)
	}
}

// Só message.received de CLIENT, em WhatsApp individual, com o bot externo
// conduzindo, chega ao cérebro (07 N5).
func TestFiltroDeTurno(t *testing.T) {
	a := novoAmbiente(t)
	// grupo de WhatsApp
	grupo := convJSON("c-110", "EXTERNAL_BOT", t0)
	grupo["contact"] = map[string]any{"name": "Grupo", "phone": "", "groupJid": "120363@g.us"}
	id, seq := a.proximo()
	a.enviar(evt(id, seq, "message.received", t0, map[string]any{
		"conversation": grupo, "message": msgJSON("m-g", "c-110", "CLIENT", "TEXT", "oi grupo", t0, false)}))
	// canal CHAT (agente desktop)
	chat := convJSON("c-111", "EXTERNAL_BOT", t0)
	chat["channel"] = "CHAT"
	id, seq = a.proximo()
	a.enviar(evt(id, seq, "message.received", t0, map[string]any{
		"conversation": chat, "message": msgJSON("m-c", "c-111", "CLIENT", "TEXT", "oi chat", t0, false)}))
	// remetente que não é o cliente (SYSTEM)
	id, seq = a.proximo()
	a.enviar(evt(id, seq, "message.received", t0, map[string]any{
		"conversation": convJSON("c-112", "EXTERNAL_BOT", t0),
		"message":      msgJSON("m-s", "c-112", "SYSTEM", "TEXT", "mensagem do sistema", t0, false)}))
	// handledBy QUEUE (humano na fila)
	a.enviar(a.recebidaDe("c-113", "m-q", "oi fila", "QUEUE", "TEXT", false, t0))
	silencio()
	if a.br.total() != 0 {
		t.Fatalf("nenhuma dessas mensagens é turno do cliente para o bot: %d chamadas", a.br.total())
	}
	// O que é turno chega.
	a.enviar(a.recebida("c-114", "m-ok", "oi", t0))
	esperar(t, "turno válido", func() bool { return a.br.total() == 1 })
}

// message.sent com o nome da NOSSA chave é eco e é ignorado (não toca o estado).
func TestEcoDaPropriaChaveIgnorado(t *testing.T) {
	a := novoAmbiente(t)
	id, seq := a.proximo()
	a.enviar(evt(id, seq, "message.sent", t0, map[string]any{
		"conversation": convJSON("c-120", "AGENT", t0),
		"message": func() map[string]any {
			m := msgJSON("m-e", "c-120", "AGENT", "TEXT", "Olá, sou a Sofia", t0, false)
			m["senderName"] = nomeChaveTeste
			return m
		}(),
	}))
	if c := a.conversa("c-120"); c != nil {
		t.Fatalf("eco da própria chave não cria nem altera conversa: %+v", c)
	}
}

// ticket.* atualiza status_infodesk/encerrado do ticket que NÓS abrimos; o
// reentregue mais velho não reabre.
func TestTicketEventosAtualizamStatusLocal(t *testing.T) {
	a := novoAmbiente(t)
	ctx := context.Background()
	if _, _, err := a.st.InserirTicketPendente(ctx, &store.Ticket{Chave: "k-1", ConversationID: "c-130", Telefone: telefoneTeste}); err != nil {
		t.Fatal(err)
	}
	if err := a.st.ConcluirTicket(ctx, "k-1", 4521, "{}", false); err != nil {
		t.Fatal(err)
	}
	ticket := func(fechado bool, nome string) map[string]any {
		return map[string]any{"ticket": map[string]any{
			"number": 4521, "isClosed": fechado, "conversationId": "c-130",
			"status": map[string]any{"id": "s1", "name": nome, "isOpen": !fechado, "isClose": fechado, "isCanceled": false},
		}}
	}
	// os eventos de ticket são datados APÓS a criação local (updated_at = agora real)
	depois := time.Now().Add(time.Hour)
	id, seq := a.proximo()
	a.enviar(evt(id, seq, "ticket.closed", depois, ticket(true, "Resolvido")))
	tk, _ := a.st.ObterTicket(ctx, "k-1")
	if !tk.Encerrado || tk.StatusInfodesk != "Resolvido" {
		t.Fatalf("ticket.closed: %+v", tk)
	}
	id, seq = a.proximo()
	a.enviar(evt(id, seq, "ticket.updated", depois.Add(-time.Minute), ticket(false, "Em andamento")))
	if tk, _ = a.st.ObterTicket(ctx, "k-1"); !tk.Encerrado {
		t.Fatalf("evento mais velho não reabre: %+v", tk)
	}
	id, seq = a.proximo()
	a.enviar(evt(id, seq, "ticket.reopened", depois.Add(time.Minute), ticket(false, "Em andamento")))
	if tk, _ = a.st.ObterTicket(ctx, "k-1"); tk.Encerrado || tk.StatusInfodesk != "Em andamento" {
		t.Fatalf("ticket.reopened: %+v", tk)
	}
}

// conversation.started só registra; a conversa nasce com o bot.
func TestConversaIniciadaSoRegistra(t *testing.T) {
	a := novoAmbiente(t)
	a.enviar(a.deConversa("conversation.started", "c-140", "EXTERNAL_BOT", t0))
	if c := a.conversa("c-140"); c == nil || c.Estado != store.EstadoBot || c.HandledBy != "EXTERNAL_BOT" {
		t.Fatalf("conversa: %+v", c)
	}
	if a.br.total() != 0 || len(a.idk.mensagens("c-140")) != 0 {
		t.Fatal("conversation.started não gera resposta")
	}
}

// Grupo e canal que não é WhatsApp não criam estado local nem registram
// ultima_msg_* (o vigia N1 da E3.3 leria isso).
func TestGrupoECanalForaDoBotNaoCriamConversa(t *testing.T) {
	a := novoAmbiente(t)
	grupo := convJSON("c-150", "EXTERNAL_BOT", t0)
	grupo["contact"] = map[string]any{"name": "Grupo", "phone": "", "groupJid": "120363@g.us"}
	id, seq := a.proximo()
	a.enviar(evt(id, seq, "message.received", t0, map[string]any{
		"conversation": grupo, "message": msgJSON("m-g", "c-150", "CLIENT", "TEXT", "oi", t0, false)}))
	chat := convJSON("c-151", "EXTERNAL_BOT", t0)
	chat["channel"] = "CHAT"
	id, seq = a.proximo()
	a.enviar(evt(id, seq, "message.received", t0, map[string]any{
		"conversation": chat, "message": msgJSON("m-c", "c-151", "CLIENT", "TEXT", "oi", t0, false)}))
	if a.conversa("c-150") != nil || a.conversa("c-151") != nil {
		t.Fatal("fora do escopo do bot não grava conversa")
	}
	// SYSTEM no WhatsApp: atualiza handled_by mas não conta como mensagem do cliente.
	id, seq = a.proximo()
	a.enviar(evt(id, seq, "message.received", t0, map[string]any{
		"conversation": convJSON("c-152", "EXTERNAL_BOT", t0),
		"message":      msgJSON("m-s", "c-152", "SYSTEM", "TEXT", "aviso", t0, false)}))
	if c := a.conversa("c-152"); c == nil || c.UltimaMsgID != "" {
		t.Fatalf("mensagem que não é turno não registra ultima_msg: %+v", c)
	}
}

// O webhook NÃO mexe no cursor do reconciliador ("infodesk"): só no seu.
func TestWebhookNaoAvancaOCursorDoReconciliador(t *testing.T) {
	a := novoAmbiente(t)
	a.enviar(a.deConversa("conversation.started", "c-160", "EXTERNAL_BOT", t0))
	if seq, _ := a.st.Cursor(context.Background(), store.NomeCursorInfodesk); seq != 0 {
		t.Fatalf("cursor do reconciliador não pode ser tocado pelo webhook: %d", seq)
	}
	if seq, _ := a.st.Cursor(context.Background(), store.NomeCursorWebhook); seq != a.seq {
		t.Fatalf("cursor do webhook = %d, esperava %d", seq, a.seq)
	}
}

// No desligamento os eventos ainda na fila em memória são desmarcados: já
// receberam 200, mas o dedup de 7 dias os esconderia da reconciliação.
func TestDesligamentoDevolveEventosPendentes(t *testing.T) {
	a := novoAmbiente(t)
	corpo := a.recebida("c-170", "m-1", "oi", t0)
	if rec := a.entregar(corpo, "d"); rec.Code != http.StatusOK {
		t.Fatalf("entrega: %d", rec.Code)
	}
	a.srv.devolverPendentes()
	if len(a.srv.jobs) != 0 {
		t.Fatal("fila devia esvaziar")
	}
	if rec := a.entregar(corpo, "d-reentrega"); rec.Code != http.StatusOK || len(a.srv.jobs) != 1 {
		t.Fatalf("o evento devolvido precisa ser aceito de novo: %d fila=%d", rec.Code, len(a.srv.jobs))
	}
}
