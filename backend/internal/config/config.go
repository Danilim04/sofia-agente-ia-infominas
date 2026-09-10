// Package config carrega a configuração da aplicação a partir de variáveis de
// ambiente (com suporte opcional a um arquivo .env).
package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
)

// Config agrega todos os parâmetros de execução do gateway.
type Config struct {
	Port          string
	LogLevel      string // debug | info | warn | error
	PublicBaseURL string // URL pública (só para montar a URL do webhook nos logs)

	ChatwootBaseURL   string
	ChatwootAccountID string
	ChatwootAPIToken  string
	WebhookSecret     string
	WebhookToken      string // token na query da URL do webhook (?token=...)

	// Persistência do gateway (dedup, estado do gate, cache telefone→perfil).
	// PGURL preenchido = Postgres (o MESMO do RAG/pgvector, schema `gateway`);
	// vazio = SQLite em DBPath (dev/local).
	DBPath string
	PGURL  string

	// LabelBot: só processa conversas com esta etiqueta (gate de borda). Vazio =
	// processa toda mensagem de entrada do contato. LabelHumano: fila para onde a
	// conversa é roteada quando a identificação falha.
	LabelBot    string
	LabelHumano string
	// InboxID: caixa de entrada cujas conversas NOVAS ganham a LabelBot no
	// conversation_created (o bot mesmo etiqueta — sem automation rule no
	// Chatwoot, que o omni-route não gerencia). 0 = todas as caixas.
	InboxID int64

	// Mongo da Azapfy (lookup de usuário por login — a 1ª "tool").
	MongoURI        string
	MongoDB         string
	MongoCollection string
	MongoTimeout    time.Duration

	// Brain: serviço Python que roda o agente (Contrato A, POST /chat).
	BrainBaseURL string
	BrainTimeout time.Duration

	// Identidade / gate.
	ConfirmField  string        // dado pedido na confirmação: "email" (default) | "nome"
	MaxTentativas int           // tentativas de login/confirmação antes de rotear p/ humano
	IdentityTTL   time.Duration // validade do cache telefone→perfil (base própria)
	GateFalhaTTL  time.Duration // após esse tempo, GateFalha expira e a identificação recomeça (F1)

	// Robustez de transporte (laudo 2026-08-27).
	DebounceJanela time.Duration // silêncio que fecha uma rajada de mensagens (coalescência, F11)
	DebounceTeto   time.Duration // espera máxima acumulada antes de processar mesmo sem silêncio
	EventoIdadeMax time.Duration // mensagens mais velhas que isso são descartadas (F10); 0 = sem corte
	ReplyMaxChars  int           // tamanho-alvo de cada mensagem enviada ao WhatsApp (F12); 0 = sem quebra
	BolhaPausa     time.Duration // pausa entre bolhas consecutivas do mesmo turno (ritmo humano); 0 = sem pausa

	// SAC (atendimento/chamados): tools de dados que o cérebro chama via toolsapi.
	// Tudo opcional — sem SAC_BASE_URL a API de tools não é exposta.
	SACBaseURL    string        // raiz do backend SAC (PHP)
	SACPortalURL  string        // raiz do portal p/ montar o link do chamado
	SACServiceCod string        // login privilegiado p/ definir prioridade (editar)
	SACGrupoEmp   string        // desk onde os chamados são abertos (ex.: AZAPERS)
	SACEmpresa    string        // incidente.empresa (ex.: AZAPFY)
	SACTimezone   string        // ex.: America/Sao_Paulo
	SACAPIToken   string        // bearer opcional do backend SAC
	SACConfigTTL  time.Duration // validade do cache de configuração (categorias/ocorrências)
	ToolsAPIToken string        // segredo compartilhado cérebro↔gateway (X-Tools-Token)
}

// Load lê o .env (se existir) e em seguida o ambiente, validando os obrigatórios.
func Load() (*Config, error) {
	_ = loadDotEnv(".env")

	cfg := &Config{
		Port:              getenv("PORT", "8080"),
		LogLevel:          strings.ToLower(getenv("LOG_LEVEL", "info")),
		PublicBaseURL:     strings.TrimRight(getenv("PUBLIC_BASE_URL", ""), "/"),
		ChatwootBaseURL:   strings.TrimRight(getenv("CHATWOOT_BASE_URL", ""), "/"),
		ChatwootAccountID: getenv("CHATWOOT_ACCOUNT_ID", ""),
		ChatwootAPIToken:  getenv("CHATWOOT_API_TOKEN", ""),
		WebhookSecret:     getenv("WEBHOOK_SECRET", ""),
		WebhookToken:      getenv("WEBHOOK_TOKEN", ""),
		DBPath:            getenv("DB_PATH", "gateway.db"),
		PGURL:             strings.TrimSpace(getenv("PG_URL", "")),
		LabelBot:          getenv("LABEL_BOT", "fila-bot"),
		LabelHumano:       getenv("LABEL_HUMANO", "fila-humano"),
		MongoURI:          getenv("MONGO_URI", ""),
		MongoDB:           getenv("MONGO_DB", "azapfy"),
		MongoCollection:   getenv("MONGO_COLLECTION", "users"),
		BrainBaseURL:      strings.TrimRight(getenv("BRAIN_BASE_URL", "http://localhost:8001"), "/"),
		ConfirmField:      strings.ToLower(getenv("CONFIRM_FIELD", "email")),
		InboxID:           int64(getenvInt("INBOX_ID", 0)),
		MaxTentativas:     getenvInt("MAX_TENTATIVAS", 3),
		SACBaseURL:        strings.TrimRight(getenv("SAC_BASE_URL", ""), "/"),
		SACPortalURL:      strings.TrimRight(getenv("SAC_PORTAL_URL", "https://atendimento.azapfy.com.br"), "/"),
		SACServiceCod:     getenv("SAC_SERVICE_COD", ""),
		SACGrupoEmp:       getenv("SAC_GRUPO_EMP", "AZAPERS"),
		SACEmpresa:        getenv("SAC_EMPRESA", "AZAPFY"),
		SACTimezone:       getenv("SAC_TIMEZONE", "America/Sao_Paulo"),
		SACAPIToken:       getenv("SAC_API_TOKEN", ""),
		ToolsAPIToken:     getenv("TOOLS_API_TOKEN", ""),
		ReplyMaxChars:     getenvInt("REPLY_MAX_CHARS", 900),
	}

	var err error
	if cfg.MongoTimeout, err = parseDuration(getenv("MONGO_TIMEOUT", "8s")); err != nil {
		return nil, fmt.Errorf("MONGO_TIMEOUT inválido: %w", err)
	}
	if cfg.BrainTimeout, err = parseDuration(getenv("BRAIN_TIMEOUT", "60s")); err != nil {
		return nil, fmt.Errorf("BRAIN_TIMEOUT inválido: %w", err)
	}
	if cfg.IdentityTTL, err = parseDuration(getenv("IDENTITY_TTL", "24h")); err != nil {
		return nil, fmt.Errorf("IDENTITY_TTL inválido: %w", err)
	}
	if cfg.SACConfigTTL, err = parseDuration(getenv("SAC_CONFIG_TTL", "10m")); err != nil {
		return nil, fmt.Errorf("SAC_CONFIG_TTL inválido: %w", err)
	}
	if cfg.GateFalhaTTL, err = parseDuration(getenv("GATE_FALHA_TTL", "1h")); err != nil {
		return nil, fmt.Errorf("GATE_FALHA_TTL inválido: %w", err)
	}
	if cfg.DebounceJanela, err = parseDuration(getenv("DEBOUNCE_JANELA", "8s")); err != nil {
		return nil, fmt.Errorf("DEBOUNCE_JANELA inválido: %w", err)
	}
	if cfg.DebounceTeto, err = parseDuration(getenv("DEBOUNCE_TETO", "20s")); err != nil {
		return nil, fmt.Errorf("DEBOUNCE_TETO inválido: %w", err)
	}
	if cfg.EventoIdadeMax, err = parseDuration(getenv("EVENTO_IDADE_MAX", "10m")); err != nil {
		return nil, fmt.Errorf("EVENTO_IDADE_MAX inválido: %w", err)
	}
	if cfg.BolhaPausa, err = parseDuration(getenv("BOLHA_PAUSA", "1500ms")); err != nil {
		return nil, fmt.Errorf("BOLHA_PAUSA inválido: %w", err)
	}

	var missing []string
	if cfg.ChatwootBaseURL == "" {
		missing = append(missing, "CHATWOOT_BASE_URL")
	}
	if cfg.ChatwootAccountID == "" {
		missing = append(missing, "CHATWOOT_ACCOUNT_ID")
	}
	if cfg.ChatwootAPIToken == "" {
		missing = append(missing, "CHATWOOT_API_TOKEN")
	}
	if cfg.MongoURI == "" {
		missing = append(missing, "MONGO_URI")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("variáveis obrigatórias ausentes: %s", strings.Join(missing, ", "))
	}

	return cfg, nil
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

func parseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	return time.ParseDuration(s)
}

// loadDotEnv faz um carregamento mínimo de um arquivo .env (KEY=VALUE por linha,
// linhas iniciadas por # são ignoradas). Não sobrescreve variáveis já definidas.
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, val)
		}
	}
	return sc.Err()
}
