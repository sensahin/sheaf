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

// Cline: an executable named PreToolUse in .clinerules/hooks/. It gets the
// task context and {"preToolUse": {"toolName", "parameters"}} on stdin and
// answers {"cancel": bool, "errorMessage"}. Cline has no "ask" answer: an
// unanswered call is not cancelled, and Cline's own approval applies.

type clineInput struct {
	WorkspaceRoots []string `json:"workspaceRoots"`
	PreToolUse     *struct {
		ToolName   string          `json:"toolName"`
		Parameters json.RawMessage `json:"parameters"`
	} `json:"preToolUse"`
	// Some versions put the call at the top level.
	ToolName   string          `json:"toolName"`
	Parameters json.RawMessage `json:"parameters"`
}

type clineOutput struct {
	Cancel       bool   `json:"cancel"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

func clineCall(in clineInput) (Call, bool) {
	name, params := in.ToolName, in.Parameters
	if in.PreToolUse != nil {
		name, params = in.PreToolUse.ToolName, in.PreToolUse.Parameters
	}
	a := argsObject(params)
	c := Call{Tool: name}
	if len(in.WorkspaceRoots) > 0 {
		c.Cwd = in.WorkspaceRoots[0]
	}
	switch name {
	case "execute_command":
		c.Action, c.Targets = core.ActionShell, []string{str(a, "command")}
	case "write_to_file", "replace_in_file", "new_file", "apply_patch":
		c.Action, c.Targets = core.ActionWrite, []string{str(a, "path", "absolutePath", "file_path")}
	case "web_fetch":
		c.Action, c.Targets = core.ActionNetwork, []string{host(str(a, "url"))}
	case "use_mcp_tool":
		server, tool := str(a, "server_name"), str(a, "tool_name")
		if server == "sheaf" {
			return Call{}, false
		}
		c.Action, c.Targets = core.ActionTool, []string{"mcp__" + server + "__" + tool}
	case "read_file", "list_files", "search_files", "list_code_definition_names", "ask_followup_question",
		"attempt_completion", "plan_mode_respond", "focus_chain", "access_mcp_resource", "load_mcp_documentation":
		return Call{}, false
	default:
		if name == "" || isSheafTool(name) {
			return Call{}, false
		}
		c.Action, c.Targets = core.ActionTool, []string{name}
	}
	return c, true
}

// Cline answers a Cline PreToolUse hook. It always prints a JSON answer.
func Cline(ctx context.Context, client *api.Client, r io.Reader, wait time.Duration) ([]byte, error) {
	var in clineInput
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return nil, fmt.Errorf("reading hook input: %w", err)
	}
	var out clineOutput
	if call, ok := clineCall(in); ok {
		if v := Decide(ctx, client, call, wait); v.Effect == "deny" {
			out = clineOutput{Cancel: true, ErrorMessage: v.Reason}
		}
	}
	return json.Marshal(out)
}
