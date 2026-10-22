package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

var spLoc = func() *time.Location {
	l, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		panic(err)
	}
	return l
}()

// em monta um instante no fuso de São Paulo. 2026-10-19 é segunda-feira.
func em(dia string, hora, min int) time.Time {
	d, err := time.ParseInLocation("2006-01-02", dia, spLoc)
	if err != nil {
		panic(err)
	}
	return d.Add(time.Duration(hora)*time.Hour + time.Duration(min)*time.Minute)
}

func horarioDe(t *testing.T, texto string, feriados ...string) *Horario {
	t.Helper()
	h, err := ParseHorario(texto, spLoc, feriados)
	if err != nil {
		t.Fatalf("ParseHorario(%q): %v", texto, err)
	}
	return h
}

// Expediente do Desk: seg–sex 08:00–18:00. O fim é exclusivo.
func TestHorarioDentroEFora(t *testing.T) {
	h := horarioDe(t, HorarioPadrao)
	casos := []struct {
		quando time.Time
		dentro bool
		nome   string
	}{
		{em("2026-10-19", 8, 0), true, "segunda 08:00 (início inclusivo)"},
		{em("2026-10-19", 12, 30), true, "segunda meio-dia"},
		{em("2026-10-19", 17, 59), true, "segunda 17:59"},
		{em("2026-10-19", 18, 0), false, "segunda 18:00 (fim exclusivo)"},
		{em("2026-10-19", 7, 59), false, "segunda 07:59"},
		{em("2026-10-23", 17, 0), true, "sexta 17:00"},
		{em("2026-10-24", 10, 0), false, "sábado"},
		{em("2026-10-25", 10, 0), false, "domingo"},
	}
	for _, c := range casos {
		if got := h.Dentro(c.quando); got != c.dentro {
			t.Errorf("%s: dentro=%v, queria %v", c.nome, got, c.dentro)
		}
	}
}

func TestHorarioNilESempreDentro(t *testing.T) {
	var h *Horario
	if !h.Dentro(em("2026-10-25", 3, 0)) || h.Descrever() != "" || h.TemFeriados() {
		t.Fatal("horário nil = sempre dentro")
	}
	if h, err := ParseHorario("  ", spLoc, nil); err != nil || h != nil {
		t.Fatalf("texto vazio = nil: %v %v", h, err)
	}
}

// Mais de uma faixa por dia, dias isolados, listas e várias entradas.
func TestHorarioMultiplasFaixasEDiasIsolados(t *testing.T) {
	h := horarioDe(t, "seg-sex 08:00-12:00,14:00-18:00; sab 08:00-12:00")
	for _, c := range []struct {
		q time.Time
		d bool
	}{
		{em("2026-10-19", 11, 59), true}, {em("2026-10-19", 12, 30), false}, {em("2026-10-19", 14, 0), true},
		{em("2026-10-24", 9, 0), true}, {em("2026-10-24", 13, 0), false}, {em("2026-10-25", 9, 0), false},
	} {
		if got := h.Dentro(c.q); got != c.d {
			t.Errorf("%v: %v, queria %v", c.q, got, c.d)
		}
	}
	lista := horarioDe(t, "seg,qua,sex 09:00-10:00")
	if !lista.Dentro(em("2026-10-21", 9, 30)) || lista.Dentro(em("2026-10-20", 9, 30)) {
		t.Error("lista de dias: quarta sim, terça não")
	}
	mista := horarioDe(t, "seg-ter,sex 09:00-10:00 ; dom 10:00-11:00")
	if !mista.Dentro(em("2026-10-20", 9, 30)) || !mista.Dentro(em("2026-10-23", 9, 30)) || !mista.Dentro(em("2026-10-25", 10, 30)) {
		t.Error("intervalo + dia na mesma lista, e domingo")
	}
	if got := horarioDe(t, "SEG-SEX 08:00 - 18:00"); got == nil || !got.Dentro(em("2026-10-19", 9, 0)) {
		t.Error("caixa alta e espaços em volta do traço da faixa")
	}
	if !horarioDe(t, "segunda 08:00-09:00").Dentro(em("2026-10-19", 8, 30)) || !horarioDe(t, "sáb 08:00-09:00").Dentro(em("2026-10-24", 8, 30)) {
		t.Error("nomes longos e com acento")
	}
}

// Virada de dia: 22:00–06:00 pertence ao dia em que começou; sex-seg dá a volta na semana.
func TestHorarioViradaDeDia(t *testing.T) {
	h := horarioDe(t, "seg-sex 22:00-06:00")
	for _, c := range []struct {
		nome string
		q    time.Time
		d    bool
	}{
		{"segunda 23:00", em("2026-10-19", 23, 0), true},
		{"terça 05:59 (continuação da segunda)", em("2026-10-20", 5, 59), true},
		{"terça 06:00", em("2026-10-20", 6, 0), false},
		{"segunda 05:00 (continuação do domingo, que não conta)", em("2026-10-19", 5, 0), false},
		{"sábado 05:00 (continuação da sexta)", em("2026-10-24", 5, 0), true},
		{"sábado 23:00 (sábado não conta)", em("2026-10-24", 23, 0), false},
	} {
		if got := h.Dentro(c.q); got != c.d {
			t.Errorf("%s: %v, queria %v", c.nome, got, c.d)
		}
	}
	volta := horarioDe(t, "sex-seg 09:00-10:00")
	if !volta.Dentro(em("2026-10-24", 9, 30)) || !volta.Dentro(em("2026-10-25", 9, 30)) || volta.Dentro(em("2026-10-21", 9, 30)) {
		t.Error("sex-seg cobre sexta, sábado, domingo e segunda")
	}
	todo := horarioDe(t, "seg 00:00-24:00")
	if !todo.Dentro(em("2026-10-19", 23, 59)) || todo.Dentro(em("2026-10-20", 0, 0)) {
		t.Error("24:00 como fim")
	}
}

func TestHorarioFeriadoForaODiaInteiro(t *testing.T) {
	h := horarioDe(t, HorarioPadrao, "2026-10-21")
	if h.Dentro(em("2026-10-21", 10, 0)) {
		t.Error("quarta 21/10 é feriado: fora o dia inteiro")
	}
	if !h.Dentro(em("2026-10-22", 10, 0)) || !h.TemFeriados() {
		t.Error("o dia seguinte volta ao normal")
	}
	// A faixa que atravessa a meia-noite pertence ao dia em que começou: feriado
	// no início tira a continuação do dia seguinte; feriado no dia seguinte só
	// tira o que começa nele.
	noite := horarioDe(t, "seg-sex 22:00-06:00", "2026-10-20")
	if noite.Dentro(em("2026-10-20", 23, 0)) {
		t.Error("terça é feriado: não começa faixa")
	}
	if !noite.Dentro(em("2026-10-20", 5, 0)) {
		t.Error("a continuação das 22:00 de segunda cai na madrugada de terça (feriado só vale para quem COMEÇA nele)")
	}
	if noite.Dentro(em("2026-10-21", 5, 0)) {
		t.Error("a continuação da terça (feriado) não vale")
	}
}

// O fuso decide o dia e a hora: o mesmo instante UTC é dentro em São Paulo e
// fora em Tóquio.
func TestHorarioFuso(t *testing.T) {
	utc := time.Date(2026, 10, 19, 13, 0, 0, 0, time.UTC) // 10:00 em São Paulo, 22:00 em Tóquio
	if !horarioDe(t, HorarioPadrao).Dentro(utc) {
		t.Error("10:00 em São Paulo é dentro")
	}
	toquio, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	h, err := ParseHorario(HorarioPadrao, toquio, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h.Dentro(utc) {
		t.Error("22:00 em Tóquio é fora")
	}
	// O dia também muda com o fuso: 02:00 UTC de terça ainda é segunda à noite em SP.
	if horarioDe(t, "seg 22:00-24:00").Dentro(time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC)) != true {
		t.Error("terça 02:00 UTC = segunda 23:00 em São Paulo")
	}
}

func TestHorarioDescrever(t *testing.T) {
	for texto, quer := range map[string]string{
		"seg-sex 08:00-18:00":                  "de segunda a sexta, das 08:00 às 18:00",
		"seg-sex 08:00-18:00; sab 08:00-12:00": "de segunda a sexta, das 08:00 às 18:00; aos sábados, das 08:00 às 12:00",
		"seg-sex 14:00-18:00,08:00-12:00":      "de segunda a sexta, das 08:00 às 12:00 e das 14:00 às 18:00",
		"seg,qua,sex 09:00-10:00":              "segunda, quarta e sexta, das 09:00 às 10:00",
		"seg-ter 09:00-10:00":                  "segunda e terça, das 09:00 às 10:00",
		"seg-qui 08:00-18:00; sex 08:00-17:00": "de segunda a quinta, das 08:00 às 18:00; às sextas, das 08:00 às 17:00",
		"dom 22:00-06:00":                      "aos domingos, das 22:00 às 06:00",
	} {
		if got := horarioDe(t, texto).Descrever(); got != quer {
			t.Errorf("%q → %q, queria %q", texto, got, quer)
		}
	}
}

func TestHorarioInvalido(t *testing.T) {
	for _, texto := range []string{
		"seg-sex", "08:00-18:00", "seg-sex 8-18", "seg-sex 08:00", "xyz 08:00-18:00", "seg-xyz 08:00-18:00",
		"seg-sex 25:00-26:00", "seg-sex 08:60-18:00", "seg-sex 08:00-08:00", "seg-sex 24:00-06:00", "seg-sex 08:00-24:30",
		"seg,, 08:00-18:00", "seg-sex 08:00-18:00,", ";;", "seg-sex 8:0-18:00",
	} {
		if h, err := ParseHorario(texto, spLoc, nil); err == nil {
			t.Errorf("%q devia ser recusado, veio %+v", texto, h)
		}
	}
}

func limparHorario(t *testing.T) {
	t.Helper()
	for _, v := range []string{"HORARIO_HUMANO", "HORARIO_FUSO", "FERIADOS", "FORA_DO_HORARIO_MODO"} {
		t.Setenv(v, "")
	}
}

// Sem variáveis: o expediente do Desk, no fuso de São Paulo, modo avisar.
func TestLoadHorarioDefaults(t *testing.T) {
	prodSegura(t)
	// ausente de verdade (t.Setenv("") deixaria a variável PRESENTE e vazia)
	for _, v := range []string{"HORARIO_HUMANO", "HORARIO_FUSO", "FERIADOS", "FORA_DO_HORARIO_MODO"} {
		t.Setenv(v, "x")
		unsetenv(t, v)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Horario == nil || cfg.ForaDoHorarioModo != ForaDoHorarioAvisar || cfg.HorarioDescricao != "de segunda a sexta, das 08:00 às 18:00" {
		t.Fatalf("defaults: %+v / %q", cfg.Horario, cfg.HorarioDescricao)
	}
	if !cfg.Horario.Dentro(em("2026-10-19", 10, 0)) || cfg.Horario.Dentro(em("2026-10-19", 20, 0)) {
		t.Fatal("o default é o expediente seg–sex 08:00–18:00 em São Paulo")
	}
}

// HORARIO_HUMANO presente e vazio = sempre dentro do horário (comportamento de antes).
func TestLoadHorarioVazioESempreDentro(t *testing.T) {
	prodSegura(t)
	limparHorario(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Horario != nil || !cfg.Horario.Dentro(em("2026-10-25", 3, 0)) || cfg.HorarioDescricao != "" {
		t.Fatalf("vazio = sempre dentro: %+v", cfg.Horario)
	}
}

func TestLoadHorarioLidoComFusoFeriadosEModo(t *testing.T) {
	prodSegura(t)
	t.Setenv("HORARIO_HUMANO", "seg-sex 08:00-18:00; sab 08:00-12:00")
	t.Setenv("HORARIO_FUSO", "America/Manaus")
	t.Setenv("FERIADOS", "2026-12-25, 2026-11-15;2026-01-01")
	t.Setenv("FORA_DO_HORARIO_MODO", " IGNORAR ")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ForaDoHorarioModo != ForaDoHorarioIgnorar || !cfg.Horario.TemFeriados() {
		t.Fatalf("config: %+v modo=%q", cfg.Horario, cfg.ForaDoHorarioModo)
	}
	// Manaus é UTC-4: 12:00 UTC de segunda = 08:00 lá (dentro); em São Paulo seriam 09:00 (dentro também),
	// então confere pelo limite do fim: 22:00 UTC = 18:00 em Manaus (fora) e 19:00 em São Paulo.
	if !cfg.Horario.Dentro(time.Date(2026, 10, 19, 21, 59, 0, 0, time.UTC)) || cfg.Horario.Dentro(time.Date(2026, 10, 19, 22, 0, 0, 0, time.UTC)) {
		t.Fatal("o fuso configurado vale (Manaus)")
	}
	if cfg.Horario.Dentro(time.Date(2026, 12, 25, 15, 0, 0, 0, time.UTC)) {
		t.Fatal("feriado configurado")
	}
	if !strings.Contains(cfg.HorarioDescricao, "aos sábados, das 08:00 às 12:00") {
		t.Fatalf("descrição: %q", cfg.HorarioDescricao)
	}
}

func TestLoadHorarioInvalidoFalhaNoBoot(t *testing.T) {
	casos := []struct{ var_, valor string }{
		{"HORARIO_HUMANO", "seg-sex 8h-18h"},
		{"HORARIO_HUMANO", "quando der"},
		{"HORARIO_FUSO", "Marte/Olympus"},
		{"FERIADOS", "2026-02-30"},
		{"FERIADOS", "25/12/2026"},
		{"FORA_DO_HORARIO_MODO", "transferir_sempre"},
		{"FORA_DO_HORARIO_MODO", "silencio"},
	}
	for _, c := range casos {
		t.Run(c.var_+"="+c.valor, func(t *testing.T) {
			prodSegura(t)
			limparHorario(t)
			t.Setenv(c.var_, c.valor)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), c.var_) {
				t.Fatalf("esperava erro citando %s, veio %v", c.var_, err)
			}
		})
	}
}

// unsetenv remove a variável de verdade (t.Setenv já registrou a restauração).
func unsetenv(t *testing.T, chave string) {
	t.Helper()
	if err := os.Unsetenv(chave); err != nil {
		t.Fatal(err)
	}
}
