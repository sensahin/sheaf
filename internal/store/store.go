// Package store opens Sheaf's SQLite database and applies its schema.
//
// The events table is an append-only log of everything that happens. The other
// tables are the current state, updated in the same transaction as the event
// that changed them, so a crash or reboot never leaves them out of step.
package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Open opens (or creates) the database at path and migrates it.
func Open(path string) (*sql.DB, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite allows one writer at a time; a single connection avoids
	// SQLITE_BUSY between our own goroutines.
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

var migrations = []string{
	`
CREATE TABLE meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE events (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	ts      TEXT NOT NULL,
	actor   TEXT NOT NULL,
	type    TEXT NOT NULL,
	subject TEXT NOT NULL DEFAULT '',
	data    TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE agents (
	name       TEXT PRIMARY KEY,
	kind       TEXT NOT NULL DEFAULT '',
	token_hash TEXT NOT NULL UNIQUE,
	policy     TEXT NOT NULL,
	resume_cmd TEXT NOT NULL DEFAULT '',
	state      TEXT NOT NULL DEFAULT 'idle',
	note       TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE TABLE tasks (
	id         TEXT PRIMARY KEY,
	title      TEXT NOT NULL,
	owner      TEXT NOT NULL DEFAULT '',
	parent     TEXT NOT NULL DEFAULT '',
	status     TEXT NOT NULL,
	repo       TEXT NOT NULL DEFAULT '',
	worktree   TEXT NOT NULL DEFAULT '',
	branch     TEXT NOT NULL DEFAULT '',
	base       TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX tasks_owner ON tasks(owner, status);

CREATE TABLE messages (
	id         TEXT PRIMARY KEY,
	sender     TEXT NOT NULL,
	recipient  TEXT NOT NULL,
	task       TEXT NOT NULL DEFAULT '',
	kind       TEXT NOT NULL,
	body       TEXT NOT NULL,
	created_at TEXT NOT NULL,
	read_at    TEXT
);
CREATE INDEX messages_inbox ON messages(recipient, read_at);

CREATE TABLE approvals (
	id         TEXT PRIMARY KEY,
	agent      TEXT NOT NULL,
	task       TEXT NOT NULL DEFAULT '',
	kind       TEXT NOT NULL,
	summary    TEXT NOT NULL,
	detail     TEXT NOT NULL DEFAULT '',
	status     TEXT NOT NULL,
	answer     TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	decided_at TEXT
);
CREATE INDEX approvals_status ON approvals(status);
`,
}

func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
