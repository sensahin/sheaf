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

// Goose: a PreToolUse hook in a goose plugin's hooks/hooks.json. It gets
// {"tool_name", "tool_input", "working_dir"} and answers
// {"decision": "block", "reason"} or {"decision": "allow"}. There is no
// "ask", so an unanswered call gets no opinion.

type gooseInput struct {
	ToolName   string          `json:"tool_name"`
	ToolInput  json.RawMessage `json:"tool_input"`
	WorkingDir string          `json:"working_dir"`
}

type gooseOutput struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

func gooseCall(in gooseInput) (Call, bool) {
	a := fields(in.ToolInput)
	c := Call{Tool: in.ToolName, Cwd: in.WorkingDir}
	switch in.ToolName {
	case "developer__shell", "shell":
		c.Action, c.Targets = core.ActionShell, []string{str(a, "command")}
	case "developer__write", "developer__edit":
		c.Action, c.Targets = core.ActionWrite, []string{str(a, "path")}
	case "developer__text_editor":
		if cmd := str(a, "command"); cmd == "view" || cmd == "" {
			return Call{}, false
		}
		c.Action, c.Targets = core.ActionWrite, []string{str(a, "path")}
	case "developer__tree", "developer__read_image", "developer__list_windows", "developer__screen_capture",
		"todo__read", "todo__write":
		return Call{}, false
	default:
		// goose names extension tools <extension>__<tool>.
		if strings.HasPrefix(in.ToolName, "sheaf__") || isSheafTool(in.ToolName) {
			return Call{}, false
		}
		c.Action, c.Targets = core.ActionTool, []string{in.ToolName}
	}
	return c, true
}

// Goose answers a goose PreToolUse hook.
func Goose(ctx context.Context, client *api.Client, r io.Reader, wait time.Duration) ([]byte, error) {
	var in gooseInput
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return nil, fmt.Errorf("reading hook input: %w", err)
	}
	call, ok := gooseCall(in)
	if !ok {
		return nil, nil
	}
	switch v := Decide(ctx, client, call, wait); v.Effect {
	case "deny":
		return json.Marshal(gooseOutput{"block", v.Reason})
	case "allow":
		return json.Marshal(gooseOutput{"allow", v.Reason})
	}
	return nil, nil
}
