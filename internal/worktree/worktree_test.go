package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@example.com", "-c", "user.name=t"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return string(out)
}

func head(t *testing.T, dir, ref string) string {
	t.Helper()
	s, err := git(dir, "rev-parse", ref)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A checkout on a server that nobody pulls still starts tasks from the
// newest pushed code, but never drops local commits.
func TestStartFollowsUpstream(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	origin := t.TempDir()
	run(t, origin, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(origin, "a"), []byte("1"), 0o644)
	run(t, origin, "add", ".")
	run(t, origin, "commit", "-q", "-m", "one")

	clone := filepath.Join(t.TempDir(), "clone")
	run(t, origin, "clone", "-q", origin, clone)
	if got, _ := Start(clone); got != head(t, clone, "HEAD") {
		t.Fatalf("up to date: %s", got)
	}

	// Someone pushes: the next task starts from it.
	os.WriteFile(filepath.Join(origin, "a"), []byte("2"), 0o644)
	run(t, origin, "commit", "-q", "-am", "two")
	if got, _ := Start(clone); got != head(t, origin, "HEAD") {
		t.Fatalf("behind: got %s want %s", got, head(t, origin, "HEAD"))
	}

	// Local work that is not pushed is kept.
	run(t, clone, "merge", "-q", "--ff-only", "@{upstream}")
	os.WriteFile(filepath.Join(clone, "b"), []byte("local"), 0o644)
	run(t, clone, "add", ".")
	run(t, clone, "commit", "-q", "-m", "local")
	if got, _ := Start(clone); got != head(t, clone, "HEAD") {
		t.Fatalf("ahead: %s", got)
	}

	// No upstream at all: HEAD.
	if got, _ := Start(origin); got != head(t, origin, "HEAD") {
		t.Fatalf("no upstream: %s", got)
	}
}
