// Package config resolves where Sheaf keeps its state on disk.
//
// Everything lives under one directory, $SHEAF_HOME (default ~/.sheaf):
//
//	sheaf.db      SQLite event log and state
//	sheaf.sock    the daemon's Unix socket
//	owner.token   the human owner's token (mode 0600)
//	worktrees/    one git worktree per task
package config

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
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

// NewToken returns a random 160-bit token with the given prefix.
func NewToken(prefix string) string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}
