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

// OpenCodeInput is what Sheaf's OpenCode plugin sends from its
// tool.execute.before hook: the tool's name and arguments, and the
// project directory.
type OpenCodeInput struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
	Cwd  string          `json:"cwd"`
}

// OpenCodeOutput is the plugin's answer. On "deny" the plugin throws, which
// stops the tool call and shows the reason to the model.
type OpenCodeOutput struct {
	Decision string `json:"decision"` // allow, deny, or "" for no opinion
	Reason   string `json:"reason,omitempty"`
}

func openCodeCall(in OpenCodeInput) (Call, bool) {
	a := fields(in.Args)
	c := Call{Tool: in.Tool, Cwd: in.Cwd}
	switch in.Tool {
	case "bash":
		c.Action, c.Targets = core.ActionShell, []string{str(a, "command")}
	case "edit", "write":
		c.Action, c.Targets = core.ActionWrite, []string{str(a, "filePath", "file_path", "path")}
	case "patch", "apply_patch":
		c.Action = core.ActionWrite
		patch := str(a, "patchText", "patch", "input")
		if c.Targets = PatchFiles(patch); len(c.Targets) == 0 {
			c.Targets = []string{oneLine(patch, 200)}
		}
	case "webfetch":
		c.Action, c.Targets = core.ActionNetwork, []string{host(str(a, "url"))}
	case "read", "glob", "grep", "list", "todoread", "todowrite", "lsp_diagnostics", "lsp_hover":
		return Call{}, false
	default:
		// OpenCode names MCP tools server_tool.
		if isSheafTool(in.Tool) {
			return Call{}, false
		}
		c.Action, c.Targets = core.ActionTool, []string{in.Tool}
	}
	return c, true
}

// OpenCode reads the plugin's payload from r and returns its answer as JSON.
// A call nobody decided in time gets no opinion, so OpenCode's own
// permission settings apply.
func OpenCode(ctx context.Context, client *api.Client, r io.Reader, wait time.Duration) ([]byte, error) {
	var in OpenCodeInput
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return nil, fmt.Errorf("reading hook input: %w", err)
	}
	var out OpenCodeOutput
	if call, ok := openCodeCall(in); ok {
		v := Decide(ctx, client, call, wait)
		out = OpenCodeOutput{Decision: v.Effect, Reason: v.Reason}
	}
	return json.Marshal(out)
}
