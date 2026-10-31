package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sensahin/sheaf/internal/api"
	"github.com/sensahin/sheaf/internal/core"
)

// GitHub Copilot CLI: a camelCase preToolUse hook. toolArgs is an object in
// current versions and a JSON string in older ones. The answer is
// {"permissionDecision": allow|deny|ask, "permissionDecisionReason": ...};
// "ask" hands the call back to Copilot's own prompt.

type copilotInput struct {
	ToolName string          `json:"toolName"`
	ToolArgs json.RawMessage `json:"toolArgs"`
	Cwd      string          `json:"cwd"`
}

type copilotOutput struct {
	PermissionDecision       string `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
}

// argsObject accepts an object or a JSON string holding one.
func argsObject(raw json.RawMessage) map[string]any {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		raw = json.RawMessage(s)
	}
	return fields(raw)
}

func copilotCall(in copilotInput) (Call, bool) {
	a := argsObject(in.ToolArgs)
	c := Call{Tool: in.ToolName, Cwd: in.Cwd}
	switch in.ToolName {
	case "bash", "powershell", "shell":
		c.Action, c.Targets = core.ActionShell, []string{str(a, "command")}
	case "create", "edit", "str_replace_editor", "write":
		c.Action, c.Targets = core.ActionWrite, []string{str(a, "path", "file_path", "filePath")}
	case "apply_patch":
		c.Action = core.ActionWrite
		patch := str(a, "input", "patch", "command")
		if c.Targets = PatchFiles(patch); len(c.Targets) == 0 {
			c.Targets = []string{oneLine(patch, 200)}
		}
	case "web_fetch":
		c.Action, c.Targets = core.ActionNetwork, []string{host(str(a, "url"))}
	case "view", "grep", "rg", "glob", "update_todo", "ask_user", "report_intent", "web_search", "read_bash", "list_bash":
		return Call{}, false
	default:
		if isSheafTool(in.ToolName) {
			return Call{}, false
		}
		c.Action, c.Targets = core.ActionTool, []string{in.ToolName}
	}
	return c, true
}

// isSheafTool reports whether an MCP tool name is one of Sheaf's own, in any
// agent's naming scheme (mcp__sheaf__send, sheaf_send, sheaf-send, ...).
func isSheafTool(name string) bool {
	for _, p := range []string{"mcp__sheaf__", "mcp_sheaf_", "sheaf_", "sheaf-", "sheaf/", "MCP:sheaf"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// Copilot answers a Copilot CLI preToolUse hook.
func Copilot(ctx context.Context, client *api.Client, r io.Reader, wait time.Duration) ([]byte, error) {
	var in copilotInput
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return nil, fmt.Errorf("reading hook input: %w", err)
	}
	call, ok := copilotCall(in)
	if !ok {
		return nil, nil
	}
	v := Decide(ctx, client, call, wait)
	switch {
	case v.Unresolved:
		return json.Marshal(copilotOutput{"ask", v.Reason})
	case v.Effect != "":
		return json.Marshal(copilotOutput{v.Effect, v.Reason})
	}
	return nil, nil
}
