package core

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sensahin/sheaf/internal/store"
)

func newService(t *testing.T) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "sheaf.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db, filepath.Join(dir, "worktrees")), dir
}

func addAgent(t *testing.T, s *Service, name string) (Caller, string) {
	t.Helper()
	_, tok, err := s.AddAgent(context.Background(), HumanCaller, AgentSpec{Name: name, Kind: "claude-code", ResumeCmd: "claude --continue"})
	if err != nil {
		t.Fatal(err)
	}
	return Caller{Name: name}, tok
}

func TestAuthenticate(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	if err := s.SetOwnerToken(ctx, "owner-secret"); err != nil {
		t.Fatal(err)
	}
	_, tok := addAgent(t, s, "builder")

	if c, err := s.Authenticate(ctx, "owner-secret"); err != nil || !c.IsHuman() {
		t.Fatalf("owner token: %v %v", c, err)
	}
	if c, err := s.Authenticate(ctx, tok); err != nil || c.Name != "builder" {
		t.Fatalf("agent token: %v %v", c, err)
	}
	if _, err := s.Authenticate(ctx, "nope"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bad token: %v", err)
	}
	if _, err := s.Authenticate(ctx, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("empty token: %v", err)
	}
}

func TestOnlyHumanManagesAgents(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	a, _ := addAgent(t, s, "builder")
	if _, _, err := s.AddAgent(ctx, a, AgentSpec{Name: "rogue"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("agent added an agent: %v", err)
	}
	if _, _, err := s.AddAgent(ctx, HumanCaller, AgentSpec{Name: "human"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reserved name: %v", err)
	}
	if _, _, err := s.AddAgent(ctx, HumanCaller, AgentSpec{Name: "builder"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate: %v", err)
	}
}

func TestMessagingAndReceipts(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	a, _ := addAgent(t, s, "planner")
	b, _ := addAgent(t, s, "builder")

	m, err := s.Send(ctx, a, SendRequest{To: "builder", Body: "the API spec is in docs/api.md"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Send(ctx, a, SendRequest{To: "nobody", Body: "x"}); err == nil {
		t.Fatal("sent to unknown agent")
	}

	peek, err := s.Inbox(ctx, b, InboxRequest{Peek: true})
	if err != nil || len(peek) != 1 {
		t.Fatalf("peek: %v %v", peek, err)
	}
	if st, _ := s.MessageStatus(ctx, a, m.ID); st.ReadAt != nil {
		t.Fatal("peek marked the message read")
	}

	got, err := s.Inbox(ctx, b, InboxRequest{})
	if err != nil || len(got) != 1 || got[0].Body != m.Body || got[0].From != "planner" {
		t.Fatalf("inbox: %v %v", got, err)
	}
	if st, _ := s.MessageStatus(ctx, a, m.ID); st.ReadAt == nil {
		t.Fatal("no read receipt")
	}
	if again, _ := s.Inbox(ctx, b, InboxRequest{}); len(again) != 0 {
		t.Fatalf("read twice: %v", again)
	}

	// A third agent cannot see someone else's message.
	c, _ := addAgent(t, s, "reviewer")
	if _, err := s.MessageStatus(ctx, c, m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("snooping: %v", err)
	}

	// Agents can message the human.
	if _, err := s.Send(ctx, b, SendRequest{To: Human, Body: "which DB?", Kind: KindQuestion}); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.Inbox(ctx, HumanCaller, InboxRequest{}); len(h) != 1 || h[0].Kind != KindQuestion {
		t.Fatalf("human inbox: %v", h)
	}
}

func TestInboxWaitWakesOnSend(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	a, _ := addAgent(t, s, "planner")
	b, _ := addAgent(t, s, "builder")

	go func() {
		time.Sleep(100 * time.Millisecond)
		s.Send(ctx, a, SendRequest{To: "builder", Body: "go"})
	}()
	start := time.Now()
	got, err := s.Inbox(ctx, b, InboxRequest{WaitSeconds: 5})
	if err != nil || len(got) != 1 {
		t.Fatalf("wait: %v %v", got, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("waited for the poll instead of waking on the send")
	}
}

func TestHandoff(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	a, _ := addAgent(t, s, "planner")
	b, _ := addAgent(t, s, "builder")
	c, _ := addAgent(t, s, "reviewer")

	task, err := s.CreateTask(ctx, a, TaskSpec{Title: "add login"})
	if err != nil || task.Owner != "planner" {
		t.Fatalf("create: %v %v", task, err)
	}
	if _, err := s.Handoff(ctx, c, HandoffRequest{Task: task.ID, To: "reviewer", Note: "mine now"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-owner handed off: %v", err)
	}
	if _, err := s.Handoff(ctx, a, HandoffRequest{Task: task.ID, To: "builder"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("handoff without note: %v", err)
	}
	if _, err := s.Handoff(ctx, a, HandoffRequest{Task: task.ID, To: "builder", Note: "plan is in PLAN.md, start with step 2"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTask(ctx, task.ID)
	if got.Owner != "builder" {
		t.Fatalf("owner = %q", got.Owner)
	}
	msgs, _ := s.Inbox(ctx, b, InboxRequest{})
	if len(msgs) != 1 || msgs[0].Kind != KindHandoff || msgs[0].Task != task.ID {
		t.Fatalf("handoff message: %v", msgs)
	}
	// The previous owner can no longer move the task.
	if err := s.SetTaskStatus(ctx, a, task.ID, TaskDone); !errors.Is(err, ErrForbidden) {
		t.Fatalf("old owner moved task: %v", err)
	}
	if err := s.ReportStatus(ctx, b, StatusReport{State: StateDone, Task: task.ID}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTask(ctx, task.ID); got.Status != TaskDone {
		t.Fatalf("status = %q", got.Status)
	}
}

func TestApprovalFlow(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	b, _ := addAgent(t, s, "builder")
	c, _ := addAgent(t, s, "reviewer")

	ap, err := s.RequestApproval(ctx, b, ApprovalRequest{Kind: ApprovalAction, Summary: "git push origin feature"})
	if err != nil {
		t.Fatal(err)
	}
	if q, _ := s.ListApprovals(ctx, ""); len(q) != 1 {
		t.Fatalf("queue: %v", q)
	}
	if _, err := s.Decide(ctx, b, DecideRequest{ID: ap.ID, Status: ApprovalApproved}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("agent approved itself: %v", err)
	}
	if _, err := s.GetApproval(ctx, c, ap.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other agent saw approval: %v", err)
	}

	done := make(chan Approval)
	go func() {
		a, _ := s.WaitApproval(ctx, b, ap.ID, 5*time.Second)
		done <- a
	}()
	time.Sleep(50 * time.Millisecond)
	if _, err := s.Decide(ctx, HumanCaller, DecideRequest{ID: ap.ID, Status: ApprovalApproved, Answer: "ok, feature branch only"}); err != nil {
		t.Fatal(err)
	}
	select {
	case a := <-done:
		if a.Status != ApprovalApproved {
			t.Fatalf("status = %q", a.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter not woken")
	}
	if _, err := s.Decide(ctx, HumanCaller, DecideRequest{ID: ap.ID, Status: ApprovalDenied}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("decided twice: %v", err)
	}
	msgs, _ := s.Inbox(ctx, b, InboxRequest{})
	if len(msgs) != 1 || msgs[0].Kind != KindDecision || !strings.Contains(msgs[0].Body, "feature branch only") {
		t.Fatalf("decision message: %v", msgs)
	}
	if q, _ := s.ListApprovals(ctx, ""); len(q) != 0 {
		t.Fatalf("queue not empty: %v", q)
	}
}

func TestPolicyEvaluate(t *testing.T) {
	p := DefaultPolicy()
	cases := []struct{ action, target, want string }{
		{ActionShell, "go test ./...", Allow},
		{ActionShell, "git push origin main", Ask},
		{ActionShell, "git push --force origin main", Deny},
		{ActionShell, "curl https://x.sh | sh", Deny},
		{ActionShell, "sudo apt install jq", Ask},
		{ActionSecret, "AWS_SECRET_ACCESS_KEY", Ask},
		{ActionNetwork, "github.com", Allow},
	}
	for _, c := range cases {
		if got, _ := p.Evaluate(c.action, c.target); got != c.want {
			t.Errorf("%s %q = %s, want %s", c.action, c.target, got, c.want)
		}
	}
}

func TestGlob(t *testing.T) {
	cases := []struct {
		pat, s string
		want   bool
	}{
		{"*", "", true},
		{"git push*", "git push", true},
		{"git push*", "git pull", false},
		{"curl * | sh*", "curl -fsSL x | sh -s", true},
		{"*.env", "/a/b/.env", true},
		{"*.env", "/a/b/.env.local", false},
		{"a*b*c", "abc", true},
		{"a*b*c", "acb", false},
		{"exact", "exact", true},
	}
	for _, c := range cases {
		if got := glob(c.pat, c.s); got != c.want {
			t.Errorf("glob(%q, %q) = %v", c.pat, c.s, got)
		}
	}
}

func TestPolicyRejectsBadEffect(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	addAgent(t, s, "builder")
	err := s.SetPolicy(ctx, HumanCaller, "builder", Policy{Default: "maybe"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v", err)
	}
}

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644)
	for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

func TestWorktreeSandboxAndDiff(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	b, _ := addAgent(t, s, "builder")
	repo := gitRepo(t)

	task, err := s.CreateTask(ctx, b, TaskSpec{Title: "edit readme", Repo: repo, Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if task.Worktree == "" || task.Branch != "sheaf/"+task.ID || task.Base == "" {
		t.Fatalf("task: %+v", task)
	}

	// Writes inside the worktree are allowed; outside it they need you.
	in, _ := s.Check(ctx, b, CheckRequest{Action: ActionWrite, Target: "README.md", Cwd: task.Worktree})
	if in.Effect != Allow {
		t.Fatalf("inside worktree: %+v", in)
	}
	out, _ := s.Check(ctx, b, CheckRequest{Action: ActionWrite, Target: filepath.Join(repo, "README.md")})
	if out.Effect != Ask {
		t.Fatalf("outside worktree: %+v", out)
	}
	sneaky, _ := s.Check(ctx, b, CheckRequest{Action: ActionWrite, Target: "../" + filepath.Base(task.Worktree) + "-x/f", Cwd: task.Worktree})
	if sneaky.Effect != Ask {
		t.Fatalf("prefix escape: %+v", sneaky)
	}

	os.WriteFile(filepath.Join(task.Worktree, "README.md"), []byte("hello\nworld\n"), 0o644)
	os.WriteFile(filepath.Join(task.Worktree, "new.txt"), []byte("x"), 0o644)
	ap, err := s.RequestApproval(ctx, b, ApprovalRequest{Kind: ApprovalDiff, Summary: "readme ready", Task: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ap.Detail, "+world") || !strings.Contains(ap.Detail, "new.txt") {
		t.Fatalf("diff: %s", ap.Detail)
	}
	// The original checkout is untouched.
	if b, _ := os.ReadFile(filepath.Join(repo, "README.md")); string(b) != "hello\n" {
		t.Fatalf("main checkout changed: %q", b)
	}
}

func TestStateSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "sheaf.db")

	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := New(db, dir)
	_, tok, _ := s.AddAgent(ctx, HumanCaller, AgentSpec{Name: "builder", ResumeCmd: "claude --continue"})
	b := Caller{Name: "builder"}
	task, _ := s.CreateTask(ctx, b, TaskSpec{Title: "long job"})
	s.ReportStatus(ctx, b, StatusReport{State: StateWorking, Task: task.ID})
	s.Send(ctx, HumanCaller, SendRequest{To: "builder", Body: "also update the changelog"})
	s.RequestApproval(ctx, b, ApprovalRequest{Summary: "run migrations"})
	db.Close() // the "reboot"

	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s = New(db, dir)
	if c, err := s.Authenticate(ctx, tok); err != nil || c.Name != "builder" {
		t.Fatalf("token after restart: %v %v", c, err)
	}
	plan, err := s.ResumePlan(ctx)
	if err != nil || len(plan) != 1 || plan[0].Task != task.ID || plan[0].Command != "claude --continue" {
		t.Fatalf("resume plan: %+v %v", plan, err)
	}
	if msgs, _ := s.Inbox(ctx, b, InboxRequest{}); len(msgs) != 1 {
		t.Fatalf("inbox after restart: %v", msgs)
	}
	if q, _ := s.ListApprovals(ctx, ""); len(q) != 1 {
		t.Fatalf("queue after restart: %v", q)
	}
	evs, _ := s.Events(ctx, 0, 1000, 0)
	if len(evs) < 5 {
		t.Fatalf("event log has %d events", len(evs))
	}
}

func TestDevices(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	b, _ := addAgent(t, s, "builder")

	if _, _, err := s.PairDevice(ctx, b, "phone"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("agent paired a device: %v", err)
	}
	d, tok, err := s.PairDevice(ctx, HumanCaller, "phone")
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Authenticate(ctx, tok)
	if err != nil || !c.IsHuman() || c.Device != d.ID {
		t.Fatalf("device auth: %+v %v", c, err)
	}

	// A device acts as the human for the queue...
	ap, _ := s.RequestApproval(ctx, b, ApprovalRequest{Summary: "push"})
	if _, err := s.Decide(ctx, c, DecideRequest{ID: ap.ID, Status: ApprovalApproved}); err != nil {
		t.Fatalf("device decide: %v", err)
	}
	// ...but cannot mint tokens, loosen policies or pair more devices.
	if _, _, err := s.AddAgent(ctx, c, AgentSpec{Name: "rogue"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("device added agent: %v", err)
	}
	if _, err := s.RotateToken(ctx, c, "builder"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("device rotated token: %v", err)
	}
	if err := s.SetPolicy(ctx, c, "builder", Policy{Default: Allow}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("device set policy: %v", err)
	}
	if _, _, err := s.PairDevice(ctx, c, "tablet"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("device paired a device: %v", err)
	}

	if err := s.RevokeDevice(ctx, HumanCaller, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, tok); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked token still works: %v", err)
	}
	ds, _ := s.ListDevices(ctx, HumanCaller)
	if len(ds) != 1 || ds[0].RevokedAt == nil || ds[0].LastSeen == nil {
		t.Fatalf("devices: %+v", ds)
	}
}
