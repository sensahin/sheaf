package desktop

import (
	"os/exec"
	"syscall"
)

const (
	createNewProcessGroup = 0x00000200
	detachedProcess       = 0x00000008
	createNoWindow        = 0x08000000
)

// detach starts the daemon without a console and outside the app's process
// group, so closing the app does not stop it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess, HideWindow: true}
}

// hide runs a helper such as PowerShell without flashing a console window.
func hide(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow, HideWindow: true}
}
