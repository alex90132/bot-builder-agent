package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"

	"bot-builder-agent/internal/mcp"
)

// DefaultBuilderURL — публичный конструктор, если в окружении адрес не задан.
const DefaultBuilderURL = "https://telegram-bot-builder-e3u-production.up.railway.app"

// Config — настройки процесса. Секреты пользователей здесь не хранятся.
type Config struct {
	TelegramBotToken string
	OpenRouterAPIKey string
	AIModel          string
	AIMaxTokens      int
	AITemperature    float64

	BuilderURL string

	AllowedIDs []int64

	MaxRounds       int
	MaxResultChars  int
	HistoryMessages int
	ToolsCacheSec   int

	DatabaseURL string
	LogLevel    string
	LogFile     string

	SearxURL string
	WebProxy string
}

// Load читает .env и окружение. Уже заданные переменные .env не затирает.
func Load() (*Config, error) {
	_ = godotenv.Load()
	return FromEnv()
}

// FromEnv собирает конфиг только из окружения.
func FromEnv() (*Config, error) {
	cfg := &Config{
		TelegramBotToken: strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")),
		OpenRouterAPIKey: strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")),
		AIModel:          strings.TrimSpace(os.Getenv("AI_MODEL")),
		BuilderURL:       strings.TrimSpace(os.Getenv("BOT_BUILDER_URL")),
		DatabaseURL:      strings.TrimSpace(os.Getenv("DATABASE_URL")),
		LogLevel:         envDefault("LOG_LEVEL", "INFO"),
		LogFile:          envDefault("LOG_FILE", "logs/bot-builder-agent.log"),
		SearxURL:         envDefault("SEARXNG_URL", "http://localhost:8890"),
		WebProxy:         strings.TrimSpace(os.Getenv("WEB_PROXY")),
	}
	if cfg.TelegramBotToken == "" {
		return nil, fmt.Errorf("TELEGRAM_BOT_TOKEN не задан")
	}
	if cfg.OpenRouterAPIKey == "" {
		return nil, fmt.Errorf("OPENROUTER_API_KEY не задан")
	}
	if cfg.AIModel == "" {
		return nil, fmt.Errorf("AI_MODEL не задан")
	}
	if cfg.DatabaseURL == "" {
		cfg.DatabaseURL = "sqlite://data/agent.db"
	}
	if cfg.BuilderURL == "" {
		cfg.BuilderURL = DefaultBuilderURL
	}

	normalized, err := mcp.NormalizeURL(cfg.BuilderURL)
	if err != nil {
		return nil, fmt.Errorf("BOT_BUILDER_URL: %w", err)
	}
	cfg.BuilderURL = normalized

	cfg.AIMaxTokens, err = envInt("AI_MAX_TOKENS", 4000)
	if err != nil {
		return nil, err
	}
	cfg.AITemperature, err = envFloat("AI_TEMPERATURE", 0.2)
	if err != nil {
		return nil, err
	}
	cfg.MaxRounds, err = envInt("AGENT_MAX_ROUNDS", 16)
	if err != nil {
		return nil, err
	}
	cfg.MaxResultChars, err = envInt("AGENT_MAX_RESULT_CHARS", 12000)
	if err != nil {
		return nil, err
	}
	cfg.HistoryMessages, err = envInt("AGENT_HISTORY_MESSAGES", 40)
	if err != nil {
		return nil, err
	}
	cfg.ToolsCacheSec, err = envInt("MCP_TOOLS_CACHE_SECONDS", 300)
	if err != nil {
		return nil, err
	}
	if cfg.MaxRounds < 1 {
		return nil, fmt.Errorf("AGENT_MAX_ROUNDS должен быть больше нуля")
	}
	if cfg.MaxResultChars < 200 {
		return nil, fmt.Errorf("AGENT_MAX_RESULT_CHARS слишком мал")
	}
	if cfg.HistoryMessages < 2 {
		return nil, fmt.Errorf("AGENT_HISTORY_MESSAGES слишком мал")
	}

	ids, err := parseIDs(os.Getenv("TELEGRAM_ALLOWED_IDS"))
	if err != nil {
		return nil, err
	}
	cfg.AllowedIDs = ids
	return cfg, nil
}

// Allowed сообщает, можно ли этому Telegram-пользователю писать боту.
// Пустой список разрешает всех.
func (c *Config) Allowed(userID int64) bool {
	if c == nil || len(c.AllowedIDs) == 0 {
		return true
	}
	for _, id := range c.AllowedIDs {
		if id == userID {
			return true
		}
	}
	return false
}

func envDefault(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	return v
}

func envInt(key string, def int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func envFloat(key string, def float64) (float64, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func parseIDs(raw string) ([]int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]int64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("TELEGRAM_ALLOWED_IDS: %w", err)
		}
		out = append(out, id)
	}
	return out, nil
}
