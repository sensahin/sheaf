package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	return home
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestTrustClaudeKeepsTheRestOfItsConfig(t *testing.T) {
	home := fakeHome(t)
	path := filepath.Join(home, ".claude.json")
	os.WriteFile(path, []byte(`{"numStartups": 12345678901234567890, "projects": {"/src/app": {"allowedTools": ["Bash"], "hasTrustDialogAccepted": true}}}`), 0o600)

	if err := Trust("claude-code", "/w/t-1"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "12345678901234567890") {
		t.Fatalf("number lost precision: %s", b)
	}
	m := readJSON(t, path)
	p := m["projects"].(map[string]any)
	if p["/w/t-1"].(map[string]any)["hasTrustDialogAccepted"] != true || p["/src/app"].(map[string]any)["allowedTools"] == nil {
		t.Fatalf("projects: %v", p)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
}

func TestTrustCodexAppendsOnce(t *testing.T) {
	home := fakeHome(t)
	path := filepath.Join(home, ".codex", "config.toml")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte("[mcp_servers.sheaf]\ncommand = \"sheaf\""), 0o600)
	for i := 0; i < 2; i++ {
		if err := Trust("codex", "/w/t-1"); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	if strings.Count(s, `[projects."/w/t-1"]`) != 1 || !strings.Contains(s, "command = \"sheaf\"\n") || !strings.HasSuffix(s, "trust_level = \"trusted\"\n") {
		t.Fatalf("config.toml:\n%s", s)
	}
}

func TestTrustGeminiAndCopilot(t *testing.T) {
	home := fakeHome(t)
	Trust("gemini", "/w/t-1")
	if m := readJSON(t, filepath.Join(home, ".gemini", "trustedFolders.json")); m["/w/t-1"] != "TRUST_FOLDER" {
		t.Fatalf("gemini: %v", m)
	}
	Trust("copilot", "/w/t-1")
	Trust("copilot", "/w/t-1")
	if m := readJSON(t, filepath.Join(home, ".copilot", "config.json")); len(m["trusted_folders"].([]any)) != 1 {
		t.Fatalf("copilot: %v", m)
	}
	// Kinds without a folder prompt touch nothing.
	if err := Trust("aider", "/w/t-1"); err != nil {
		t.Fatal(err)
	}
}

func TestTrustLeavesBrokenConfigAlone(t *testing.T) {
	home := fakeHome(t)
	path := filepath.Join(home, ".claude.json")
	os.WriteFile(path, []byte("{not json"), 0o600)
	if err := Trust("claude-code", "/w/t-1"); err == nil {
		t.Fatal("rewrote a file it could not read")
	}
	if b, _ := os.ReadFile(path); string(b) != "{not json" {
		t.Fatalf("changed: %s", b)
	}
}
