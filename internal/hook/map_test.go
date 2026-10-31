package hook

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sensahin/sheaf/internal/core"
)

func TestPatchFiles(t *testing.T) {
	patch := `*** Begin Patch
*** Update File: src/app.go
@@
-old
+new
*** Add File: docs/new.md
+hello
*** Update File: src/app.go
*** Move to: src/main.go
*** Delete File: tmp/x
*** End Patch`
	got := PatchFiles(patch)
	want := []string{"src/app.go", "docs/new.md", "src/main.go", "tmp/x"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestCallMapping(t *testing.T) {
	raw := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
	cases := []struct {
		name    string
		got     Call
		ok      bool
		action  string
		targets []string
	}{}
	add := func(name string, c Call, ok bool, action string, targets ...string) {
		cases = append(cases, struct {
			name    string
			got     Call
			ok      bool
			action  string
			targets []string
		}{name, c, ok, action, targets})
	}
	c, ok := codexCall(PreToolUseInput{ToolName: "Bash", ToolInput: raw(map[string]any{"command": "go test ./..."})})
	add("codex bash", c, ok, core.ActionShell, "go test ./...")
	c, ok = codexCall(PreToolUseInput{ToolName: "Bash", ToolInput: raw(map[string]any{"command": []string{"git", "push"}})})
	add("codex argv", c, ok, core.ActionShell, "git push")
	c, ok = codexCall(PreToolUseInput{ToolName: "apply_patch", ToolInput: raw(map[string]any{"command": "*** Begin Patch\n*** Add File: a.go\n+x\n*** End Patch"})})
	add("codex patch", c, ok, core.ActionWrite, "a.go")
	c, ok = codexCall(PreToolUseInput{ToolName: "mcp__sheaf__inbox"})
	add("codex sheaf tool", c, ok, "")
	c, ok = codexCall(PreToolUseInput{ToolName: "mcp__github__create_issue"})
	add("codex mcp tool", c, ok, core.ActionTool, "mcp__github__create_issue")

	c, ok = openCodeCall(OpenCodeInput{Tool: "bash", Args: raw(map[string]any{"command": "rm -rf build"})})
	add("opencode bash", c, ok, core.ActionShell, "rm -rf build")
	c, ok = openCodeCall(OpenCodeInput{Tool: "edit", Args: raw(map[string]any{"filePath": "/etc/hosts"})})
	add("opencode edit", c, ok, core.ActionWrite, "/etc/hosts")
	c, ok = openCodeCall(OpenCodeInput{Tool: "webfetch", Args: raw(map[string]any{"url": "https://example.com/a?b"})})
	add("opencode webfetch", c, ok, core.ActionNetwork, "example.com")
	c, ok = openCodeCall(OpenCodeInput{Tool: "read", Args: raw(map[string]any{"filePath": "x"})})
	add("opencode read", c, ok, "")
	c, ok = openCodeCall(OpenCodeInput{Tool: "sheaf_send"})
	add("opencode sheaf tool", c, ok, "")

	c, ok = claudeCall(PreToolUseInput{ToolName: "WebFetch", ToolInput: raw(map[string]any{"url": "https://api.github.com/x"})})
	add("claude webfetch", c, ok, core.ActionNetwork, "api.github.com")

	for _, tc := range cases {
		if tc.action == "" {
			if tc.ok {
				t.Errorf("%s: should be skipped, got %+v", tc.name, tc.got)
			}
			continue
		}
		if !tc.ok || tc.got.Action != tc.action || !reflect.DeepEqual(tc.got.Targets, tc.targets) {
			t.Errorf("%s: got %+v (ok=%v)", tc.name, tc.got, tc.ok)
		}
	}
}
