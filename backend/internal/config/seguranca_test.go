package config

import (
	"strings"
	"testing"
)

// prodSegura prepara um ambiente "de produção": Infodesk real, segredos longos,
// piloto ligado. Cada teste estraga uma coisa só.
func prodSegura(t *testing.T) {
	t.Helper()
	comObrigatorias(t)
	limparE36(t)
	t.Setenv("SOFIA_PRODUCAO_ABERTA", "")
	t.Setenv("TESTADORES_TELEFONES", "+5531990000000")
	t.Setenv("TOOLS_API_TOKEN", segredoLongo)
}

func TestProducaoSeguraSobe(t *testing.T) {
	prodSegura(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("a configuração segura devia subir: %v", err)
	}
	if cfg.ModoDev() || cfg.Modo() != ModoPiloto {
		t.Fatalf("modo dev=%v modo=%s", cfg.ModoDev(), cfg.Modo())
	}
}

// I4: sem segredo do webhook o gateway não sobe, salvo WEBHOOK_SEM_AUTENTICACAO=true.
func TestWebhookSemSegredoFalhaSalvoFlagExplicita(t *testing.T) {
	prodSegura(t)
	t.Setenv("INFODESK_WEBHOOK_SECRET", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "INFODESK_WEBHOOK_SECRET") || !strings.Contains(err.Error(), "WEBHOOK_SEM_AUTENTICACAO") {
		t.Fatalf("esperava erro citando o segredo e a flag, veio %v", err)
	}
	t.Setenv("WEBHOOK_SEM_AUTENTICACAO", "true")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("com a flag explícita sobe: %v", err)
	}
	if !cfg.ModoDev() {
		t.Fatal("a flag explícita é modo dev")
	}
	if av := avisosCom(cfg, "WEBHOOK_SEM_AUTENTICACAO"); len(av) != 1 {
		t.Fatalf("sobe com aviso claro: %v", cfg.Avisos())
	}
	t.Setenv("WEBHOOK_SEM_AUTENTICACAO", "talvez")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "WEBHOOK_SEM_AUTENTICACAO") {
		t.Fatalf("valor inválido da flag derruba o boot: %v", err)
	}
}

// I4: segredos curtos só valem em dev; o erro nunca repete o valor.
func TestSegredosCurtosFalhamForaDoDev(t *testing.T) {
	for _, v := range []string{"INFODESK_WEBHOOK_SECRET", "TOOLS_API_TOKEN"} {
		t.Run(v, func(t *testing.T) {
			prodSegura(t)
			t.Setenv(v, "curto-demais-123")
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), v) || !strings.Contains(err.Error(), "32") {
				t.Fatalf("esperava erro citando %s e o mínimo, veio %v", v, err)
			}
			if strings.Contains(err.Error(), "curto-demais-123") {
				t.Fatalf("o erro não pode repetir o segredo: %v", err)
			}
			// 31 não basta, 32 basta.
			t.Setenv(v, strings.Repeat("a", 31))
			if _, err := Load(); err == nil {
				t.Fatal("31 caracteres é curto")
			}
			t.Setenv(v, strings.Repeat("a", 32))
			if _, err := Load(); err != nil {
				t.Fatalf("32 caracteres basta: %v", err)
			}
		})
	}
	// TOOLS_API_TOKEN vazio = API de tools desligada, não é erro.
	prodSegura(t)
	t.Setenv("TOOLS_API_TOKEN", "")
	if _, err := Load(); err != nil {
		t.Fatalf("sem token a API de tools só não sobe: %v", err)
	}
}

// Dev (Infodesk local, o mock) mantém segredos curtos e lista vazia — o
// harness e o E2E seguem funcionando — com aviso.
func TestModoDevPorInfodeskLocalAceitaSegredoCurtoEListaVazia(t *testing.T) {
	for _, base := range []string{"http://localhost:9000/api", "http://127.0.0.1:9000", "http://[::1]:9000", "http://mock.localhost:9000"} {
		t.Run(base, func(t *testing.T) {
			comObrigatorias(t)
			limparE36(t)
			t.Setenv("INFODESK_BASE_URL", base)
			t.Setenv("INFODESK_WEBHOOK_SECRET", "segredo-mock")
			t.Setenv("TOOLS_API_TOKEN", "dev-token")
			t.Setenv("SOFIA_PRODUCAO_ABERTA", "")
			cfg, err := Load()
			if err != nil {
				t.Fatalf("o harness de dev precisa subir: %v", err)
			}
			if !cfg.ModoDev() {
				t.Fatal("Infodesk local é modo dev")
			}
			if len(avisosCom(cfg, "modo dev")) != 2 {
				t.Fatalf("avisos dos segredos curtos: %v", cfg.Avisos())
			}
		})
	}
	// Host que só PARECE local não é dev.
	comObrigatorias(t)
	limparE36(t)
	t.Setenv("INFODESK_BASE_URL", "https://localhost.exemplo.com")
	t.Setenv("INFODESK_WEBHOOK_SECRET", "segredo-mock")
	if _, err := Load(); err == nil {
		t.Fatal("localhost.exemplo.com não é loopback")
	}
}

// I6: lista vazia só sobe com SOFIA_PRODUCAO_ABERTA=true (fora do dev).
func TestListaVaziaExigeProducaoAberta(t *testing.T) {
	for _, vazia := range []string{"", ",", " , ,", ",,"} {
		t.Run("vazia="+vazia, func(t *testing.T) {
			prodSegura(t)
			t.Setenv("TESTADORES_TELEFONES", vazia)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), "TESTADORES_TELEFONES") || !strings.Contains(err.Error(), "SOFIA_PRODUCAO_ABERTA") {
				t.Fatalf("esperava erro citando a lista e o opt-in, veio %v", err)
			}
			t.Setenv("SOFIA_PRODUCAO_ABERTA", "true")
			cfg, err := Load()
			if err != nil {
				t.Fatalf("com o opt-in sobe: %v", err)
			}
			if cfg.Modo() != ModoAberto || len(avisosCom(cfg, "PRODUÇÃO ABERTA")) != 1 {
				t.Fatalf("modo=%s avisos=%v", cfg.Modo(), cfg.Avisos())
			}
		})
	}
	// Com a lista preenchida o opt-in é desnecessário (e inofensivo).
	prodSegura(t)
	t.Setenv("SOFIA_PRODUCAO_ABERTA", "true")
	if cfg, err := Load(); err != nil || cfg.Modo() != ModoPiloto {
		t.Fatalf("lista preenchida = piloto: %v %v", cfg, err)
	}
	prodSegura(t)
	t.Setenv("SOFIA_PRODUCAO_ABERTA", "quem sabe")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "SOFIA_PRODUCAO_ABERTA") {
		t.Fatalf("valor inválido do opt-in derruba o boot: %v", err)
	}
}
