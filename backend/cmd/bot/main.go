// Command bot é o gateway Chatwoot da Azapfy: recebe os webhooks, resolve a
// identidade do usuário (gate) e encaminha as mensagens já autenticadas ao
// cérebro Python (Contrato A). As tools de dados (MCP) ficam para a fase
// seguinte.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"bot-azapfy/internal/brain"
	"bot-azapfy/internal/chatwoot"
	"bot-azapfy/internal/config"
	"bot-azapfy/internal/engine"
	"bot-azapfy/internal/identity"
	"bot-azapfy/internal/mongo"
	"bot-azapfy/internal/sac"
	"bot-azapfy/internal/store"
	"bot-azapfy/internal/toolsapi"
	"bot-azapfy/internal/webhook"
)

func main() {
	bootLog := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load()
	if err != nil {
		bootLog.Error("config inválida", "err", err)
		os.Exit(1)
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseLogLevel(cfg.LogLevel)}))
	slog.SetDefault(log)
	log.Info("log inicializado", "level", cfg.LogLevel)

	st, err := abrirStore(cfg, log)
	if err != nil {
		log.Error("abrir store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	repo, err := mongo.NewRepo(ctx, cfg.MongoURI, cfg.MongoDB, cfg.MongoCollection, cfg.MongoTimeout)
	if err != nil {
		log.Error("conectar ao Mongo", "err", err)
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = repo.Close(shutdownCtx)
	}()

	cw := chatwoot.NewClient(cfg.ChatwootBaseURL, cfg.ChatwootAccountID, cfg.ChatwootAPIToken)
	brainClient := brain.NewClient(cfg.BrainBaseURL, cfg.BrainTimeout)
	// brainClient também extrai o login de frases livres (fallback do gate).
	gate := identity.New(st, repo, cfg.ConfirmField, cfg.MaxTentativas, cfg.IdentityTTL, cfg.GateFalhaTTL, brainClient, log)
	eng := engine.New(cfg, cw, gate, brainClient, st, log)
	// Contexto-raiz dos workers de conversa (coalescência/fila por conversa).
	eng.Start(ctx)

	if cfg.WebhookSecret == "" && cfg.WebhookToken == "" {
		log.Warn("webhook SEM autenticação: defina WEBHOOK_TOKEN (?token=...) ou WEBHOOK_SECRET")
	}
	log.Info("configure este URL no webhook do Chatwoot", "url", webhookURL(cfg), "brain", cfg.BrainBaseURL)
	srv := webhook.NewServer(cfg.WebhookSecret, cfg.WebhookToken, st, eng, log, 4, 256)
	go srv.Start(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", srv.ServeHTTP)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// API interna de tools de dados (abrir/listar chamados no SAC) que o cérebro
	// Python chama durante o loop do agente. Opcional: só sobe com SAC_BASE_URL +
	// TOOLS_API_TOKEN. O relator vem da identidade do gate, nunca do LLM.
	if cfg.SACBaseURL != "" {
		sacClient := sac.New(sac.Options{
			BaseURL: cfg.SACBaseURL, PortalURL: cfg.SACPortalURL, ServiceCod: cfg.SACServiceCod,
			GrupoEmp: cfg.SACGrupoEmp, Empresa: cfg.SACEmpresa, Timezone: cfg.SACTimezone,
			APIToken: cfg.SACAPIToken, ConfigTTL: cfg.SACConfigTTL,
		})
		if cfg.ToolsAPIToken == "" {
			log.Warn("SAC configurado, mas TOOLS_API_TOKEN vazio: API de tools NÃO exposta")
		} else {
			toolsapi.New(st, sacClient, cfg.ToolsAPIToken, log).Register(mux)
			log.Info("API de tools SAC habilitada", "grupo_emp", cfg.SACGrupoEmp,
				"conta_servico_prioridade", sacClient.TemContaServico())
		}
	}

	httpServer := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("servidor iniciado", "port", cfg.Port)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("encerrando...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown", "err", err)
	}
}

// abrirStore escolhe a persistência do gateway: Postgres quando PG_URL está
// definido (o mesmo Postgres do RAG/pgvector, schema `gateway`), senão o
// SQLite local em DB_PATH. Falha de conexão no boot derruba o processo — é
// melhor o compose reiniciar do que atender sem estado.
func abrirStore(cfg *config.Config, log *slog.Logger) (store.Store, error) {
	if cfg.PGURL == "" {
		log.Info("store: sqlite", "path", cfg.DBPath)
		return store.NewSQLite(cfg.DBPath)
	}
	bootCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := store.NewPostgres(bootCtx, cfg.PGURL)
	if err != nil {
		return nil, err
	}
	log.Info("store: postgres", "schema", "gateway")
	return st, nil
}

// webhookURL monta a URL a cadastrar no webhook do Chatwoot, incluindo o token.
func webhookURL(cfg *config.Config) string {
	base := cfg.PublicBaseURL
	if base == "" {
		base = "https://<seu-host>"
	}
	u := base + "/webhook"
	if cfg.WebhookToken != "" {
		u += "?token=" + url.QueryEscape(cfg.WebhookToken)
	}
	return u
}

func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
