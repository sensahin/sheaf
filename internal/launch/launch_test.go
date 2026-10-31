package launch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQuote(t *testing.T) {
	cases := map[string]string{
		"claude":          "claude",
		"/usr/bin/sheaf":  "/usr/bin/sheaf",
		"":                "''",
		"claude --resume": "'claude --resume'",
		"it's":            `'it'\''s'`,
		"$(rm -rf ~)":     "'$(rm -rf ~)'",
	}
	for in, want := range cases {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestShellRoundTrips(t *testing.T) {
	s := Spec{Agent: "builder", Task: "t-1", Command: `echo "it's $HOME"`, Sheaf: "/opt/my tools/sheaf"}
	got := s.Shell()
	want := `exec '/opt/my tools/sheaf' run --agent builder --task t-1 -- sh -c 'echo "it'\''s $HOME"'`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	// The shell must hand sheaf exactly these arguments.
	out, err := exec.Command("sh", "-c", strings.Replace(got, "exec '/opt/my tools/sheaf'", `printf '%s\n'`, 1)).Output()
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	wantArgs := []string{"run", "--agent", "builder", "--task", "t-1", "--", "sh", "-c", `echo "it's $HOME"`}
	if strings.Join(args, "|") != strings.Join(wantArgs, "|") {
		t.Fatalf("args %q", args)
	}
}

func TestTmuxArgs(t *testing.T) {
	s := Spec{Agent: "builder", Task: "t-1", Dir: "/w", Command: "claude", Sheaf: "sheaf"}
	if a := TmuxArgs(s, true, false); a[0] != "new-window" || a[2] != "builder:t-1" || a[4] != "/w" {
		t.Errorf("inside tmux: %q", a)
	}
	if a := TmuxArgs(s, false, true); strings.Join(a[:3], " ") != "new-window -t sheaf:" {
		t.Errorf("existing session: %q", a)
	}
	if a := TmuxArgs(s, false, false); strings.Join(a[:4], " ") != "new-session -d -s sheaf" {
		t.Errorf("new session: %q", a)
	}
}

func TestTmuxOpensWindows(t *testing.T) {
	if exec.Command("tmux", "-V").Run() != nil {
		t.Skip("tmux not installed")
	}
	// A private tmux server, so the test never touches the user's.
	dir, err := os.MkdirTemp("", "tm")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("TMUX_TMPDIR", dir)
	defer exec.Command("tmux", "kill-server").Run()

	// A stand-in for sheaf that drops its own arguments and runs the agent.
	fake := filepath.Join(dir, "sheaf")
	os.WriteFile(fake, []byte("#!/bin/sh\nwhile [ \"$1\" != -- ]; do shift; done\nshift\nexec \"$@\"\n"), 0o755)
	for _, agent := range []string{"planner", "builder"} {
		s := Spec{Agent: agent, Dir: dir, Command: "touch ran-" + agent + "; sleep 30", Sheaf: fake}
		if _, err := Tmux(s, false); err != nil {
			t.Fatalf("tmux for %s: %v", agent, err)
		}
	}
	for i := 0; i < 50; i++ {
		_, e1 := os.Stat(filepath.Join(dir, "ran-planner"))
		_, e2 := os.Stat(filepath.Join(dir, "ran-builder"))
		if e1 == nil && e2 == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, agent := range []string{"planner", "builder"} {
		if _, err := os.Stat(filepath.Join(dir, "ran-"+agent)); err != nil {
			t.Errorf("%s did not run in its directory: %v", agent, err)
		}
	}
	out, err := exec.Command("tmux", "list-windows", "-t", "="+Session, "-F", "#{window_name}").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(string(out)); strings.Join(got, ",") != "planner,builder" {
		t.Fatalf("windows: %q", got)
	}
}
