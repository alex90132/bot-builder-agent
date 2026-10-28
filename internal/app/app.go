package app

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"bot-builder-agent/internal/agent"
	"bot-builder-agent/internal/config"
	"bot-builder-agent/internal/logger"
	"bot-builder-agent/internal/mcp"
	"bot-builder-agent/internal/mcphub"
	"bot-builder-agent/internal/openrouter"
	"bot-builder-agent/internal/store"
	"bot-builder-agent/internal/telegram"
	"bot-builder-agent/internal/websearch"
)

// App связывает хранилище, MCP, модель и Telegram.
type App struct {
	cfg *config.Config
	log *logger.Logger
}

// New создаёт приложение.
func New(cfg *config.Config, log *logger.Logger) *App {
	return &App{cfg: cfg, log: log}
}

// Run поднимает подсистемы и ждёт остановки контекста.
func (a *App) Run(ctx context.Context) error {
	st, err := store.Open(a.cfg.DatabaseURL, a.cfg.HistoryMessages)
	if err != nil {
		return err
	}
	defer st.Close()

	mcpClient, err := mcp.New(a.cfg.BuilderURL, time.Duration(a.cfg.ToolsCacheSec)*time.Second)
	if err != nil {
		return err
	}
	orClient, err := openrouter.New(a.cfg.OpenRouterAPIKey)
	if err != nil {
		return err
	}
	hub := mcphub.New(st, a.cfg.BuilderURL, time.Duration(a.cfg.ToolsCacheSec)*time.Second)
	hub.AllowPrivate = a.cfg.AllowPrivate
	hub.PublicURL = a.cfg.OAuthPublicURL
	if a.cfg.OAuthListen != "" {
		srv := &http.Server{Addr: a.cfg.OAuthListen, Handler: hub, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				a.log.Error("oauth callback: %v", err)
			}
		}()
		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
		}()
	}
	runner := &agent.Runner{
		OR:             orClient,
		MCP:            mcpClient,
		Remotes:        hub,
		Model:          a.cfg.AIModel,
		MaxRounds:      a.cfg.MaxRounds,
		MaxResultChars: a.cfg.MaxResultChars,
		MaxTokens:      a.cfg.AIMaxTokens,
		Temperature:    float32(a.cfg.AITemperature),
		Web:            websearch.New(a.cfg.SearxURL, a.cfg.WebProxy),
	}
	bot, err := telegram.New(a.cfg, a.log, st, mcpClient, runner, hub)
	if err != nil {
		return err
	}
	a.log.Info("bot-builder-agent запущен, конструктор %s", a.cfg.BuilderURL)
	if err := bot.Start(ctx); err != nil {
		return fmt.Errorf("telegram: %w", err)
	}
	a.log.Info("остановка")
	return nil
}
