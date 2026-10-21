// Package config carrega a configuração do gateway a partir de variáveis de
// ambiente (com suporte opcional a um arquivo .env). Variáveis e defaults:
// docs/plano/01-ferramentas.md (seção B) e .env.example na raiz.
package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
)

// Valores de IDENT_LOOKUP_AMBIGUO.
const IdentAmbiguoPedirCPF = "pedir_cpf"

// Config agrega todos os parâmetros de execução do gateway.
type Config struct {
	Port          string
	LogLevel      string // debug | info | warn | error
	PublicBaseURL string // URL pública (só para montar a URL do webhook nos logs)

	// Infodesk (docs/infodesk/, skill infodesk-api). BaseURL e APIKey são
	// obrigatórias; o resto é opcional.
	InfodeskBaseURL        string        // raiz da API de integração (…/api/integration/v1)
	InfodeskAPIKey         string        // Bearer
	InfodeskWebhookSecret  string        // HMAC-SHA256 do corpo cru do webhook; vazio = sem verificação (só dev)
	InfodeskOrganizationID string        // se preenchido, webhook de outra organização é recusado
	InfodeskTimeout        time.Duration // timeout das chamadas HTTP ao Infodesk (default 15s)
	// ReferenciaCacheTTL: validade do cache de mesas/departamentos/atendentes
	// (GET /desks, /departments, /users). Default 10 min.
	ReferenciaCacheTTL time.Duration
	// DepartamentoHumanoPadrao: departamento do Infodesk que recebe a
	// conversa transferida pelo bot quando a taxonomia não aponta outro.
	// Vazio = a transferência avisa o cliente e registra a ocorrência, mas não
	// move a conversa no canal (erro no log).
	DepartamentoHumanoPadrao string
	// WebhookToken: segredo opcional na query da URL do webhook (?token=...),
	// além do HMAC.
	WebhookToken string
	// WebhookJanela: tolerância de |now − X-Infominas-Timestamp| (docs/infodesk/05:
	// 5 min). Fora dela a entrega é recusada com 401 (possível replay).
	WebhookJanela time.Duration
	// InfodeskNomeChave: nome da chave de API = `senderName` das nossas
	// mensagens (eco `message.sent` a ignorar, docs/infodesk/07 W8). Vazio =
	// usa o `name` de GET /me no boot.
	InfodeskNomeChave string

	// Identificação do cliente (ADR 0004, revisão 2026-10-20; docs/infodesk/07 N3).
	// IdentExigirCPF=false (hipótese de plano): telefone reconhecido pelo
	// lookup dispensa CPF; true restaura "sempre pede". TODO(N3): valor final
	// em produção é decisão de negócio (E5.3).
	IdentExigirCPF bool
	// IdentLookupAmbiguo: o que fazer quando o lookup devolve mais de um
	// solicitante. Único valor suportado hoje: "pedir_cpf" (tratar como não
	// identificado e pedir CPF/contrato).
	IdentLookupAmbiguo string
	// IdentTTL: validade do cache de identificação por telefone (default 24 h).
	IdentTTL time.Duration

	// Persistência do gateway (dedup, ocorrências; E2.1: conversas, contatos,
	// tickets). PGURL preenchido = Postgres (o MESMO do RAG/pgvector, schema
	// `gateway`); vazio = SQLite em DBPath (dev/local).
	DBPath string
	PGURL  string

	// Brain: serviço Python que roda o agente (Contrato A, POST /chat).
	BrainBaseURL string
	BrainTimeout time.Duration
	// TurnoTeto: prazo total do turno desde a MENSAGEM do cliente (coalescência
	// + chamada ao cérebro + novas tentativas). Passado o teto, não há nova
	// tentativa e a conversa é transferida (ZC-5). Default 45s.
	TurnoTeto time.Duration

	// Alertas ao responsável (ZC-7): a decisão de QUANDO alertar é do pacote
	// alerta; o destino é o log `sofia_alerta` + este webhook opcional
	// (POST JSON {"text": ...}). Vazio = só log.
	AlertaWebhookURL string

	// Robustez de transporte (laudo 2026-08-27).
	DebounceJanela time.Duration // silêncio que fecha uma rajada de mensagens (coalescência, F11)
	DebounceTeto   time.Duration // espera máxima acumulada antes de processar mesmo sem silêncio
	EventoIdadeMax time.Duration // mensagens mais velhas que isso são descartadas (F10); 0 = sem corte
	ReplyMaxChars  int           // tamanho-alvo de cada mensagem enviada ao WhatsApp (F12); 0 = sem quebra
	BolhaPausa     time.Duration // pausa entre bolhas consecutivas do mesmo turno (ritmo humano); 0 = sem pausa

	// ToolsAPIToken: segredo compartilhado cérebro↔gateway (X-Tools-Token).
	// Vazio = a API /tools/infodesk/* não é exposta.
	ToolsAPIToken string
}

// Load lê o .env (se existir) e em seguida o ambiente, validando os obrigatórios.
func Load() (*Config, error) {
	_ = loadDotEnv(".env")

	cfg := &Config{
		Port:                     getenv("PORT", "8080"),
		LogLevel:                 strings.ToLower(getenv("LOG_LEVEL", "info")),
		PublicBaseURL:            strings.TrimRight(getenv("PUBLIC_BASE_URL", ""), "/"),
		InfodeskBaseURL:          strings.TrimRight(strings.TrimSpace(getenv("INFODESK_BASE_URL", "")), "/"),
		InfodeskAPIKey:           strings.TrimSpace(getenv("INFODESK_API_KEY", "")),
		InfodeskWebhookSecret:    getenv("INFODESK_WEBHOOK_SECRET", ""),
		InfodeskOrganizationID:   strings.TrimSpace(getenv("INFODESK_ORGANIZATION_ID", "")),
		DepartamentoHumanoPadrao: strings.TrimSpace(getenv("DEPARTAMENTO_HUMANO_PADRAO", "")),
		WebhookToken:             getenv("WEBHOOK_TOKEN", ""),
		AlertaWebhookURL:         strings.TrimSpace(getenv("ALERTA_WEBHOOK_URL", "")),
		DBPath:                   getenv("DB_PATH", "gateway.db"),
		PGURL:                    strings.TrimSpace(getenv("PG_URL", "")),
		BrainBaseURL:             strings.TrimRight(getenv("BRAIN_BASE_URL", "http://localhost:8001"), "/"),
		ToolsAPIToken:            getenv("TOOLS_API_TOKEN", ""),
		ReplyMaxChars:            getenvInt("REPLY_MAX_CHARS", 900),
		InfodeskNomeChave:        strings.TrimSpace(getenv("INFODESK_NOME_CHAVE", "")),
		IdentExigirCPF:           getenvBool("IDENT_EXIGIR_CPF", false),
		IdentLookupAmbiguo:       strings.ToLower(strings.TrimSpace(getenv("IDENT_LOOKUP_AMBIGUO", IdentAmbiguoPedirCPF))),
	}

	duracoes := []struct {
		dst *time.Duration
		env string
		def string
	}{
		{&cfg.InfodeskTimeout, "INFODESK_TIMEOUT", "15s"},
		{&cfg.ReferenciaCacheTTL, "REFERENCIA_CACHE_TTL", "10m"},
		{&cfg.BrainTimeout, "BRAIN_TIMEOUT", "60s"},
		{&cfg.TurnoTeto, "TURNO_TETO", "45s"},
		{&cfg.DebounceJanela, "DEBOUNCE_JANELA", "8s"},
		{&cfg.DebounceTeto, "DEBOUNCE_TETO", "20s"},
		{&cfg.EventoIdadeMax, "EVENTO_IDADE_MAX", "10m"},
		{&cfg.BolhaPausa, "BOLHA_PAUSA", "1500ms"},
		{&cfg.WebhookJanela, "WEBHOOK_JANELA", "5m"},
		{&cfg.IdentTTL, "IDENT_TTL", "24h"},
	}
	for _, d := range duracoes {
		v, err := parseDuration(getenv(d.env, d.def))
		if err != nil {
			return nil, fmt.Errorf("%s inválido: %w", d.env, err)
		}
		*d.dst = v
	}

	if cfg.IdentLookupAmbiguo != IdentAmbiguoPedirCPF {
		return nil, fmt.Errorf("IDENT_LOOKUP_AMBIGUO inválido (%q): só %q é suportado", cfg.IdentLookupAmbiguo, IdentAmbiguoPedirCPF)
	}

	var missing []string
	if cfg.InfodeskBaseURL == "" {
		missing = append(missing, "INFODESK_BASE_URL")
	}
	if cfg.InfodeskAPIKey == "" {
		missing = append(missing, "INFODESK_API_KEY")
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

func getenvBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "t", "sim", "yes", "on":
			return true
		case "0", "false", "f", "nao", "não", "no", "off":
			return false
		}
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
