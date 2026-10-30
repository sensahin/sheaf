// Package worktree gives each task its own git worktree, so agents never edit
// the same checkout.
package worktree

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// Add creates a worktree at path on a new branch, starting from the repo's
// current HEAD. It returns the commit the branch started from.
func Add(repo, path, branch string) (base string, err error) {
	base, err = git(repo, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if _, err := git(repo, "worktree", "add", "-b", branch, path, base); err != nil {
		return "", err
	}
	return base, nil
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
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}
