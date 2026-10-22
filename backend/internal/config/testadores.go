package config

import (
	"fmt"
	"strings"
)

// Lista de testadores (E3.9): homologação em produção sem atingir clientes
// reais. O Infodesk não tem ambiente de teste e a flag "Conduz o atendimento"
// da chave vale para a organização inteira (docs/infodesk/08 e 10 §4); com a
// lista ligada, só estes números falam com a Sofia e todos os outros vão direto
// para um atendente.

// ChaveTelefone devolve a chave de comparação de um telefone, igual à do lookup
// do Infodesk para números brasileiros: com ou sem +55 e com ou sem o 9º dígito
// dos celulares dão a mesma chave. Número que não é brasileiro (com `+` e outro
// código de país) compara pelo E.164 inteiro. ok=false quando o texto não é um
// telefone (só dígitos, com `+` opcional na frente; 10 a 15 dígitos; espaço,
// hífen, ponto e parênteses são ignorados).
func ChaveTelefone(s string) (string, bool) {
	limpo := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '-', '.', '(', ')':
			return -1
		}
		return r
	}, s)
	mais := strings.HasPrefix(limpo, "+")
	digitos := strings.TrimPrefix(limpo, "+")
	if len(digitos) < 10 || len(digitos) > 15 {
		return "", false
	}
	for _, r := range digitos {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	if mais && !strings.HasPrefix(digitos, "55") {
		return "+" + digitos, true
	}
	nacional := digitos
	if strings.HasPrefix(digitos, "55") && (len(digitos) == 12 || len(digitos) == 13) {
		nacional = digitos[2:]
	}
	switch len(nacional) {
	case 10:
		return "br:" + nacional, true
	case 11:
		if nacional[2] == '9' { // DDD + 9 + 8 dígitos: o 9º dígito do celular some da chave
			return "br:" + nacional[:2] + nacional[3:], true
		}
		return "br:" + nacional, true
	}
	return "+" + digitos, true
}

// parseTestadores lê TESTADORES_TELEFONES (separados por vírgula; vazio =
// lista desligada). Item que não é telefone derruba o boot citando a posição.
func parseTestadores(bruto string) ([]string, error) {
	var out []string
	for i, item := range strings.Split(bruto, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := ChaveTelefone(item); !ok {
			return nil, fmt.Errorf("TESTADORES_TELEFONES inválido: o item %d não é um telefone (use E.164, ex.: +5531990000000)", i+1)
		}
		out = append(out, item)
	}
	return out, nil
}

// TestadoresAtivo diz se a lista de testadores está ligada.
func (c *Config) TestadoresAtivo() bool { return len(c.TestadoresTelefones) > 0 }

// Testadores devolve o conjunto de chaves (ChaveTelefone) dos testadores; vazio
// = lista desligada. Números repetidos ou variantes do mesmo número contam uma vez.
func (c *Config) Testadores() map[string]struct{} {
	set := make(map[string]struct{}, len(c.TestadoresTelefones))
	for _, t := range c.TestadoresTelefones {
		if k, ok := ChaveTelefone(t); ok {
			set[k] = struct{}{}
		}
	}
	return set
}
