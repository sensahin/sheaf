package hook

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sensahin/sheaf/internal/core"
)

func TestMoreAgentsMapping(t *testing.T) {
	check := func(name string, c Call, ok bool, action string, targets ...string) {
		t.Helper()
		if action == "" {
			if ok {
				t.Errorf("%s: should be skipped, got %+v", name, c)
			}
			return
		}
		if !ok || c.Action != action || !reflect.DeepEqual(c.Targets, targets) {
			t.Errorf("%s: got %+v (ok=%v)", name, c, ok)
		}
	}
	c, ok := copilotCall(copilotInput{ToolName: "bash", ToolArgs: []byte(`{"command":"npm test"}`)})
	check("copilot bash", c, ok, core.ActionShell, "npm test")
	c, ok = copilotCall(copilotInput{ToolName: "bash", ToolArgs: []byte(`"{\"command\":\"git push\"}"`)})
	check("copilot args as string", c, ok, core.ActionShell, "git push")
	c, ok = copilotCall(copilotInput{ToolName: "create", ToolArgs: []byte(`{"path":"src/a.ts","file_text":"x"}`)})
	check("copilot create", c, ok, core.ActionWrite, "src/a.ts")
	c, ok = copilotCall(copilotInput{ToolName: "view", ToolArgs: []byte(`{"path":"x"}`)})
	check("copilot view", c, ok, "")
	c, ok = copilotCall(copilotInput{ToolName: "sheaf-send"})
	check("copilot sheaf tool", c, ok, "")

	c, ok = cursorCall(cursorInput{ToolName: "Shell", ToolInput: []byte(`{"command":"make"}`), Roots: []string{"/w"}})
	check("cursor shell", c, ok, core.ActionShell, "make")
	if c.Cwd != "/w" {
		t.Errorf("cursor cwd from workspace root: %q", c.Cwd)
	}
	c, ok = cursorCall(cursorInput{ToolName: "Delete", ToolInput: []byte(`{"file_path":"/w/x"}`)})
	check("cursor delete", c, ok, core.ActionWrite, "/w/x")
	c, ok = cursorCall(cursorInput{ToolName: "MCP:send"})
	check("cursor mcp", c, ok, core.ActionTool, "MCP:send")
	c, ok = cursorCall(cursorInput{ToolName: "Read"})
	check("cursor read", c, ok, "")

	c, ok = geminiCall(PreToolUseInput{ToolName: "run_shell_command", ToolInput: []byte(`{"command":"ls"}`)})
	check("gemini shell", c, ok, core.ActionShell, "ls")
	c, ok = geminiCall(PreToolUseInput{ToolName: "replace", ToolInput: []byte(`{"file_path":"/p/a.go"}`)})
	check("gemini replace", c, ok, core.ActionWrite, "/p/a.go")
	c, ok = geminiCall(PreToolUseInput{ToolName: "web_fetch", ToolInput: []byte(`{"prompt":"summarize https://a.example/x and http://b.example"}`)})
	check("gemini web_fetch", c, ok, core.ActionNetwork, "a.example", "b.example")
	c, ok = geminiCall(PreToolUseInput{ToolName: "mcp_sheaf_inbox"})
	check("gemini sheaf tool", c, ok, "")
	c, ok = geminiCall(PreToolUseInput{ToolName: "read_file"})
	check("gemini read", c, ok, "")

	c, ok = gooseCall(gooseInput{ToolName: "developer__text_editor", ToolInput: []byte(`{"command":"view","path":"x"}`)})
	check("goose view", c, ok, "")
	c, ok = gooseCall(gooseInput{ToolName: "developer__edit", ToolInput: []byte(`{"path":"/p/a","before":"x","after":"y"}`), WorkingDir: "/p"})
	check("goose edit", c, ok, core.ActionWrite, "/p/a")
	c, ok = gooseCall(gooseInput{ToolName: "sheaf__send"})
	check("goose sheaf tool", c, ok, "")
	c, ok = ampCall(ampInput{Tool: "Bash", Input: []byte(`{"cmd":"make","cwd":"/r"}`)})
	check("amp bash", c, ok, core.ActionShell, "make")
	if c.Cwd != "/r" {
		t.Errorf("amp cwd from input: %q", c.Cwd)
	}
	c, ok = ampCall(ampInput{Tool: "read_web_page", Input: []byte(`{"url":"https://docs.example/x"}`)})
	check("amp web", c, ok, core.ActionNetwork, "docs.example")

	c, ok = kiroCall(kiroInput{ToolTags: &kiroCallFields{ToolName: "fs_write", ToolInput: []byte(`{"path":"/k/a.rs"}`)}})
	check("kiro v2 tool_tags", c, ok, core.ActionWrite, "/k/a.rs")
	c, ok = kiroCall(kiroInput{kiroCallFields: kiroCallFields{ToolName: "fs_read"}})
	check("kiro read", c, ok, "")

	c, ok = droidCall(PreToolUseInput{ToolName: "Execute", ToolInput: []byte(`{"command":"make"}`)})
	check("droid execute", c, ok, core.ActionShell, "make")
	c, ok = droidCall(PreToolUseInput{ToolName: "Create", ToolInput: []byte(`{"file_path":"/p/n.go","content":"x"}`)})
	check("droid create", c, ok, core.ActionWrite, "/p/n.go")
	c, ok = droidCall(PreToolUseInput{ToolName: "mcp__sheaf__send"})
	check("droid sheaf tool", c, ok, "")

	c, ok = antigravityCall(antigravityInput{ToolCall: struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	}{"run_command", []byte(`{"CommandLine":"cat /etc/hosts"}`)}, WorkspacePaths: []string{"/w"}})
	check("agy run_command", c, ok, core.ActionShell, "cat /etc/hosts")
	if c.Cwd != "/w" {
		t.Errorf("agy cwd: %q", c.Cwd)
	}
}
