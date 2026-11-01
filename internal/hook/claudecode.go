package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/sensahin/sheaf/internal/api"
	"github.com/sensahin/sheaf/internal/core"
)

// PreToolUseInput is the part of a Claude Code or Codex PreToolUse payload
// Sheaf reads. The two agents share the format.
type PreToolUseInput struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	Cwd       string          `json:"cwd"`
}

type hookOutput struct {
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
}

// fields reads a tool_input object; a non-object reads as empty.
func fields(raw json.RawMessage) map[string]any {
	var m map[string]any
	json.Unmarshal(raw, &m)
	return m
}

func str(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if v != "" {
				return v
			}
		case []any: // argv form, as some agents send commands
			parts := make([]string, 0, len(v))
			for _, p := range v {
				if s, ok := p.(string); ok {
					parts = append(parts, s)
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, " ")
			}
		}
	}
	return ""
}

func host(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Hostname()
}

func claudeCall(in PreToolUseInput) (Call, bool) {
	ti := fields(in.ToolInput)
	c := Call{Tool: in.ToolName, Cwd: in.Cwd}
	switch in.ToolName {
	case "Bash":
		c.Action, c.Targets = core.ActionShell, []string{str(ti, "command")}
	case "Write", "Edit", "MultiEdit":
		c.Action, c.Targets = core.ActionWrite, []string{str(ti, "file_path")}
	case "NotebookEdit":
		c.Action, c.Targets = core.ActionWrite, []string{str(ti, "notebook_path")}
	case "WebFetch":
		c.Action, c.Targets = core.ActionNetwork, []string{host(str(ti, "url"))}
	case "Read", "Glob", "Grep", "LS", "TodoWrite", "TodoRead":
		return Call{}, false
	default:
		if isSheafTool(in.ToolName) {
			return Call{}, false
		}
		c.Action, c.Targets = core.ActionTool, []string{in.ToolName}
	}
	return c, true
}

// ClaudeCode reads a PreToolUse payload from r and returns the hook's stdout.
// Empty output means "no opinion": Claude Code's own permission settings
// apply. wait bounds how long an "ask" waits in the queue before falling back
// to Claude Code's own prompt.
func ClaudeCode(ctx context.Context, client *api.Client, r io.Reader, wait time.Duration) ([]byte, error) {
	var in PreToolUseInput
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return nil, fmt.Errorf("reading hook input: %w", err)
	}
	if isSheafTool(in.ToolName) {
		// Sheaf's own tools are checked by the daemon. Allowing them here
		// means an agent nobody is watching never stops on a prompt to use
		// its inbox.
		return preToolUse("allow", "Sheaf's own tool; the daemon checks it"), nil
	}
	call, ok := claudeCall(in)
	if !ok {
		return nil, nil
	}
	v := Decide(ctx, client, call, wait)
	switch {
	case v.Unresolved:
		// Claude Code supports "ask": hand the call back to its own prompt.
		return preToolUse("ask", v.Reason), nil
	case v.Effect != "":
		return preToolUse(v.Effect, v.Reason), nil
	}
	return nil, nil
}

func preToolUse(effect, reason string) []byte {
	var o hookOutput
	o.HookSpecificOutput.HookEventName = "PreToolUse"
	o.HookSpecificOutput.PermissionDecision = effect
	o.HookSpecificOutput.PermissionDecisionReason = reason
	b, _ := json.Marshal(o)
	return b
}
