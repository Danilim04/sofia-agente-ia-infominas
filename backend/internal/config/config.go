// Package config carrega a configuração do gateway a partir de variáveis de
// ambiente (com suporte opcional a um arquivo .env). Variáveis e defaults:
// docs/plano/01-ferramentas.md (seção B) e .env.example na raiz.
package config

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Valores de IDENT_LOOKUP_AMBIGUO.
const IdentAmbiguoPedirCPF = "pedir_cpf"

// Valores de CADASTRO_SEM_CLIENTE (ADR 0009; hipótese, TODO(decisão do produto)).
const (
	CadastroSemClienteTransferir = "transferir" // handoff com nota por código
	CadastroSemClienteOferecer   = "oferecer"   // só orienta o cliente a falar com um atendente
)

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
	// tentativa e a conversa é transferida (ZC-5). Default 105s. Regra de ordem
	// (E3.7), checada no boot: TURNO_TETO >= DEBOUNCE_TETO + BRAIN_TIMEOUT (o teto
	// conta desde a PRIMEIRA mensagem da rajada, então uma chamada ao cérebro
	// começada no fim da coalescência ainda cabe inteira). BRAIN_TIMEOUT (80s)
	// tem de ser MAIOR que o BRAIN_TURNO_PRAZO do cérebro (70s) — o gateway não o
	// conhece; o cérebro confere do lado dele. A nova tentativa em falha
	// transitória (2 s, 5 s) só cabe quando a rajada foi curta.
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

	// Reconciliador (E3.3). Todos os defaults são hipóteses de trabalho
	// configuráveis (docs/infodesk/05, 5.5; 07 itens A4, N1, W9) e nenhum é
	// segredo.
	ReconciliaAtivo      bool          // RECONCILIA_ATIVO (true): liga o loop
	ReconciliaIntervalo  time.Duration // RECONCILIA_INTERVALO (60s): entre ciclos
	ReconciliaRetroativo time.Duration // RECONCILIA_RETROATIVO (1h): janela da 1ª execução sem cursor
	ReconciliaAtraso     time.Duration // RECONCILIA_ATRASO (2m): o cursor persistido anda este tempo atrás
	ReconciliaBackoffMax time.Duration // RECONCILIA_BACKOFF_MAX (5m): teto da espera após RATE_LIMITED e entre repetições de handoff
	ReconciliaSkew       time.Duration // RECONCILIA_SKEW (60s): tolerância entre o relógio do Infodesk e o nosso
	BotTimeoutHandoff    time.Duration // BOT_TIMEOUT_HANDOFF (5m): conversa sem resposta há mais que isto vai a um humano
	TicketPendenteTTL    time.Duration // TICKET_PENDENTE_TTL (10m): prazo para o ticket pendente aparecer na listagem

	// Tools de ticket (E3.1; docs/infodesk/07).
	TicketFinishConversa  bool          // TICKET_FINISH_CONVERSA (false): `finish` do POST /conversations/:id/ticket (07 C6)
	TicketRodape          bool          // TICKET_RODAPE (true): rodapé "[Aberto pela Sofia · …]" na descrição (07 T8)
	TransferirAposTicket  bool          // TRANSFERIR_APOS_TICKET (true): handoff depois do ticket aberto
	TicketDedupJanela     time.Duration // TICKET_DEDUP_JANELA (24h): mesma categoria + telefone nesta janela = chamado já aberto
	DedupConsultaInfodesk bool          // DEDUP_CONSULTA_INFODESK (true): consulta tickets abertos do telefone no Infodesk (07 T5)
	IdentContratoRE       string        // IDENT_CONTRATO_RE (^\d{4,12}$): formato aceito de número de contrato
	// Cadastro de solicitante (E5.3, ADR 0009). Hipóteses sem decisão do produto.
	CadastroSemCliente    string // CADASTRO_SEM_CLIENTE (transferir|oferecer): empresa não achada/ambígua após as tentativas
	CadastroMaxTentativas int    // CADASTRO_MAX_TENTATIVAS (2): falhas de empresa antes de desistir
	// IdentHashSal: sal do hash do CPF/contrato (IDENT_HASH_SAL). Vazio = usa o
	// TOOLS_API_TOKEN, e rotacionar o token invalida os hashes já gravados.
	IdentHashSal string
	// StrictTaxonomia (STRICT_TAXONOMIA, false): divergência da taxonomia com o
	// Infodesk derruba o boot. TaxonomiaPath (TAXONOMIA_PATH): arquivo da
	// taxonomia; vazio = procura nos caminhos padrão (taxonomia.CaminhoDoAmbiente).
	StrictTaxonomia bool
	TaxonomiaPath   string

	// TestadoresTelefones (TESTADORES_TELEFONES, E3.9): lista de telefones E.164
	// separados por vírgula. Vazia = desligada (todo mundo fala com a Sofia). Ligada,
	// só estes números falam com ela; os demais vão direto para um atendente, sem
	// mensagem ao cliente. Ver testadores.go.
	TestadoresTelefones []string

	// WebhookSemAutenticacao (WEBHOOK_SEM_AUTENTICACAO, default false): deixa o
	// gateway subir SEM INFODESK_WEBHOOK_SECRET (webhook sem HMAC). Só dev.
	// ProducaoAberta (SOFIA_PRODUCAO_ABERTA, default false): opt-in explícito para
	// atender todo cliente (TESTADORES_TELEFONES vazia). Ver seguranca.go.
	WebhookSemAutenticacao bool
	ProducaoAberta         bool
}

// Avisos lista o que, na configuração carregada, funciona mas merece atenção
// no boot (o chamador loga cada um em WARN).
func (c *Config) Avisos() []string {
	var out []string
	out = append(out, c.avisosDeSeguranca()...)
	if c.ToolsAPIToken != "" && c.IdentHashSal == "" {
		out = append(out, "IDENT_HASH_SAL vazio: o sal dos hashes de CPF/contrato cai no TOOLS_API_TOKEN; "+
			"rotacionar o TOOLS_API_TOKEN invalida os hashes já gravados (defina um IDENT_HASH_SAL próprio em produção)")
	}
	if c.TestadoresAtivo() {
		out = append(out, fmt.Sprintf("TESTADORES_TELEFONES LIGADA com %d número(s): SÓ eles falam com a Sofia; todo outro cliente que "+
			"escrever é passado direto a um atendente, sem resposta automática. Para a produção aberta, deixe a variável vazia",
			len(c.Testadores())))
		if c.DepartamentoHumanoPadrao == "" {
			out = append(out, "TESTADORES_TELEFONES ligada com DEPARTAMENTO_HUMANO_PADRAO vazio: os clientes de fora do grupo "+
				"vão para a distribuição automática do Infodesk, sem departamento")
		}
	}
	return out
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
		IdentContratoRE:          getenv("IDENT_CONTRATO_RE", `^\d{4,12}$`),
		IdentHashSal:             os.Getenv("IDENT_HASH_SAL"),
		CadastroSemCliente:       strings.ToLower(strings.TrimSpace(getenv("CADASTRO_SEM_CLIENTE", CadastroSemClienteTransferir))),
		TaxonomiaPath:            strings.TrimSpace(getenv("TAXONOMIA_PATH", "")),
	}

	// Chaves liga/desliga novas: valor que não é booleano derruba o boot (um
	// STRICT_TAXONOMIA=ture silenciosamente desligado esconderia a divergência).
	booleanos := []struct {
		dst *bool
		env string
		def bool
	}{
		{&cfg.ReconciliaAtivo, "RECONCILIA_ATIVO", true},
		{&cfg.TicketFinishConversa, "TICKET_FINISH_CONVERSA", false},
		{&cfg.TicketRodape, "TICKET_RODAPE", true},
		{&cfg.TransferirAposTicket, "TRANSFERIR_APOS_TICKET", true},
		{&cfg.DedupConsultaInfodesk, "DEDUP_CONSULTA_INFODESK", true},
		{&cfg.StrictTaxonomia, "STRICT_TAXONOMIA", false},
		{&cfg.WebhookSemAutenticacao, "WEBHOOK_SEM_AUTENTICACAO", false},
		{&cfg.ProducaoAberta, "SOFIA_PRODUCAO_ABERTA", false},
	}
	for _, b := range booleanos {
		v, err := lerBool(b.env, b.def)
		if err != nil {
			return nil, err
		}
		*b.dst = v
	}

	// Durações estritamente positivas (0 ou negativo não tem sentido aqui).
	positivas := []struct {
		dst *time.Duration
		env string
		def string
	}{
		{&cfg.ReconciliaIntervalo, "RECONCILIA_INTERVALO", "60s"},
		{&cfg.ReconciliaRetroativo, "RECONCILIA_RETROATIVO", "1h"},
		{&cfg.ReconciliaAtraso, "RECONCILIA_ATRASO", "2m"},
		{&cfg.ReconciliaBackoffMax, "RECONCILIA_BACKOFF_MAX", "5m"},
		{&cfg.ReconciliaSkew, "RECONCILIA_SKEW", "60s"},
		{&cfg.BotTimeoutHandoff, "BOT_TIMEOUT_HANDOFF", "5m"},
		{&cfg.TicketPendenteTTL, "TICKET_PENDENTE_TTL", "10m"},
		{&cfg.TicketDedupJanela, "TICKET_DEDUP_JANELA", "24h"},
	}
	for _, d := range positivas {
		v, err := parseDuration(strings.TrimSpace(getenv(d.env, d.def)))
		if err != nil {
			return nil, fmt.Errorf("%s inválido: %w", d.env, err)
		}
		if v <= 0 {
			return nil, fmt.Errorf("%s inválido (%s): precisa ser maior que zero", d.env, strings.TrimSpace(getenv(d.env, d.def)))
		}
		*d.dst = v
	}
	testadores, err := parseTestadores(os.Getenv("TESTADORES_TELEFONES"))
	if err != nil {
		return nil, err
	}
	cfg.TestadoresTelefones = testadores
	if _, err := regexp.Compile(cfg.IdentContratoRE); err != nil {
		return nil, fmt.Errorf("IDENT_CONTRATO_RE inválido: %w", err)
	}

	duracoes := []struct {
		dst *time.Duration
		env string
		def string
	}{
		{&cfg.InfodeskTimeout, "INFODESK_TIMEOUT", "15s"},
		{&cfg.ReferenciaCacheTTL, "REFERENCIA_CACHE_TTL", "10m"},
		{&cfg.BrainTimeout, "BRAIN_TIMEOUT", "80s"},
		{&cfg.TurnoTeto, "TURNO_TETO", "105s"},
		{&cfg.DebounceJanela, "DEBOUNCE_JANELA", "8s"},
		{&cfg.DebounceTeto, "DEBOUNCE_TETO", "20s"},
		{&cfg.EventoIdadeMax, "EVENTO_IDADE_MAX", "10m"},
		{&cfg.BolhaPausa, "BOLHA_PAUSA", "1500ms"},
		{&cfg.WebhookJanela, "WEBHOOK_JANELA", "5m"},
		{&cfg.IdentTTL, "IDENT_TTL", "24h"},
	}
	for _, d := range duracoes {
		v, err := parseDuration(strings.TrimSpace(getenv(d.env, d.def)))
		if err != nil {
			return nil, fmt.Errorf("%s inválido: %w", d.env, err)
		}
		*d.dst = v
	}

	if cfg.CadastroSemCliente != CadastroSemClienteTransferir && cfg.CadastroSemCliente != CadastroSemClienteOferecer {
		return nil, fmt.Errorf("CADASTRO_SEM_CLIENTE inválido (%q): use %q ou %q", cfg.CadastroSemCliente, CadastroSemClienteTransferir, CadastroSemClienteOferecer)
	}
	cfg.CadastroMaxTentativas = 2
	if v, ok := os.LookupEnv("CADASTRO_MAX_TENTATIVAS"); ok && strings.TrimSpace(v) != "" {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 1 || n > 10 {
			return nil, fmt.Errorf("CADASTRO_MAX_TENTATIVAS inválido (%q): use um inteiro de 1 a 10", v)
		}
		cfg.CadastroMaxTentativas = n
	}

	// Ordem dos prazos (E3.7): o teto do turno conta desde a primeira mensagem da
	// rajada; a coalescência (DEBOUNCE_TETO) gasta parte dele antes de o cérebro ser
	// chamado, e a chamada pode durar até BRAIN_TIMEOUT. Sem essa folga o gateway
	// desistiria (e transferiria) enquanto o cérebro ainda trabalha.
	if cfg.TurnoTeto < cfg.DebounceTeto+cfg.BrainTimeout {
		return nil, fmt.Errorf("TURNO_TETO (%s) precisa ser maior ou igual a DEBOUNCE_TETO (%s) + "+
			"BRAIN_TIMEOUT (%s) = %s; BRAIN_TIMEOUT, por sua vez, precisa ser maior que o "+
			"BRAIN_TURNO_PRAZO do cérebro",
			cfg.TurnoTeto, cfg.DebounceTeto, cfg.BrainTimeout, cfg.DebounceTeto+cfg.BrainTimeout)
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
	if err := cfg.validarSeguranca(); err != nil {
		return nil, err
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

// lerBool lê uma variável booleana; ausente ou vazia = def, texto que não é
// booleano = erro citando a variável.
func lerBool(key string, def bool) (bool, error) {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def, nil
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "t", "sim", "yes", "on":
		return true, nil
	case "0", "false", "f", "nao", "não", "no", "off":
		return false, nil
	}
	return def, fmt.Errorf("%s inválido (%q): use true ou false", key, v)
}
