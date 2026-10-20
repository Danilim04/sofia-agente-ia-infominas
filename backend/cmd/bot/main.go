// Command bot é o gateway da Sofia: recebe os webhooks do Infodesk, enfileira
// e coalesce as mensagens por conversa, encaminha ao cérebro Python
// (Contrato A) e responde pelo transporte. Expõe também a API interna de
// tools (/tools/infodesk/*) que o cérebro chama.
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

	"bot-infominas/internal/brain"
	"bot-infominas/internal/config"
	"bot-infominas/internal/engine"
	"bot-infominas/internal/store"
	"bot-infominas/internal/toolsapi"
	"bot-infominas/internal/webhook"
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

	// TODO(E2.1): trocar pelo cliente do Infodesk (internal/infodesk), que
	// satisfaz engine.Transporte. Até lá nada sai para o cliente.
	var transporte engine.Transporte = &transporteNulo{log: log}
	log.Warn("transporte NULO: mensagens e transferências vão só para o log até E2.1",
		"infodesk_base_url", cfg.InfodeskBaseURL)
	if cfg.DepartamentoHumanoPadrao == "" {
		log.Warn("DEPARTAMENTO_HUMANO_PADRAO vazio: transferências avisam o cliente mas não movem a conversa")
	}

	brainClient := brain.NewClient(cfg.BrainBaseURL, cfg.BrainTimeout)
	eng := engine.New(cfg, transporte, brainClient, st, log)
	// Contexto-raiz dos workers de conversa (coalescência/fila por conversa).
	eng.Start(ctx)

	if cfg.InfodeskWebhookSecret == "" && cfg.WebhookToken == "" {
		log.Warn("webhook SEM autenticação: defina INFODESK_WEBHOOK_SECRET (HMAC) ou WEBHOOK_TOKEN (?token=...)")
	}
	log.Info("configure este URL no webhook do Infodesk", "url", webhookURL(cfg), "brain", cfg.BrainBaseURL)
	srv := webhook.NewServer(cfg.InfodeskWebhookSecret, cfg.WebhookToken, st, eng, log, 4, 256)
	go srv.Start(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", srv.ServeHTTP)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// API interna de tools que o cérebro Python chama durante o loop do
	// agente. Só sobe com TOOLS_API_TOKEN (segredo compartilhado).
	if cfg.ToolsAPIToken == "" {
		log.Warn("TOOLS_API_TOKEN vazio: API de tools (/tools/infodesk/*) NÃO exposta")
	} else {
		toolsapi.New(cfg.ToolsAPIToken, log).Register(mux)
		log.Info("API de tools habilitada", "rota", "/tools/infodesk/*")
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

// transporteNulo satisfaz engine.Transporte sem falar com ninguém: loga o
// que seria enviado. Fica até E2.1 ligar o cliente do Infodesk.
type transporteNulo struct{ log *slog.Logger }

func (t *transporteNulo) EnviarMensagem(_ context.Context, convID, body string) error {
	t.log.Info("transporte nulo: mensagem NÃO enviada", "conversation_id", convID, "chars", len(body))
	return nil
}

func (t *transporteNulo) TransferirDepartamento(_ context.Context, convID, deptID string) error {
	t.log.Info("transporte nulo: transferência NÃO executada", "conversation_id", convID, "departamento", deptID)
	return nil
}

func (t *transporteNulo) LerConversa(_ context.Context, convID string) (map[string]any, error) {
	return map[string]any{"id": convID}, nil
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

// webhookURL monta a URL a cadastrar no webhook do Infodesk, incluindo o token.
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
