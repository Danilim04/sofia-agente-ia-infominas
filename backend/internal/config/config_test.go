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
