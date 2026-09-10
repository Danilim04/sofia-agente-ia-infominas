// Formatação WhatsApp-native e higiene de envelope (F2/F3/F12 do laudo).
//
// Entrada: StripAssinatura remove a assinatura que relays (ex.: Evolution)
// grudam no texto ("**Fulano:**\n...") — o gate e o cérebro devem ver a FALA
// do cliente, nunca o envelope do transporte.
//
// Saída: FormatWhatsApp converte o markdown que o LLM emite para o dialeto do
// WhatsApp (**x** → *x*, [t](u) → t: u, sem títulos #), e QuebrarMensagem
// divide respostas longas em partes que cabem numa bolha de chat.
package engine

import (
	"regexp"
	"strings"
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
	reBold    = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	reHeading = regexp.MustCompile(`(?m)^#{1,6}[ \t]+(.*)$`)
	reLink    = regexp.MustCompile(`\[([^\]\n]+)\]\(([^)\s]+)\)`)
)

// FormatWhatsApp converte markdown comum para o dialeto do WhatsApp:
//   - **negrito** → *negrito* (WhatsApp usa asterisco simples)
//   - # Título   → *Título*
//   - [texto](url) → "texto: url" (WhatsApp não renderiza link markdown)
func FormatWhatsApp(s string) string {
	s = reBold.ReplaceAllString(s, "*$1*")
	s = reHeading.ReplaceAllString(s, "*$1*")
	s = reLink.ReplaceAllStringFunc(s, func(m string) string {
		parts := reLink.FindStringSubmatch(m)
		texto, url := strings.TrimSpace(parts[1]), parts[2]
		if texto == "" || texto == url {
			return url
		}
		return texto + ": " + url
	})
	return strings.TrimSpace(s)
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
// devolve o texto inteiro em uma parte.
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
		corte := acharCorte(resto, max)
		parte := strings.TrimSpace(string(resto[:corte]))
		if parte != "" {
			partes = append(partes, parte)
		}
		resto = []rune(strings.TrimSpace(string(resto[corte:])))
	}
	if sobra := strings.TrimSpace(string(resto)); sobra != "" {
		partes = append(partes, sobra)
	}
	return partes
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
