//go:build !windows

package run

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/creack/pty"
)

// watchResize keeps the agent's terminal the size of ours. It returns a
// function that stops watching.
func watchResize(tty, ptmx *os.File) func() {
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		for range winch {
			pty.InheritSize(tty, ptmx)
		}
	}()
	return func() { signal.Stop(winch); close(winch) }
}
