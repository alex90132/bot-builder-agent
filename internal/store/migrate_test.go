package store

import (
	"context"
	"database/sql"
	"testing"
)

func TestMigrateBuilderConnection(t *testing.T) {
	path := t.TempDir() + "/old.db"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE users (
	  telegram_user_id INTEGER PRIMARY KEY,
	  mcp_token TEXT NOT NULL DEFAULT '',
	  active_project_id INTEGER NOT NULL DEFAULT 0,
	  updated_at TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(telegram_user_id, mcp_token, active_project_id, updated_at) VALUES(4, 'mcp_oldtoken', 9, '2020-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := Open("sqlite://"+path, 20)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	items, err := st.ListServers(ctx, 4)
	if err != nil || len(items) != 1 || !items[0].Builder || items[0].Name != "botcraft-builder" || items[0].AuthKind != "bearer" {
		t.Fatalf("%+v %v", items, err)
	}
	if items[0].AuthJSON == "" || items[0].AuthJSON == "{}" {
		t.Fatal("токен не перенесён")
	}
	token, endpoint, err := st.Access(ctx, 4)
	if err != nil || token != "mcp_oldtoken" || endpoint != "" {
		t.Fatalf("%q %q %v", token, endpoint, err)
	}
	project, err := st.ActiveProject(ctx, 4)
	if err != nil || project != 9 {
		t.Fatalf("проект %d %v", project, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	again, err := Open("sqlite://"+path, 20)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	items, err = again.ListServers(ctx, 4)
	if err != nil || len(items) != 1 {
		t.Fatalf("повторная миграция: %+v %v", items, err)
	}
}
