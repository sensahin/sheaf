// Package dispatch lets the daemon run agents on its own, so Sheaf works on a
// server you reach only from a phone or a browser.
//
// It does three things. StartTask opens a task's owner in a tmux window, in
// the task's worktree, with the task as its first prompt. Run follows the
// event log: when an agent that is not running is handed a task, it starts
// it; when an agent that is running and idle gets new messages, it tells the
// agent to read its inbox (the message itself stays on the bus; only the
// doorbell is typed). Screen and Type show an agent's terminal and type into
// it, for the prompts an agent sometimes stops on.
package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sensahin/sheaf/internal/config"
	"github.com/sensahin/sheaf/internal/core"
	"github.com/sensahin/sheaf/internal/launch"
	"github.com/sensahin/sheaf/internal/setup"
)

// ErrNotRunning means the agent has no tmux window.
var ErrNotRunning = errors.New("agent is not running")

// Dispatcher starts and nudges agents for the daemon.
type Dispatcher struct {
	Svc   *core.Service
	Paths config.Paths
	// Sheaf is the path to the sheaf binary agents run under.
	Sheaf string
	Term  Terminal
	// Auto starts an agent that is handed a task while it is not running.
	Auto bool
	// Logf reports what the dispatcher did; nil is quiet.
	Logf func(format string, a ...any)

	mu       sync.Mutex
	notified map[string]string    // agent -> newest message it was told about
	started  map[string]time.Time // agent -> when the dispatcher last started it
}

// settle is how long a freshly started agent is left alone: it is still
// loading and reading its first prompt, which is what a doorbell would
// interrupt. Its first status report after that brings the doorbell.
var settle = 20 * time.Second

// StartResult and Screen live in core so the API can name them without
// importing this package.
type (
	StartResult = core.StartResult
	Screen      = core.Screen
)

func (d *Dispatcher) logf(format string, a ...any) {
	if d.Logf != nil {
		d.Logf(format, a...)
	}
}

// window finds the agent's tmux window, preferring the one for task.
func (d *Dispatcher) window(agent, task string) (Window, bool, error) {
	ws, err := d.Term.Windows()
	if err != nil {
		return Window{}, false, err
	}
	var found *Window
	for i, w := range ws {
		if w.Agent() != agent {
			continue
		}
		if task != "" && w.Name == agent+":"+task {
			return w, true, nil
		}
		if found == nil {
			found = &ws[i]
		}
	}
	if found == nil {
		return Window{}, false, nil
	}
	return *found, true, nil
}

// Running lists the agents that have a tmux window, with the window names.
func (d *Dispatcher) Running() (map[string][]string, error) {
	ws, err := d.Term.Windows()
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, w := range ws {
		out[w.Agent()] = append(out[w.Agent()], w.Name)
	}
	return out, nil
}

// maxDetail bounds how much of a task's description goes into the prompt;
// the rest is a list_tasks call away.
const maxDetail = 4000

// Prompt is the first message an agent gets for a task. lead, if set, comes
// first (for example, why it was started).
func Prompt(agent string, t core.Task, lead string) string {
	var b strings.Builder
	if lead != "" {
		b.WriteString(lead + "\n\n")
	}
	fmt.Fprintf(&b, "Sheaf task %s: %s\n", t.ID, t.Title)
	if d := strings.TrimSpace(t.Detail); d != "" {
		if len(d) > maxDetail {
			d = d[:maxDetail] + "\n[... cut; the full text is in list_tasks]"
		}
		b.WriteString("\n" + d + "\n")
	}
	b.WriteString("\n")
	if t.Branch != "" {
		fmt.Fprintf(&b, "You are %s, in this task's own git worktree on branch %s. ", agent, t.Branch)
	} else {
		fmt.Fprintf(&b, "You are %s. ", agent)
	}
	fmt.Fprintf(&b, "Use Sheaf's MCP tools: read your inbox first, report_status with task %q as you work, and send to \"human\" if you need a decision. ", t.ID)
	b.WriteString("When the change is ready, commit it on this branch, then request_approval with kind \"diff\" so it can be reviewed, or hand the task off to another agent for review. If the task asks you to push or open a pull request, do that after approval.")
	return b.String()
}

// promptArg is how each agent program takes a first prompt on its command
// line and still stays interactive. Kinds not listed get the task in their
// inbox instead.
func promptArg(kind, prompt string) (string, bool) {
	switch kind {
	case "claude-code", "codex":
		return " " + launch.Quote(prompt), true
	case "gemini", "qwen", "copilot":
		return " -i " + launch.Quote(prompt), true
	}
	return "", false
}

// StartTask starts the task's owner on it, in a tmux window of its own. If
// the owner already has a window for this task, the task is typed into it
// instead.
func (d *Dispatcher) StartTask(ctx context.Context, id, lead string) (StartResult, error) {
	t, err := d.Svc.GetTask(ctx, id)
	if err != nil {
		return StartResult{}, err
	}
	if t.Owner == "" {
		return StartResult{}, fmt.Errorf("%w: task %s has no owner; assign an agent first", core.ErrInvalid, id)
	}
	a, err := d.Svc.GetAgent(ctx, t.Owner)
	if err != nil {
		return StartResult{}, err
	}
	res := StartResult{Agent: a.Name, Task: t.ID}
	prompt := Prompt(a.Name, t, lead)

	// Already running on this task: give it the task in that window. Running
	// on another task means another worktree, so it gets a second window.
	if w, ok, err := d.window(a.Name, t.ID); err != nil {
		return res, err
	} else if ok && w.Name == a.Name+":"+t.ID {
		res.Window = w.Name
		if err := d.Term.Type(w.Target, oneLine(prompt), true); err != nil {
			return res, err
		}
		d.logf("gave %s to %s in its window %s", t.ID, a.Name, w.Name)
		return res, d.Svc.Record(ctx, core.Human, "agent.nudged", a.Name, map[string]string{"task": t.ID, "window": w.Name})
	}

	if _, err := d.Paths.LoadAgentToken(a.Name); err != nil {
		return res, fmt.Errorf("%w: %v", core.ErrInvalid, err)
	}
	command := a.StartCmd
	if command == "" {
		command = launch.DefaultCommands[a.Kind]
	}
	if command == "" {
		return res, fmt.Errorf("%w: no start command for agent %q (kind %q); set one with `sheaf agent set %s --command CMD`", core.ErrInvalid, a.Name, a.Kind, a.Name)
	}
	arg, inline := promptArg(a.Kind, prompt)
	command += arg

	dir := t.Worktree
	if dir == "" {
		dir = t.Repo
	}
	if dir == "" {
		if dir, err = os.UserHomeDir(); err != nil {
			return res, err
		}
	}
	if t.Worktree != "" {
		if _, err := setup.PrepareWorktree(t.Worktree, a.Kind, d.Sheaf); err != nil {
			return res, fmt.Errorf("setting up %s in %s: %w", a.Kind, t.Worktree, err)
		}
	}
	if !inline {
		// The agent will find the task in its inbox when the doorbell rings.
		if _, err := d.Svc.Send(ctx, core.HumanCaller, core.SendRequest{To: a.Name, Task: t.ID, Body: prompt}); err != nil {
			return res, err
		}
	}
	spec := launch.Spec{Agent: a.Name, Task: t.ID, Dir: dir, Command: command, Sheaf: d.Sheaf}
	if err := d.Term.Open(spec); err != nil {
		return res, err
	}
	res.Started, res.Dir, res.Window = true, dir, a.Name+":"+t.ID
	d.mu.Lock()
	if d.started == nil {
		d.started = map[string]time.Time{}
	}
	d.started[a.Name] = time.Now()
	d.mu.Unlock()
	d.logf("started %s on %s in %s", a.Name, t.ID, dir)
	return res, d.Svc.Record(ctx, core.Human, "agent.started", a.Name, map[string]string{"task": t.ID, "dir": dir})
}

// oneLine flattens a prompt for typing into a TUI, where a newline would
// submit early.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// Screen returns the last lines of the agent's terminal.
func (d *Dispatcher) Screen(ctx context.Context, agent, task string, lines int) (Screen, error) {
	if lines <= 0 || lines > 500 {
		lines = 60
	}
	w, ok, err := d.window(agent, task)
	if err != nil {
		return Screen{}, err
	}
	if !ok {
		return Screen{}, fmt.Errorf("%w: %w: %s", core.ErrNotFound, ErrNotRunning, agent)
	}
	text, err := d.Term.Capture(w.Target, lines)
	return Screen{Agent: agent, Window: w.Name, Text: strings.TrimRight(text, "\n ")}, err
}

// Type types text into the agent's terminal, for answering a prompt the
// agent stopped on. Prefer messages for instructions: they are logged and
// survive restarts.
func (d *Dispatcher) Type(ctx context.Context, agent, task, text string, enter bool) error {
	w, ok, err := d.window(agent, task)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %w: %s", core.ErrNotFound, ErrNotRunning, agent)
	}
	if err := d.Term.Type(w.Target, text, enter); err != nil {
		return err
	}
	return d.Svc.Record(ctx, core.Human, "agent.typed", agent, map[string]any{"window": w.Name, "chars": len(text), "enter": enter})
}

// Keys are the keys the phone can press in an agent's terminal: enough to
// move through a menu, cancel, or interrupt. tmux names them.
var Keys = map[string]bool{"Up": true, "Down": true, "Left": true, "Right": true, "Enter": true, "Escape": true, "Tab": true, "BSpace": true, "C-c": true}

// Key presses one of Keys in the agent's terminal.
func (d *Dispatcher) Key(ctx context.Context, agent, task, key string) error {
	if !Keys[key] {
		return fmt.Errorf("%w: unknown key %q", core.ErrInvalid, key)
	}
	w, ok, err := d.window(agent, task)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %w: %s", core.ErrNotFound, ErrNotRunning, agent)
	}
	if err := d.Term.Key(w.Target, key); err != nil {
		return err
	}
	return d.Svc.Record(ctx, core.Human, "agent.key", agent, map[string]string{"window": w.Name, "key": key})
}

// Run follows the event log until ctx ends, starting agents that were handed
// work and ringing idle agents that have unread messages.
func (d *Dispatcher) Run(ctx context.Context) error {
	after, err := d.Svc.LastEventID(ctx)
	if err != nil {
		return err
	}
	for {
		evs, err := d.Svc.Events(ctx, after, 500, 30*time.Second)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			d.logf("dispatch: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}
		touched := map[string]bool{}
		for _, e := range evs {
			after = e.ID
			if name := wakes(e); name != "" && name != core.Human {
				touched[name] = true
			}
		}
		for name := range touched {
			if err := d.Check(ctx, name); err != nil {
				d.logf("dispatch %s: %v", name, err)
			}
		}
	}
}

// wakes returns the agent an event may give work to.
func wakes(e core.Event) string {
	switch e.Type {
	case "message.sent", "task.handoff":
		var v struct {
			To string `json:"to"`
		}
		json.Unmarshal(e.Data, &v)
		return v.To
	case "agent.status":
		return e.Actor
	}
	return ""
}

// Check looks at one agent's unread messages and acts: rings the doorbell if
// it is running and idle, or starts it on its task if Auto is set and it is
// not running. Each message is acted on once.
func (d *Dispatcher) Check(ctx context.Context, name string) error {
	a, err := d.Svc.GetAgent(ctx, name)
	if err != nil {
		return nil // removed meanwhile
	}
	msgs, err := d.Svc.Inbox(ctx, core.Caller{Name: name}, core.InboxRequest{Peek: true})
	if err != nil || len(msgs) == 0 {
		return err
	}
	newest := msgs[len(msgs)-1].ID
	d.mu.Lock()
	if d.notified == nil {
		d.notified = map[string]string{}
	}
	seen := d.notified[name] == newest
	d.mu.Unlock()
	if seen {
		return nil
	}
	mark := func() {
		d.mu.Lock()
		d.notified[name] = newest
		d.mu.Unlock()
	}

	w, running, err := d.window(name, "")
	if err != nil {
		return err
	}
	if running {
		d.mu.Lock()
		booting := time.Since(d.started[name]) < settle
		d.mu.Unlock()
		if a.State != core.StateIdle || booting {
			return nil // asked again when it next reports idle
		}
		mark()
		text := fmt.Sprintf("You have %d new message(s) in your Sheaf inbox. Read them with the inbox tool and act on them.", len(msgs))
		if err := d.Term.Type(w.Target, text, true); err != nil {
			return err
		}
		d.logf("told %s about %d new message(s)", name, len(msgs))
		return d.Svc.Record(ctx, core.Human, "agent.nudged", name, map[string]any{"window": w.Name, "messages": len(msgs)})
	}
	if !d.Auto {
		return nil
	}
	mark()
	// Start it on the newest active task it owns that a message is about.
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Task == "" {
			continue
		}
		t, err := d.Svc.GetTask(ctx, m.Task)
		if err != nil || t.Owner != name || !active(t.Status) {
			continue
		}
		lead := fmt.Sprintf("%s sent you a %s about this task. Read your Sheaf inbox first.", m.From, m.Kind)
		_, err = d.StartTask(ctx, t.ID, lead)
		return err
	}
	return nil
}

func active(status string) bool {
	return status == core.TaskOpen || status == core.TaskWorking || status == core.TaskBlocked
}
