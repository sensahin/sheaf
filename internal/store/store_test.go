package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

// A database from v0.1 opens with its tasks intact, and the repositories
// those tasks used become the registered ones.
func TestMigrateFromV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sheaf.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[:2] {
		if _, err := db.Exec(m); err != nil {
			t.Fatal(err)
		}
	}
	db.Exec(`PRAGMA user_version = 2`)
	for i, repo := range []string{"/src/app", "/src/app", ""} {
		if _, err := db.Exec(`INSERT INTO tasks (id, title, status, repo, created_at, updated_at) VALUES (?, 't', 'open', ?, '2026-10-31', '2026-10-31')`, fmt.Sprint("t-", i), repo); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	var p, detail string
	if err := db.QueryRow(`SELECT COUNT(*), MIN(path) FROM repos`).Scan(&n, &p); err != nil || n != 1 || p != "/src/app" {
		t.Fatalf("repos: %d %q %v", n, p, err)
	}
	if err := db.QueryRow(`SELECT detail FROM tasks WHERE id = 't-0'`).Scan(&detail); err != nil || detail != "" {
		t.Fatalf("detail: %q %v", detail, err)
	}
}
