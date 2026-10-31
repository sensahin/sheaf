// Package run starts an agent inside a pseudo-terminal that Sheaf watches.
//
// Everything the agent prints passes through to your terminal unchanged. On
// the way, Sheaf reads the OSC 7501 status sequences that Claude Code, Codex,
// Amp, goose, opencode and others already emit, and reports them to the
// daemon. So the queue knows an agent is working, blocked or done without the
// agent calling a single Sheaf tool.
package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/term"

	"github.com/sensahin/sheaf/internal/core"
	"github.com/sensahin/sheaf/internal/osc"
)

// Options configures Run.
type Options struct {
	Command []string
	Dir     string
	Env     []string
	// Task, if set, moves with the agent's working, blocked and done states.
	Task   string
	Stdin  *os.File
	Stdout io.Writer
	// Report sends a status to the daemon. Errors are ignored: a daemon that
	// is down must never break the agent's terminal.
	Report func(context.Context, core.StatusReport) error
}

// Run runs the command to completion and returns its exit code.
func Run(ctx context.Context, o Options) (int, error) {
	if len(o.Command) == 0 {
		return 0, errors.New("no command to run")
	}
	cmd := exec.Command(o.Command[0], o.Command[1:]...)
	cmd.Dir = o.Dir
	cmd.Env = o.Env

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return 0, err
	}
	defer ptmx.Close()

	if o.Stdin != nil && term.IsTerminal(int(o.Stdin.Fd())) {
		pty.InheritSize(o.Stdin, ptmx)
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		defer signal.Stop(winch)
		go func() {
			for range winch {
				pty.InheritSize(o.Stdin, ptmx)
			}
		}()
		old, err := term.MakeRaw(int(o.Stdin.Fd()))
		if err != nil {
			return 0, err
		}
		defer term.Restore(int(o.Stdin.Fd()), old)
	}
	if o.Stdin != nil {
		go io.Copy(ptmx, o.Stdin)
	}

	rep := newReporter(ctx, o.Task, o.Report)
	var scan osc.Scanner
	buf := make([]byte, 32*1024)
	for {
		n, err := ptmx.Read(buf)
		if n > 0 {
			o.Stdout.Write(buf[:n])
			for _, st := range scan.Write(buf[:n]) {
				rep.set(fromOSC(st))
			}
		}
		if err != nil {
			// EIO is how Linux reports that the child closed the pty.
			break
		}
	}

	code := 0
	if err := cmd.Wait(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			return 0, err
		}
		code = ee.ExitCode()
	}
	note := "exited"
	if code != 0 {
		note = fmt.Sprintf("exited with code %d", code)
	}
	rep.set(core.StatusReport{State: core.StateIdle, Note: note})
	rep.close()
	return code, nil
}

// fromOSC turns an OSC 7501 report into a Sheaf status.
func fromOSC(st osc.Status) core.StatusReport {
	r := core.StatusReport{Note: st.Msg}
	switch st.State {
	case "working":
		r.State = core.StateWorking
		if st.Progress != "" {
			r.Note = joinNote(r.Note, "("+st.Progress+"%)")
		}
	case "blocked":
		r.State = core.StateBlocked
		switch st.Kind {
		case "permission":
			r.Note = joinNote("needs permission:", r.Note)
		case "question":
			r.Note = joinNote("has a question:", r.Note)
		case "auth":
			r.Note = joinNote("needs you to sign in:", r.Note)
		}
	case "done":
		r.State = core.StateDone
	case "error":
		r.State = core.StateError
	default: // idle, clear, or anything newer than this code
		r.State = core.StateIdle
	}
	return r
}

func joinNote(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + " " + b
}

// reporter sends the latest status in the background, skipping repeats, so a
// chatty agent never waits on the daemon.
type reporter struct {
	ctx    context.Context
	task   string
	send   func(context.Context, core.StatusReport) error
	mu     sync.Mutex
	next   *core.StatusReport
	last   core.StatusReport
	wake   chan struct{}
	done   chan struct{}
	closed bool
}

func newReporter(ctx context.Context, task string, send func(context.Context, core.StatusReport) error) *reporter {
	r := &reporter{ctx: ctx, task: task, send: send, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go r.loop()
	return r
}

func (r *reporter) set(s core.StatusReport) {
	r.mu.Lock()
	r.next = &s
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *reporter) close() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
	<-r.done
}

func (r *reporter) loop() {
	defer close(r.done)
	for range r.wake {
		r.mu.Lock()
		next, closed := r.next, r.closed
		r.next = nil
		r.mu.Unlock()
		if next != nil && (next.State != r.last.State || next.Note != r.last.Note) && r.send != nil {
			s := *next
			switch s.State {
			case core.StateWorking, core.StateBlocked, core.StateDone:
				s.Task = r.task
			}
			r.send(r.ctx, s)
			r.last = *next
		}
		if closed {
			return
		}
	}
}
