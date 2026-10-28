package app

import (
	"context"
	"fmt"
	"time"

	"bot-builder-agent/internal/agent"
	"bot-builder-agent/internal/config"
	"bot-builder-agent/internal/logger"
	"bot-builder-agent/internal/mcp"
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
	runner := &agent.Runner{
		OR:             orClient,
		MCP:            mcpClient,
		Model:          a.cfg.AIModel,
		MaxRounds:      a.cfg.MaxRounds,
		MaxResultChars: a.cfg.MaxResultChars,
		MaxTokens:      a.cfg.AIMaxTokens,
		Temperature:    float32(a.cfg.AITemperature),
		Web:            websearch.New(a.cfg.SearxURL, a.cfg.WebProxy),
	}
	bot, err := telegram.New(a.cfg, a.log, st, mcpClient, runner)
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
