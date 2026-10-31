package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/sensahin/sheaf/internal/worktree"
)

// Task statuses. working, blocked and done mirror agent states; open means
// nobody has started; cancelled is set by the owner.
const (
	TaskOpen      = "open"
	TaskWorking   = "working"
	TaskBlocked   = "blocked"
	TaskDone      = "done"
	TaskCancelled = "cancelled"
)

var validTaskStatus = map[string]bool{TaskOpen: true, TaskWorking: true, TaskBlocked: true, TaskDone: true, TaskCancelled: true}

// Task is a unit of work with one owner agent and, optionally, its own git
// worktree.
type Task struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Owner     string    `json:"owner,omitempty"`
	Parent    string    `json:"parent,omitempty"`
	Status    string    `json:"status"`
	Repo      string    `json:"repo,omitempty"`
	Worktree  string    `json:"worktree,omitempty"`
	Branch    string    `json:"branch,omitempty"`
	Base      string    `json:"base,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TaskSpec describes a task to create.
type TaskSpec struct {
	Title  string `json:"title"`
	Owner  string `json:"owner,omitempty"`
	Parent string `json:"parent,omitempty"`
	// Repo is a path inside a git repository. With Worktree set, the task gets
	// its own worktree on branch sheaf/<task id>.
	Repo     string `json:"repo,omitempty"`
	Worktree bool   `json:"worktree,omitempty"`
}

// CreateTask creates a task. An agent creating a task owns it unless it names
// another owner.
func (s *Service) CreateTask(ctx context.Context, c Caller, spec TaskSpec) (Task, error) {
	if spec.Title == "" {
		return Task{}, invalidf("task title is required")
	}
	if spec.Owner == "" && !c.IsHuman() {
		spec.Owner = c.Name
	}
	if spec.Owner != "" {
		if _, err := s.getAgent(ctx, s.db, spec.Owner); err != nil {
			return Task{}, err
		}
	}
	if spec.Parent != "" {
		if _, err := s.getTask(ctx, s.db, spec.Parent); err != nil {
			return Task{}, err
		}
	}
	t := Task{ID: newID("t-"), Title: spec.Title, Owner: spec.Owner, Parent: spec.Parent, Status: TaskOpen}
	if spec.Repo != "" {
		root, err := worktree.Root(spec.Repo)
		if err != nil {
			return Task{}, invalidf("repo: %v", err)
		}
		t.Repo = root
	}
	if spec.Worktree {
		if t.Repo == "" {
			return Task{}, invalidf("a worktree needs a repo")
		}
		t.Worktree = filepath.Join(s.WorktreeRoot, t.ID)
		t.Branch = "sheaf/" + t.ID
		base, err := worktree.Add(t.Repo, t.Worktree, t.Branch)
		if err != nil {
			return Task{}, err
		}
		t.Base = base
	}
	now := s.now()
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO tasks (id, title, owner, parent, status, repo, worktree, branch, base, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID, t.Title, t.Owner, t.Parent, t.Status, t.Repo, t.Worktree, t.Branch, t.Base, now, now); err != nil {
			return err
		}
		return s.emit(tx, c.Name, "task.created", t.ID, map[string]string{
			"title": t.Title, "owner": t.Owner, "parent": t.Parent, "worktree": t.Worktree, "branch": t.Branch,
		})
	})
	if err != nil {
		return Task{}, err
	}
	return s.getTask(ctx, s.db, t.ID)
}

// GetTask returns one task.
func (s *Service) GetTask(ctx context.Context, id string) (Task, error) {
	return s.getTask(ctx, s.db, id)
}

// TaskFilter narrows ListTasks. Empty fields match everything.
type TaskFilter struct {
	Owner string `json:"owner,omitempty"`
	// Active limits the list to open, working and blocked tasks.
	Active bool `json:"active,omitempty"`
}

// ListTasks returns tasks, newest first.
func (s *Service) ListTasks(ctx context.Context, f TaskFilter) ([]Task, error) {
	q := `SELECT ` + taskCols + ` FROM tasks WHERE 1=1`
	var args []any
	if f.Owner != "" {
		q += ` AND owner = ?`
		args = append(args, f.Owner)
	}
	if f.Active {
		q += ` AND status IN ('open', 'working', 'blocked')`
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetTaskStatus moves a task. Only its owner or the human may.
func (s *Service) SetTaskStatus(ctx context.Context, c Caller, id, status string) error {
	if !validTaskStatus[status] {
		return invalidf("status %q must be open, working, blocked, done or cancelled", status)
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		t, err := s.getTask(ctx, tx, id)
		if err != nil {
			return err
		}
		if !c.IsHuman() && t.Owner != c.Name {
			return forbiddenf("task %s is owned by %q", id, t.Owner)
		}
		if _, err := tx.Exec(`UPDATE tasks SET status = ?, updated_at = ? WHERE id = ?`, status, s.now(), id); err != nil {
			return err
		}
		return s.emit(tx, c.Name, "task.status", id, map[string]string{"from": t.Status, "to": status})
	})
}

// HandoffRequest passes a task to another agent.
type HandoffRequest struct {
	Task string `json:"task"`
	To   string `json:"to"`
	// Note is what the next owner needs to know: what is done, what is left,
	// where to look.
	Note string `json:"note"`
}

// Handoff transfers a task's ownership and delivers the note to the new
// owner's inbox, in one transaction.
func (s *Service) Handoff(ctx context.Context, c Caller, h HandoffRequest) (Message, error) {
	if h.Note == "" {
		return Message{}, invalidf("a handoff needs a note for the next owner")
	}
	var m Message
	err := s.tx(ctx, func(tx *sql.Tx) error {
		t, err := s.getTask(ctx, tx, h.Task)
		if err != nil {
			return err
		}
		if !c.IsHuman() && t.Owner != c.Name {
			return forbiddenf("only the owner of task %s (%q) can hand it off", t.ID, t.Owner)
		}
		if _, err := s.getAgent(ctx, tx, h.To); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE tasks SET owner = ?, status = ?, updated_at = ? WHERE id = ?`, h.To, TaskOpen, s.now(), t.ID); err != nil {
			return err
		}
		m, err = s.insertMessage(tx, c.Name, h.To, t.ID, KindHandoff, h.Note)
		if err != nil {
			return err
		}
		return s.emit(tx, c.Name, "task.handoff", t.ID, map[string]string{"from": t.Owner, "to": h.To, "message": m.ID})
	})
	return m, err
}

const taskCols = `id, title, owner, parent, status, repo, worktree, branch, base, created_at, updated_at`

func scanTask(r scanner) (Task, error) {
	var t Task
	var created, updated string
	if err := r.Scan(&t.ID, &t.Title, &t.Owner, &t.Parent, &t.Status, &t.Repo, &t.Worktree, &t.Branch, &t.Base, &created, &updated); err != nil {
		return Task{}, err
	}
	t.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	t.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return t, nil
}

func (s *Service) getTask(ctx context.Context, q querier, id string) (Task, error) {
	t, err := scanTask(q.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, fmt.Errorf("%w: no task %q", ErrNotFound, id)
	}
	return t, err
}

// ResumeItem is an agent to restart after a reboot.
type ResumeItem struct {
	Agent   string `json:"agent"`
	Task    string `json:"task"`
	Title   string `json:"title"`
	Dir     string `json:"dir"`
	Command string `json:"command"`
}

// ResumePlan lists, for every agent with a resume command, its active tasks
// and where to restart it. Tasks are state in the database, so this works
// the same after a crash or a reboot.
func (s *Service) ResumePlan(ctx context.Context) ([]ResumeItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.name, a.resume_cmd, t.id, t.title, t.worktree, t.repo
		FROM agents a JOIN tasks t ON t.owner = a.name
		WHERE a.resume_cmd != '' AND t.status IN ('open', 'working', 'blocked')
		ORDER BY a.name, t.updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ResumeItem{}
	for rows.Next() {
		var it ResumeItem
		var wt, repo string
		if err := rows.Scan(&it.Agent, &it.Command, &it.Task, &it.Title, &wt, &repo); err != nil {
			return nil, err
		}
		it.Dir = wt
		if it.Dir == "" {
			it.Dir = repo
		}
		out = append(out, it)
	}
	return out, rows.Err()
}
