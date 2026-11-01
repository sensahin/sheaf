package setup

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sensahin/sheaf/internal/hook"
)

func TestPlansAreValid(t *testing.T) {
	for _, kind := range Kinds() {
		p, err := For(kind, "/opt/bin/sheaf")
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range p.Files {
			if !strings.Contains(f.Content, f.Marker) {
				t.Errorf("%s %s: content lacks its own marker %q", kind, f.Path, f.Marker)
			}
			if strings.HasSuffix(f.Path, ".json") {
				var v any
				if err := json.Unmarshal([]byte(f.Content), &v); err != nil {
					t.Errorf("%s %s: invalid JSON: %v", kind, f.Path, err)
				}
			}
			if strings.Contains(f.Marker, "hook ") && !strings.Contains(f.Content, "/opt/bin/sheaf") {
				t.Errorf("%s %s: does not use the sheaf path", kind, f.Path)
			}
		}
	}
	if _, err := For("nope", "sheaf"); err == nil {
		t.Fatal("unknown kind accepted")
	}
}

func TestApplyNeverRewritesAgentConfig(t *testing.T) {
	dir := t.TempDir()
	p, _ := For("opencode", "sheaf")
	res, err := Apply(dir, p)
	if err != nil || res[0].Status != "created" || res[1].Status != "created" {
		t.Fatalf("first apply: %+v %v", res, err)
	}
	res, _ = Apply(dir, p)
	if res[0].Status != "ok" || res[1].Status != "ok" {
		t.Fatalf("second apply: %+v", res)
	}

	// The user's own opencode.json gains Sheaf's entry and keeps theirs,
	// with a backup; Sheaf's plugin file is rewritten.
	cfg := filepath.Join(dir, "opencode.json")
	mine := `{"model": "anthropic/claude"}`
	os.WriteFile(cfg, []byte(mine), 0o644)
	os.WriteFile(filepath.Join(dir, ".opencode/plugins/sheaf.js"), []byte("old"), 0o644)
	res, _ = Apply(dir, p)
	if res[0].Status != "updated" || res[1].Status != "merged" {
		t.Fatalf("third apply: %+v", res)
	}
	if b, _ := os.ReadFile(cfg + ".sheaf-backup"); string(b) != mine {
		t.Fatalf("backup: %s", b)
	}
	b, _ := os.ReadFile(cfg)
	if !strings.Contains(string(b), `"model": "anthropic/claude"`) || !strings.Contains(string(b), `"sheaf"`) {
		t.Fatalf("merged opencode.json: %s", b)
	}
	if res, _ = Apply(dir, p); res[1].Status != "ok" {
		t.Fatalf("after merge: %+v", res)
	}

	// A config Sheaf cannot parse (comments) is left untouched.
	jsonc := "// mine\n{\"model\": \"x\"}"
	os.WriteFile(cfg, []byte(jsonc), 0o644)
	res, _ = Apply(dir, p)
	if res[1].Status != "skipped" || res[1].Content == "" {
		t.Fatalf("jsonc: %+v", res)
	}
	if b, _ := os.ReadFile(cfg); string(b) != jsonc {
		t.Fatalf("jsonc rewritten: %s", b)
	}
}

func TestMergeKeepsTheUsersClaudeSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude/settings.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	mine := `{
  "model": "opus",
  "permissions": {"allow": ["Bash(npm test)"]},
  "hooks": {
    "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "my-linter", "timeout": 30}]}]
  }
}`
	os.WriteFile(path, []byte(mine), 0o600)
	p, _ := For("claude-code", "/bin/sheaf")
	res, err := Apply(dir, p)
	if err != nil || res[0].Status != "merged" {
		t.Fatalf("%+v %v", res, err)
	}
	var got struct {
		Model       string
		Permissions struct{ Allow []string }
		Hooks       map[string][]struct {
			Matcher string
			Hooks   []struct{ Command string }
		}
	}
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	pre := got.Hooks["PreToolUse"]
	if got.Model != "opus" || len(got.Permissions.Allow) != 1 || len(pre) != 2 ||
		pre[0].Hooks[0].Command != "my-linter" || pre[1].Hooks[0].Command != "/bin/sheaf hook claude-code" {
		t.Fatalf("merged settings: %s", b)
	}
	if len(got.Hooks["Stop"]) != 1 {
		t.Fatalf("status hooks missing: %s", b)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode changed to %v", st.Mode())
	}
	if again, _ := Apply(dir, p); again[0].Status != "ok" {
		t.Fatalf("second apply: %+v", again)
	}
}

func TestEveryKindHasAHook(t *testing.T) {
	for _, kind := range Kinds() {
		p0, _ := For(kind, "sheaf")
		if p0.MCPOnly {
			if hook.Adapters[kind] != nil {
				t.Errorf("%s is MCP-only but has a hook adapter", kind)
			}
			continue
		}
		if hook.Adapters[kind] == nil {
			t.Errorf("sheaf setup %s writes a hook that `sheaf hook %s` does not serve", kind, kind)
		}
		p, _ := For(kind, "sheaf")
		found := false
		for _, f := range p.Files {
			found = found || strings.Contains(f.Content, "hook "+kind)
		}
		if !found {
			t.Errorf("%s: no file runs `sheaf hook %s`", kind, kind)
		}
	}
}

func TestClineHookIsExecutable(t *testing.T) {
	dir := t.TempDir()
	p, _ := For("cline", "/opt/my sheaf/sheaf")
	if _, err := Apply(dir, p); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dir, ".clinerules/hooks/PreToolUse"))
	if err != nil || st.Mode().Perm() != 0o755 {
		t.Fatalf("mode: %v %v", st, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".clinerules/hooks/PreToolUse"))
	if !strings.Contains(string(b), "exec '/opt/my sheaf/sheaf' hook cline") {
		t.Fatalf("script: %s", b)
	}
}

func TestStatusHooks(t *testing.T) {
	for kind, want := range map[string][]string{
		"claude-code": {`"UserPromptSubmit"`, `"Notification"`, `"Stop"`, "hook status blocked"},
		"codex":       {`"Stop"`, "hook status idle"},
		"copilot":     {`"agentStop"`, `"userPromptSubmitted"`},
		"cursor":      {`"stop"`},
		"gemini":      {`"AfterAgent"`, `"BeforeAgent"`, `"timeout": 10000`},
		"droid":       {`"Notification"`, `"Stop"`},
		"goose":       {`"Stop"`},
	} {
		p, _ := For(kind, "sheaf")
		all := ""
		for _, f := range p.Files {
			all += f.Content
		}
		for _, w := range want {
			if !strings.Contains(all, w) {
				t.Errorf("%s: config lacks %s", kind, w)
			}
		}
	}
}

func TestPrepareWorktreeGivesTheAgentItsHooksWithoutCommittingThem(t *testing.T) {
	home := fakeHome(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	git := func(dir string, args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=a@b", "-c", "user.name=a"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	git(repo, "init", "-q")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a"), 0o644)
	git(repo, "add", ".")
	git(repo, "commit", "-qm", "init")
	wt := filepath.Join(t.TempDir(), "wt")
	git(repo, "worktree", "add", "-q", "-b", "task", wt)

	wrote, err := PrepareWorktree(wt, "claude-code", "/bin/sheaf")
	if err != nil || len(wrote) != 1 || wrote[0] != ".claude/settings.json" {
		t.Fatalf("%v %v", wrote, err)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, ".claude/settings.json")); !strings.Contains(string(b), "/bin/sheaf hook claude-code") {
		t.Fatalf("settings: %s", b)
	}
	if st := git(wt, "status", "--porcelain"); st != "" {
		t.Fatalf("worktree not clean: %q", st)
	}
	// Claude Code will not ask whether to trust the worktree.
	if p := readJSON(t, filepath.Join(home, ".claude.json"))["projects"].(map[string]any)[wt]; p == nil {
		t.Fatal("worktree not trusted for Claude Code")
	}
	again, err := PrepareWorktree(wt, "claude-code", "/bin/sheaf")
	if err != nil || len(again) != 0 {
		t.Fatalf("second run wrote %v %v", again, err)
	}
	if wrote, _ := PrepareWorktree(wt, "", "/bin/sheaf"); wrote != nil {
		t.Fatal("unknown kind wrote files")
	}
}
