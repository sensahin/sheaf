package run

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/sensahin/sheaf/internal/core"
	"github.com/sensahin/sheaf/internal/osc"
)

func TestRunReportsStatusAndPassesOutputThrough(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	// "Fixing tests" and "Allow git push?" in base64.
	script := `printf 'starting\n'
printf '\033]7501;state=working:app=fake:progress=40:msg=Rml4aW5nIHRlc3Rz\033\\'
printf '\033]7501;state=working:app=fake:progress=40:msg=Rml4aW5nIHRlc3Rz\033\\'
printf 'working...\n'
printf '\033]7501;state=blocked:kind=permission:msg=QWxsb3cgZ2l0IHB1c2g/\a'
printf '\033]7501;state=done\033\\'
exit 3`

	var mu sync.Mutex
	var got []core.StatusReport
	var out bytes.Buffer
	code, err := Run(context.Background(), Options{
		Command: []string{"sh", "-c", script},
		Task:    "t-1",
		Stdout:  &out,
		Report: func(_ context.Context, r core.StatusReport) error {
			mu.Lock()
			got = append(got, r)
			mu.Unlock()
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if code != 3 {
		t.Errorf("exit code %d", code)
	}
	if !strings.Contains(out.String(), "starting") || !strings.Contains(out.String(), "\x1b]7501;state=done") {
		t.Errorf("output not passed through: %q", out.String())
	}

	// Repeats of one state may collapse, but every change of state arrives
	// in order: working, blocked, done, then the exit.
	if len(got) < 2 {
		t.Fatalf("reports: %+v", got)
	}
	var states []string
	for _, r := range got {
		states = append(states, r.State)
	}
	if strings.Join(states, ",") != "working,blocked,done,idle" {
		t.Errorf("states %v", states)
	}
	if got[1].Note != "needs permission: Allow git push?" || got[1].Task != "t-1" {
		t.Errorf("blocked report: %+v", got[1])
	}
	last, exit := got[len(got)-2], got[len(got)-1]
	if last.State != core.StateDone || last.Task != "t-1" {
		t.Errorf("final status: %+v", last)
	}
	if exit.State != core.StateIdle || exit.Note != "exited with code 3" || exit.Task != "" {
		t.Errorf("exit status: %+v", exit)
	}
	for i := 1; i < len(got); i++ {
		if got[i] == got[i-1] {
			t.Errorf("repeated report %+v", got[i])
		}
	}
}

func TestFromOSCNotes(t *testing.T) {
	cases := map[string]struct{ state, kind, msg, progress, wantState, wantNote string }{
		"progress":   {"working", "", "Fixing tests", "40", core.StateWorking, "Fixing tests (40%)"},
		"permission": {"blocked", "permission", "Allow git push?", "", core.StateBlocked, "needs permission: Allow git push?"},
		"error":      {"error", "", "rsync failed", "", core.StateError, "rsync failed"},
		"clear":      {"clear", "", "", "", core.StateIdle, ""},
	}
	for name, c := range cases {
		r := fromOSC(oscStatus(c.state, c.kind, c.msg, c.progress))
		if r.State != c.wantState || r.Note != c.wantNote {
			t.Errorf("%s: %+v", name, r)
		}
	}
}

func oscStatus(state, kind, msg, progress string) osc.Status {
	return osc.Status{State: state, Kind: kind, Msg: msg, Progress: progress}
}

func TestReporterKeepsStateChangesWhenDaemonIsSlow(t *testing.T) {
	release := make(chan struct{})
	var got []string
	r := newReporter(context.Background(), "t-1", func(_ context.Context, s core.StatusReport) error {
		<-release // the daemon is stuck until everything below has been set
		got = append(got, s.State+":"+s.Note)
		return nil
	})
	r.set(core.StatusReport{State: core.StateWorking, Note: "10%"})
	r.set(core.StatusReport{State: core.StateWorking, Note: "20%"})
	r.set(core.StatusReport{State: core.StateWorking, Note: "90%"})
	r.set(core.StatusReport{State: core.StateDone})
	r.set(core.StatusReport{State: core.StateIdle, Note: "exited"})
	close(release)
	r.close()
	// The first report may have been picked up before the rest arrived.
	want1 := "working:90%,done:,idle:exited"
	want2 := "working:10%," + want1
	if j := strings.Join(got, ","); j != want1 && j != want2 {
		t.Fatalf("got %s", j)
	}
}
