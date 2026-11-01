// Package worktree gives each task its own git worktree, so agents never edit
// the same checkout.
package worktree

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Add creates a worktree at path on a new branch, starting from the repo's
// current HEAD, or from its upstream when that is newer (see Start). It
// returns the commit the branch started from.
func Add(repo, path, branch string) (base string, err error) {
	base, err = Start(repo)
	if err != nil {
		return "", err
	}
	if _, err := git(repo, "worktree", "add", "-b", branch, path, base); err != nil {
		return "", err
	}
	return base, nil
}

// Start is the commit a new task branch starts from. It is HEAD, unless the
// current branch tracks a remote branch that is ahead of it: then Start
// fetches and uses the remote branch, so a checkout on a server that nobody
// pulls still gives agents the latest code. Local commits that are not
// pushed are never skipped, and a failed fetch (offline, no credentials)
// falls back to HEAD.
func Start(repo string) (string, error) {
	head, err := git(repo, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if _, err := git(repo, "rev-parse", "--abbrev-ref", "@{upstream}"); err != nil {
		return head, nil
	}
	if _, err := gitTimeout(repo, 30*time.Second, "fetch", "--quiet"); err != nil {
		return head, nil
	}
	up, err := git(repo, "rev-parse", "@{upstream}")
	if err != nil || up == head {
		return head, nil
	}
	if _, err := git(repo, "merge-base", "--is-ancestor", head, up); err != nil {
		return head, nil // HEAD has commits the upstream lacks
	}
	return up, nil
}

// Diff returns the changes in the worktree since base, committed or not.
// Untracked files are listed by name at the end.
func Diff(path, base string) (string, error) {
	d, err := git(path, "diff", base)
	if err != nil {
		return "", err
	}
	untracked, err := git(path, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return "", err
	}
	if untracked != "" {
		d += "\n# untracked files:\n# " + strings.ReplaceAll(untracked, "\n", "\n# ")
	}
	return d, nil
}

// Root returns the top level of the git repository containing dir.
func Root(dir string) (string, error) {
	return git(dir, "rev-parse", "--show-toplevel")
}

func git(dir string, args ...string) (string, error) {
	return gitTimeout(dir, 0, args...)
}

func gitTimeout(dir string, timeout time.Duration, args ...string) (string, error) {
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	// Never wait on a credential prompt nobody can see.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND="+sshCommand())
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// sshCommand keeps the user's GIT_SSH_COMMAND, made non-interactive.
func sshCommand() string {
	if c := os.Getenv("GIT_SSH_COMMAND"); c != "" {
		return c
	}
	return "ssh -o BatchMode=yes"
}
