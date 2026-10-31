package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sensahin/sheaf/internal/worktree"
)

// Approval kinds.
const (
	ApprovalAction   = "action"   // permission to do something the policy marks "ask"
	ApprovalDiff     = "diff"     // review of a task's changes
	ApprovalQuestion = "question" // the agent needs an answer to go on
)

// Approval statuses.
const (
	ApprovalPending  = "pending"
	ApprovalApproved = "approved"
	ApprovalDenied   = "denied"
	ApprovalAnswered = "answered"
)

// Approval is an item in the human's "needs me" queue.
type Approval struct {
	ID        string     `json:"id"`
	Agent     string     `json:"agent"`
	Task      string     `json:"task,omitempty"`
	Kind      string     `json:"kind"`
	Summary   string     `json:"summary"`
	Detail    string     `json:"detail,omitempty"`
	Status    string     `json:"status"`
	Answer    string     `json:"answer,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	DecidedAt *time.Time `json:"decided_at,omitempty"`
}

// ApprovalRequest asks the human for something.
type ApprovalRequest struct {
	// Kind is action (default), diff or question.
	Kind    string `json:"kind,omitempty"`
	Summary string `json:"summary"`
	// Detail is the command, diff or context. For a diff request on a task
	// with a worktree, it is filled in from git when left empty.
	Detail string `json:"detail,omitempty"`
	Task   string `json:"task,omitempty"`
}

// RequestApproval adds an item to the human's queue.
func (s *Service) RequestApproval(ctx context.Context, c Caller, r ApprovalRequest) (Approval, error) {
	if c.IsHuman() {
		return Approval{}, invalidf("only agents request approval")
	}
	switch r.Kind {
	case ApprovalAction, ApprovalDiff, ApprovalQuestion:
	case "":
		r.Kind = ApprovalAction
	default:
		return Approval{}, invalidf("kind %q must be action, diff or question", r.Kind)
	}
	if r.Summary == "" {
		return Approval{}, invalidf("summary is required")
	}
	if r.Task != "" {
		t, err := s.getTask(ctx, s.db, r.Task)
		if err != nil {
			return Approval{}, err
		}
		if r.Kind == ApprovalDiff && r.Detail == "" && t.Worktree != "" {
			d, err := worktree.Diff(t.Worktree, t.Base)
			if err != nil {
				return Approval{}, fmt.Errorf("diff for task %s: %w", t.ID, err)
			}
			r.Detail = d
		}
	}
	a := Approval{ID: newID("a-"), Agent: c.Name, Task: r.Task, Kind: r.Kind, Summary: r.Summary, Detail: r.Detail,
		Status: ApprovalPending, CreatedAt: s.Now().UTC()}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO approvals (id, agent, task, kind, summary, detail, status, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			a.ID, a.Agent, a.Task, a.Kind, a.Summary, a.Detail, a.Status, a.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		return s.emit(tx, c.Name, "approval.requested", a.ID, map[string]string{"kind": a.Kind, "summary": a.Summary, "task": a.Task})
	})
	return a, err
}

// DecideRequest is the human's decision on a queue item.
type DecideRequest struct {
	ID string `json:"id"`
	// Status is approved, denied or answered.
	Status string `json:"status"`
	// Answer is the reply to a question, or a note on an approval or denial.
	Answer string `json:"answer,omitempty"`
}

// Decide resolves a pending item and tells the agent in its inbox, so an agent
// that stopped waiting still hears back.
func (s *Service) Decide(ctx context.Context, c Caller, d DecideRequest) (Approval, error) {
	if err := requireHuman(c); err != nil {
		return Approval{}, err
	}
	switch d.Status {
	case ApprovalApproved, ApprovalDenied, ApprovalAnswered:
	default:
		return Approval{}, invalidf("decision %q must be approved, denied or answered", d.Status)
	}
	if d.Status == ApprovalAnswered && d.Answer == "" {
		return Approval{}, invalidf("an answer needs text")
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		a, err := s.getApproval(ctx, tx, d.ID)
		if err != nil {
			return err
		}
		if a.Status != ApprovalPending {
			return invalidf("%s is already %s", a.ID, a.Status)
		}
		if _, err := tx.Exec(`UPDATE approvals SET status = ?, answer = ?, decided_at = ? WHERE id = ?`, d.Status, d.Answer, s.now(), d.ID); err != nil {
			return err
		}
		body := fmt.Sprintf("%s %s: %s", a.ID, d.Status, a.Summary)
		if d.Answer != "" {
			body += "\n\n" + d.Answer
		}
		if _, err := s.insertMessage(tx, Human, a.Agent, a.Task, KindDecision, body); err != nil {
			return err
		}
		return s.emit(tx, c.Name, "approval.decided", a.ID, map[string]string{"status": d.Status, "answer": d.Answer})
	})
	if err != nil {
		return Approval{}, err
	}
	return s.getApproval(ctx, s.db, d.ID)
}

// ListApprovals returns queue items, oldest first. An empty status means
// pending.
func (s *Service) ListApprovals(ctx context.Context, status string) ([]Approval, error) {
	if status == "" {
		status = ApprovalPending
	}
	q := `SELECT ` + approvalCols + ` FROM approvals`
	var args []any
	if status != "all" {
		q += ` WHERE status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY created_at`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Approval{}
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetApproval returns one item. Agents can only see their own.
func (s *Service) GetApproval(ctx context.Context, c Caller, id string) (Approval, error) {
	a, err := s.getApproval(ctx, s.db, id)
	if err != nil {
		return Approval{}, err
	}
	if !c.IsHuman() && a.Agent != c.Name {
		return Approval{}, ErrNotFound
	}
	return a, nil
}

// WaitApproval blocks until the item is decided or the timeout passes, then
// returns its current state.
func (s *Service) WaitApproval(ctx context.Context, c Caller, id string, timeout time.Duration) (Approval, error) {
	var a Approval
	err := s.waitFor(ctx, timeout, func() (bool, error) {
		var err error
		a, err = s.GetApproval(ctx, c, id)
		return err != nil || a.Status != ApprovalPending, err
	})
	return a, err
}

const approvalCols = `id, agent, task, kind, summary, detail, status, answer, created_at, decided_at`

func scanApproval(r scanner) (Approval, error) {
	var a Approval
	var created string
	var decided sql.NullString
	if err := r.Scan(&a.ID, &a.Agent, &a.Task, &a.Kind, &a.Summary, &a.Detail, &a.Status, &a.Answer, &created, &decided); err != nil {
		return Approval{}, err
	}
	a.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if decided.Valid {
		t, _ := time.Parse(time.RFC3339Nano, decided.String)
		a.DecidedAt = &t
	}
	return a, nil
}

func (s *Service) getApproval(ctx context.Context, q querier, id string) (Approval, error) {
	a, err := scanApproval(q.QueryRowContext(ctx, `SELECT `+approvalCols+` FROM approvals WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Approval{}, fmt.Errorf("%w: no approval %q", ErrNotFound, id)
	}
	return a, err
}
