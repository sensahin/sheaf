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

	// Reports are latest-wins, so intermediate ones may be skipped, but the
	// last two are always the agent's final status and its exit.
	if len(got) < 2 {
		t.Fatalf("reports: %+v", got)
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
