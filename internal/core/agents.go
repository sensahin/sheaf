package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/sensahin/sheaf/internal/config"
)

// Agent states, as reported by the agent (or read from OSC 7501 by an adapter).
const (
	StateIdle    = "idle"
	StateWorking = "working"
	StateBlocked = "blocked"
	StateDone    = "done"
)

var validStates = map[string]bool{StateIdle: true, StateWorking: true, StateBlocked: true, StateDone: true}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// Agent is a registered agent: a stalk in the sheaf.
type Agent struct {
	Name      string    `json:"name"`
	Kind      string    `json:"kind,omitempty"`
	Policy    Policy    `json:"policy"`
	ResumeCmd string    `json:"resume_cmd,omitempty"`
	State     string    `json:"state"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AgentSpec describes an agent to register.
type AgentSpec struct {
	Name string `json:"name"`
	// Kind is the agent program, such as claude-code, codex or opencode.
	Kind string `json:"kind,omitempty"`
	// ResumeCmd restarts the agent where it left off, run in its task's
	// worktree after a reboot (for example "claude --continue").
	ResumeCmd string  `json:"resume_cmd,omitempty"`
	Policy    *Policy `json:"policy,omitempty"`
}

// AddAgent registers an agent and returns its token. The token is shown once;
// only its hash is stored.
func (s *Service) AddAgent(ctx context.Context, c Caller, spec AgentSpec) (Agent, string, error) {
	if err := requireHuman(c); err != nil {
		return Agent{}, "", err
	}
	if !nameRE.MatchString(spec.Name) || spec.Name == Human {
		return Agent{}, "", invalidf("agent name %q must be 1-32 lowercase letters, digits, - or _, and not %q", spec.Name, Human)
	}
	p := DefaultPolicy()
	if spec.Policy != nil {
		p = *spec.Policy
	}
	if err := p.Validate(); err != nil {
		return Agent{}, "", err
	}
	pj, err := jsonString(p)
	if err != nil {
		return Agent{}, "", err
	}
	token := config.NewToken("sha_")
	now := s.now()
	err = s.tx(ctx, func(tx *sql.Tx) error {
		var exists int
		tx.QueryRow(`SELECT COUNT(*) FROM agents WHERE name = ?`, spec.Name).Scan(&exists)
		if exists > 0 {
			return invalidf("agent %q already exists", spec.Name)
		}
		if _, err := tx.Exec(`INSERT INTO agents (name, kind, token_hash, policy, resume_cmd, state, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, spec.Name, spec.Kind, hashToken(token), pj, spec.ResumeCmd, StateIdle, now, now); err != nil {
			return err
		}
		return s.emit(tx, c.Name, "agent.added", spec.Name, map[string]string{"kind": spec.Kind})
	})
	if err != nil {
		return Agent{}, "", err
	}
	a, err := s.getAgent(ctx, s.db, spec.Name)
	return a, token, err
}

// RotateToken issues a new token for an agent, revoking the old one.
func (s *Service) RotateToken(ctx context.Context, c Caller, name string) (string, error) {
	if err := requireHuman(c); err != nil {
		return "", err
	}
	token := config.NewToken("sha_")
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE agents SET token_hash = ?, updated_at = ? WHERE name = ?`, hashToken(token), s.now(), name)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return s.emit(tx, c.Name, "agent.token_rotated", name, nil)
	})
	return token, err
}

// RemoveAgent deletes an agent. Its tasks keep their history but lose their
// owner.
func (s *Service) RemoveAgent(ctx context.Context, c Caller, name string) error {
	if err := requireHuman(c); err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM agents WHERE name = ?`, name)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if _, err := tx.Exec(`UPDATE tasks SET owner = '', updated_at = ? WHERE owner = ?`, s.now(), name); err != nil {
			return err
		}
		return s.emit(tx, c.Name, "agent.removed", name, nil)
	})
}

// ListAgents returns all agents.
func (s *Service) ListAgents(ctx context.Context) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+agentCols+` FROM agents ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAgent returns one agent.
func (s *Service) GetAgent(ctx context.Context, name string) (Agent, error) {
	return s.getAgent(ctx, s.db, name)
}

// StatusReport is an agent telling Sheaf what it is doing.
type StatusReport struct {
	State string `json:"state"`
	Note  string `json:"note,omitempty"`
	// Task, if set, also moves that task: working→working, blocked→blocked,
	// done→done.
	Task string `json:"task,omitempty"`
}

// ReportStatus updates the caller's state and, optionally, its task's status.
func (s *Service) ReportStatus(ctx context.Context, c Caller, r StatusReport) error {
	if c.IsHuman() {
		return invalidf("only agents report status")
	}
	if !validStates[r.State] {
		return invalidf("state %q must be idle, working, blocked or done", r.State)
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		now := s.now()
		if _, err := tx.Exec(`UPDATE agents SET state = ?, note = ?, updated_at = ? WHERE name = ?`, r.State, r.Note, now, c.Name); err != nil {
			return err
		}
		if r.Task != "" && r.State != StateIdle {
			t, err := s.getTask(ctx, tx, r.Task)
			if err != nil {
				return err
			}
			if t.Owner != c.Name {
				return forbiddenf("task %s is owned by %q", t.ID, t.Owner)
			}
			if _, err := tx.Exec(`UPDATE tasks SET status = ?, updated_at = ? WHERE id = ?`, r.State, now, r.Task); err != nil {
				return err
			}
		}
		return s.emit(tx, c.Name, "agent.status", r.Task, r)
	})
}

const agentCols = `name, kind, policy, resume_cmd, state, note, created_at, updated_at`

type scanner interface{ Scan(...any) error }

type querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func scanAgent(r scanner) (Agent, error) {
	var a Agent
	var policy, created, updated string
	if err := r.Scan(&a.Name, &a.Kind, &policy, &a.ResumeCmd, &a.State, &a.Note, &created, &updated); err != nil {
		return Agent{}, err
	}
	if err := json.Unmarshal([]byte(policy), &a.Policy); err != nil {
		return Agent{}, err
	}
	a.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	a.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return a, nil
}

func (s *Service) getAgent(ctx context.Context, q querier, name string) (Agent, error) {
	a, err := scanAgent(q.QueryRowContext(ctx, `SELECT `+agentCols+` FROM agents WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, fmt.Errorf("%w: no agent named %q", ErrNotFound, name)
	}
	return a, err
}

func jsonString(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}
