package config

import (
	"strings"
	"testing"
	"time"
)

// comObrigatorias define o mínimo para Load passar das validações.
func comObrigatorias(t *testing.T) {
	t.Helper()
	t.Setenv("INFODESK_BASE_URL", "https://infodesk.test/api/integration/v1/")
	t.Setenv("INFODESK_API_KEY", "chave-de-teste")
}

// TestObrigatoriasAusentes: sem INFODESK_BASE_URL/INFODESK_API_KEY o boot
// falha com a mensagem citando as duas variáveis.
func TestObrigatoriasAusentes(t *testing.T) {
	t.Setenv("INFODESK_BASE_URL", "")
	t.Setenv("INFODESK_API_KEY", "")
	_, err := Load()
	if err == nil {
		t.Fatal("esperava erro sem as obrigatórias")
	}
	for _, v := range []string{"INFODESK_BASE_URL", "INFODESK_API_KEY"} {
		if !strings.Contains(err.Error(), v) {
			t.Fatalf("erro devia citar %s: %v", v, err)
		}
	}
}

// TestDefaultsDoGateway: defaults de docs/plano/01-ferramentas.md e a barra
// final da base URL removida.
func TestDefaultsDoGateway(t *testing.T) {
	comObrigatorias(t)
	for _, v := range []string{"INFODESK_TIMEOUT", "REFERENCIA_CACHE_TTL", "DEPARTAMENTO_HUMANO_PADRAO", "INFODESK_WEBHOOK_SECRET", "TOOLS_API_TOKEN", "PORT"} {
		t.Setenv(v, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if cfg.InfodeskBaseURL != "https://infodesk.test/api/integration/v1" {
		t.Fatalf("base URL devia perder a barra final: %q", cfg.InfodeskBaseURL)
	}
	if cfg.InfodeskTimeout != 15*time.Second || cfg.ReferenciaCacheTTL != 10*time.Minute {
		t.Fatalf("defaults do Infodesk: timeout=%v ttl=%v", cfg.InfodeskTimeout, cfg.ReferenciaCacheTTL)
	}
	if cfg.DepartamentoHumanoPadrao != "" || cfg.InfodeskWebhookSecret != "" || cfg.ToolsAPIToken != "" {
		t.Fatalf("opcionais deviam ficar vazias: %+v", cfg)
	}
	if cfg.Port != "8080" || cfg.TurnoTeto != 45*time.Second || cfg.DebounceJanela != 8*time.Second || cfg.ReplyMaxChars != 900 {
		t.Fatalf("defaults de transporte inesperados: %+v", cfg)
	}
}

// TestDuracaoInvalidaCitaAVariavel: valor malformado não vira 0 em silêncio.
func TestDuracaoInvalidaCitaAVariavel(t *testing.T) {
	comObrigatorias(t)
	t.Setenv("INFODESK_TIMEOUT", "quinze")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "INFODESK_TIMEOUT") {
		t.Fatalf("esperava erro citando INFODESK_TIMEOUT, veio %v", err)
	}
}

// TestValoresLidos: as variáveis novas chegam ao Config.
func TestValoresLidos(t *testing.T) {
	comObrigatorias(t)
	t.Setenv("INFODESK_ORGANIZATION_ID", "org-1")
	t.Setenv("INFODESK_WEBHOOK_SECRET", "segredo")
	t.Setenv("DEPARTAMENTO_HUMANO_PADRAO", "dept-9")
	t.Setenv("INFODESK_TIMEOUT", "3s")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if cfg.InfodeskOrganizationID != "org-1" || cfg.InfodeskWebhookSecret != "segredo" ||
		cfg.DepartamentoHumanoPadrao != "dept-9" || cfg.InfodeskTimeout != 3*time.Second {
		t.Fatalf("valores não lidos: %+v", cfg)
	}
}

// TestIdentificacaoEWebhookDefaults: hipóteses de plano configuráveis (07 N3,
// ADR 0004 revisado) e a janela do webhook.
func TestIdentificacaoEWebhookDefaults(t *testing.T) {
	comObrigatorias(t)
	for _, v := range []string{"IDENT_EXIGIR_CPF", "IDENT_LOOKUP_AMBIGUO", "IDENT_TTL", "WEBHOOK_JANELA", "INFODESK_NOME_CHAVE"} {
		t.Setenv(v, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if cfg.IdentExigirCPF || cfg.IdentLookupAmbiguo != "pedir_cpf" || cfg.IdentTTL != 24*time.Hour ||
		cfg.WebhookJanela != 5*time.Minute || cfg.InfodeskNomeChave != "" {
		t.Fatalf("defaults inesperados: %+v", cfg)
	}
}

func TestIdentificacaoEWebhookLidos(t *testing.T) {
	comObrigatorias(t)
	t.Setenv("IDENT_EXIGIR_CPF", "true")
	t.Setenv("IDENT_TTL", "2h")
	t.Setenv("WEBHOOK_JANELA", "90s")
	t.Setenv("INFODESK_NOME_CHAVE", "Sofia")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if !cfg.IdentExigirCPF || cfg.IdentTTL != 2*time.Hour || cfg.WebhookJanela != 90*time.Second || cfg.InfodeskNomeChave != "Sofia" {
		t.Fatalf("valores não lidos: %+v", cfg)
	}
}

// TestLookupAmbiguoDesconhecidoFalha: valor sem implementação não vira
// comportamento inventado em silêncio.
func TestLookupAmbiguoDesconhecidoFalha(t *testing.T) {
	comObrigatorias(t)
	t.Setenv("IDENT_LOOKUP_AMBIGUO", "primeiro")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "IDENT_LOOKUP_AMBIGUO") {
		t.Fatalf("esperava erro citando IDENT_LOOKUP_AMBIGUO, veio %v", err)
	}
}

// variaveisE36 são as variáveis que a E3.6 trouxe para cá; os testes as zeram
// para não herdar o ambiente de quem roda.
var variaveisE36 = []string{
	"RECONCILIA_ATIVO", "RECONCILIA_INTERVALO", "RECONCILIA_RETROATIVO", "RECONCILIA_ATRASO", "RECONCILIA_BACKOFF_MAX",
	"RECONCILIA_SKEW", "BOT_TIMEOUT_HANDOFF", "TICKET_PENDENTE_TTL", "TICKET_FINISH_CONVERSA", "TICKET_RODAPE",
	"TRANSFERIR_APOS_TICKET", "TICKET_DEDUP_JANELA", "DEDUP_CONSULTA_INFODESK", "IDENT_CONTRATO_RE", "IDENT_HASH_SAL",
	"STRICT_TAXONOMIA", "TAXONOMIA_PATH",
}

func limparE36(t *testing.T) {
	t.Helper()
	for _, v := range variaveisE36 {
		t.Setenv(v, "")
	}
}

// TestDefaultsDaE36: reconciliador, tools de ticket e taxonomia nascem com os
// defaults documentados em docs/plano/01 e no .env.example.
func TestDefaultsDaE36(t *testing.T) {
	comObrigatorias(t)
	limparE36(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if !cfg.ReconciliaAtivo || cfg.ReconciliaIntervalo != time.Minute || cfg.ReconciliaRetroativo != time.Hour ||
		cfg.ReconciliaAtraso != 2*time.Minute || cfg.ReconciliaBackoffMax != 5*time.Minute || cfg.ReconciliaSkew != time.Minute ||
		cfg.BotTimeoutHandoff != 5*time.Minute || cfg.TicketPendenteTTL != 10*time.Minute {
		t.Fatalf("defaults do reconciliador: %+v", cfg)
	}
	if cfg.TicketFinishConversa || !cfg.TicketRodape || !cfg.TransferirAposTicket || !cfg.DedupConsultaInfodesk ||
		cfg.TicketDedupJanela != 24*time.Hour || cfg.IdentContratoRE != `^\d{4,12}$` || cfg.IdentHashSal != "" {
		t.Fatalf("defaults das tools de ticket: %+v", cfg)
	}
	if cfg.StrictTaxonomia || cfg.TaxonomiaPath != "" {
		t.Fatalf("defaults da taxonomia: %+v", cfg)
	}
}

func TestValoresDaE36Lidos(t *testing.T) {
	comObrigatorias(t)
	limparE36(t)
	for k, v := range map[string]string{
		"RECONCILIA_ATIVO": "false", "RECONCILIA_INTERVALO": "30s", "RECONCILIA_RETROATIVO": "2h", "RECONCILIA_ATRASO": "90s",
		"RECONCILIA_BACKOFF_MAX": "10m", "RECONCILIA_SKEW": "2m", "BOT_TIMEOUT_HANDOFF": "3m", "TICKET_PENDENTE_TTL": "15m",
		"TICKET_FINISH_CONVERSA": "true", "TICKET_RODAPE": "false", "TRANSFERIR_APOS_TICKET": "off",
		"TICKET_DEDUP_JANELA": "12h", "DEDUP_CONSULTA_INFODESK": "0", "IDENT_CONTRATO_RE": `^C\d{6}$`,
		"IDENT_HASH_SAL": "sal-proprio", "STRICT_TAXONOMIA": "sim", "TAXONOMIA_PATH": " /etc/sofia/taxonomia.yaml ",
	} {
		t.Setenv(k, v)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if cfg.ReconciliaAtivo || cfg.ReconciliaIntervalo != 30*time.Second || cfg.ReconciliaRetroativo != 2*time.Hour ||
		cfg.ReconciliaAtraso != 90*time.Second || cfg.ReconciliaBackoffMax != 10*time.Minute || cfg.ReconciliaSkew != 2*time.Minute ||
		cfg.BotTimeoutHandoff != 3*time.Minute || cfg.TicketPendenteTTL != 15*time.Minute {
		t.Fatalf("reconciliador não lido: %+v", cfg)
	}
	if !cfg.TicketFinishConversa || cfg.TicketRodape || cfg.TransferirAposTicket || cfg.DedupConsultaInfodesk ||
		cfg.TicketDedupJanela != 12*time.Hour || cfg.IdentContratoRE != `^C\d{6}$` || cfg.IdentHashSal != "sal-proprio" {
		t.Fatalf("tools de ticket não lidas: %+v", cfg)
	}
	if !cfg.StrictTaxonomia || cfg.TaxonomiaPath != "/etc/sofia/taxonomia.yaml" {
		t.Fatalf("taxonomia não lida: %+v", cfg)
	}
}

// TestValoresInvalidosDaE36Falham: valor malformado, zero ou negativo derruba o
// boot citando a variável (nada de cair em silêncio no default).
func TestValoresInvalidosDaE36Falham(t *testing.T) {
	casos := []struct{ var_, valor string }{
		{"RECONCILIA_INTERVALO", "um minuto"},
		{"RECONCILIA_INTERVALO", "0s"},
		{"RECONCILIA_RETROATIVO", "-1h"},
		{"RECONCILIA_ATRASO", "0"},
		{"RECONCILIA_BACKOFF_MAX", "xyz"},
		{"RECONCILIA_SKEW", "-5s"},
		{"BOT_TIMEOUT_HANDOFF", "invalido"},
		{"TICKET_PENDENTE_TTL", "0s"},
		{"TICKET_DEDUP_JANELA", "-24h"},
		{"RECONCILIA_ATIVO", "talvez"},
		{"TICKET_FINISH_CONVERSA", "ture"},
		{"TICKET_RODAPE", "2"},
		{"TRANSFERIR_APOS_TICKET", "?"},
		{"DEDUP_CONSULTA_INFODESK", "nunca"},
		{"STRICT_TAXONOMIA", "ligado"},
		{"IDENT_CONTRATO_RE", "(abre"},
	}
	for _, c := range casos {
		t.Run(c.var_+"="+c.valor, func(t *testing.T) {
			comObrigatorias(t)
			limparE36(t)
			t.Setenv(c.var_, c.valor)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), c.var_) {
				t.Fatalf("esperava erro citando %s, veio %v", c.var_, err)
			}
		})
	}
}

// TestAvisoDoSalDoHashVazio: IDENT_HASH_SAL vazio funciona, mas o boot avisa
// que rotacionar o TOOLS_API_TOKEN invalida os hashes — e só quando a API de
// tools está exposta.
func TestAvisoDoSalDoHashVazio(t *testing.T) {
	comObrigatorias(t)
	limparE36(t)

	t.Setenv("TOOLS_API_TOKEN", "tok")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	av := cfg.Avisos()
	if len(av) != 1 || !strings.Contains(av[0], "IDENT_HASH_SAL") || !strings.Contains(av[0], "TOOLS_API_TOKEN") ||
		!strings.Contains(av[0], "invalida") {
		t.Fatalf("avisos = %v", av)
	}

	t.Setenv("IDENT_HASH_SAL", "sal")
	if cfg, _ = Load(); len(cfg.Avisos()) != 0 {
		t.Fatalf("com sal próprio não há aviso: %v", cfg.Avisos())
	}

	t.Setenv("IDENT_HASH_SAL", "")
	t.Setenv("TOOLS_API_TOKEN", "")
	if cfg, _ = Load(); len(cfg.Avisos()) != 0 {
		t.Fatalf("sem a API de tools o sal não importa: %v", cfg.Avisos())
	}
}
