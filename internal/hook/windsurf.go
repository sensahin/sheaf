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

// Blocked is returned by adapters for agents that block a call by exit
// status rather than by output: `sheaf hook` prints Reason to stderr and
// exits with status 2.
type Blocked struct{ Reason string }

func (b *Blocked) Error() string { return b.Reason }

// Windsurf (Cascade, now also Devin Local): pre_run_command, pre_write_code
// and pre_mcp_tool_use hooks get {"agent_action_name", "tool_info": {...}}
// and block only by exiting 2. There is no "allow" or "ask": an approved or
// unanswered call simply proceeds to Windsurf's own approval.

type windsurfInput struct {
	Action   string `json:"agent_action_name"`
	ToolInfo struct {
		CommandLine string          `json:"command_line"`
		Cwd         string          `json:"cwd"`
		FilePath    string          `json:"file_path"`
		Server      string          `json:"mcp_server_name"`
		Tool        string          `json:"mcp_tool_name"`
		Arguments   json.RawMessage `json:"mcp_tool_arguments"`
	} `json:"tool_info"`
}

func windsurfCall(in windsurfInput) (Call, bool) {
	t := in.ToolInfo
	c := Call{Tool: in.Action, Cwd: t.Cwd}
	switch in.Action {
	case "pre_run_command":
		c.Action, c.Targets = core.ActionShell, []string{t.CommandLine}
	case "pre_write_code":
		c.Action, c.Targets = core.ActionWrite, []string{t.FilePath}
	case "pre_mcp_tool_use":
		if strings.EqualFold(t.Server, "sheaf") {
			return Call{}, false
		}
		c.Action, c.Targets = core.ActionTool, []string{"mcp__" + t.Server + "__" + t.Tool}
	default:
		return Call{}, false
	}
	return c, true
}

// Windsurf answers a Windsurf pre-hook. A deny comes back as *Blocked.
func Windsurf(ctx context.Context, client *api.Client, r io.Reader, wait time.Duration) ([]byte, error) {
	var in windsurfInput
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return nil, fmt.Errorf("reading hook input: %w", err)
	}
	call, ok := windsurfCall(in)
	if !ok {
		return nil, nil
	}
	if v := Decide(ctx, client, call, wait); v.Effect == "deny" {
		return nil, &Blocked{Reason: v.Reason}
	}
	return nil, nil
}
