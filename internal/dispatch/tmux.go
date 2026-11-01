package dispatch

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/sensahin/sheaf/internal/launch"
)

// Window is one tmux window running an agent.
type Window struct {
	// Target addresses the window for tmux, such as "@3".
	Target string
	// Name is the window's name: AGENT or AGENT:TASK.
	Name string
}

// Agent is the agent the window was opened for.
func (w Window) Agent() string {
	name, _, _ := strings.Cut(w.Name, ":")
	return name
}

// Terminal is what the dispatcher needs from tmux. Tests replace it.
type Terminal interface {
	Windows() ([]Window, error)
	Open(s launch.Spec) error
	Capture(target string, lines int) (string, error)
	Type(target, text string, enter bool) error
	Key(target, key string) error
}

// Tmux drives the tmux server of the user the daemon runs as.
type Tmux struct{}

// Available reports whether tmux is installed.
func (Tmux) Available() bool { return exec.Command("tmux", "-V").Run() == nil }

func tmux(args ...string) (string, error) {
	cmd := exec.Command("tmux", args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("tmux %s: %v: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// Windows lists every window on the tmux server. No server means no windows.
func (Tmux) Windows() ([]Window, error) {
	out, err := tmux("list-windows", "-a", "-F", "#{window_id} #{window_name}")
	if err != nil {
		if strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "error connecting") || strings.Contains(err.Error(), "No such file") {
			return nil, nil
		}
		return nil, err
	}
	var ws []Window
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		id, name, ok := strings.Cut(line, " ")
		if ok {
			ws = append(ws, Window{Target: id, Name: name})
		}
	}
	return ws, nil
}

// Open starts s in a new window of the "sheaf" session.
func (Tmux) Open(s launch.Spec) error {
	_, err := launch.Tmux(s, false)
	return err
}

// Capture returns the last lines of the window's visible pane.
func (Tmux) Capture(target string, lines int) (string, error) {
	return tmux("capture-pane", "-p", "-J", "-t", target, "-S", fmt.Sprint(-lines))
}

// Type types text into the window, literally, then presses Enter if asked.
func (Tmux) Type(target, text string, enter bool) error {
	if text != "" {
		if _, err := tmux("send-keys", "-t", target, "-l", "--", text); err != nil {
			return err
		}
	}
	if enter {
		if text != "" {
			// Agent TUIs treat text and Enter that arrive together as a
			// paste with a newline in it, not as submitting the prompt.
			time.Sleep(300 * time.Millisecond)
		}
		_, err := tmux("send-keys", "-t", target, "Enter")
		return err
	}
	return nil
}

// Key presses one named key, such as Up or Escape.
func (Tmux) Key(target, key string) error {
	_, err := tmux("send-keys", "-t", target, key)
	return err
}
