// Package herdr mirrors Sheaf agent status into herdr, the agent terminal
// multiplexer, so a pane running `sheaf run` shows working, blocked and idle
// in herdr's sidebar and can be resumed by herdr after a restart.
//
// herdr injects HERDR_ENV, HERDR_PANE_ID and HERDR_BIN_PATH into every pane;
// reports go through `$HERDR_BIN_PATH pane report-agent`.
package herdr

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sensahin/sheaf/internal/core"
)

// Source is how Sheaf identifies itself to herdr.
const Source = "sheaf"

// Pane is the herdr pane this process runs in.
type Pane struct {
	ID  string
	Bin string
	seq atomic.Int64
}

// Detect returns the current herdr pane, or false outside herdr.
func Detect() (*Pane, bool) {
	if os.Getenv("HERDR_ENV") != "1" || os.Getenv("HERDR_PANE_ID") == "" {
		return nil, false
	}
	bin := os.Getenv("HERDR_BIN_PATH")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("herdr"); err != nil {
			return nil, false
		}
	}
	p := &Pane{ID: os.Getenv("HERDR_PANE_ID"), Bin: bin}
	// Sequence numbers must only grow, also across Sheaf restarts in the
	// same pane: start from the clock.
	p.seq.Store(time.Now().UnixMilli())
	return p, true
}

// State maps a Sheaf agent state to the states herdr accepts from
// reporters: idle, working, blocked.
func State(s string) string {
	switch s {
	case core.StateWorking:
		return "working"
	case core.StateBlocked:
		return "blocked"
	}
	return "idle" // idle, done, error
}

// Args builds the report-agent arguments for one status. resume, when set,
// is the command herdr runs to bring the agent back in this pane.
func (p *Pane) Args(agent string, r core.StatusReport, resume []string) []string {
	args := []string{"pane", "report-agent", p.ID,
		"--source", Source, "--agent", agent,
		"--state", State(r.State), "--seq", fmt.Sprint(p.seq.Add(1))}
	msg := r.Note
	if r.State == core.StateError && msg == "" {
		msg = "error"
	}
	if msg != "" {
		args = append(args, "--message", oneLine(msg))
	}
	if len(resume) > 0 {
		args = append(append(args, "--"), resume...)
	}
	return args
}

// Report sends one status to herdr.
func (p *Pane) Report(ctx context.Context, agent string, r core.StatusReport, resume []string) error {
	return p.run(ctx, p.Args(agent, r, resume)...)
}

// Release tells herdr the agent has left the pane.
func (p *Pane) Release(ctx context.Context, agent string) error {
	return p.run(ctx, "pane", "release-agent", p.ID, "--source", Source, "--agent", agent, "--seq", fmt.Sprint(p.seq.Add(1)))
}

func (p *Pane) run(ctx context.Context, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, p.Bin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("herdr %s: %v: %s", args[1], err, strings.TrimSpace(string(out)))
	}
	return nil
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
