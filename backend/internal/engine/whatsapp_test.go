package engine

import (
	"strings"
	"testing"
)

func TestStripAssinatura(t *testing.T) {
	casos := []struct{ in, want string }{
		{"**Claude:**\n105.966.936.64", "105.966.936.64"},
		{"*Fulano de Tal:*\nmeu email é x@y.com", "meu email é x@y.com"},
		{"sem assinatura nenhuma", "sem assinatura nenhuma"},
		{"**negrito** no meio não é assinatura", "**negrito** no meio não é assinatura"},
		{"  **Bot:**  \noi", "oi"},
	}
	for _, c := range casos {
		if got := StripAssinatura(c.in); got != c.want {
			t.Errorf("StripAssinatura(%q) = %q, esperava %q", c.in, got, c.want)
		}
	}
}

func TestFormatWhatsApp(t *testing.T) {
	casos := []struct{ in, want string }{
		{"**Zapin (bot):** resposta", "*Zapin (bot):* resposta"},
		{"# Título\ntexto", "*Título*\ntexto"},
		{"veja [o chamado](https://x.com/t/1)", "veja o chamado: https://x.com/t/1"},
		{"[https://x.com](https://x.com)", "https://x.com"},
		{"texto simples", "texto simples"},
	}
	for _, c := range casos {
		if got := FormatWhatsApp(c.in); got != c.want {
			t.Errorf("FormatWhatsApp(%q) = %q, esperava %q", c.in, got, c.want)
		}
	}
}

func TestDividirBolhas(t *testing.T) {
	casos := []struct {
		in   string
		want []string
	}{
		// Contrato com o prompt: linha só de hífens separa bolhas.
		{"primeira bolha\n---\nsegunda bolha\n---\nterceira", []string{"primeira bolha", "segunda bolha", "terceira"}},
		// Tolera espaços ao redor e 3+ hífens.
		{"a\n  ----  \nb", []string{"a", "b"}},
		// Hífens no meio de linha NÃO separam (datas, intervalos, listas).
		{"das 8h - 12h\n- item um\n- item dois", []string{"das 8h - 12h\n- item um\n- item dois"}},
		// Sem marcador: bolha única.
		{"resposta curta", []string{"resposta curta"}},
		// Marcadores nas bordas/consecutivos não geram bolha vazia.
		{"---\nsó uma\n---\n---\n", []string{"só uma"}},
	}
	for _, c := range casos {
		got := DividirBolhas(c.in)
		if len(got) != len(c.want) {
			t.Errorf("DividirBolhas(%q) = %v, esperava %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("DividirBolhas(%q)[%d] = %q, esperava %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestDividirBolhasVazio(t *testing.T) {
	if got := DividirBolhas("   "); got != nil {
		t.Fatalf("texto vazio deveria devolver nil: %v", got)
	}
}

func TestQuebrarMensagemRespeitaLimiteEParagrafos(t *testing.T) {
	texto := strings.Repeat("frase curta. ", 30) + "\n\n" + strings.Repeat("outra frase. ", 30)
	partes := QuebrarMensagem(texto, 200)
	if len(partes) < 2 {
		t.Fatalf("esperava várias partes, veio %d", len(partes))
	}
	for i, p := range partes {
		if n := len([]rune(p)); n > 200 {
			t.Errorf("parte %d tem %d runas (> 200)", i, n)
		}
		if strings.TrimSpace(p) == "" {
			t.Errorf("parte %d vazia", i)
		}
	}
	// Conteúdo preservado (sem perder palavras).
	junto := strings.Join(partes, " ")
	if !strings.Contains(junto, "frase curta.") || !strings.Contains(junto, "outra frase.") {
		t.Error("conteúdo perdido na quebra")
	}
}

func TestQuebrarMensagemSemLimite(t *testing.T) {
	if partes := QuebrarMensagem("abc", 0); len(partes) != 1 || partes[0] != "abc" {
		t.Fatalf("max=0 deveria devolver o texto inteiro: %v", partes)
	}
	if partes := QuebrarMensagem("   ", 100); partes != nil {
		t.Fatalf("texto vazio deveria devolver nil: %v", partes)
	}
}
