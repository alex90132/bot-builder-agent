package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"bot-builder-agent/internal/mcp"
)

// Message — одна реплика истории, включая вызовы инструментов.
type Message struct {
	Role          string
	Content       string
	ToolCallID    string
	Name          string
	ToolCallsJSON string
}

// Store хранит токены и историю. Файл базы создаётся с правами 0600.
type Store struct {
	db           *sql.DB
	path         string
	historyLimit int
}

// Open открывает SQLite по DSN вида sqlite://data/agent.db.
func Open(dsn string, historyLimit int) (*Store, error) {
	path := PathFromDSN(dsn)
	if path == "" {
		return nil, fmt.Errorf("пустой путь базы")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("каталог базы: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	// Читатели не ждут чужую запись. Писатели делят файл через busy_timeout.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	s := &Store{db: db, path: path, historyLimit: historyLimit}
	if s.historyLimit < 2 {
		s.historyLimit = 40
	}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.tighten(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) tighten() error {
	for _, path := range []string{s.path, s.path + "-wal", s.path + "-shm"} {
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("права базы: %w", err)
		}
	}
	return nil
}

// Path возвращает путь к файлу базы.
func (s *Store) Path() string { return s.path }

// Close закрывает соединение.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// PathFromDSN вытаскивает путь файла из sqlite://...
func PathFromDSN(dsn string) string {
	s := strings.TrimSpace(dsn)
	if s == "" {
		return "data/agent.db"
	}
	if strings.HasPrefix(s, "sqlite://") {
		s = strings.TrimPrefix(s, "sqlite://")
	}
	return s
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS users (
  telegram_user_id INTEGER PRIMARY KEY,
  mcp_token TEXT NOT NULL DEFAULT '',
  mcp_url TEXT NOT NULL DEFAULT '',
  active_project_id INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  telegram_user_id INTEGER NOT NULL,
  role TEXT NOT NULL,
  content TEXT NOT NULL DEFAULT '',
  tool_call_id TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL DEFAULT '',
  tool_calls_json TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_user ON messages(telegram_user_id, id);
`)
	if err != nil {
		return err
	}
	if err := s.ensureMCPURL(); err != nil {
		return err
	}
	return s.migrateServers()
}

func (s *Store) ensureMCPURL() error {
	rows, err := s.db.Query(`PRAGMA table_info(users)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	hasURL := false
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == "mcp_url" {
			hasURL = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if hasURL {
		return nil
	}
	_, err = s.db.Exec(`ALTER TABLE users ADD COLUMN mcp_url TEXT NOT NULL DEFAULT ''`)
	return err
}

// SaveToken записывает только токен. Адрес при этом берётся из настроек процесса.
func (s *Store) SaveToken(ctx context.Context, userID int64, token string) error {
	return s.SaveAccess(ctx, userID, token, "")
}

// SaveAccess записывает токен и адрес из блока mcpServers. Пустой endpoint значит адрес по умолчанию.
func (s *Store) SaveAccess(ctx context.Context, userID int64, token, endpoint string) error {
	token = strings.TrimSpace(token)
	endpoint = strings.TrimSpace(endpoint)
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO users(telegram_user_id, mcp_token, mcp_url, active_project_id, updated_at)
VALUES(?, ?, ?, 0, ?)
ON CONFLICT(telegram_user_id) DO UPDATE SET
  mcp_token=excluded.mcp_token,
  mcp_url=excluded.mcp_url,
  updated_at=excluded.updated_at
`, userID, token, endpoint, now)
	if err != nil {
		return err
	}
	creds := mcp.Credentials{Token: token}
	raw, err := creds.JSON()
	if err != nil {
		return err
	}
	_, err = s.insertServer(ctx, Server{
		UserID:    userID,
		Name:      mcp.BuilderName,
		URL:       endpoint,
		Transport: "streamable",
		AuthKind:  mcp.AuthBearer,
		AuthJSON:  raw,
		Enabled:   true,
		Builder:   true,
	})
	return err
}

// Token возвращает токен или пустую строку.
func (s *Store) Token(ctx context.Context, userID int64) (string, error) {
	token, _, err := s.Access(ctx, userID)
	return token, err
}

// Access возвращает токен и адрес конструктора этого пользователя.
func (s *Store) Access(ctx context.Context, userID int64) (token, endpoint string, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT mcp_token, mcp_url FROM users WHERE telegram_user_id=?`, userID).Scan(&token, &endpoint)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	return token, endpoint, err
}

// Logout стирает токен и активный проект, историю не трогает.
func (s *Store) Logout(ctx context.Context, userID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `
UPDATE users SET mcp_token='', mcp_url='', active_project_id=0, updated_at=? WHERE telegram_user_id=?
`, now, userID)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM mcp_servers WHERE telegram_user_id=? AND builder=1`, userID)
	return err
}

// SetActiveProject запоминает проект.
func (s *Store) SetActiveProject(ctx context.Context, userID, projectID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO users(telegram_user_id, mcp_token, active_project_id, updated_at)
VALUES(?, '', ?, ?)
ON CONFLICT(telegram_user_id) DO UPDATE SET active_project_id=excluded.active_project_id, updated_at=excluded.updated_at
`, userID, projectID, now)
	return err
}

// ActiveProject возвращает id проекта или 0.
func (s *Store) ActiveProject(ctx context.Context, userID int64) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT active_project_id FROM users WHERE telegram_user_id=?`, userID).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}

// Append добавляет реплики и оставляет только последние historyLimit.
func (s *Store) Append(ctx context.Context, userID int64, msgs []Message) error {
	if len(msgs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC().Format(time.RFC3339)
	for _, m := range msgs {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO messages(telegram_user_id, role, content, tool_call_id, name, tool_calls_json, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?)
`, userID, m.Role, m.Content, m.ToolCallID, m.Name, m.ToolCallsJSON, now); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM messages WHERE telegram_user_id=? AND id NOT IN (
  SELECT id FROM messages WHERE telegram_user_id=? ORDER BY id DESC LIMIT ?
)
`, userID, userID, s.historyLimit); err != nil {
		return err
	}
	return tx.Commit()
}

// History возвращает последние реплики от старых к новым.
func (s *Store) History(ctx context.Context, userID int64) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT role, content, tool_call_id, name, tool_calls_json
FROM messages WHERE telegram_user_id=? ORDER BY id DESC LIMIT ?
`, userID, s.historyLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rev []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.Role, &m.Content, &m.ToolCallID, &m.Name, &m.ToolCallsJSON); err != nil {
			return nil, err
		}
		rev = append(rev, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev, nil
}

// ResetHistory удаляет диалог и не меняет токен.
func (s *Store) ResetHistory(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE telegram_user_id=?`, userID)
	return err
}

// MaskToken показывает только префикс и хвост.
func MaskToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return "не задан"
	}
	if len(token) <= 8 {
		return "задан"
	}
	return token[:4] + "…" + token[len(token)-4:]
}
