// Package launch starts agents for you, wrapped in `sheaf run` so their
// status is tracked from the first second, either in a tmux window or in the
// current terminal.
package launch

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// DefaultCommands is what `sheaf start` runs for an agent kind when no
// command is given.
var DefaultCommands = map[string]string{
	"claude-code": "claude",
	"codex":       "codex",
	"opencode":    "opencode",
	"copilot":     "copilot",
	"cursor":      "cursor-agent",
	"gemini":      "gemini",
	"qwen":        "qwen",
	"droid":       "droid",
	"antigravity": "agy",
	"amp":         "amp",
	"goose":       "goose session",
	"pi":          "pi",
}

// Spec is one agent to launch.
type Spec struct {
	Agent string
	Task  string
	Dir   string
	// Command is a shell command line, such as "claude --continue".
	Command string
	// Sheaf is the path to the sheaf binary.
	Sheaf string
}

// Shell is the command line that runs the agent under `sheaf run`. The agent's
// token is read from its file by `sheaf run --agent`, so no secret appears in
// tmux's arguments or the process list.
func (s Spec) Shell() string {
	parts := []string{"exec", Quote(s.Sheaf), "run", "--agent", Quote(s.Agent)}
	if s.Task != "" {
		parts = append(parts, "--task", Quote(s.Task))
	}
	parts = append(parts, "--", "sh", "-c", Quote(s.Command))
	return strings.Join(parts, " ")
}

// Quote quotes s for a POSIX shell.
func Quote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./=:@") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Session is the tmux session agents open in when you are not inside tmux.
const Session = "sheaf"

// TmuxArgs returns the tmux command that opens s in a new window: in the
// current session when inTmux, otherwise in the "sheaf" session, creating it
// if needed.
func TmuxArgs(s Spec, inTmux, sessionExists bool) []string {
	window := s.Agent
	if s.Task != "" {
		window += ":" + s.Task
	}
	common := []string{"-n", window, "-c", s.Dir, s.Shell()}
	switch {
	case inTmux:
		return append([]string{"new-window"}, common...)
	case sessionExists:
		return append([]string{"new-window", "-t", Session + ":"}, common...)
	}
	return append([]string{"new-session", "-d", "-s", Session}, common...)
}

// Tmux opens s in a tmux window and says where.
func Tmux(s Spec, inTmux bool) (string, error) {
	exists := exec.Command("tmux", "has-session", "-t", "="+Session).Run() == nil
	cmd := exec.Command("tmux", TmuxArgs(s, inTmux, exists)...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("tmux: %v: %s", err, strings.TrimSpace(errb.String()))
	}
	if inTmux {
		return "a new tmux window", nil
	}
	return fmt.Sprintf("tmux session %q (attach with: tmux attach -t %s)", Session, Session), nil
}
