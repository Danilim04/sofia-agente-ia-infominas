package config

import (
	"strings"
	"testing"
)

// comObrigatorias define o mínimo para Load passar das validações.
func comObrigatorias(t *testing.T) {
	t.Helper()
	t.Setenv("CHATWOOT_BASE_URL", "http://chatwoot.test")
	t.Setenv("CHATWOOT_ACCOUNT_ID", "1")
	t.Setenv("CHATWOOT_API_TOKEN", "token-de-teste")
	t.Setenv("MONGO_URI", "mongodb://mongo.test")
}

// TestInboxIDMalformadoNaoViraZero (ZD-2): INBOX_ID que não é número — por
// exemplo o placeholder "${INBOX_ID}" de um template não renderizado — é erro
// de configuração explícito, nunca 0 (= atender todas as caixas) em silêncio.
func TestInboxIDMalformadoNaoViraZero(t *testing.T) {
	comObrigatorias(t)
	for _, v := range []string{"${INBOX_ID}", "abc", "5x", "-1", "1.5"} {
		t.Setenv("INBOX_ID", v)
		cfg, err := Load()
		if err == nil {
			t.Fatalf("INBOX_ID=%q: esperava erro, veio cfg.InboxID=%d", v, cfg.InboxID)
		}
		if !strings.Contains(err.Error(), "INBOX_ID") {
			t.Fatalf("INBOX_ID=%q: mensagem devia citar a variável: %v", v, err)
		}
	}
}

// TestInboxIDValido: número (com espaços tolerados) é lido; vazio = 0 = todas
// as caixas, sinalizado por AtendeTodasAsCaixas.
func TestInboxIDValido(t *testing.T) {
	comObrigatorias(t)
	casos := map[string]int64{"7": 7, " 12 ": 12, "0": 0, "": 0}
	for v, quer := range casos {
		t.Setenv("INBOX_ID", v)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("INBOX_ID=%q: erro inesperado: %v", v, err)
		}
		if cfg.InboxID != quer {
			t.Fatalf("INBOX_ID=%q: queria %d, veio %d", v, quer, cfg.InboxID)
		}
		if cfg.AtendeTodasAsCaixas() != (quer == 0) {
			t.Fatalf("INBOX_ID=%q: AtendeTodasAsCaixas=%v inesperado", v, cfg.AtendeTodasAsCaixas())
		}
	}
}

// TestBotUserIDOpcional (ZD-1): vazio = 0 (descoberta no boot).
func TestBotUserIDOpcional(t *testing.T) {
	comObrigatorias(t)
	t.Setenv("BOT_USER_ID", "")
	cfg, err := Load()
	if err != nil || cfg.BotUserID != 0 {
		t.Fatalf("sem BOT_USER_ID esperava 0, veio %d (err=%v)", cfg.BotUserID, err)
	}
	t.Setenv("BOT_USER_ID", "3")
	if cfg, err = Load(); err != nil || cfg.BotUserID != 3 {
		t.Fatalf("BOT_USER_ID=3 esperava 3, veio %d (err=%v)", cfg.BotUserID, err)
	}
}
