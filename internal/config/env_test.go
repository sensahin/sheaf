package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseEnv(t *testing.T) {
	vars, err := ParseEnv([]byte(`
# comment
export GH_TOKEN=ghp_abc
VERCEL_TOKEN="with space"
CLOUDFLARE_ACCOUNT_ID='abc#123'
GH_TOKEN=ghp_new
EMPTY=
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []EnvVar{{"GH_TOKEN", "ghp_new"}, {"VERCEL_TOKEN", "with space"}, {"CLOUDFLARE_ACCOUNT_ID", "abc#123"}, {"EMPTY", ""}}
	if len(vars) != len(want) {
		t.Fatalf("got %v", vars)
	}
	for i := range want {
		if vars[i] != want[i] {
			t.Errorf("var %d = %v, want %v", i, vars[i], want[i])
		}
	}
	if _, err := ParseEnv([]byte("not a line")); err == nil {
		t.Error("want error for a line without =")
	}
}

func TestSetEnvAndAgentEnv(t *testing.T) {
	p := At(t.TempDir())
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(SetEnv(p.EnvPath(""), "VERCEL_TOKEN", "v1", false))
	must(SetEnv(p.EnvPath(""), "CLOUDFLARE_API_TOKEN", "it's quoted", false))
	must(SetEnv(p.EnvPath(""), "GONE", "x", false))
	must(SetEnv(p.EnvPath(""), "GONE", "", true))
	must(SetEnv(p.EnvPath("codex"), "VERCEL_TOKEN", "codex-only", false))
	if err := SetEnv(p.EnvPath(""), "BAD-KEY", "x", false); err == nil {
		t.Error("want error for a bad key")
	}
	if err := SetEnv(p.EnvPath(""), "K", "a\nb", false); err == nil {
		t.Error("want error for a multi-line value")
	}

	info, err := os.Stat(p.EnvPath(""))
	must(err)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	if filepath.Dir(p.EnvPath("codex")) != filepath.Join(p.Home, "agents") {
		t.Errorf("agent env path = %s", p.EnvPath("codex"))
	}

	env, err := p.AgentEnv("codex", []string{"PATH=/bin", "VERCEL_TOKEN=stale"})
	must(err)
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := cut(kv)
		if _, dup := got[k]; dup {
			t.Errorf("duplicate %s", k)
		}
		got[k] = v
	}
	if got["VERCEL_TOKEN"] != "codex-only" || got["CLOUDFLARE_API_TOKEN"] != "it's quoted" || got["PATH"] != "/bin" {
		t.Errorf("env = %v", got)
	}
	if _, ok := got["GONE"]; ok {
		t.Error("removed key still set")
	}

	env, err = p.AgentEnv("claude", nil)
	must(err)
	if len(env) != 2 {
		t.Errorf("claude env = %v", env)
	}
}

func cut(kv string) (string, string, bool) {
	for i := range kv {
		if kv[i] == '=' {
			return kv[:i], kv[i+1:], true
		}
	}
	return kv, "", false
}
