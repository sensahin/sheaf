package core

import (
	"context"
	"database/sql"
	"time"
)

// Message kinds.
const (
	KindMessage  = "message"  // free text
	KindQuestion = "question" // expects a reply
	KindHandoff  = "handoff"  // a task changed hands; the body is the note
	KindDecision = "decision" // the human decided an approval
)

// Message is one entry in an agent's (or the human's) inbox.
type Message struct {
	ID        string     `json:"id"`
	From      string     `json:"from"`
	To        string     `json:"to"`
	Task      string     `json:"task,omitempty"`
	Kind      string     `json:"kind"`
	Body      string     `json:"body"`
	CreatedAt time.Time  `json:"created_at"`
	ReadAt    *time.Time `json:"read_at,omitempty"`
}

// SendRequest sends a message.
type SendRequest struct {
	To   string `json:"to"`
	Body string `json:"body"`
	Task string `json:"task,omitempty"`
	// Kind is message (default) or question.
	Kind string `json:"kind,omitempty"`
}

// Send delivers a message to an agent's inbox, or to the human's.
func (s *Service) Send(ctx context.Context, c Caller, r SendRequest) (Message, error) {
	if r.Body == "" {
		return Message{}, invalidf("message body is required")
	}
	if r.Kind == "" {
		r.Kind = KindMessage
	}
	if r.Kind != KindMessage && r.Kind != KindQuestion {
		return Message{}, invalidf("kind %q must be message or question", r.Kind)
	}
	if r.To == c.Name {
		return Message{}, invalidf("cannot message yourself")
	}
	var m Message
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if r.To != Human {
			if _, err := s.getAgent(ctx, tx, r.To); err != nil {
				return err
			}
		}
		if r.Task != "" {
			if _, err := s.getTask(ctx, tx, r.Task); err != nil {
				return err
			}
		}
		var err error
		m, err = s.insertMessage(tx, c.Name, r.To, r.Task, r.Kind, r.Body)
		return err
	})
	return m, err
}

// InboxRequest reads the caller's inbox.
type InboxRequest struct {
	// All includes messages already read (the newest 200).
	All bool `json:"all,omitempty"`
	// Peek leaves unread messages unread.
	Peek bool `json:"peek,omitempty"`
	// WaitSeconds blocks until a message arrives, up to this long.
	WaitSeconds int `json:"wait_seconds,omitempty"`
}

// Inbox returns the caller's messages, oldest first, and marks them read
// unless Peek is set. Marking read is the delivery receipt the sender sees.
func (s *Service) Inbox(ctx context.Context, c Caller, r InboxRequest) ([]Message, error) {
	q := `SELECT id, sender, recipient, task, kind, body, created_at, read_at FROM messages WHERE recipient = ?`
	if !r.All {
		q += ` AND read_at IS NULL`
	}
	// The newest 200, returned oldest first.
	q = `SELECT * FROM (` + q + ` ORDER BY created_at DESC, id DESC LIMIT 200) ORDER BY created_at, id`
	out := []Message{}
	err := s.waitFor(ctx, time.Duration(r.WaitSeconds)*time.Second, func() (bool, error) {
		rows, err := s.db.QueryContext(ctx, q, c.Name)
		if err != nil {
			return false, err
		}
		defer rows.Close()
		out = out[:0]
		for rows.Next() {
			var m Message
			var created string
			var read sql.NullString
			if err := rows.Scan(&m.ID, &m.From, &m.To, &m.Task, &m.Kind, &m.Body, &created, &read); err != nil {
				return false, err
			}
			m.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
			if read.Valid {
				t, _ := time.Parse(time.RFC3339Nano, read.String)
				m.ReadAt = &t
			}
			out = append(out, m)
		}
		return len(out) > 0, rows.Err()
	})
	if err != nil || r.Peek {
		return out, err
	}
	var unread []string
	for _, m := range out {
		if m.ReadAt == nil {
			unread = append(unread, m.ID)
		}
	}
	if len(unread) == 0 {
		return out, nil
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		now := s.now()
		for _, id := range unread {
			if _, err := tx.Exec(`UPDATE messages SET read_at = ? WHERE id = ? AND read_at IS NULL`, now, id); err != nil {
				return err
			}
		}
		return s.emit(tx, c.Name, "message.read", "", map[string][]string{"ids": unread})
	})
	return out, err
}

// MessageStatus returns a message the caller sent or received, with its read
// receipt.
func (s *Service) MessageStatus(ctx context.Context, c Caller, id string) (Message, error) {
	var m Message
	var created string
	var read sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, sender, recipient, task, kind, body, created_at, read_at FROM messages WHERE id = ?`, id).
		Scan(&m.ID, &m.From, &m.To, &m.Task, &m.Kind, &m.Body, &created, &read)
	if err == sql.ErrNoRows {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, err
	}
	if !c.IsHuman() && c.Name != m.From && c.Name != m.To {
		return Message{}, ErrNotFound
	}
	m.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if read.Valid {
		t, _ := time.Parse(time.RFC3339Nano, read.String)
		m.ReadAt = &t
	}
	return m, nil
}

func (s *Service) insertMessage(tx *sql.Tx, from, to, task, kind, body string) (Message, error) {
	m := Message{ID: newID("m-"), From: from, To: to, Task: task, Kind: kind, Body: body, CreatedAt: s.Now().UTC()}
	if _, err := tx.Exec(`INSERT INTO messages (id, sender, recipient, task, kind, body, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		m.ID, from, to, task, kind, body, m.CreatedAt.Format(time.RFC3339Nano)); err != nil {
		return Message{}, err
	}
	return m, s.emit(tx, from, "message.sent", m.ID, map[string]string{"to": to, "kind": kind, "task": task})
}
