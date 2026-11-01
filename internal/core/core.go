// Package core is Sheaf's coordination model: agents, tasks, messages,
// handoffs, approvals and the permission broker.
//
// Every change is written as an event in the same SQLite transaction as the
// state it changes. The database is the source of truth, so the daemon can be
// killed or the machine rebooted at any point and nothing is lost.
package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Human is the name of the person who owns this Sheaf. Agents can message it
// and it decides approvals.
const Human = "human"

// Errors returned by the service. The API layer maps them to status codes.
var (
	ErrNotFound  = errors.New("not found")
	ErrForbidden = errors.New("forbidden")
	ErrInvalid   = errors.New("invalid")
)

func invalidf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

func forbiddenf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrForbidden, fmt.Sprintf(format, a...))
}

// Caller is who is making a request: the human owner, or a named agent.
type Caller struct {
	Name string `json:"name"`
	// Device is set when the human is calling from a paired device rather
	// than with the owner token.
	Device string `json:"device,omitempty"`
}

// IsHuman reports whether the caller is the owner.
func (c Caller) IsHuman() bool { return c.Name == Human }

// HumanCaller is the owner's identity.
var HumanCaller = Caller{Name: Human}

// Service implements Sheaf's coordination model on top of a database.
type Service struct {
	db *sql.DB

	// WorktreeRoot is where task worktrees are created.
	WorktreeRoot string
	// Now is the clock; tests replace it.
	Now func() time.Time

	mu   sync.Mutex
	wake chan struct{}
}

// New returns a service backed by db.
func New(db *sql.DB, worktreeRoot string) *Service {
	return &Service{db: db, WorktreeRoot: worktreeRoot, Now: time.Now, wake: make(chan struct{})}
}

// changed wakes everyone waiting for new state.
func (s *Service) changed() {
	s.mu.Lock()
	close(s.wake)
	s.wake = make(chan struct{})
	s.mu.Unlock()
}

func (s *Service) waitChan() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wake
}

// waitFor calls check until it reports done, the timeout passes, or ctx ends.
// It wakes on every state change, with a slow poll as a fallback for changes
// made by another process.
func (s *Service) waitFor(ctx context.Context, timeout time.Duration, check func() (bool, error)) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		ch := s.waitChan()
		done, err := check()
		if err != nil || done {
			return err
		}
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
		case <-deadline.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Service) now() string { return s.Now().UTC().Format(time.RFC3339Nano) }

// tx runs fn in a transaction and wakes waiters after it commits.
func (s *Service) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.changed()
	return nil
}

func (s *Service) emit(tx *sql.Tx, actor, typ, subject string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO events (ts, actor, type, subject, data) VALUES (?, ?, ?, ?, ?)`,
		s.now(), actor, typ, subject, string(b))
	return err
}

// Event is one entry in the append-only log.
type Event struct {
	ID      int64           `json:"id"`
	TS      string          `json:"ts"`
	Actor   string          `json:"actor"`
	Type    string          `json:"type"`
	Subject string          `json:"subject,omitempty"`
	Data    json.RawMessage `json:"data"`
}

// Events returns up to limit events after the given id. With wait > 0 it
// blocks until at least one event exists or the wait ends.
func (s *Service) Events(ctx context.Context, after int64, limit int, wait time.Duration) ([]Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	out := []Event{}
	err := s.waitFor(ctx, wait, func() (bool, error) {
		rows, err := s.db.QueryContext(ctx, `SELECT id, ts, actor, type, subject, data FROM events WHERE id > ? ORDER BY id LIMIT ?`, after, limit)
		if err != nil {
			return false, err
		}
		defer rows.Close()
		out = out[:0]
		for rows.Next() {
			var e Event
			var data string
			if err := rows.Scan(&e.ID, &e.TS, &e.Actor, &e.Type, &e.Subject, &data); err != nil {
				return false, err
			}
			e.Data = json.RawMessage(data)
			out = append(out, e)
		}
		return len(out) > 0, rows.Err()
	})
	return out, err
}

// SetOwnerToken records the hash of the owner's token.
func (s *Service) SetOwnerToken(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES ('owner_token_hash', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, hashToken(token))
	return err
}

// Authenticate resolves a bearer token to a caller.
func (s *Service) Authenticate(ctx context.Context, token string) (Caller, error) {
	if token == "" {
		return Caller{}, forbiddenf("missing token")
	}
	h := hashToken(token)
	var owner string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'owner_token_hash'`).Scan(&owner)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Caller{}, err
	}
	if owner != "" && owner == h {
		return HumanCaller, nil
	}
	if c, ok, err := s.authenticateDevice(ctx, h); err != nil || ok {
		return c, err
	}
	var name string
	err = s.db.QueryRowContext(ctx, `SELECT name FROM agents WHERE token_hash = ?`, h).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return Caller{}, forbiddenf("unknown token")
	}
	if err != nil {
		return Caller{}, err
	}
	return Caller{Name: name}, nil
}

// LastEventID returns the id of the newest event, or 0.
func (s *Service) LastEventID(ctx context.Context) (int64, error) {
	var id sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(id) FROM events`).Scan(&id)
	return id.Int64, err
}

// MarkStarted records that the daemon started, for the audit trail.
func (s *Service) MarkStarted(ctx context.Context, version string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		return s.emit(tx, Human, "daemon.started", "", map[string]string{"version": version})
	})
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

func newID(prefix string) string {
	const alphabet = "0123456789abcdefghjkmnpqrstvwxyz"
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return prefix + string(b)
}

func requireHuman(c Caller) error {
	if !c.IsHuman() {
		return forbiddenf("only the human owner can do this")
	}
	return nil
}

// Record appends an event for something that happened outside the core, such
// as the daemon starting an agent, so the audit trail and live views see it.
func (s *Service) Record(ctx context.Context, actor, typ, subject string, data any) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		return s.emit(tx, actor, typ, subject, data)
	})
}

// StartResult says what starting an agent on a task did.
type StartResult struct {
	Agent  string `json:"agent"`
	Task   string `json:"task"`
	Window string `json:"window"`
	Dir    string `json:"dir,omitempty"`
	// Started is false when the agent already had a window for the task; it
	// was given the task there instead.
	Started bool `json:"started"`
}

// Screen is the visible text of an agent's terminal.
type Screen struct {
	Agent  string `json:"agent"`
	Window string `json:"window"`
	Text   string `json:"text"`
}
