package engine

import (
	"strings"
	"testing"
	"unicode"
)

// TestFormatWhatsApp: a saída é o dialeto do relay — o que a Evolution
// convertia para WhatsApp limpo (ver simularEntrega). Negrito sai `**x**`.
func TestFormatWhatsApp(t *testing.T) {
	casos := []struct{ nome, in, want string }{
		// Negrito (ZF-1): `**x**` fica; `*x*` simples (sintaxe do WhatsApp, a que
		// o prompt pede) vira `**x**` — senão a Evolution o entregava como itálico.
		{"negrito markdown mantido", "**Sofia (bot):** resposta", "**Sofia (bot):** resposta"},
		{"negrito simples vira duplo", "clique em *Configurações* e salve", "clique em **Configurações** e salve"},
		{"vários negritos simples", "*Nome:* Ana\n*Login:* ana.silva", "**Nome:** Ana\n**Login:** ana.silva"},
		{"negrito triplo", "***urgente***", "**urgente**"},
		{"itálico underscore passa", "_leve_ e __forte__", "_leve_ e __forte__"},
		{"negrito simples dentro de negrito vira itálico", "**a *b* c**", "**a _b_ c**"},
		{"asterisco com espaço não é marcador", "5 * 3 * 2 = 30", "5 * 3 * 2 = 30"},
		{"asterisco solto sobrevive", "*obrigatório", "*obrigatório"},
		{"título vira negrito", "# Título\ntexto", "**Título**\ntexto"},
		{"título com fechamento", "## Passos ##\n1. a", "**Passos**\n1. a"},
		// Links: WhatsApp reconhece URL/e-mail nus; markdown chegaria literal.
		{"link com texto", "veja [o chamado](https://x.com/t/1)", "veja o chamado: https://x.com/t/1"},
		{"link cujo texto é a url", "[https://x.com](https://x.com)", "https://x.com"},
		{"link cujo texto é a url sem esquema", "[x.com/ajuda](https://x.com/ajuda/)", "https://x.com/ajuda/"},
		{"mailto com texto igual", "escreva para [suporte@infominas.net](mailto:suporte@infominas.net)", "escreva para suporte@infominas.net"},
		{"mailto com texto diferente", "[nosso e-mail](mailto:suporte@infominas.net)", "nosso e-mail: suporte@infominas.net"},
		{"mailto solto", "e-mail: mailto:suporte@infominas.net ok", "e-mail: suporte@infominas.net ok"},
		{"autolink url", "acesse <https://app.infominas.net/login>", "acesse https://app.infominas.net/login"},
		{"autolink e-mail", "fale com <suporte@infominas.net>", "fale com suporte@infominas.net"},
		{"autolink mailto", "<mailto:a@b.co>", "a@b.co"},
		{"link com título", `[Portal](https://x.com "Portal Infominas")`, "Portal: https://x.com"},
		{"link com destino entre <>", "[Portal](<https://x.com/a>)", "Portal: https://x.com/a"},
		{"link de referência", "veja [a documentação][docs] hoje\n\n[docs]: https://x.com/docs \"Docs\"\n", "veja a documentação: https://x.com/docs hoje"},
		{"link de referência colapsado", "[https://x.com][]\n[https://x.com]: https://x.com", "https://x.com"},
		{"link de referência atalho", "leia [Guia]\n\n[guia]: <https://x.com/g>", "leia Guia: https://x.com/g"},
		{"colchete sem definição fica", "[opcional] e [x][y]", "[opcional] e [x][y]"},
		{"tel", "[ligue](tel:+5531999990000)", "ligue: +5531999990000"},
		// Escape markdown: ninguém depois de nós interpreta a barra.
		{"escape de pontuação", `1\. nome\_arquivo \*não\* \#1`, "1. nome_arquivo **não** #1"},
		{"texto simples", "texto simples", "texto simples"},
		{"crlf vira lf", "a\r\n*b*", "a\n**b**"},
	}
	for _, c := range casos {
		if got := FormatWhatsApp(c.in); got != c.want {
			t.Errorf("%s: FormatWhatsApp(%q) = %q, esperava %q", c.nome, c.in, got, c.want)
		}
	}
}

// TestFormatWhatsAppBalanceiaBolha: dentro de cada bolha nenhum `**`/`~~` fica
// sem par (chegaria literal), e cada par é o que as regex da Evolution casam:
// mesma linha, colado no texto, sem `*` dentro.
func TestFormatWhatsAppBalanceiaBolha(t *testing.T) {
	casos := []struct{ nome, in, want string }{
		{"abertura sem fechamento some", "clique em **Configurações", "clique em Configurações"},
		{"fechamento sem abertura some", "Configurações** e salve", "Configurações e salve"},
		{"negrito atravessando linha", "**linha um\nlinha dois**", "linha um\nlinha dois"},
		{"linhas independentes", "**a**\nb **c** d\n**e", "**a**\nb **c** d\ne"},
		{"marcador solto entre espaços some (não abre nem fecha)", "oi ** tudo bem ** ?", "oi  tudo bem  ?"},
		{"marcador que só fecha não abre", "a** b **c", "a b c"},
		{"par vazio some", "a ** ** b", "a   b"},
		{"asterisco solto dentro invalida o par", "**2*3** ok", "2*3 ok"},
		{"riscado balanceado", "~~x~~ e ~~y", "~~x~~ e y"},
		{"riscado com negrito dentro perde o riscado", "~~a **b** c~~", "a **b** c"},
		{"negrito com riscado dentro mantém os dois", "**a ~~b~~ c**", "**a ~~b~~ c**"},
		{"só marcadores", "** ~~ **", ""},
	}
	for _, c := range casos {
		if got := FormatWhatsApp(c.in); got != c.want {
			t.Errorf("%s: FormatWhatsApp(%q) = %q, esperava %q", c.nome, c.in, got, c.want)
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

// TestQuebrarMensagemBalanceiaPartes (ZF-1): um negrito cortado ao meio pela
// quebra é fechado no fim da parte e reaberto na seguinte — cada parte cabe em
// max, continua balanceada e chega ao cliente sem `**` sobrando.
func TestQuebrarMensagemBalanceiaPartes(t *testing.T) {
	// Parágrafo único sem quebra de linha: a quebra cai num espaço, dentro do
	// negrito longo do meio.
	texto := FormatWhatsApp(strings.Repeat("palavra ", 20) + "**" + strings.Repeat("negrito longo ", 12) + "fim** " + strings.Repeat("cauda ", 20))
	partes := QuebrarMensagem(texto, 120)
	if len(partes) < 3 {
		t.Fatalf("esperava 3+ partes, veio %d: %q", len(partes), partes)
	}
	comNegrito := 0
	for i, p := range partes {
		if n := len([]rune(p)); n > 120 {
			t.Errorf("parte %d tem %d runas (> 120)", i, n)
		}
		if p != equilibrar(p) {
			t.Errorf("parte %d desbalanceada: %q", i, p)
		}
		entregue := simularEntrega(p)
		if strings.Contains(entregue, "**") {
			t.Errorf("parte %d chega com ** sobrando: %q", i, entregue)
		}
		if strings.Contains(entregue, "*") {
			comNegrito++
		}
	}
	if comNegrito < 2 {
		t.Errorf("o negrito cortado deveria continuar negrito nas duas partes: %q", partes)
	}
	// Nenhuma palavra perdida: o texto sem marcadores é o mesmo.
	semMarcas := func(s string) string {
		return strings.Join(strings.Fields(strings.ReplaceAll(s, "**", "")), " ")
	}
	if got, want := semMarcas(strings.Join(partes, " ")), semMarcas(texto); got != want {
		t.Errorf("conteúdo alterado pela quebra:\n got=%q\nwant=%q", got, want)
	}
}

// TestQuebrarMensagemLimiteMinusculo: com max pequeno demais para caber o
// fechamento, a parte sai sem o marcador (nunca com `**` solto), sem loop.
func TestQuebrarMensagemLimiteMinusculo(t *testing.T) {
	for _, max := range []int{1, 2, 3, 4, 5} {
		partes := QuebrarMensagem("**ab cd** ef **gh**", max)
		for i, p := range partes {
			if n := len([]rune(p)); n > max {
				t.Errorf("max=%d: parte %d tem %d runas: %q", max, i, n, p)
			}
			if strings.Contains(simularEntrega(p), "**") {
				t.Errorf("max=%d: parte %d chega com ** sobrando: %q", max, i, p)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Teste de ouro: o que chega ao WhatsApp do cliente.
//
// simularEntrega reproduz, fora deste repositório, o caminho painel de
// atendimento → relay Evolution usado até a Onda 0. É uma SIMULAÇÃO copiada
// do código de cada um, nas versões fixadas abaixo (TODO(C7): trocar pela
// entrega do Infodesk quando C7 for respondido):
//
//  1. Painel v4.15.1 — `Messages::WebhookContentNormalizer.normalize`
//     (conteúdo do webhook para o canal):
//     text.gsub(/\\\r?\n/, "\n").sub(/(\r?\n)+\z/, '')
//  2. Evolution API v2.3.6 — `receiveWebhook` (painel → WhatsApp), as quatro
//     regex nesta ordem:
//     .replaceAll(/(?<!\*)\*((?!\s)([^\n*]+?)(?<!\s))\*(?!\*)/g, '_$1_')   // * vira _
//     .replaceAll(/\*{2}((?!\s)([^\n*]+?)(?<!\s))\*{2}/g, '*$1*')           // ** vira *
//     .replaceAll(/~{2}((?!\s)([^\n*]+?)(?<!\s))~{2}/g, '~$1~')             // ~~ vira ~
//     .replaceAll(/(?<!`)`((?!\s)([^`*]+?)(?<!\s))`(?!`)/g, '```$1```')     // ` vira ```
//
// Go (RE2) não tem lookaround, então cada regex está transcrita à mão abaixo;
// TestSimularEntregaConfereComRegexReais prende a transcrição a vetores
// gerados com as regex originais em Node.
// ---------------------------------------------------------------------------

func simularEntrega(s string) string {
	s = normalizarRelay(s)
	s = regexItalicoEvolution(s)
	s = regexParEvolution(s, '*', "*")
	s = regexParEvolution(s, '~', "~")
	return regexCodigoEvolution(s)
}

// espacoJSTeste é o `\s` do JavaScript (igual a espacoJS, repetido aqui para
// a simulação não depender do código testado).
func espacoJSTeste(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' }

// normalizarRelay: `\` antes da quebra de linha some; quebras no fim somem.
func normalizarRelay(s string) string {
	r := []rune(s)
	var out []rune
	for i := 0; i < len(r); i++ {
		if r[i] == '\\' && i+1 < len(r) {
			if r[i+1] == '\n' {
				out = append(out, '\n')
				i++
				continue
			}
			if r[i+1] == '\r' && i+2 < len(r) && r[i+2] == '\n' {
				out = append(out, '\n')
				i += 2
				continue
			}
		}
		out = append(out, r[i])
	}
	for len(out) > 0 && out[len(out)-1] == '\n' {
		out = out[:len(out)-1]
		if len(out) > 0 && out[len(out)-1] == '\r' {
			out = out[:len(out)-1]
		}
	}
	return string(out)
}

// regexItalicoEvolution: (?<!\*)\*((?!\s)([^\n*]+?)(?<!\s))\*(?!\*) → _$1_
func regexItalicoEvolution(s string) string {
	r := []rune(s)
	var out []rune
	for i := 0; i < len(r); {
		if r[i] != '*' || (i > 0 && r[i-1] == '*') || i+1 >= len(r) || r[i+1] == '*' || r[i+1] == '\n' || espacoJSTeste(r[i+1]) {
			out = append(out, r[i])
			i++
			continue
		}
		j := i + 1
		for j < len(r) && r[j] != '*' && r[j] != '\n' {
			j++
		}
		if j >= len(r) || r[j] != '*' || espacoJSTeste(r[j-1]) || (j+1 < len(r) && r[j+1] == '*') {
			out = append(out, r[i])
			i++
			continue
		}
		out = append(out, '_')
		out = append(out, r[i+1:j]...)
		out = append(out, '_')
		i = j + 1
	}
	return string(out)
}

// regexParEvolution: m{2}((?!\s)([^\n*]+?)(?<!\s))m{2} → novo$1novo
// (m = '*' para negrito, '~' para riscado; o conteúdo nunca tem `*`).
func regexParEvolution(s string, m rune, novo string) string {
	r := []rune(s)
	var out []rune
	for i := 0; i < len(r); {
		c := i + 2
		if r[i] != m || i+1 >= len(r) || r[i+1] != m || c >= len(r) || r[c] == '*' || r[c] == '\n' || espacoJSTeste(r[c]) {
			out = append(out, r[i])
			i++
			continue
		}
		fim := -1
		for j := c + 1; j < len(r); j++ {
			if j+1 < len(r) && r[j] == m && r[j+1] == m && !espacoJSTeste(r[j-1]) {
				fim = j
				break
			}
			if r[j] == '*' || r[j] == '\n' {
				break
			}
		}
		if fim < 0 {
			out = append(out, r[i])
			i++
			continue
		}
		out = append(out, []rune(novo)...)
		out = append(out, r[c:fim]...)
		out = append(out, []rune(novo)...)
		i = fim + 2
	}
	return string(out)
}

// regexCodigoEvolution: (?<!`)`((?!\s)([^`*]+?)(?<!\s))`(?!`) → ```$1```
func regexCodigoEvolution(s string) string {
	r := []rune(s)
	var out []rune
	for i := 0; i < len(r); {
		if r[i] != '`' || (i > 0 && r[i-1] == '`') || i+1 >= len(r) || r[i+1] == '`' || r[i+1] == '*' || espacoJSTeste(r[i+1]) {
			out = append(out, r[i])
			i++
			continue
		}
		j := i + 1
		for j < len(r) && r[j] != '`' && r[j] != '*' {
			j++
		}
		if j >= len(r) || r[j] != '`' || espacoJSTeste(r[j-1]) || (j+1 < len(r) && r[j+1] == '`') {
			out = append(out, r[i])
			i++
			continue
		}
		out = append(out, []rune("```")...)
		out = append(out, r[i+1:j]...)
		out = append(out, []rune("```")...)
		i = j + 1
	}
	return string(out)
}

// TestSimularEntregaConfereComRegexReais: vetores gerados em Node (v24) com
// as regex originais acima (`delivered(s)`), inclusive os casos que motivaram
// o ZF-1 — `**x**` chega `*x*` e `*x*` chega `_x_`.
func TestSimularEntregaConfereComRegexReais(t *testing.T) {
	casos := []struct{ in, want string }{
		{"**x**", "*x*"}, {"*x*", "_x_"}, {"***x***", "**x**"}, {"**a *b* c**", "*a _b_ c*"},
		{"*a**b*", "*a**b*"}, {"** x **", "** x **"}, {"**a\nb**", "**a\nb**"}, {"`x`", "```x```"},
		{"~~x~~", "~x~"}, {"**a**b", "*a*b"}, {"x**y**", "x*y*"}, {"*Sofia:* oi", "_Sofia:_ oi"},
		{"5*3*2", "5_3_2"}, {"a * b * c", "a * b * c"}, {"**R$ 10,00**", "*R$ 10,00*"},
		{"*\u00a0x\u00a0*", "*\u00a0x\u00a0*"}, {"**x**\n", "*x*"}, {"a\\\nb", "a\nb"},
		{"**a**\n**b**", "*a*\n*b*"}, {"_x_", "_x_"}, {"**_x_**", "*_x_*"}, {"*_x_*", "__x__"},
		{"**a:**b", "*a:*b"}, {"**a** **b**", "*a* *b*"}, {"*a* *b*", "_a_ _b_"}, {"a*b*c", "a_b_c"},
		{"**a-b**", "*a-b*"}, {"`a*b`", "`a*b`"}, {"~~a*b~~", "~~a*b~~"}, {"***", "***"}, {"****", "****"},
		{"**", "**"}, {"*", "*"}, {"** **", "** **"}, {"**a**\n\n**b**", "*a*\n\n*b*"},
		{"```x```", "```x```"}, {"``x``", "``x``"}, {"**x**y**z**", "*x*y*z*"},
		{"**x** y **z**", "*x* y *z*"}, {"**olá, *mundo***", "**olá, *mundo***"},
		{"~~a ~~b~~", "~a ~~b~"}, {"`a\nb`", "```a\nb```"}, {"**a ~~b~~ c**", "*a ~b~ c*"},
		{"\\*x\\*", "\\_x\\_"}, {"a\\\r\nb\r\n\n", "a\nb"}, {"*\tx*", "*\tx*"}, {"*x\uFEFF*", "*x\uFEFF*"},
	}
	for _, c := range casos {
		if got := simularEntrega(c.in); got != c.want {
			t.Errorf("simularEntrega(%q) = %q, esperava %q (regex real)", c.in, got, c.want)
		}
	}
}

// restosMarkdown devolve os restos de markdown que apareciam no WhatsApp do
// cliente (B2/B3): barra de escape, `**` sobrando, `](` de link, `mailto:` e
// autolink `<http`.
func restosMarkdown(s string) []string {
	var restos []string
	for _, r := range []string{`\`, "**", "](", "mailto:", "<http"} {
		if strings.Contains(s, r) {
			restos = append(restos, r)
		}
	}
	return restos
}

// TestOuroEntregaNoWhatsApp (ZF-1): respostas como o LLM/gateway as escrevem,
// passadas pelo caminho real de envio (bolhas → FormatWhatsApp → quebra) e
// pela simulação da Evolution — o que chega não tem `\`, `**` sobrando nem
// `](`, e o negrito chega NEGRITO (`*x*`), não itálico (`_x_`).
func TestOuroEntregaNoWhatsApp(t *testing.T) {
	casos := []struct {
		nome, resposta string
		contem         []string
		naoContem      []string
	}{
		{
			nome:     "negrito simples (sintaxe do prompt) chega negrito",
			resposta: "Boa tarde, Ana! 🧡\n---\nAbra *Configurações* > *Usuários* e clique em *Novo*.",
			contem:   []string{"*Configurações*", "*Usuários*", "*Novo*"},
		},
		{
			nome:      "negrito markdown chega negrito",
			resposta:  "**Passo 1:** acesse o menu.\n**Passo 2:** salve.",
			contem:    []string{"*Passo 1:*", "*Passo 2:*"},
			naoContem: []string{"_Passo"},
		},
		{
			nome:     "e-mail e links chegam nus",
			resposta: "Escreva para [suporte@infominas.net](mailto:suporte@infominas.net) ou veja <https://ajuda.infominas.net/senha>.\n---\nManual: [aqui](https://x.com/m \"Manual\") e [docs][d]\n\n[d]: https://x.com/docs",
			contem:   []string{"suporte@infominas.net", "https://ajuda.infominas.net/senha", "aqui: https://x.com/m", "docs: https://x.com/docs"},
		},
		{
			nome:     "escape markdown não chega",
			resposta: `Use o arquivo nome\_final\.pdf e marque \*todas\* as opções \#1.`,
			contem:   []string{"nome_final.pdf", "*todas*", "#1"},
		},
		{
			nome:     "título, lista, negrito triplo e riscado",
			resposta: "# Resumo\n- ***Importante***: salve antes\n- ~~antigo~~ novo\n- `menu > sair`",
			contem:   []string{"*Resumo*", "*Importante*", "~antigo~", "```menu > sair```"},
		},
		{
			nome:     "negrito sem fechamento (resposta truncada) e atravessando bolha",
			resposta: "O valor é **R$ 10,00\n---\nSegunda bolha** com *texto* e um ** par vazio.",
			contem:   []string{"R$ 10,00", "Segunda bolha", "*texto*"},
		},
		{
			nome:     "asteriscos de matemática e asterisco solto",
			resposta: "O cálculo é 5 * 3 e 2*3*4 dá 24. *Obrigatório",
			contem:   []string{"5 * 3", "*Obrigatório"},
		},
	}
	for _, c := range casos {
		var entregues []string
		for _, bolha := range DividirBolhas(c.resposta) {
			for _, parte := range QuebrarMensagem(FormatWhatsApp(bolha), 900) {
				entregues = append(entregues, simularEntrega(parte))
			}
		}
		tudo := strings.Join(entregues, "\n")
		if restos := restosMarkdown(tudo); len(restos) > 0 {
			t.Errorf("%s: restos de markdown %q no WhatsApp: %q", c.nome, restos, entregues)
		}
		for _, s := range c.contem {
			if !strings.Contains(tudo, s) {
				t.Errorf("%s: esperava %q no WhatsApp, veio %q", c.nome, s, entregues)
			}
		}
		for _, s := range c.naoContem {
			if strings.Contains(tudo, s) {
				t.Errorf("%s: não esperava %q no WhatsApp, veio %q", c.nome, s, entregues)
			}
		}
	}
}

// TestPropriedadeNenhumMarcadorSobra: para TODA combinação curta de
// asteriscos, tils, letra, espaço e quebra de linha, o que chega ao cliente
// não tem `**` nem `~~` — em bolha inteira e em cada parte quebrada.
func TestPropriedadeNenhumMarcadorSobra(t *testing.T) {
	alfabeto := []string{"*", "~", "a", " ", "\n"}
	var entradas []string
	var gerar func(prefixo string, n int)
	gerar = func(prefixo string, n int) {
		entradas = append(entradas, prefixo)
		if n == 0 {
			return
		}
		for _, c := range alfabeto {
			gerar(prefixo+c, n-1)
		}
	}
	gerar("", 6)
	falhas := 0
	for _, in := range entradas {
		bolha := FormatWhatsApp(in)
		if bolha != equilibrar(bolha) {
			t.Errorf("FormatWhatsApp(%q) = %q não é ponto fixo de equilibrar", in, bolha)
		}
		for _, p := range append(QuebrarMensagem(bolha, 3), bolha) {
			e := simularEntrega(p)
			if strings.Contains(e, "**") || strings.Contains(e, "~~") {
				if falhas++; falhas < 20 {
					t.Errorf("entrada %q → parte %q → chega %q com marcador sobrando", in, p, e)
				}
			}
		}
	}
}
