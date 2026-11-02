package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Agent environment files hold the credentials agents need for outside
// services (GH_TOKEN, VERCEL_TOKEN, CLOUDFLARE_API_TOKEN, ...). They live
// outside every repository, owner-only, and `sheaf run` adds them to the
// agent's environment, so an agent started by the daemon, from the phone or
// in an old tmux server still sees them.
//
//	env               every agent
//	agents/NAME.env   one agent; wins over env
//
// The format is KEY=VALUE per line. Blank lines and lines starting with # are
// skipped, a leading "export " is allowed, and a value may be wrapped in
// single or double quotes.

// EnvPath is the file shared by all agents, or one agent's file when agent is
// not empty.
func (p Paths) EnvPath(agent string) string {
	if agent == "" {
		return filepath.Join(p.Home, "env")
	}
	return filepath.Join(p.Home, "agents", agent+".env")
}

var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidEnvKey reports whether k can be an environment variable name.
func ValidEnvKey(k string) bool { return envKey.MatchString(k) }

// EnvVar is one entry of an environment file.
type EnvVar struct {
	Key, Value string
}

// ReadEnv parses an environment file. A missing file is empty.
func ReadEnv(path string) ([]EnvVar, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return ParseEnv(b)
}

// ParseEnv parses the KEY=VALUE format described above. Later lines win.
func ParseEnv(b []byte) ([]EnvVar, error) {
	var out []EnvVar
	idx := map[string]int{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || !ValidEnvKey(k) {
			return nil, fmt.Errorf("line %d: want KEY=VALUE", n)
		}
		v = unquote(strings.TrimSpace(v))
		if i, seen := idx[k]; seen {
			out[i].Value = v
			continue
		}
		idx[k] = len(out)
		out = append(out, EnvVar{k, v})
	}
	return out, sc.Err()
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

// SetEnv adds or replaces key in the file, or removes it when remove is set,
// and writes the file owner-only. Comments in the file are not kept.
func SetEnv(path, key, value string, remove bool) error {
	if !ValidEnvKey(key) {
		return fmt.Errorf("%q is not a valid variable name", key)
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("the value of %s must be one line", key)
	}
	vars, err := ReadEnv(path)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	var kept []EnvVar
	found := false
	for _, v := range vars {
		if v.Key == key {
			found = true
			if remove {
				continue
			}
			v.Value = value
		}
		kept = append(kept, v)
	}
	if !found && !remove {
		kept = append(kept, EnvVar{key, value})
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Key < kept[j].Key })
	var b strings.Builder
	b.WriteString("# Written by `sheaf env`. Added to every agent Sheaf starts. Keep out of git.\n")
	for _, v := range kept {
		fmt.Fprintf(&b, "%s=%s\n", v.Key, quoteEnv(v.Value))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func quoteEnv(v string) string {
	if v == "" || strings.ContainsAny(v, " \t#'\"") {
		if !strings.Contains(v, "'") {
			return "'" + v + "'"
		}
		return `"` + v + `"`
	}
	return v
}

// AgentEnv returns environ with the shared file and then the agent's own file
// applied on top, so the files win over whatever the launching shell had.
func (p Paths) AgentEnv(agent string, environ []string) ([]string, error) {
	files := []string{p.EnvPath("")}
	if agent != "" {
		files = append(files, p.EnvPath(agent))
	}
	var vars []EnvVar
	for _, f := range files {
		if info, err := os.Stat(f); err == nil && info.Mode().Perm()&0o077 != 0 {
			fmt.Fprintf(os.Stderr, "sheaf: warning: %s is readable by others; run chmod 600 %s\n", f, f)
		}
		v, err := ReadEnv(f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		vars = append(vars, v...)
	}
	return MergeEnv(environ, vars), nil
}

// MergeEnv sets vars in environ, replacing existing entries.
func MergeEnv(environ []string, vars []EnvVar) []string {
	if len(vars) == 0 {
		return environ
	}
	set := map[string]string{}
	for _, v := range vars {
		set[v.Key] = v.Value
	}
	out := make([]string, 0, len(environ)+len(vars))
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := set[k]; ok {
			continue
		}
		out = append(out, kv)
	}
	for _, v := range vars {
		if _, ok := set[v.Key]; ok {
			out = append(out, v.Key+"="+set[v.Key])
			delete(set, v.Key)
		}
	}
	return out
}
