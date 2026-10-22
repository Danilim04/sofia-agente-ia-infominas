package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Fail-closed da configuração de segurança (E4.5, revisão de segurança v1):
// o gateway recusa subir com o webhook aberto, com segredos fracos ou em
// "produção aberta" sem querer. A exceção é o modo dev (ModoDev), que existe
// para o harness local e o E2E contra o mock-infodesk.

// SegredoMinimo é o tamanho mínimo (caracteres) de TOOLS_API_TOKEN e de
// INFODESK_WEBHOOK_SECRET fora do modo dev. 32 caracteres aleatórios são
// 128+ bits em hex; o Infodesk gera o `whsec_…` com folga acima disso.
const SegredoMinimo = 32

// Modos do atendimento (campo `piloto.modo` do /health).
const (
	ModoPiloto = "piloto" // TESTADORES_TELEFONES preenchida: só os testadores falam com a Sofia
	ModoAberto = "aberto" // lista vazia: todo cliente fala com a Sofia
)

// ModoDev diz se o gateway roda contra um Infodesk LOCAL (o mock do harness e do
// E2E, host loopback): nada ali alcança cliente real. Só neste modo valem
// segredos curtos e a lista de testadores vazia sem SOFIA_PRODUCAO_ABERTA.
// WEBHOOK_SEM_AUTENTICACAO NÃO liga o modo dev: ela só dispensa o segredo do
// webhook; quem pula o HMAC contra um Infodesk real continua sujeito ao
// tamanho dos segredos e ao opt-in de produção aberta.
func (c *Config) ModoDev() bool {
	return baseURLLocal(c.InfodeskBaseURL)
}

// baseURLLocal: o host da URL é loopback — `localhost`, `*.localhost` ou um IP
// loopback (127.0.0.0/8, ::1). Nome que só COMEÇA ou CONTÉM "127." ou
// "localhost" (127.0.0.1.nip.io, localhost.exemplo.com) não é.
func baseURLLocal(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// Modo devolve o modo do atendimento (ModoPiloto ou ModoAberto).
func (c *Config) Modo() string {
	if c.TestadoresAtivo() {
		return ModoPiloto
	}
	return ModoAberto
}

// validarSeguranca aplica as recusas de boot. As mensagens dizem o que fazer e
// nunca repetem o valor do segredo.
func (c *Config) validarSeguranca() error {
	dev := c.ModoDev()
	if c.InfodeskWebhookSecret == "" && !c.WebhookSemAutenticacao {
		return fmt.Errorf("INFODESK_WEBHOOK_SECRET vazio: o webhook ficaria aberto a qualquer um. " +
			"Defina o segredo (HMAC) do webhook do Infodesk; só em dev, WEBHOOK_SEM_AUTENTICACAO=true")
	}
	if !dev {
		if c.InfodeskWebhookSecret != "" && len(c.InfodeskWebhookSecret) < SegredoMinimo {
			return fmt.Errorf("INFODESK_WEBHOOK_SECRET curto demais (%d caracteres): o mínimo é %d fora do modo dev", len(c.InfodeskWebhookSecret), SegredoMinimo)
		}
		if c.ToolsAPIToken != "" && len(c.ToolsAPIToken) < SegredoMinimo {
			return fmt.Errorf("TOOLS_API_TOKEN curto demais (%d caracteres): o mínimo é %d fora do modo dev", len(c.ToolsAPIToken), SegredoMinimo)
		}
	}
	if !c.TestadoresAtivo() && !c.ProducaoAberta && !dev {
		return fmt.Errorf("TESTADORES_TELEFONES vazia: com ela vazia TODO cliente que escrever cai na Sofia, em silêncio. " +
			"Preencha a lista (homologação) ou, para abrir de propósito, SOFIA_PRODUCAO_ABERTA=true")
	}
	return nil
}

// avisosDeSeguranca são os WARN de boot dos modos que funcionam mas expõem
// risco (chamado por Avisos).
func (c *Config) avisosDeSeguranca() []string {
	var out []string
	if c.InfodeskWebhookSecret == "" {
		out = append(out, "WEBHOOK_SEM_AUTENTICACAO=true: o webhook NÃO verifica a assinatura (HMAC); qualquer um que alcance o gateway injeta mensagens. Só dev")
	}
	if c.ModoDev() {
		if c.InfodeskWebhookSecret != "" && len(c.InfodeskWebhookSecret) < SegredoMinimo {
			out = append(out, fmt.Sprintf("modo dev: INFODESK_WEBHOOK_SECRET com %d caracteres (mínimo de %d fora do dev)", len(c.InfodeskWebhookSecret), SegredoMinimo))
		}
		if c.ToolsAPIToken != "" && len(c.ToolsAPIToken) < SegredoMinimo {
			out = append(out, fmt.Sprintf("modo dev: TOOLS_API_TOKEN com %d caracteres (mínimo de %d fora do dev)", len(c.ToolsAPIToken), SegredoMinimo))
		}
	} else if !c.TestadoresAtivo() {
		out = append(out, "PRODUÇÃO ABERTA (SOFIA_PRODUCAO_ABERTA=true, TESTADORES_TELEFONES vazia): TODO cliente que escrever fala com a Sofia")
	}
	if c.Horario == nil && !c.ModoDev() {
		out = append(out, "HORARIO_HUMANO vazio: sem horário de atendimento humano o aviso de fora do horário nunca é usado (se a equipe não atende à noite, o cliente só recebe o aviso normal)")
	}
	return out
}

// lerInteiroPositivo lê uma variável inteira > 0; ausente ou vazia = def; valor
// que não é inteiro positivo = erro citando a variável (nada de cair em silêncio
// num teto diferente do que o operador quis).
func lerInteiroPositivo(key string, def int) (int, error) {
	v := strings.TrimSpace(getenv(key, ""))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s inválido (%q): precisa ser um inteiro maior que zero", key, v)
	}
	return n, nil
}
