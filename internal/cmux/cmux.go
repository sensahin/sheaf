// Package cmux mirrors Sheaf agent status into cmux, the macOS terminal for
// coding agents: each agent gets a status entry in its workspace's sidebar,
// and a notification when it needs you.
//
// cmux sets CMUX_WORKSPACE_ID and CMUX_SURFACE_ID in its terminals; reports
// go through the cmux CLI.
package cmux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/sensahin/sheaf/internal/core"
)

// Workspace is the cmux workspace this process runs in.
type Workspace struct {
	ID  string
	Bin string
}

// Detect returns the current cmux workspace, or false outside cmux.
func Detect() (*Workspace, bool) {
	id := os.Getenv("CMUX_WORKSPACE_ID")
	if id == "" || os.Getenv("CMUX_SURFACE_ID") == "" {
		return nil, false
	}
	bin, err := exec.LookPath("cmux")
	if err != nil {
		return nil, false
	}
	return &Workspace{ID: id, Bin: bin}, true
}

// Key is the status entry for one agent, so several agents can share a
// workspace.
func Key(agent string) string { return "sheaf-" + agent }

var colors = map[string]string{
	core.StateWorking: "#D9962B", // Sheaf's band color
	core.StateBlocked: "#E5484D",
	core.StateError:   "#E5484D",
	core.StateDone:    "#46A758",
}

// StatusArgs builds the set-status call for one report.
func (w *Workspace) StatusArgs(agent string, r core.StatusReport) []string {
	text := agent + ": " + r.State
	if r.Note != "" {
		text += " · " + oneLine(r.Note)
	}
	args := []string{"set-status", Key(agent), text, "--workspace", w.ID}
	if c, ok := colors[r.State]; ok {
		args = append(args, "--color", c)
	}
	return args
}

// Report shows one status, and notifies when the agent is blocked. resume is
// unused: cmux does not restore processes.
func (w *Workspace) Report(ctx context.Context, agent string, r core.StatusReport, resume []string) error {
	if err := w.run(ctx, w.StatusArgs(agent, r)...); err != nil {
		return err
	}
	if r.State == core.StateBlocked {
		body := r.Note
		if body == "" {
			body = "It is waiting on you in Sheaf."
		}
		return w.run(ctx, "notify", "--title", "Sheaf: "+agent+" needs you", "--body", oneLine(body))
	}
	return nil
}

// Release removes the agent's status entry.
func (w *Workspace) Release(ctx context.Context, agent string) error {
	return w.run(ctx, "clear-status", Key(agent), "--workspace", w.ID)
}

func (w *Workspace) run(ctx context.Context, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, w.Bin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("cmux %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 120 {
		s = s[:119] + "…"
	}
	return s
}
