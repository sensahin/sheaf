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

// Factory Droid: Claude-Code-shaped PreToolUse hooks with Droid's own tool
// names (Execute, Create, Edit, ApplyPatch, FetchUrl). "ask" is supported,
// so an unanswered call goes back to Droid's own prompt.

func droidCall(in PreToolUseInput) (Call, bool) {
	a := fields(in.ToolInput)
	c := Call{Tool: in.ToolName, Cwd: in.Cwd}
	switch in.ToolName {
	case "Execute":
		c.Action, c.Targets = core.ActionShell, []string{str(a, "command")}
	case "Create", "Edit", "MultiEdit":
		c.Action, c.Targets = core.ActionWrite, []string{str(a, "file_path", "path")}
	case "ApplyPatch":
		c.Action = core.ActionWrite
		patch := str(a, "patch", "input", "command")
		if c.Targets = PatchFiles(patch); len(c.Targets) == 0 {
			c.Targets = []string{oneLine(patch, 200)}
		}
	case "FetchUrl":
		c.Action, c.Targets = core.ActionNetwork, []string{host(str(a, "url"))}
	case "Read", "LS", "Glob", "Grep", "Task", "WebSearch", "TodoWrite":
		return Call{}, false
	default:
		if isSheafTool(in.ToolName) {
			return Call{}, false
		}
		c.Action, c.Targets = core.ActionTool, []string{in.ToolName}
	}
	return c, true
}

// Droid answers a Factory Droid PreToolUse hook.
func Droid(ctx context.Context, client *api.Client, r io.Reader, wait time.Duration) ([]byte, error) {
	var in PreToolUseInput
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return nil, fmt.Errorf("reading hook input: %w", err)
	}
	call, ok := droidCall(in)
	if !ok {
		return nil, nil
	}
	v := Decide(ctx, client, call, wait)
	switch {
	case v.Unresolved:
		return preToolUse("ask", v.Reason), nil
	case v.Effect != "":
		return preToolUse(v.Effect, v.Reason), nil
	}
	return nil, nil
}
