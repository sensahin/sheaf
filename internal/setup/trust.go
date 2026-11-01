package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Trust marks dir as trusted in the agent program's own settings, so an
// agent started there does not stop on a "do you trust this folder?" prompt
// that nobody is at the keyboard to answer. Sheaf calls it for task
// worktrees only: folders it created itself, from a repository you chose.
// Kinds without a folder prompt, or whose settings Sheaf does not know, are a
// no-op.
func Trust(kind, dir string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	switch kind {
	case "claude-code":
		return trustClaude(home, dir)
	case "codex":
		return trustCodex(home, dir)
	case "gemini":
		return updateJSON(filepath.Join(home, ".gemini", "trustedFolders.json"), func(m map[string]any) bool {
			if m[dir] == "TRUST_FOLDER" {
				return false
			}
			m[dir] = "TRUST_FOLDER"
			return true
		})
	case "copilot":
		return updateJSON(filepath.Join(home, ".copilot", "config.json"), func(m map[string]any) bool {
			list, _ := m["trusted_folders"].([]any)
			for _, f := range list {
				if f == dir {
					return false
				}
			}
			m["trusted_folders"] = append(list, dir)
			return true
		})
	}
	return nil
}

// Claude Code keeps per-folder trust in ~/.claude.json (or
// $CLAUDE_CONFIG_DIR/.claude.json), under projects[dir].
func trustClaude(home, dir string) error {
	path := filepath.Join(home, ".claude.json")
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		path = filepath.Join(d, ".claude.json")
	}
	return updateJSON(path, func(m map[string]any) bool {
		projects, _ := m["projects"].(map[string]any)
		if projects == nil {
			projects = map[string]any{}
			m["projects"] = projects
		}
		p, _ := projects[dir].(map[string]any)
		if p == nil {
			p = map[string]any{}
			projects[dir] = p
		}
		if p["hasTrustDialogAccepted"] == true {
			return false
		}
		p["hasTrustDialogAccepted"] = true
		return true
	})
}

// Codex keeps per-folder trust in ~/.codex/config.toml (or
// $CODEX_HOME/config.toml) as [projects."DIR"] trust_level = "trusted".
// A trusted folder also gets its project hooks, which is how Sheaf's hook
// reaches a Codex started in a task worktree.
func trustCodex(home, dir string) error {
	codexHome := filepath.Join(home, ".codex")
	if d := os.Getenv("CODEX_HOME"); d != "" {
		codexHome = d
	}
	path := filepath.Join(codexHome, "config.toml")
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	header := "[projects." + tomlString(dir) + "]"
	if bytes.Contains(old, []byte(header)) {
		return nil
	}
	var b strings.Builder
	b.Write(old)
	if len(old) > 0 && !bytes.HasSuffix(old, []byte("\n")) {
		b.WriteString("\n")
	}
	b.WriteString("\n# Sheaf task worktree\n" + header + "\ntrust_level = \"trusted\"\n")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		return err
	}
	return replaceFile(path, []byte(b.String()), 0o600)
}

// updateJSON edits a JSON object file in place; edit reports whether it
// changed anything. A missing file starts empty. Numbers keep their exact
// text, and the file is replaced atomically, so a program reading it never
// sees half a file.
func updateJSON(path string, edit func(map[string]any) bool) error {
	m := map[string]any{}
	old, err := os.ReadFile(path)
	switch {
	case err == nil && len(bytes.TrimSpace(old)) > 0:
		d := json.NewDecoder(bytes.NewReader(old))
		d.UseNumber()
		if err := d.Decode(&m); err != nil {
			return err // not ours to repair
		}
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return err
	}
	if !edit(m) {
		return nil
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return replaceFile(path, append(b, '\n'), 0o600)
}

// replaceFile writes data to path through a temporary file and a rename,
// keeping an existing file's mode.
func replaceFile(path string, data []byte, mode os.FileMode) error {
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".sheaf-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
