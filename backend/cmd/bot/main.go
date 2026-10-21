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

	"bot-infominas/internal/alerta"
	"bot-infominas/internal/brain"
	"bot-infominas/internal/config"
	"bot-infominas/internal/engine"
	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/reconciliador"
	"bot-infominas/internal/store"
	"bot-infominas/internal/taxonomia"
	"bot-infominas/internal/toolsapi"
	"bot-infominas/internal/webhook"
)

func main() { os.Exit(run()) }

// run é o main com código de saída: os defers (store, sinal) rodam antes do os.Exit.
func run() int {
	bootLog := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load()
	if err != nil {
		bootLog.Error("config inválida", "err", err)
		return 1
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseLogLevel(cfg.LogLevel)}))
	slog.SetDefault(log)
	log.Info("log inicializado", "level", cfg.LogLevel)

	st, err := abrirStore(cfg, log)
	if err != nil {
		log.Error("abrir store", "err", err)
		return 1
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Cliente do Infodesk: o ÚNICO pacote que fala com a API (skill infodesk-api).
	idk := infodesk.New(infodesk.Options{
		BaseURL:  cfg.InfodeskBaseURL,
		APIKey:   cfg.InfodeskAPIKey,
		HTTP:     &http.Client{Timeout: cfg.InfodeskTimeout},
		CacheTTL: cfg.ReferenciaCacheTTL,
	})
	if cfg.DepartamentoHumanoPadrao == "" {
		// TODO(P11): departamento humano padrão (Infominas). Sem ele o handoff
		// vai sem departamento (distribuição automática do Infodesk).
		log.Warn("DEPARTAMENTO_HUMANO_PADRAO vazio: o handoff vai sem departamento (distribuição automática)")
	}

	brainClient := brain.NewClient(cfg.BrainBaseURL, cfg.BrainTimeout)
	eng := engine.New(cfg, engine.NovoTransporteInfodesk(idk), brainClient, st, log)
	// Contexto-raiz dos workers de conversa (coalescência/fila por conversa).
	eng.Start(ctx)

	// GET /me no boot: a chave precisa conduzir conversas e ter os escopos
	// mínimos; o organizationId dela é o esperado nos webhooks e o nome dela
	// é o senderName do eco das nossas mensagens.
	me, err := consultarMe(ctx, idk, time.Sleep)
	if err == nil {
		err = validarChave(me, cfg.InfodeskOrganizationID)
	}
	if err != nil {
		log.Error("chave do Infodesk não serve", "err", err)
		eng.Vigia().Aviso(alerta.TipoChaveSemEscopo, "Chave do Infodesk inválida no boot",
			"O gateway não subiu: "+err.Error(), "")
		return 1
	}
	nomeChave := cfg.InfodeskNomeChave
	if nomeChave == "" {
		nomeChave = me.Name
	}
	log.Info("chave do Infodesk validada", "key_id", me.KeyID, "nome_chave", nomeChave, "escopos", len(me.Scopes))

	if cfg.InfodeskWebhookSecret == "" && cfg.WebhookToken == "" {
		log.Warn("webhook SEM autenticação: defina INFODESK_WEBHOOK_SECRET (HMAC) ou WEBHOOK_TOKEN (?token=...)")
	}
	// O token NÃO vai ao log (segredo): a URL logada mostra só onde ele entra.
	log.Info("configure este URL no webhook do Infodesk", "url", webhookURL(cfg, true), "brain", cfg.BrainBaseURL)
	srv := webhook.NewServer(webhook.Options{
		Secret:         cfg.InfodeskWebhookSecret,
		Token:          cfg.WebhookToken,
		Janela:         cfg.WebhookJanela,
		OrganizationID: me.OrganizationID,
		NomeChave:      nomeChave,
		Workers:        4,
		QueueSize:      256,
	}, st, eng, eng.Vigia(), log)
	// Reconciliador (E3.3; bloco de wiring fora da zona declarada da etapa): a
	// recuperação de crash roda ANTES de o webhook receber o 1º evento; o loop
	// (feed /events, tickets pendentes, vigia BOT_TIMEOUT_HANDOFF) nasce ligado.
	rec := reconciliador.New(reconciliador.OptionsDoAmbiente(log), idk, srv, eng, st, eng.Vigia(), log)
	if err := rec.Recuperar(ctx); err != nil {
		log.Error("recuperação de crash do reconciliador falhou", "err", err)
	}
	go srv.Start(ctx)
	go rec.Run(ctx)

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
		tx, err := carregarTaxonomia(ctx, idk, eng, log)
		if err != nil {
			log.Error("taxonomia inválida", "err", err)
			return 1
		}
		toolsapi.New(toolsapi.Deps{
			Token: cfg.ToolsAPIToken, Log: log, Store: st, Infodesk: idk, Taxonomia: tx,
			Transferidor: eng, Alertas: eng.Vigia(), Opcoes: toolsapi.OpcoesDoAmbiente(cfg),
		}).Register(mux)
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
	return 0
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

// webhookURL monta a URL a cadastrar no webhook do Infodesk. Com mascarar, o
// token aparece como <WEBHOOK_TOKEN> (para log); senão vai escapado.
func webhookURL(cfg *config.Config, mascarar bool) string {
	base := cfg.PublicBaseURL
	if base == "" {
		base = "https://<seu-host>"
	}
	u := base + "/webhook"
	if cfg.WebhookToken != "" {
		if mascarar {
			u += "?token=<WEBHOOK_TOKEN>"
		} else {
			u += "?token=" + url.QueryEscape(cfg.WebhookToken)
		}
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

// carregarTaxonomia lê config/taxonomia.yaml e resolve mesa/departamento/
// prioridade/catálogo contra o Infodesk (E3.1). Com STRICT_TAXONOMIA=true uma
// divergência derruba o boot; sem ele vira alerta `taxonomia_invalida` e só as
// categorias afetadas ficam sem mapa. Infodesk fora do ar no boot não derruba o
// gateway: a taxonomia tenta carregar de novo sob demanda.
func carregarTaxonomia(ctx context.Context, idk *infodesk.Client, eng *engine.Engine, log *slog.Logger) (*taxonomia.Taxonomia, error) {
	caminho := taxonomia.CaminhoDoAmbiente()
	def, err := taxonomia.Ler(caminho)
	if err != nil {
		return nil, err
	}
	tx := taxonomia.Nova(def, idk, taxonomia.Opcoes{Strict: taxonomia.StrictDoAmbiente(), Log: log})
	rel, err := tx.Carregar(ctx)
	switch {
	case errors.Is(err, taxonomia.ErrReferenciaIndisponivel):
		log.Warn("taxonomia: referência do Infodesk indisponível no boot; tentarei de novo sob demanda", "err", err)
		eng.Vigia().Aviso(toolsapi.AlertaTaxonomia, "Taxonomia não carregada no boot",
			"Não consegui ler mesas/departamentos do Infodesk; os chamados ficam em 'referencia_indisponivel' até a próxima tentativa.", "")
	case err != nil:
		return nil, err
	case len(rel.Problemas) > 0:
		eng.Vigia().Aviso(toolsapi.AlertaTaxonomia, "Taxonomia diverge do Infodesk",
			strings.Join(rel.Problemas, "; "), "")
	}
	log.Info("taxonomia pronta", "arquivo", caminho, "placeholder", def.Placeholder)
	return tx, nil
}
