// Formatação WhatsApp-native e higiene de envelope (F2/F3/F12 do laudo).
//
// Entrada: StripAssinatura remove a assinatura que relays (ex.: Evolution)
// grudam no texto ("**Fulano:**\n...") — o gate e o cérebro devem ver a FALA
// do cliente, nunca o envelope do transporte.
//
// Saída: FormatWhatsApp converte o que o LLM emite para o DIALETO DO CHATWOOT,
// porque quem fala com o WhatsApp não somos nós: a mensagem vai ao Chatwoot e a
// Evolution (v2.3.6, `chatwoot.service.ts`, `receiveWebhook`) só passa quatro
// regex antes de entregar — `*x*`→`_x_`, `**x**`→`*x*`, `~~x~~`→`~x~` e
// crase simples→crase tripla — sem interpretar markdown nem escape. Logo:
// negrito sai daqui como `**x**` (chega `*x*`); um `*x*` simples viraria
// itálico lá, então vira `**x**` aqui; link markdown, `mailto:` e autolink
// `<url>` viram texto/URL nua; marcadores ficam balanceados em cada bolha e em
// cada parte de QuebrarMensagem (um `**` sem par chegaria literal ao cliente).
// A simulação das regex está em whatsapp_test.go (simularEntrega).
package engine

import (
	"regexp"
	"strings"
	"unicode"
)

// reAssinatura casa a assinatura de relay no INÍCIO do texto: "*Nome:*\n" ou
// "**Nome:**\n" (até 64 chars de nome, sem quebra de linha dentro).
var reAssinatura = regexp.MustCompile(`^\*{1,2}[^\n*]{1,64}:\*{1,2}[ \t]*\n`)

// StripAssinatura remove a assinatura de relay do começo da mensagem, se
// houver. Aplicado a TODA mensagem de entrada antes do gate/cérebro.
func StripAssinatura(s string) string {
	return strings.TrimSpace(reAssinatura.ReplaceAllString(strings.TrimSpace(s), ""))
}

var (
	// Definição de link de referência numa linha própria: `[ref]: url "título"`.
	reLinkDef = regexp.MustCompile(`(?m)^[ \t]{0,3}\[([^\]\n]+)\]:[ \t]*<?([^\s<>]+)>?(?:[ \t]+(?:"[^"\n]*"|'[^'\n]*'|\([^)\n]*\)))?[ \t]*$\n?`)
	// Link inline: `[texto](url)`, `[texto](<url>)`, `[texto](url "título")`.
	reLinkInline = regexp.MustCompile(`\[([^\]\n]*)\]\([ \t]*<?([^\s<>()]+)>?(?:[ \t]+(?:"[^"\n]*"|'[^'\n]*'|\([^)\n]*\)))?[ \t]*\)`)
	// Uso de link de referência: `[texto][ref]`, `[texto][]` ou `[ref]`.
	reLinkRef = regexp.MustCompile(`\[([^\]\n]+)\](?:\[([^\]\n]*)\])?`)
	// Autolink: `<https://…>`, `<mailto:…>`, `<e@mail>`.
	reAutolink = regexp.MustCompile(`<((?:[a-zA-Z][a-zA-Z0-9+.-]*:)[^\s<>]+|[^\s<>@]+@[^\s<>@]+\.[^\s<>@]+)>`)
	// `mailto:` solto no texto vira o e-mail puro.
	reMailto = regexp.MustCompile(`(?i)\bmailto:([^\s<>()\[\]]+@[^\s<>()\[\]]+)`)
	// Escape markdown de pontuação ASCII (`\*`, `\_`, `\#`…): ninguém na cadeia
	// interpreta a barra, que chegaria literal ao cliente.
	reEscape = regexp.MustCompile(`\\([!-/:-@\[-` + "`" + `{-~])`)
	// Título markdown vira linha em negrito (com `#` de fechamento opcionais).
	reHeading = regexp.MustCompile(`(?m)^[ \t]*#{1,6}[ \t]+(.*?)[ \t#]*$`)
	// Sequência de 3+ asteriscos (`***x***`) é negrito: a Evolution deixaria um
	// `**` sobrando.
	reAsteriscos = regexp.MustCompile(`\*{3,}`)
)

// FormatWhatsApp converte o texto do LLM (markdown comum ou sintaxe de
// WhatsApp) para o dialeto do Chatwoot que a Evolution entrega limpo:
//   - `**x**` fica `**x**` e `*x*` simples vira `**x**` (chegam `*x*`, negrito);
//     `*x*` dentro de um negrito vira `_x_` (itálico no WhatsApp)
//   - `# Título` → `**Título**`
//   - `[texto](url)` → "texto: url"; `[url](url)`, `<url>` → url;
//     `[t][ref]` + `[ref]: url` → "t: url"; `mailto:e` → e; `\*` → `*`
//   - marcadores `**`/`~~` ficam balanceados em cada linha: marcador sem par,
//     solto entre espaços, atravessando linha ou com `*` dentro do par é
//     removido (o texto fica)
func FormatWhatsApp(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = resolverLinks(s)
	s = reEscape.ReplaceAllString(s, "$1")
	s = reHeading.ReplaceAllString(s, "**$1**")
	s = reAsteriscos.ReplaceAllString(s, "**")
	return strings.TrimSpace(equilibrar(s))
}

// resolverLinks troca toda forma de link markdown por texto plano: o WhatsApp
// reconhece URL e e-mail nus; `[texto](href)` e `mailto:` chegariam literais.
func resolverLinks(s string) string {
	defs := map[string]string{}
	s = reLinkDef.ReplaceAllStringFunc(s, func(m string) string {
		p := reLinkDef.FindStringSubmatch(m)
		defs[strings.ToLower(strings.TrimSpace(p[1]))] = p[2]
		return ""
	})
	s = reLinkInline.ReplaceAllStringFunc(s, func(m string) string {
		p := reLinkInline.FindStringSubmatch(m)
		return linkPlano(p[1], p[2])
	})
	if len(defs) > 0 {
		s = reLinkRef.ReplaceAllStringFunc(s, func(m string) string {
			p := reLinkRef.FindStringSubmatch(m)
			texto, ref := p[1], p[2]
			if ref == "" {
				ref = texto
			}
			url, ok := defs[strings.ToLower(strings.TrimSpace(ref))]
			if !ok {
				return m
			}
			return linkPlano(texto, url)
		})
	}
	s = reAutolink.ReplaceAllStringFunc(s, func(m string) string {
		return destinoPlano(reAutolink.FindStringSubmatch(m)[1])
	})
	return reMailto.ReplaceAllString(s, "$1")
}

// linkPlano devolve "texto: destino", ou só o destino quando o texto já é o
// próprio endereço (ou está vazio).
func linkPlano(texto, destino string) string {
	texto, destino = strings.TrimSpace(texto), destinoPlano(destino)
	if texto == "" || enderecoNu(texto) == enderecoNu(destino) {
		return destino
	}
	return texto + ": " + destino
}

// destinoPlano tira o que é coisa de href: `mailto:`/`tel:` e os `<>`.
func destinoPlano(destino string) string {
	destino = strings.Trim(strings.TrimSpace(destino), "<>")
	for _, esquema := range []string{"mailto:", "tel:"} {
		if len(destino) > len(esquema) && strings.EqualFold(destino[:len(esquema)], esquema) {
			return destino[len(esquema):]
		}
	}
	return destino
}

// enderecoNu normaliza para comparar texto e destino: sem esquema, sem `/`
// final, minúsculo.
func enderecoNu(s string) string {
	s = strings.ToLower(destinoPlano(s))
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	return strings.TrimRight(s, "/")
}

// espacoJS replica o `\s` do JavaScript (a classe das regex da Evolution):
// os espaços Unicode + U+FEFF. Marcador encostado nesses chars não casa lá.
func espacoJS(r rune) bool {
	return unicode.IsSpace(r) || r == 0xFEFF
}

// equilibrar aplica equilibrarLinha a cada linha: as regex da Evolution (e o
// WhatsApp) só casam um par de marcadores na MESMA linha.
func equilibrar(s string) string {
	linhas := strings.Split(s, "\n")
	for i, l := range linhas {
		linhas[i] = equilibrarLinha(l)
	}
	return strings.Join(linhas, "\n")
}

// equilibrarLinha deixa a linha no dialeto que as regex da Evolution convertem
// por inteiro: negrito `**x**` (dois ou mais asteriscos contam como um
// marcador) e riscado `~~x~~`, pareados da esquerda para a direita (ver
// parear); o par só vale sem `*` dentro (a regex de ambos exclui `*`).
// Marcador sem par some. Fora de um negrito, `*x*` simples (exatamente o que
// a regex de itálico da Evolution casaria) vira `**x**`; dentro, vira `_x_`.
// Repete até estabilizar: o passo do riscado pode mudar um negrito e vice-versa.
func equilibrarLinha(linha string) string {
	for i := 0; i < 8; i++ {
		nova := passoEquilibrar(linha)
		if nova == linha {
			break
		}
		linha = nova
	}
	return linha
}

func passoEquilibrar(linha string) string {
	r := []rune(linha)
	var out []rune
	segs, _ := parear(r, '*')
	for _, seg := range segs {
		if !seg.marcado {
			out = append(out, converterSimples(seg.texto, []rune("**"))...)
			continue
		}
		conteudo := converterSimples(seg.texto, []rune("_"))
		out = append(out, envolver(conteudo, []rune("**"), '*')...)
	}
	r, out = out, nil
	segs, _ = parear(r, '~')
	for _, seg := range segs {
		if !seg.marcado {
			out = append(out, seg.texto...)
			continue
		}
		out = append(out, envolver(seg.texto, []rune("~~"), '*')...)
	}
	return string(out)
}

// segmento é um trecho da linha: texto solto ou conteúdo entre um par de
// marcadores (já sem os marcadores).
type segmento struct {
	texto   []rune
	marcado bool
}

// parear fatia a linha em segmentos pelos marcadores (2+ `marcador`
// consecutivos). Como no CommonMark e nas regex da Evolution, um marcador só
// ABRE se vier colado ao texto à direita e só FECHA se vier colado ao texto à
// esquerda; pareia da esquerda para a direita. Marcador que não pareia some.
// `aberto` diz se a linha termina com um marcador aberto sem fechamento (ele
// também é removido dos segmentos) — QuebrarMensagem usa isso.
func parear(r []rune, marcador rune) (segs []segmento, aberto bool) {
	type trecho struct{ ini, fim int } // posição de cada marcador em r
	var marcadores []trecho
	for i := 0; i < len(r); {
		if r[i] != marcador || i+1 >= len(r) || r[i+1] != marcador {
			i++
			continue
		}
		fim := i
		for fim < len(r) && r[fim] == marcador {
			fim++
		}
		marcadores = append(marcadores, trecho{i, fim})
		i = fim
	}
	abre := func(m trecho) bool { return m.fim < len(r) && !espacoJS(r[m.fim]) }
	fecha := func(m trecho) bool { return m.ini > 0 && !espacoJS(r[m.ini-1]) }

	// 1) Papel de cada marcador: abre (pareado), fecha (pareado) ou some.
	const some, abreM, fechaM = 0, 1, 2
	papel := make([]int, len(marcadores))
	pendente := -1 // índice do abridor à espera de fechamento
	for i, m := range marcadores {
		switch {
		case pendente >= 0 && fecha(m):
			papel[pendente], papel[i] = abreM, fechaM
			pendente = -1
		case abre(m):
			pendente = i // o abridor anterior sem fechamento (se havia) some
		}
	}
	aberto = pendente >= 0

	// 2) Segmentos: texto solto / conteúdo de par, sem os marcadores que somem.
	var texto []rune
	ini := 0
	for i, m := range marcadores {
		texto = append(texto, r[ini:m.ini]...)
		ini = m.fim
		if papel[i] != some {
			segs = append(segs, segmento{texto: texto, marcado: papel[i] == fechaM})
			texto = nil
		}
	}
	texto = append(texto, r[ini:]...)
	return append(segs, segmento{texto: texto}), aberto
}

// envolver devolve conteúdo entre marcadores; com `proibido` dentro (a regex
// da Evolution não casaria) sai sem marcadores.
func envolver(conteudo, marcador []rune, proibido rune) []rune {
	if contem(conteudo, proibido) {
		return conteudo
	}
	out := append([]rune{}, marcador...)
	out = append(out, conteudo...)
	return append(out, marcador...)
}

func contem(r []rune, c rune) bool {
	for _, x := range r {
		if x == c {
			return true
		}
	}
	return false
}

// converterSimples troca cada `*x*` que a regex de itálico da Evolution
// casaria (`(?<!\*)\*((?!\s)([^\n*]+?)(?<!\s))\*(?!\*)`: asteriscos solitários,
// conteúdo colado neles, sem `*` nem quebra de linha) por `novo`+x+`novo`.
// Varre como a regex: da esquerda para a direita, sem sobreposição.
func converterSimples(r []rune, novo []rune) []rune {
	out := make([]rune, 0, len(r))
	for i := 0; i < len(r); {
		if r[i] != '*' || (i > 0 && r[i-1] == '*') || i+1 >= len(r) || r[i+1] == '*' || espacoJS(r[i+1]) {
			out = append(out, r[i])
			i++
			continue
		}
		j := i + 1
		for j < len(r) && r[j] != '*' && r[j] != '\n' {
			j++
		}
		if j >= len(r) || r[j] != '*' || espacoJS(r[j-1]) || (j+1 < len(r) && r[j+1] == '*') {
			out = append(out, r[i])
			i++
			continue
		}
		out = append(out, novo...)
		out = append(out, r[i+1:j]...)
		out = append(out, novo...)
		i = j + 1
	}
	return out
}

// reBolha casa uma linha-delimitadora de bolha: nada além de 3+ hífens. É o
// marcador que o SYSTEM_PROMPT_AGENTE instrui o LLM a colocar entre mensagens.
var reBolha = regexp.MustCompile(`(?m)^[ \t]*-{3,}[ \t]*$`)

// DividirBolhas separa a resposta do cérebro nas "bolhas" marcadas com uma
// linha `---` (contrato com o prompt do agente): cada bolha vira uma mensagem
// separada no chat, imitando o ritmo de quem digita. Sem marcador, devolve o
// texto inteiro como bolha única; partes vazias são descartadas.
func DividirBolhas(s string) []string {
	var bolhas []string
	for _, b := range reBolha.Split(s, -1) {
		if b = strings.TrimSpace(b); b != "" {
			bolhas = append(bolhas, b)
		}
	}
	return bolhas
}

// QuebrarMensagem divide um texto em partes de até max runas, preferindo
// quebrar em parágrafo (\n\n), depois em linha, depois em espaço. max <= 0
// devolve o texto inteiro em uma parte. Um negrito/riscado cortado ao meio é
// fechado no fim da parte e reaberto no começo da seguinte (cabendo em max);
// cada parte sai balanceada (ver equilibrar).
func QuebrarMensagem(s string, max int) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if max <= 0 || len([]rune(s)) <= max {
		return []string{s}
	}
	var partes []string
	resto := []rune(s)
	for len(resto) > max {
		corte, parte, fecho := cortarBalanceado(resto, max)
		if parte != "" {
			partes = append(partes, parte)
		}
		resto = []rune(strings.TrimSpace(string(resto[corte:])))
		if fecho != "" {
			resto = append([]rune(reabertura(fecho)), resto...)
		}
	}
	if sobra := strings.TrimSpace(equilibrar(string(resto))); sobra != "" {
		partes = append(partes, sobra)
	}
	return partes
}

// cortarBalanceado escolhe o corte (acharCorte) sem partir um marcador ao
// meio e, se ele cair dentro de um negrito/riscado, fecha o marcador no fim da
// parte — procurando um corte mais curto para o fechamento caber em max. Se
// não couber de jeito nenhum, a parte sai só balanceada (marcador descartado).
func cortarBalanceado(resto []rune, max int) (corte int, parte, fecho string) {
	limite := max
	for {
		corte = acharCorte(resto, limite)
		for corte > 1 && corte < len(resto) && resto[corte] == resto[corte-1] && (resto[corte] == '*' || resto[corte] == '~') {
			corte--
		}
		parte = strings.TrimSpace(string(resto[:corte]))
		fecho = fechamentos(parte)
		if fecho == "" {
			return corte, equilibrar(parte), ""
		}
		if len([]rune(parte))+len(fecho) <= max {
			return corte, equilibrar(parte + fecho), fecho
		}
		if limite -= len(fecho); limite <= len(fecho) {
			return corte, strings.TrimSpace(equilibrar(parte)), ""
		}
	}
}

// fechamentos devolve os marcadores que ficaram abertos na última linha de
// parte (na ordem em que devem ser fechados: o último aberto primeiro).
func fechamentos(parte string) string {
	ultima := parte
	if i := strings.LastIndex(parte, "\n"); i >= 0 {
		ultima = parte[i+1:]
	}
	var abertos []string
	for _, m := range []rune{'*', '~'} {
		if _, aberto := parear([]rune(ultima), m); aberto {
			abertos = append([]string{string([]rune{m, m})}, abertos...)
		}
	}
	return strings.Join(abertos, "")
}

// reabertura inverte a ordem dos fechamentos para reabrir na parte seguinte.
func reabertura(fecho string) string {
	var out string
	for i := 0; i+2 <= len(fecho); i += 2 {
		out = fecho[i:i+2] + out
	}
	return out
}

// acharCorte devolve o índice de corte ≤ max, preferindo \n\n > \n > espaço
// (procurando de trás para frente, sem subir além da metade da janela). Sem
// separador razoável, corta seco em max.
func acharCorte(r []rune, max int) int {
	for i := max - 1; i > max/2; i-- {
		if r[i] == '\n' && r[i-1] == '\n' {
			return i + 1
		}
	}
	for i := max - 1; i > max/2; i-- {
		if r[i] == '\n' {
			return i + 1
		}
	}
	for i := max - 1; i > max/2; i-- {
		if r[i] == ' ' {
			return i + 1
		}
	}
	return max
}
