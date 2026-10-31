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

// Cursor (IDE and cursor-agent): a preToolUse hook in hooks.json. Tool names
// are Shell, Write, Delete, Read, Grep, Task and MCP:<tool>. The answer is
// {"permission": allow|deny, "user_message", "agent_message"}; preToolUse
// has no "ask", so an unanswered call gets no opinion.

type cursorInput struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	Cwd       string          `json:"cwd"`
	Roots     []string        `json:"workspace_roots"`
}

type cursorOutput struct {
	Permission   string `json:"permission,omitempty"`
	UserMessage  string `json:"user_message,omitempty"`
	AgentMessage string `json:"agent_message,omitempty"`
}

func cursorCall(in cursorInput) (Call, bool) {
	a := argsObject(in.ToolInput)
	cwd := in.Cwd
	if cwd == "" && len(in.Roots) > 0 {
		cwd = in.Roots[0]
	}
	c := Call{Tool: in.ToolName, Cwd: cwd}
	switch in.ToolName {
	case "Shell", "run_terminal_cmd":
		c.Action, c.Targets = core.ActionShell, []string{str(a, "command")}
	case "Write", "Edit", "Delete", "edit_file", "delete_file", "search_replace":
		c.Action, c.Targets = core.ActionWrite, []string{str(a, "file_path", "path", "target_file", "filePath")}
	case "WebFetch", "fetch":
		c.Action, c.Targets = core.ActionNetwork, []string{host(str(a, "url"))}
	case "Read", "Grep", "Glob", "LS", "Task", "TodoWrite", "read_file", "grep", "codebase_search", "list_dir", "file_search", "todo_write", "web_search":
		return Call{}, false
	default:
		name := strings.TrimPrefix(in.ToolName, "MCP:")
		if isSheafTool(in.ToolName) || isSheafTool(name) {
			return Call{}, false
		}
		c.Action, c.Targets = core.ActionTool, []string{in.ToolName}
	}
	return c, true
}

// Cursor answers a Cursor preToolUse hook. It always prints a JSON object,
// empty for no opinion.
func Cursor(ctx context.Context, client *api.Client, r io.Reader, wait time.Duration) ([]byte, error) {
	var in cursorInput
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return nil, fmt.Errorf("reading hook input: %w", err)
	}
	var out cursorOutput
	if call, ok := cursorCall(in); ok {
		if v := Decide(ctx, client, call, wait); v.Effect != "" {
			out = cursorOutput{Permission: v.Effect, UserMessage: v.Reason, AgentMessage: v.Reason}
		}
	}
	return json.Marshal(out)
}
