package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/sensahin/sheaf/internal/api"
	"github.com/sensahin/sheaf/internal/core"
)

// Google Antigravity (agy): a PreToolUse hook reading
// {"toolCall": {"name", "args"}, "workspacePaths": [...]} and answering a
// flat {"decision": allow|force_ask|deny, "reason"}. An unanswered call is
// answered force_ask, so agy shows its own prompt. Some agy versions ignore
// "allow" and prompt anyway; deny always holds.

type antigravityInput struct {
	ToolCall struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"toolCall"`
	WorkspacePaths []string `json:"workspacePaths"`
}

type antigravityOutput struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

func antigravityCall(in antigravityInput) (Call, bool) {
	a := fields(in.ToolCall.Args)
	c := Call{Tool: in.ToolCall.Name}
	if len(in.WorkspacePaths) > 0 {
		c.Cwd = in.WorkspacePaths[0]
	}
	switch in.ToolCall.Name {
	case "run_command":
		c.Action, c.Targets = core.ActionShell, []string{str(a, "CommandLine", "command")}
	case "write_to_file", "replace_file_content", "multi_replace_file_content", "edit_file":
		c.Action, c.Targets = core.ActionWrite, []string{str(a, "TargetFile", "AbsolutePath", "file_path")}
	case "read_url_content", "fetch_url":
		c.Action, c.Targets = core.ActionNetwork, []string{host(str(a, "Url", "url"))}
	case "view_file", "view_file_outline", "view_code_item", "list_dir", "grep_search", "find_by_name",
		"codebase_search", "search_web", "command_status", "read_terminal":
		return Call{}, false
	default:
		if isSheafTool(in.ToolCall.Name) {
			return Call{}, false
		}
		c.Action, c.Targets = core.ActionTool, []string{in.ToolCall.Name}
	}
	return c, true
}

// Antigravity answers an Antigravity PreToolUse hook. Output is empty for no
// opinion, which agy accepts.
func Antigravity(ctx context.Context, client *api.Client, r io.Reader, wait time.Duration) ([]byte, error) {
	var in antigravityInput
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return nil, fmt.Errorf("reading hook input: %w", err)
	}
	call, ok := antigravityCall(in)
	if !ok {
		return nil, nil
	}
	v := Decide(ctx, client, call, wait)
	switch {
	case v.Unresolved:
		return json.Marshal(antigravityOutput{"force_ask", v.Reason})
	case v.Effect != "":
		return json.Marshal(antigravityOutput{v.Effect, v.Reason})
	}
	return nil, nil
}
