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

// Amp: Sheaf's Amp plugin sends each tool.call event's tool and input here
// and gets {"decision": allow|deny|ask, "reason"} back. On "ask" (nobody
// answered in Sheaf in time) the plugin asks in Amp with ctx.ui.confirm.

type ampInput struct {
	Tool  string          `json:"tool"`
	Input json.RawMessage `json:"input"`
	Cwd   string          `json:"cwd"`
}

// AmpOutput is the plugin's answer; "" means no opinion.
type AmpOutput struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

func ampCall(in ampInput) (Call, bool) {
	a := fields(in.Input)
	c := Call{Tool: in.Tool, Cwd: in.Cwd}
	if d := str(a, "cwd"); d != "" {
		c.Cwd = d
	}
	switch in.Tool {
	case "Bash", "bash":
		c.Action, c.Targets = core.ActionShell, []string{str(a, "cmd", "command")}
	case "create_file", "edit_file", "undo_edit", "format_file", "delete_file":
		c.Action, c.Targets = core.ActionWrite, []string{str(a, "path", "file_path")}
	case "apply_patch":
		c.Action = core.ActionWrite
		patch := str(a, "patch", "input", "patchText")
		if c.Targets = PatchFiles(patch); len(c.Targets) == 0 {
			c.Targets = []string{oneLine(patch, 200)}
		}
	case "read_web_page":
		c.Action, c.Targets = core.ActionNetwork, []string{host(str(a, "url"))}
	case "Read", "read", "Grep", "grep", "glob", "list_directory", "codebase_search_agent", "web_search",
		"todo_read", "todo_write", "mermaid", "get_diagnostics", "oracle", "Task", "finder", "librarian":
		return Call{}, false
	default:
		if isSheafTool(in.Tool) {
			return Call{}, false
		}
		c.Action, c.Targets = core.ActionTool, []string{in.Tool}
	}
	return c, true
}

// Amp answers Sheaf's Amp plugin.
func Amp(ctx context.Context, client *api.Client, r io.Reader, wait time.Duration) ([]byte, error) {
	var in ampInput
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return nil, fmt.Errorf("reading hook input: %w", err)
	}
	var out AmpOutput
	if call, ok := ampCall(in); ok {
		v := Decide(ctx, client, call, wait)
		out = AmpOutput{Decision: v.Effect, Reason: v.Reason}
		if v.Unresolved {
			out.Decision = "ask"
		}
	}
	return json.Marshal(out)
}
