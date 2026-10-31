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

// Codex's PreToolUse hook takes the same payload and output as Claude Code's,
// with different tool names: shell commands arrive as Bash, file edits as
// apply_patch carrying a whole patch, MCP tools as mcp__server__tool. Codex
// does not support an "ask" answer, so a call nobody decided in time gets no
// opinion and Codex's own approval policy applies.

func codexCall(in PreToolUseInput) (Call, bool) {
	ti := fields(in.ToolInput)
	c := Call{Tool: in.ToolName, Cwd: in.Cwd}
	switch in.ToolName {
	case "Bash", "shell", "exec_command", "local_shell":
		c.Tool = "Bash"
		c.Action, c.Targets = core.ActionShell, []string{str(ti, "command", "cmd")}
	case "apply_patch", "Edit", "Write":
		c.Tool = "apply_patch"
		patch := str(ti, "command", "patch", "input")
		c.Action, c.Targets = core.ActionWrite, PatchFiles(patch)
		if len(c.Targets) == 0 {
			// Not a patch we can read: ask about it as a whole.
			c.Targets = []string{oneLine(patch, 200)}
		}
	case "read_file", "list_dir", "grep_files", "view_image", "update_plan":
		return Call{}, false
	default:
		if isSheafTool(in.ToolName) {
			return Call{}, false
		}
		c.Action, c.Targets = core.ActionTool, []string{in.ToolName}
	}
	return c, true
}

// PatchFiles lists the files a Codex apply_patch patch adds, updates,
// deletes or moves to.
func PatchFiles(patch string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(patch, "\n") {
		line = strings.TrimSpace(line)
		for _, p := range []string{"*** Add File: ", "*** Update File: ", "*** Delete File: ", "*** Move to: "} {
			if f, ok := strings.CutPrefix(line, p); ok {
				f = strings.TrimSpace(f)
				if f != "" && !seen[f] {
					seen[f] = true
					out = append(out, f)
				}
			}
		}
	}
	return out
}

// Codex reads a Codex PreToolUse payload from r and returns the hook's
// stdout. Empty output means "no opinion".
func Codex(ctx context.Context, client *api.Client, r io.Reader, wait time.Duration) ([]byte, error) {
	var in PreToolUseInput
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return nil, fmt.Errorf("reading hook input: %w", err)
	}
	call, ok := codexCall(in)
	if !ok {
		return nil, nil
	}
	v := Decide(ctx, client, call, wait)
	if v.Effect == "" {
		return nil, nil
	}
	return preToolUse(v.Effect, v.Reason), nil
}
