package core

import (
	"context"
	"database/sql"
	"path/filepath"
	"time"

	"github.com/sensahin/sheaf/internal/worktree"
)

// Repo is a git repository tasks can be created in. The web queue offers
// these when you create a task from your phone, and a paired device may only
// use these.
type Repo struct {
	Path      string    `json:"path"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// RepoSpec registers a repository.
type RepoSpec struct {
	// Path is any path inside the repository; its root is stored.
	Path string `json:"path"`
	// Name is a short label; the directory name by default.
	Name string `json:"name,omitempty"`
}

// AddRepo registers a repository. Only the owner token may.
func (s *Service) AddRepo(ctx context.Context, c Caller, r RepoSpec) (Repo, error) {
	if err := requireOwnerToken(c); err != nil {
		return Repo{}, err
	}
	root, err := worktree.Root(r.Path)
	if err != nil {
		return Repo{}, invalidf("repo: %v", err)
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO repos (path, name, created_at) VALUES (?, ?, ?)
			ON CONFLICT(path) DO UPDATE SET name = excluded.name`, root, r.Name, s.now()); err != nil {
			return err
		}
		return s.emit(tx, c.Name, "repo.added", root, map[string]string{"name": r.Name})
	})
	if err != nil {
		return Repo{}, err
	}
	repos, err := s.ListRepos(ctx)
	for _, rp := range repos {
		if rp.Path == root {
			return rp, nil
		}
	}
	return Repo{}, err
}

// RemoveRepo forgets a repository. Its tasks and worktrees are untouched.
func (s *Service) RemoveRepo(ctx context.Context, c Caller, path string) error {
	if err := requireOwnerToken(c); err != nil {
		return err
	}
	// Stored paths are git's top level, with symlinks resolved (on macOS,
	// /var is /private/var); resolve the same way, even for a deleted repo.
	if root, err := worktree.Root(path); err == nil {
		path = root
	} else if abs, err := filepath.Abs(path); err == nil {
		path = abs
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			path = real
		}
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM repos WHERE path = ?`, path)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return s.emit(tx, c.Name, "repo.removed", path, nil)
	})
}

// ListRepos returns the registered repositories, by name.
func (s *Service) ListRepos(ctx context.Context) ([]Repo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path, name, created_at FROM repos ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Repo{}
	for rows.Next() {
		var r Repo
		var created string
		if err := rows.Scan(&r.Path, &r.Name, &created); err != nil {
			return nil, err
		}
		if r.Name == "" {
			r.Name = filepath.Base(r.Path)
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) repoKnown(ctx context.Context, root string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM repos WHERE path = ?`, root).Scan(&n)
	return n > 0, err
}
