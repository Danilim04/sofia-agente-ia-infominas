package engine

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bot-infominas/internal/config"
	"bot-infominas/internal/store"
)

func lugar(t *testing.T, nome string) *time.Location {
	t.Helper()
	l, err := time.LoadLocation(nome)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// engineComHorario monta a engine com um horário humano. O relógio é o falso da
// engine, posicionado por `defineAgora` (em São Paulo). 2026-10-23 é sexta-feira.
func engineComHorario(t *testing.T, texto, modo string, feriados ...string) (*Engine, *fakeInfodesk, *relogioFalso) {
	t.Helper()
	st, err := store.NewSQLite(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.Config{DepartamentoHumanoPadrao: deptHumano, ForaDoHorarioModo: modo}
	h, err := config.ParseHorario(texto, lugar(t, "America/Sao_Paulo"), feriados)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Horario = h
	if h != nil {
		cfg.HorarioDescricao = h.Descrever()
	}
	tr := novoFakeInfodesk()
	e := New(cfg, tr, &fakeBrain{}, st, nil)
	return e, tr, comRelogioFalso(e)
}

func (r *relogioFalso) em(t *testing.T, dia string, hora, min int) {
	t.Helper()
	d, err := time.ParseInLocation("2006-01-02", dia, lugar(t, "America/Sao_Paulo"))
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.agora = d.Add(time.Duration(hora)*time.Hour + time.Duration(min)*time.Minute)
	r.mu.Unlock()
}

const expediente = "seg-sex 08:00-18:00"

func transferirPedido(e *Engine, conv string) {
	e.processarLote(context.Background(), []Inbound{msg(conv, "quero falar com um atendente")})
}

// Dentro do horário: o aviso é o de sempre.
func TestHorarioDentroMantemOAvisoNormal(t *testing.T) {
	e, tr, rel := engineComHorario(t, expediente, config.ForaDoHorarioAvisar)
	rel.em(t, "2026-10-23", 10, 0)
	transferirPedido(e, "c-h1")
	if got := tr.mensagens("c-h1"); len(got) != 1 || got[0] != MsgPedidoAtendente {
		t.Fatalf("aviso normal: %v", got)
	}
	if n := tr.notas("c-h1"); len(n) != 1 || strings.Contains(n[0], notaForaDoHorario) {
		t.Fatalf("a nota não tem a marca de fora do horário: %v", n)
	}
}

// Fora do horário: o handoff acontece igual (a conversa cai na fila), o aviso é o
// de fora do horário, com o horário CONFIGURADO, e a nota ganha a marca.
func TestHorarioForaTrocaOAvisoEMarcaANota(t *testing.T) {
	e, tr, rel := engineComHorario(t, expediente, config.ForaDoHorarioAvisar)
	rel.em(t, "2026-10-23", 20, 0) // sexta 20:00
	transferirPedido(e, "c-h2")
	msgs := tr.mensagens("c-h2")
	if len(msgs) != 1 {
		t.Fatalf("um aviso: %v", msgs)
	}
	for _, quer := range []string{"de segunda a sexta, das 08:00 às 18:00", "fica registrada", "não está disponível"} {
		if !strings.Contains(msgs[0], quer) {
			t.Errorf("aviso sem %q: %q", quer, msgs[0])
		}
	}
	if got := tr.transferida("c-h2"); len(got) != 1 || got[0] != deptHumano {
		t.Fatalf("o handoff continua fora do horário: %v", got)
	}
	if n := tr.notas("c-h2"); len(n) != 1 || !strings.Contains(n[0], "pedido do cliente") || !strings.Contains(n[0], "fora do horário") {
		t.Fatalf("nota com motivo e marca: %v", n)
	}
}

// O aviso é da persona: pt-BR, "você", ≤ 1 emoji, sem prazo nem hora inventada.
func TestMsgForaDoHorarioNaPersonaSemPrazo(t *testing.T) {
	for _, c := range []struct {
		horario  string
		feriados bool
	}{
		{"de segunda a sexta, das 08:00 às 18:00", false},
		{"de segunda a sexta, das 08:00 às 18:00; aos sábados, das 08:00 às 12:00", true},
		{"", false},
	} {
		m := msgForaDoHorarioBase(c.horario, c.feriados)
		if strings.Count(m, "🙏") > 1 || !strings.Contains(m, "Sua conversa") || !strings.Contains(m, "você") {
			t.Errorf("persona: %q", m)
		}
		for _, proibido := range []string{"em breve", "logo", "minuto", "amanhã", "em até", "dentro de", "segunda-feira às", "hoje"} {
			if strings.Contains(strings.ToLower(m), proibido) {
				t.Errorf("a Sofia não promete prazo (%q): %q", proibido, m)
			}
		}
		if c.horario == "" && strings.ContainsAny(m, "0123456789") {
			t.Errorf("sem horário configurado não há hora no texto: %q", m)
		}
		if c.feriados && !strings.Contains(m, "exceto feriados") {
			t.Errorf("com feriados o aviso diz: %q", m)
		}
	}
}

func TestHorarioSabadoFeriadoEViradaDeDia(t *testing.T) {
	casos := []struct {
		nome  string
		texto string
		fer   []string
		dia   string
		h, m  int
		fora  bool
	}{
		{"sábado fora do expediente", expediente, nil, "2026-10-24", 10, 0, true},
		{"sábado dentro (faixa de sábado)", expediente + "; sab 08:00-12:00", nil, "2026-10-24", 10, 0, false},
		{"sábado 12:00 fora (fim exclusivo)", expediente + "; sab 08:00-12:00", nil, "2026-10-24", 12, 0, true},
		{"feriado em dia útil", expediente, []string{"2026-10-23"}, "2026-10-23", 10, 0, true},
		{"véspera do feriado dentro", expediente, []string{"2026-10-23"}, "2026-10-22", 10, 0, false},
		{"virada de dia, 23:30", "seg-sex 22:00-06:00", nil, "2026-10-22", 23, 30, false},
		{"virada de dia, 05:00 do dia seguinte", "seg-sex 22:00-06:00", nil, "2026-10-23", 5, 0, false},
		{"virada de dia, 07:00 fora", "seg-sex 22:00-06:00", nil, "2026-10-23", 7, 0, true},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			e, tr, rel := engineComHorario(t, c.texto, config.ForaDoHorarioAvisar, c.fer...)
			rel.em(t, c.dia, c.h, c.m)
			transferirPedido(e, "c-hx")
			msgs := tr.mensagens("c-hx")
			if len(msgs) != 1 {
				t.Fatalf("msgs: %v", msgs)
			}
			if fora := msgs[0] != MsgPedidoAtendente; fora != c.fora {
				t.Fatalf("fora=%v, queria %v: %q", fora, c.fora, msgs[0])
			}
		})
	}
}

// O fuso do horário decide: o mesmo instante é dentro em São Paulo e fora em Tóquio.
func TestHorarioFusoNaEngine(t *testing.T) {
	st, err := store.NewSQLite(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	toquio, err := config.ParseHorario(expediente, lugar(t, "Asia/Tokyo"), nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{DepartamentoHumanoPadrao: deptHumano, Horario: toquio, HorarioDescricao: toquio.Descrever()}
	tr := novoFakeInfodesk()
	e := New(cfg, tr, &fakeBrain{}, st, nil)
	rel := comRelogioFalso(e)
	rel.em(t, "2026-10-23", 10, 0) // 10:00 em São Paulo = 22:00 em Tóquio
	transferirPedido(e, "c-hf")
	if got := tr.mensagens("c-hf"); len(got) != 1 || got[0] == MsgPedidoAtendente {
		t.Fatalf("22:00 em Tóquio é fora do horário: %v", got)
	}
}

// FORA_DO_HORARIO_MODO=ignorar mantém o aviso normal, mas a nota e o log continuam
// dizendo que foi fora do horário.
func TestHorarioModoIgnorar(t *testing.T) {
	e, tr, rel := engineComHorario(t, expediente, config.ForaDoHorarioIgnorar)
	rel.em(t, "2026-10-24", 10, 0)
	var buf strings.Builder
	e.log = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	transferirPedido(e, "c-hi")
	if got := tr.mensagens("c-hi"); len(got) != 1 || got[0] != MsgPedidoAtendente {
		t.Fatalf("modo ignorar mantém o aviso normal: %v", got)
	}
	if len(tr.transferida("c-hi")) != 1 {
		t.Fatal("o handoff continua")
	}
	if n := tr.notas("c-hi"); len(n) != 1 || !strings.Contains(n[0], "fora do horário") {
		t.Fatalf("a nota segue com a marca: %v", n)
	}
	if !strings.Contains(buf.String(), `"fora_do_horario":true`) {
		t.Fatalf("o log transferencia segue com fora_do_horario=true:\n%s", buf.String())
	}
}

// Todas as origens de transferência continuam fazendo o handoff fora do horário.
func TestHorarioForaTodasAsOrigensFazemHandoff(t *testing.T) {
	origens := []string{OrigemCliente, OrigemAnexo, OrigemFrustracao, OrigemConfianca, OrigemCerebro, OrigemDisjuntor, OrigemAcao, OrigemTicketAberto, OrigemTicketPendente, OrigemVigia}
	for _, o := range origens {
		t.Run(o, func(t *testing.T) {
			e, tr, rel := engineComHorario(t, expediente, config.ForaDoHorarioAvisar)
			rel.em(t, "2026-10-23", 23, 0)
			conv := "c-ho-" + o
			e.TransferirParaHumano(context.Background(), conv, Transferencia{Origem: o, Mensagem: "texto normal da origem"})
			if got := tr.transferida(conv); len(got) != 1 {
				t.Fatalf("handoff: %v", got)
			}
			msgs := tr.mensagens(conv)
			if len(msgs) != 1 || msgs[0] == "texto normal da origem" || !strings.Contains(msgs[0], "fica registrada") {
				t.Fatalf("aviso de fora do horário: %v", msgs)
			}
			if n := tr.notas(conv); len(n) != 1 || !strings.Contains(n[0], "fora do horário") {
				t.Fatalf("nota: %v", n)
			}
		})
	}
}

// O aviso fora do horário não perde o que o normal tinha de próprio: o número do
// chamado aberto, a limitação de anexo e o chamado não confirmado.
func TestHorarioForaPreservaOQueAMensagemNormalDiz(t *testing.T) {
	e, tr, rel := engineComHorario(t, expediente, config.ForaDoHorarioAvisar)
	rel.em(t, "2026-10-23", 21, 0)
	ctx := context.Background()
	_, _ = e.st.AtualizarConversa(ctx, "c-hp-t", func(c *store.Conversa, _ bool) bool {
		c.Estado, c.TicketNumber = store.EstadoBot, 4521
		return true
	})
	e.TransferirParaHumano(ctx, "c-hp-t", Transferencia{Origem: OrigemTicketAberto, Mensagem: MsgChamadoAberto(4521)})
	if m := tr.mensagens("c-hp-t"); len(m) != 1 || !strings.Contains(m[0], "chamado nº 4521") || !strings.Contains(m[0], "fica registrada") {
		t.Fatalf("o número do chamado segue no aviso: %v", m)
	}
	if strings.Count(tr.mensagens("c-hp-t")[0], "🙏") > 1 {
		t.Fatal("no máximo 1 emoji")
	}

	e.TransferirParaHumano(ctx, "c-hp-a", Transferencia{Origem: OrigemAnexo, Mensagem: MsgMidia})
	if m := tr.mensagens("c-hp-a"); len(m) != 1 || !strings.Contains(m[0], "não consigo ver áudio, imagem ou arquivo") || !strings.Contains(m[0], "fica registrada") {
		t.Fatalf("anexo: %v", m)
	}
	e.TransferirParaHumano(ctx, "c-hp-p", Transferencia{Origem: OrigemTicketPendente, Mensagem: "Não consegui registrar o seu chamado agora. Vou te encaminhar para um de nossos atendentes, que segue com você por aqui. 🙏"})
	if m := tr.mensagens("c-hp-p"); len(m) != 1 || !strings.Contains(m[0], "Não consegui registrar o seu chamado agora.") && !strings.Contains(m[0], "Vou te encaminhar") {
		t.Fatalf("pendente: %v", m)
	}
}

// Transferência silenciosa (fora do piloto) segue sem mensagem, dentro ou fora do horário.
func TestHorarioForaNaoFalaComQuemEstaForaDoPiloto(t *testing.T) {
	e, tr, rel := engineComHorario(t, expediente, config.ForaDoHorarioAvisar)
	e.cfg.TestadoresTelefones = []string{"+5511999990060"}
	rel.em(t, "2026-10-23", 21, 0)
	e.processarLote(context.Background(), []Inbound{msgDe("c-hpil", "+5531988887777", "oi")})
	if len(tr.mensagens("c-hpil")) != 0 || len(tr.transferida("c-hpil")) != 1 {
		t.Fatalf("msgs=%v handoffs=%v", tr.mensagens("c-hpil"), tr.transferida("c-hpil"))
	}
}

// Sem horário configurado (nil) tudo segue como antes, a qualquer hora.
func TestHorarioNaoConfiguradoNaoMudaNada(t *testing.T) {
	e, tr, rel := engineComHorario(t, "", "")
	rel.em(t, "2026-10-25", 3, 0) // domingo 03:00
	transferirPedido(e, "c-hn")
	if got := tr.mensagens("c-hn"); len(got) != 1 || got[0] != MsgPedidoAtendente {
		t.Fatalf("aviso normal: %v", got)
	}
	if n := tr.notas("c-hn"); len(n) != 1 || strings.Contains(n[0], "fora do horário") {
		t.Fatalf("nota: %v", n)
	}
}

// A Sofia segue atendendo fora do horário: o turno vai ao cérebro e responde.
func TestHorarioForaASofiaSegueAtendendo(t *testing.T) {
	e, tr, rel := engineComHorario(t, expediente, config.ForaDoHorarioAvisar)
	rel.em(t, "2026-10-24", 22, 0)
	br := e.brain.(*fakeBrain)
	e.processarLote(context.Background(), []Inbound{msg("c-hs", "meu wifi caiu")})
	if br.total() != 1 || len(tr.transferida("c-hs")) != 0 {
		t.Fatalf("fora do horário o cérebro responde e não transfere: cérebro=%d handoffs=%v", br.total(), tr.transferida("c-hs"))
	}
}

// Quando a resposta do cérebro já anunciou a transferência (SemAviso: frustração,
// baixa confiança, pedido do cliente), fora do horário o cliente ainda recebe o
// aviso de fora do horário — sem repetir "vou te encaminhar". Dentro do horário
// segue sem mensagem; fora do piloto, nunca.
func TestHorarioForaAvisaMesmoQuandoOCerebroJaAnunciouATransferencia(t *testing.T) {
	e, tr, rel := engineComHorario(t, expediente, config.ForaDoHorarioAvisar)
	ctx := context.Background()
	rel.em(t, "2026-10-23", 22, 0)
	e.TransferirParaHumano(ctx, "c-sa1", Transferencia{Origem: OrigemFrustracao, SemAviso: true})
	m := tr.mensagens("c-sa1")
	if len(m) != 1 || !strings.Contains(m[0], "fica registrada") || strings.Contains(m[0], "Vou te encaminhar") {
		t.Fatalf("aviso de fora do horário, sem repetir a transferência: %v", m)
	}
	if len(tr.transferida("c-sa1")) != 1 {
		t.Fatal("o handoff segue")
	}

	rel.em(t, "2026-10-23", 10, 0)
	e.TransferirParaHumano(ctx, "c-sa2", Transferencia{Origem: OrigemFrustracao, SemAviso: true})
	if len(tr.mensagens("c-sa2")) != 0 {
		t.Fatalf("dentro do horário o SemAviso segue sem mensagem: %v", tr.mensagens("c-sa2"))
	}

	eIgn, trIgn, relIgn := engineComHorario(t, expediente, config.ForaDoHorarioIgnorar)
	relIgn.em(t, "2026-10-23", 22, 0)
	eIgn.TransferirParaHumano(ctx, "c-sa3", Transferencia{Origem: OrigemFrustracao, SemAviso: true})
	if len(trIgn.mensagens("c-sa3")) != 0 {
		t.Fatalf("modo ignorar: SemAviso segue sem mensagem: %v", trIgn.mensagens("c-sa3"))
	}
}

// O número do chamado vem da própria mensagem da origem (não do store): segue no
// aviso mesmo que a gravação do número na conversa tenha falhado.
func TestHorarioForaNumeroDoChamadoNaoDependeDoStore(t *testing.T) {
	e, tr, rel := engineComHorario(t, expediente, config.ForaDoHorarioAvisar)
	rel.em(t, "2026-10-23", 22, 0)
	e.TransferirParaHumano(context.Background(), "c-nn", Transferencia{Origem: OrigemTicketAberto, Mensagem: MsgChamadoAberto(9001)})
	if m := tr.mensagens("c-nn"); len(m) != 1 || !strings.Contains(m[0], "chamado nº 9001") || !strings.Contains(m[0], "fica registrada") {
		t.Fatalf("aviso: %v", m)
	}
}

// As mensagens próprias das tools mantêm a primeira frase (o porquê), as genéricas
// de transferência não deixam prefixo.
func TestHorarioForaPrefixoDaMensagemDaOrigem(t *testing.T) {
	e, tr, rel := engineComHorario(t, expediente, config.ForaDoHorarioAvisar)
	rel.em(t, "2026-10-23", 22, 0)
	ctx := context.Background()
	casos := map[string]struct {
		msg     string
		prefixo string
	}{
		"cadastro": {"Não consegui localizar a sua empresa no nosso cadastro. Vou te passar para um atendente, que segue com você por aqui.", "Não consegui localizar a sua empresa no nosso cadastro."},
		"tool":     {"Estou te passando para um atendente da equipe de Suporte. Ele continua com você por aqui.", "Estou te passando para um atendente da equipe de Suporte."},
		"generica": {MsgTransferencia, ""},
		"pedido":   {MsgPedidoAtendente, ""},
		"vazia":    {"", ""},
	}
	for nome, c := range casos {
		conv := "c-px-" + nome
		e.TransferirParaHumano(ctx, conv, Transferencia{Origem: OrigemAcao, Mensagem: c.msg})
		m := tr.mensagens(conv)
		if len(m) != 1 || !strings.Contains(m[0], "fica registrada") {
			t.Fatalf("%s: %v", nome, m)
		}
		if c.prefixo != "" && !strings.HasPrefix(m[0], c.prefixo+" Agora a nossa equipe") {
			t.Errorf("%s: sem o prefixo %q: %q", nome, c.prefixo, m[0])
		}
		if c.prefixo == "" && !strings.HasPrefix(m[0], "Agora a nossa equipe") {
			t.Errorf("%s: não devia ter prefixo: %q", nome, m[0])
		}
		if strings.Count(m[0], "🙏") > 1 {
			t.Errorf("%s: no máximo 1 emoji: %q", nome, m[0])
		}
	}
}
