// Package config resolves where Sheaf keeps its state on disk.
//
// Everything lives under one directory, $SHEAF_HOME (default ~/.sheaf):
//
//	sheaf.db      SQLite event log and state
//	sheaf.sock    the daemon's Unix socket
//	owner.token   the human owner's token (mode 0600)
//	agents/       each agent's token, so Sheaf can launch it (mode 0600)
//	worktrees/    one git worktree per task
package config

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Paths holds the resolved locations of Sheaf's files.
type Paths struct {
	Home         string
	DB           string
	Socket       string
	OwnerToken   string
	WorktreeRoot string
}

// Resolve returns the paths for $SHEAF_HOME, falling back to ~/.sheaf.
func Resolve() (Paths, error) {
	home := os.Getenv("SHEAF_HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return Paths{}, err
		}
		home = filepath.Join(h, ".sheaf")
	}
	return At(home), nil
}

// At returns the paths rooted at dir.
func At(dir string) Paths {
	return Paths{
		Home:         dir,
		DB:           filepath.Join(dir, "sheaf.db"),
		Socket:       filepath.Join(dir, "sheaf.sock"),
		OwnerToken:   filepath.Join(dir, "owner.token"),
		WorktreeRoot: filepath.Join(dir, "worktrees"),
	}
}

// Ensure creates the home directory with owner-only permissions.
func (p Paths) Ensure() error {
	return os.MkdirAll(p.Home, 0o700)
}

// LoadOrCreateOwnerToken returns the owner token, creating it on first use.
func (p Paths) LoadOrCreateOwnerToken() (string, error) {
	b, err := os.ReadFile(p.OwnerToken)
	if err == nil {
		return strings.TrimSpace(string(b)), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := p.Ensure(); err != nil {
		return "", err
	}
	tok := NewToken("sho_")
	if err := os.WriteFile(p.OwnerToken, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

// AgentTokenPath is where an agent's token is kept.
func (p Paths) AgentTokenPath(name string) string {
	return filepath.Join(p.Home, "agents", name+".token")
}

// SaveAgentToken stores an agent's token, readable only by this user.
func (p Paths) SaveAgentToken(name, token string) error {
	dir := filepath.Join(p.Home, "agents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(p.AgentTokenPath(name), []byte(token+"\n"), 0o600)
}

// LoadAgentToken reads a token saved by SaveAgentToken.
func (p Paths) LoadAgentToken(name string) (string, error) {
	b, err := os.ReadFile(p.AgentTokenPath(name))
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("no saved token for agent %q; run `sheaf agent token %s` to issue one", name, name)
	}
	return strings.TrimSpace(string(b)), err
}

// RemoveAgentToken deletes a saved token, if any.
func (p Paths) RemoveAgentToken(name string) error {
	err := os.Remove(p.AgentTokenPath(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// NewToken returns a random 160-bit token with the given prefix.
func NewToken(prefix string) string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}
