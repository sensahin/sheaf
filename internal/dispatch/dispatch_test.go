package dispatch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sensahin/sheaf/internal/config"
	"github.com/sensahin/sheaf/internal/core"
	"github.com/sensahin/sheaf/internal/launch"
	"github.com/sensahin/sheaf/internal/store"
)

type typedKeys struct {
	target, text string
	enter        bool
}

// fakeTerm is a tmux server in memory.
type fakeTerm struct {
	mu      sync.Mutex
	windows []Window
	opened  []launch.Spec
	typed   []typedKeys
}

func (f *fakeTerm) Windows() ([]Window, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Window(nil), f.windows...), nil
}

func (f *fakeTerm) Open(s launch.Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, s)
	name := s.Agent
	if s.Task != "" {
		name += ":" + s.Task
	}
	f.windows = append(f.windows, Window{Target: "@" + name, Name: name})
	return nil
}

func (f *fakeTerm) Capture(target string, lines int) (string, error) {
	return "screen of " + target + "\n\n", nil
}

func (f *fakeTerm) Type(target, text string, enter bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.typed = append(f.typed, typedKeys{target, text, enter})
	return nil
}

func (f *fakeTerm) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.opened), len(f.typed)
}

func newDispatcher(t *testing.T) (*Dispatcher, *fakeTerm, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "sheaf.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	paths := config.At(dir)
	svc := core.New(db, paths.WorktreeRoot)
	ctx := context.Background()
	for _, a := range []core.AgentSpec{
		{Name: "claude", Kind: "claude-code"},
		{Name: "codex", Kind: "codex", StartCmd: "codex -a never"},
		{Name: "aider", Kind: "aider"},
	} {
		_, tok, err := svc.AddAgent(ctx, core.HumanCaller, a)
		if err != nil {
			t.Fatal(err)
		}
		paths.SaveAgentToken(a.Name, tok)
	}
	term := &fakeTerm{}
	return &Dispatcher{Svc: svc, Paths: paths, Sheaf: "/usr/local/bin/sheaf", Term: term, Auto: true}, term, gitRepo(t)
}

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "t"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644)
	for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

func TestStartTask(t *testing.T) {
	ctx := context.Background()
	d, term, repo := newDispatcher(t)
	task, err := d.Svc.CreateTask(ctx, core.HumanCaller, core.TaskSpec{Title: "Fix login", Detail: "It loops on /login.", Owner: "claude", Repo: repo, Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	res, err := d.StartTask(ctx, task.ID, "")
	if err != nil || !res.Started || res.Dir != task.Worktree {
		t.Fatalf("start: %+v %v", res, err)
	}
	s := term.opened[0]
	if s.Dir != task.Worktree || !strings.HasPrefix(s.Command, "claude '") || !strings.Contains(s.Command, "It loops on /login.") || !strings.Contains(s.Command, task.Branch) {
		t.Fatalf("spec: %+v", s)
	}
	// The worktree got the agent's hooks.
	if _, err := os.Stat(filepath.Join(task.Worktree, ".claude", "settings.json")); err != nil {
		t.Fatalf("worktree not prepared: %v", err)
	}

	// Starting again types into the existing window instead of opening another.
	res, err = d.StartTask(ctx, task.ID, "")
	if err != nil || res.Started {
		t.Fatalf("restart: %+v %v", res, err)
	}
	if o, n := term.counts(); o != 1 || n != 1 || strings.Contains(term.typed[0].text, "\n") {
		t.Fatalf("opened %d typed %d: %+v", o, n, term.typed)
	}

	// The agent's own start command is used, with the prompt after it.
	t2, _ := d.Svc.CreateTask(ctx, core.HumanCaller, core.TaskSpec{Title: "Review", Owner: "codex", Repo: repo})
	if _, err := d.StartTask(ctx, t2.ID, ""); err != nil {
		t.Fatal(err)
	}
	if c := term.opened[1].Command; !strings.HasPrefix(c, "codex -a never 'Sheaf task") || term.opened[1].Dir != t2.Repo {
		t.Fatalf("codex command: %q in %s", c, term.opened[1].Dir)
	}

	// An agent that takes no prompt finds the task in its inbox.
	t3, _ := d.Svc.CreateTask(ctx, core.HumanCaller, core.TaskSpec{Title: "Docs", Owner: "aider"})
	if _, err := d.StartTask(ctx, t3.ID, ""); err != nil {
		t.Fatal(err)
	}
	if c := term.opened[2].Command; c != "aider" {
		t.Fatalf("aider command: %q", c)
	}
	msgs, _ := d.Svc.Inbox(ctx, core.Caller{Name: "aider"}, core.InboxRequest{Peek: true})
	if len(msgs) != 1 || !strings.Contains(msgs[0].Body, "Docs") {
		t.Fatalf("aider inbox: %+v", msgs)
	}

	// No owner, no start.
	t4, _ := d.Svc.CreateTask(ctx, core.HumanCaller, core.TaskSpec{Title: "Someday"})
	if _, err := d.StartTask(ctx, t4.ID, ""); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("ownerless start: %v", err)
	}
}

func TestHandoffStartsAgentAndIdleAgentsHearMessages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, term, repo := newDispatcher(t)
	defer func(old time.Duration) { settle = old }(settle)
	settle = 0
	go d.Run(ctx)
	time.Sleep(50 * time.Millisecond)

	task, _ := d.Svc.CreateTask(ctx, core.HumanCaller, core.TaskSpec{Title: "Fix login", Owner: "claude", Repo: repo, Worktree: true})
	if _, err := d.StartTask(ctx, task.ID, ""); err != nil {
		t.Fatal(err)
	}
	// Claude hands off to Codex for review: Codex, not running, is started
	// in the same worktree.
	claude := core.Caller{Name: "claude"}
	if _, err := d.Svc.Handoff(ctx, claude, core.HandoffRequest{Task: task.ID, To: "codex", Note: "Please review the diff."}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { o, _ := term.counts(); return o == 2 })
	if s := term.opened[1]; s.Agent != "codex" || s.Dir != task.Worktree || !strings.Contains(s.Command, "Read your Sheaf inbox first") {
		t.Fatalf("codex start: %+v", s)
	}

	// A message to Claude while it works waits; when it goes idle, it hears.
	d.Svc.ReportStatus(ctx, claude, core.StatusReport{State: core.StateWorking})
	if _, err := d.Svc.Send(ctx, core.HumanCaller, core.SendRequest{To: "claude", Body: "Also fix the logout link."}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, n := term.counts(); n != 0 {
		t.Fatalf("typed into a working agent: %+v", term.typed)
	}
	d.Svc.ReportStatus(ctx, claude, core.StatusReport{State: core.StateIdle})
	waitFor(t, func() bool { _, n := term.counts(); return n == 1 })
	if k := term.typed[0]; k.target != "@claude:"+task.ID || !strings.Contains(k.text, "inbox") || !k.enter {
		t.Fatalf("doorbell: %+v", k)
	}
	// Once per message, however often it goes idle.
	d.Svc.ReportStatus(ctx, claude, core.StatusReport{State: core.StateWorking})
	d.Svc.ReportStatus(ctx, claude, core.StatusReport{State: core.StateIdle})
	time.Sleep(200 * time.Millisecond)
	if _, n := term.counts(); n != 1 {
		t.Fatalf("rang twice: %+v", term.typed)
	}
}

func TestFreshAgentIsLeftAlone(t *testing.T) {
	ctx := context.Background()
	d, term, repo := newDispatcher(t)
	task, _ := d.Svc.CreateTask(ctx, core.HumanCaller, core.TaskSpec{Title: "x", Owner: "claude", Repo: repo})
	if _, err := d.StartTask(ctx, task.ID, ""); err != nil {
		t.Fatal(err)
	}
	d.Svc.Send(ctx, core.HumanCaller, core.SendRequest{To: "claude", Body: "hi"})
	if err := d.Check(ctx, "claude"); err != nil {
		t.Fatal(err)
	}
	if _, n := term.counts(); n != 0 {
		t.Fatalf("rang a booting agent: %+v", term.typed)
	}
}

func TestScreenAndType(t *testing.T) {
	ctx := context.Background()
	d, term, _ := newDispatcher(t)
	if _, err := d.Screen(ctx, "claude", "", 0); !errors.Is(err, ErrNotRunning) || !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("screen of stopped agent: %v", err)
	}
	term.windows = []Window{{Target: "@1", Name: "claude:t-1"}, {Target: "@2", Name: "codex"}}
	s, err := d.Screen(ctx, "claude", "", 0)
	if err != nil || s.Text != "screen of @1" || s.Window != "claude:t-1" {
		t.Fatalf("screen: %+v %v", s, err)
	}
	if err := d.Type(ctx, "codex", "", "1", true); err != nil || term.typed[0] != (typedKeys{"@2", "1", true}) {
		t.Fatalf("type: %v %+v", err, term.typed)
	}
	running, _ := d.Running()
	if len(running["claude"]) != 1 || len(running["codex"]) != 1 {
		t.Fatalf("running: %+v", running)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out")
}
