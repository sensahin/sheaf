//go:build !windows

package desktop

import (
	"os/exec"
	"syscall"
)

// detach starts the daemon in its own session, so closing the app or its
// terminal does not stop it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func hide(*exec.Cmd) {}
