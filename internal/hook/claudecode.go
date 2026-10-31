// Package hook connects agents' own permission hooks to Sheaf's broker.
//
// For Claude Code, `sheaf hook claude-code` is a PreToolUse hook. It maps each
// tool call to a Sheaf action, checks the agent's policy, and for "ask" puts
// the call in the human's queue and waits for a decision, so you can approve
// from wherever you read the queue instead of the agent's terminal.
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

// ClaudeCodeInput is the part of Claude Code's PreToolUse payload we read.
type ClaudeCodeInput struct {
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

// Action maps a Claude Code tool call to a Sheaf action and target. ok is
// false for calls Sheaf does not police, such as reads and Sheaf's own tools.
func Action(in ClaudeCodeInput) (action, target string, ok bool) {
	var ti map[string]any
	json.Unmarshal(in.ToolInput, &ti)
	str := func(k string) string { s, _ := ti[k].(string); return s }
	switch in.ToolName {
	case "Bash":
		return core.ActionShell, str("command"), true
	case "Write", "Edit", "MultiEdit":
		return core.ActionWrite, str("file_path"), true
	case "NotebookEdit":
		return core.ActionWrite, str("notebook_path"), true
	case "WebFetch":
		u, err := url.Parse(str("url"))
		if err != nil || u.Host == "" {
			return core.ActionNetwork, str("url"), true
		}
		return core.ActionNetwork, u.Hostname(), true
	case "Read", "Glob", "Grep", "LS", "TodoWrite", "TodoRead":
		return "", "", false
	}
	if strings.HasPrefix(in.ToolName, "mcp__sheaf__") {
		return "", "", false
	}
	return core.ActionTool, in.ToolName, true
}

// ClaudeCode reads a PreToolUse payload from r and returns the hook's stdout.
// Empty output means "no opinion": Claude Code's own permission settings
// apply. wait bounds how long an "ask" waits in the queue before falling back
// to Claude Code's own prompt.
func ClaudeCode(ctx context.Context, client *api.Client, r io.Reader, wait time.Duration) ([]byte, error) {
	var in ClaudeCodeInput
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return nil, fmt.Errorf("reading hook input: %w", err)
	}
	action, target, ok := Action(in)
	if !ok {
		return nil, nil
	}
	var who core.Caller
	if err := client.Call(ctx, "whoami", nil, &who); err != nil {
		return decision("ask", "Sheaf could not check this call: "+err.Error()), nil
	}
	if who.IsHuman() {
		// Not running as a Sheaf agent (no SHEAF_TOKEN): no opinion.
		return nil, nil
	}
	var d core.Decision
	if err := client.Call(ctx, "policy.check", core.CheckRequest{Action: action, Target: target, Cwd: in.Cwd}, &d); err != nil {
		return decision("ask", "Sheaf could not check this call: "+err.Error()), nil
	}
	switch d.Effect {
	case core.Allow:
		return nil, nil
	case core.Deny:
		return decision("deny", "Sheaf policy denies this ("+d.Reason+")"), nil
	}

	var a core.Approval
	summary := fmt.Sprintf("%s: %s", in.ToolName, oneLine(target, 120))
	if err := client.Call(ctx, "approval.request", core.ApprovalRequest{Kind: core.ApprovalAction, Summary: summary, Detail: target}, &a); err != nil {
		return decision("ask", "Sheaf could not queue this call: "+err.Error()), nil
	}
	if err := client.Call(ctx, "approval.wait", api.WaitReq{ID: a.ID, WaitSeconds: int(wait / time.Second)}, &a); err != nil {
		return decision("ask", "Sheaf lost track of "+a.ID+": "+err.Error()), nil
	}
	switch a.Status {
	case core.ApprovalApproved:
		return decision("allow", "approved in Sheaf ("+a.ID+")"+note(a.Answer)), nil
	case core.ApprovalDenied:
		return decision("deny", "denied in Sheaf ("+a.ID+")"+note(a.Answer)), nil
	}
	return decision("ask", "no decision in Sheaf yet ("+a.ID+"); "+d.Reason), nil
}

func decision(effect, reason string) []byte {
	var o hookOutput
	o.HookSpecificOutput.HookEventName = "PreToolUse"
	o.HookSpecificOutput.PermissionDecision = effect
	o.HookSpecificOutput.PermissionDecisionReason = reason
	b, _ := json.Marshal(o)
	return b
}

func note(s string) string {
	if s == "" {
		return ""
	}
	return ": " + s
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n-1] + "…"
	}
	return s
}
