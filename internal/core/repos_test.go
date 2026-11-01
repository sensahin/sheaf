package core

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestReposAndRemoteTasks(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	addAgent(t, s, "builder")
	repo := gitRepo(t)
	other := gitRepo(t)
	d, tok, err := s.PairDevice(ctx, HumanCaller, "phone")
	if err != nil {
		t.Fatal(err)
	}
	phone, err := s.Authenticate(ctx, tok)
	if err != nil || phone.Device != d.ID {
		t.Fatal(err)
	}

	// A phone cannot register repositories or use one nobody registered.
	if _, err := s.AddRepo(ctx, phone, RepoSpec{Path: repo}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("device added repo: %v", err)
	}
	if _, err := s.CreateTask(ctx, phone, TaskSpec{Title: "x", Owner: "builder", Repo: other, Worktree: true}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("device used unregistered repo: %v", err)
	}

	r, err := s.AddRepo(ctx, HumanCaller, RepoSpec{Path: filepath.Join(repo, "."), Name: "app"})
	if err != nil || r.Name != "app" {
		t.Fatalf("add repo: %+v %v", r, err)
	}
	task, err := s.CreateTask(ctx, phone, TaskSpec{Title: "Fix login", Detail: "Steps: open /login", Owner: "builder", Repo: repo, Worktree: true})
	if err != nil {
		t.Fatalf("device task in registered repo: %v", err)
	}
	if got, _ := s.GetTask(ctx, task.ID); got.Detail != "Steps: open /login" || got.Worktree == "" {
		t.Fatalf("task: %+v", got)
	}

	// A repository the owner used from the CLI is offered afterwards.
	if _, err := s.CreateTask(ctx, HumanCaller, TaskSpec{Title: "y", Repo: other}); err != nil {
		t.Fatal(err)
	}
	repos, err := s.ListRepos(ctx)
	if err != nil || len(repos) != 2 {
		t.Fatalf("repos: %+v %v", repos, err)
	}
	if err := s.RemoveRepo(ctx, phone, other); !errors.Is(err, ErrForbidden) {
		t.Fatalf("device removed repo: %v", err)
	}
	if err := s.RemoveRepo(ctx, HumanCaller, other); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateAgent(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	b, _ := addAgent(t, s, "builder")
	cmd := "codex --ask-for-approval never"
	if _, err := s.UpdateAgent(ctx, b, AgentUpdate{Name: "builder", StartCmd: &cmd}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("agent changed its own command: %v", err)
	}
	a, err := s.UpdateAgent(ctx, HumanCaller, AgentUpdate{Name: "builder", StartCmd: &cmd})
	if err != nil || a.StartCmd != cmd || a.ResumeCmd != "claude --continue" {
		t.Fatalf("update: %+v %v", a, err)
	}
}
