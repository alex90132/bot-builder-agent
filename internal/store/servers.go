package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"bot-builder-agent/internal/mcp"
)

// Server — один MCP-сервер чата. AuthJSON содержит секреты и наружу не отдаётся.
type Server struct {
	ID        int64
	UserID    int64
	Name      string
	URL       string
	Transport string
	AuthKind  string
	AuthJSON  string
	Enabled   bool
	Builder   bool
	Revision  string
	LastError string
	LastOK    bool
}

func (s *Store) migrateServers() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS mcp_servers (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  telegram_user_id INTEGER NOT NULL,
  name TEXT NOT NULL,
  url TEXT NOT NULL DEFAULT '',
  transport TEXT NOT NULL DEFAULT 'auto',
  auth_kind TEXT NOT NULL DEFAULT 'none',
  auth_json TEXT NOT NULL DEFAULT '{}',
  enabled INTEGER NOT NULL DEFAULT 1,
  builder INTEGER NOT NULL DEFAULT 0,
  revision TEXT NOT NULL DEFAULT '',
  last_error TEXT NOT NULL DEFAULT '',
  last_ok INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(telegram_user_id, name)
);
CREATE INDEX IF NOT EXISTS idx_mcp_servers_user ON mcp_servers(telegram_user_id, id);
`)
	if err != nil {
		return err
	}
	return s.migrateBuilderRows()
}

func (s *Store) migrateBuilderRows() error {
	rows, err := s.db.Query(`SELECT telegram_user_id, mcp_token, mcp_url FROM users WHERE trim(mcp_token) != ''`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type row struct {
		user       int64
		token, url string
	}
	var pending []row
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.user, &item.token, &item.url); err != nil {
			return err
		}
		pending = append(pending, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range pending {
		var exists int
		err := s.db.QueryRow(`SELECT COUNT(1) FROM mcp_servers WHERE telegram_user_id=? AND builder=1`, item.user).Scan(&exists)
		if err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		creds := mcp.Credentials{Token: strings.TrimSpace(item.token)}
		raw, err := creds.JSON()
		if err != nil {
			return err
		}
		if _, err := s.insertServer(context.Background(), Server{
			UserID:    item.user,
			Name:      mcp.BuilderName,
			URL:       strings.TrimSpace(item.url),
			Transport: "streamable",
			AuthKind:  mcp.AuthBearer,
			AuthJSON:  raw,
			Enabled:   true,
			Builder:   true,
		}); err != nil {
			return err
		}
	}
	return nil
}

// PutServer создаёт или обновляет сервер этого чата.
func (s *Store) PutServer(ctx context.Context, item Server) (Server, error) {
	item.Name = strings.TrimSpace(item.Name)
	item.URL = strings.TrimSpace(item.URL)
	item.Transport = normalizeTransport(item.Transport)
	item.AuthKind = normalizeAuth(item.AuthKind)
	if item.AuthJSON == "" {
		item.AuthJSON = "{}"
	}
	if item.Name == "" {
		return Server{}, fmt.Errorf("пустое имя сервера")
	}
	if item.ID == 0 {
		return s.insertServer(ctx, item)
	}
	cur, err := s.GetServer(ctx, item.UserID, item.ID)
	if err != nil {
		return Server{}, err
	}
	if cur.ID == 0 {
		return Server{}, fmt.Errorf("сервер не найден")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	item.Revision = revisionOf(item)
	_, err = s.db.ExecContext(ctx, `
UPDATE mcp_servers
SET name=?, url=?, transport=?, auth_kind=?, auth_json=?, enabled=?, builder=?, revision=?, updated_at=?
WHERE id=? AND telegram_user_id=?
`, item.Name, item.URL, item.Transport, item.AuthKind, item.AuthJSON, boolInt(item.Enabled), boolInt(item.Builder), item.Revision, now, item.ID, item.UserID)
	if err != nil {
		return Server{}, err
	}
	return s.GetServer(ctx, item.UserID, item.ID)
}

func (s *Store) insertServer(ctx context.Context, item Server) (Server, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	item.Revision = revisionOf(item)
	res, err := s.db.ExecContext(ctx, `
INSERT INTO mcp_servers(telegram_user_id, name, url, transport, auth_kind, auth_json, enabled, builder, revision, last_error, last_ok, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, '', 0, ?, ?)
ON CONFLICT(telegram_user_id, name) DO UPDATE SET
  url=excluded.url,
  transport=excluded.transport,
  auth_kind=excluded.auth_kind,
  auth_json=excluded.auth_json,
  enabled=excluded.enabled,
  builder=excluded.builder,
  revision=excluded.revision,
  updated_at=excluded.updated_at
`, item.UserID, item.Name, item.URL, item.Transport, item.AuthKind, item.AuthJSON, boolInt(item.Enabled), boolInt(item.Builder), item.Revision, now, now)
	if err != nil {
		return Server{}, err
	}
	id, _ := res.LastInsertId()
	if id == 0 {
		err = s.db.QueryRowContext(ctx, `SELECT id FROM mcp_servers WHERE telegram_user_id=? AND name=?`, item.UserID, item.Name).Scan(&id)
		if err != nil {
			return Server{}, err
		}
	}
	return s.GetServer(ctx, item.UserID, id)
}

// ListServers возвращает серверы чата от старых к новым.
func (s *Store) ListServers(ctx context.Context, userID int64) ([]Server, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, telegram_user_id, name, url, transport, auth_kind, auth_json, enabled, builder, revision, last_error, last_ok
FROM mcp_servers WHERE telegram_user_id=? ORDER BY id
`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Server
	for rows.Next() {
		item, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// GetServer возвращает сервер чата. Чужой id даёт пустой результат.
func (s *Store) GetServer(ctx context.Context, userID, id int64) (Server, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, telegram_user_id, name, url, transport, auth_kind, auth_json, enabled, builder, revision, last_error, last_ok
FROM mcp_servers WHERE id=? AND telegram_user_id=?
`, id, userID)
	item, err := scanServer(row)
	if err == sql.ErrNoRows {
		return Server{}, nil
	}
	return item, err
}

// DeleteServer удаляет сервер чата. Сервер конструктора также стирает токен в users.
func (s *Store) DeleteServer(ctx context.Context, userID, id int64) error {
	item, err := s.GetServer(ctx, userID, id)
	if err != nil {
		return err
	}
	if item.ID == 0 {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM mcp_servers WHERE id=? AND telegram_user_id=?`, id, userID); err != nil {
		return err
	}
	if item.Builder {
		now := time.Now().UTC().Format(time.RFC3339)
		_, err = s.db.ExecContext(ctx, `UPDATE users SET mcp_token='', mcp_url='', updated_at=? WHERE telegram_user_id=?`, now, userID)
	}
	return err
}

// SetServerEnabled включает или выключает сервер и меняет revision.
func (s *Store) SetServerEnabled(ctx context.Context, userID, id int64, enabled bool) error {
	item, err := s.GetServer(ctx, userID, id)
	if err != nil {
		return err
	}
	if item.ID == 0 {
		return fmt.Errorf("сервер не найден")
	}
	item.Enabled = enabled
	_, err = s.PutServer(ctx, item)
	return err
}

// SetServerStatus запоминает итог проверки без секретов.
func (s *Store) SetServerStatus(ctx context.Context, userID, id int64, ok bool, errText string) error {
	errText = strings.TrimSpace(errText)
	if len(errText) > 300 {
		errText = errText[:300]
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE mcp_servers SET last_ok=?, last_error=?, updated_at=? WHERE id=? AND telegram_user_id=?
`, boolInt(ok), errText, time.Now().UTC().Format(time.RFC3339), id, userID)
	return err
}

func scanServer(row interface{ Scan(dest ...any) error }) (Server, error) {
	var item Server
	var enabled, builder, lastOK int
	err := row.Scan(&item.ID, &item.UserID, &item.Name, &item.URL, &item.Transport, &item.AuthKind, &item.AuthJSON, &enabled, &builder, &item.Revision, &item.LastError, &lastOK)
	if err != nil {
		return Server{}, err
	}
	item.Enabled = enabled != 0
	item.Builder = builder != 0
	item.LastOK = lastOK != 0
	return item, nil
}

func revisionOf(item Server) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		item.URL,
		normalizeTransport(item.Transport),
		normalizeAuth(item.AuthKind),
		authMaterial(item.AuthJSON),
		fmt.Sprintf("%t", item.Enabled),
	}, "\n")))
	return hex.EncodeToString(sum[:8])
}

// authMaterial входит в revision без access token, refresh token и срока.
// Обновление OAuth не сбрасывает сессию, смена пароля или ключа сбрасывает.
func authMaterial(raw string) string {
	creds, err := mcp.ParseCredentials(raw)
	if err != nil {
		return raw
	}
	creds.AccessToken = ""
	creds.RefreshToken = ""
	creds.Expiry = time.Time{}
	encoded, err := creds.JSON()
	if err != nil {
		return raw
	}
	return encoded
}

func normalizeTransport(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "streamable", "http":
		return "streamable"
	case "sse":
		return "sse"
	default:
		return "auto"
	}
}

func normalizeAuth(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case mcp.AuthBearer, mcp.AuthAPIKey, mcp.AuthBasic, mcp.AuthHeaders, mcp.AuthOAuth, mcp.AuthNone:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return mcp.AuthNone
	}
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
