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

// Kiro CLI: a PreToolUse command hook from .kiro/hooks/. It gets the tool
// call as tool_name and tool_input (at the top level, or under tool_tags in
// the V2 engine) and blocks by exiting 2 with the reason on stderr. There is
// no "allow" or "ask" answer, so anything not denied goes on to Kiro's own
// approval.

type kiroCallFields struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

type kiroInput struct {
	kiroCallFields
	Cwd      string          `json:"cwd"`
	ToolTags *kiroCallFields `json:"tool_tags"`
}

func kiroCall(in kiroInput) (Call, bool) {
	f := in.kiroCallFields
	if f.ToolName == "" && in.ToolTags != nil {
		f = *in.ToolTags
	}
	a := fields(f.ToolInput)
	c := Call{Tool: f.ToolName, Cwd: in.Cwd}
	switch f.ToolName {
	case "execute_bash", "shell", "execute_cmd":
		c.Action, c.Targets = core.ActionShell, []string{str(a, "command")}
	case "fs_write", "write":
		c.Action, c.Targets = core.ActionWrite, []string{str(a, "path", "file_path")}
	case "web_fetch":
		c.Action, c.Targets = core.ActionNetwork, []string{host(str(a, "url"))}
	case "fs_read", "read", "grep", "glob", "code", "introspect", "tool_search", "web_search", "use_subagent", "todo_list", "":
		return Call{}, false
	default:
		if strings.HasPrefix(f.ToolName, "@sheaf") || isSheafTool(f.ToolName) {
			return Call{}, false
		}
		c.Action, c.Targets = core.ActionTool, []string{f.ToolName}
	}
	return c, true
}

// Kiro answers a Kiro CLI PreToolUse hook. A deny comes back as *Blocked.
func Kiro(ctx context.Context, client *api.Client, r io.Reader, wait time.Duration) ([]byte, error) {
	var in kiroInput
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return nil, fmt.Errorf("reading hook input: %w", err)
	}
	call, ok := kiroCall(in)
	if !ok {
		return nil, nil
	}
	if v := Decide(ctx, client, call, wait); v.Effect == "deny" {
		return nil, &Blocked{Reason: v.Reason}
	}
	return nil, nil
}
