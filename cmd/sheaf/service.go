package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sensahin/sheaf/internal/config"
	"github.com/sensahin/sheaf/internal/launch"
)

// cmdService prints a systemd user unit that runs the daemon, for a server
// (macOS users have `brew services` or the desktop app).
func cmdService(paths config.Paths, args []string) error {
	fs := flag.NewFlagSet("service", flag.ContinueOnError)
	web := fs.String("web", defaultWeb, "address for the web queue")
	public := fs.String("public-url", "", "the address your phone opens, if it differs from --web")
	headless := fs.Bool("headless", true, "start and wake agents without anyone at the keyboard")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	fmt.Print(systemdUnit(self(), paths.Home, os.Getenv("PATH"), *web, *public, *headless))
	return nil
}

func systemdUnit(sheaf, home, path, web, public string, headless bool) string {
	exec := []string{launch.Quote(sheaf), "daemon", "--web", launch.Quote(web)}
	if public != "" {
		exec = append(exec, "--public-url", launch.Quote(public))
	}
	if headless {
		exec = append(exec, "--headless")
	}
	var b strings.Builder
	b.WriteString(`# Sheaf daemon as a systemd user service. Install with:
#   mkdir -p ~/.config/systemd/user
#   sheaf service > ~/.config/systemd/user/sheaf.service
#   systemctl --user daemon-reload
#   systemctl --user enable --now sheaf
#   sudo loginctl enable-linger $USER    # keep it running when you log out
[Unit]
Description=Sheaf coordination daemon
After=network-online.target
Wants=network-online.target

[Service]
`)
	fmt.Fprintf(&b, "ExecStart=%s\n", strings.Join(exec, " "))
	// Agents run in tmux, started by the daemon. KillMode=process stops only
	// the daemon on restart, so the agents keep working through an upgrade.
	b.WriteString("KillMode=process\nRestart=on-failure\nRestartSec=3\n")
	fmt.Fprintf(&b, "Environment=SHEAF_HOME=%s\n", home)
	// A user service starts with a minimal PATH; agents need the one you
	// installed claude, codex, git and tmux with.
	if path != "" {
		fmt.Fprintf(&b, "Environment=PATH=%s\n", path)
	}
	b.WriteString("Environment=TERM=xterm-256color\n\n[Install]\nWantedBy=default.target\n")
	return b.String()
}
