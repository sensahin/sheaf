// Package hook connects agents' own permission hooks to Sheaf's broker.
//
// Each supported agent has a pre-tool hook that runs `sheaf hook KIND`. The
// hook maps the tool call to a Sheaf action, checks the agent's policy, and
// for "ask" puts the call in the human's queue and waits for a decision, so
// you can approve from wherever you read the queue instead of the agent's
// terminal.
package hook

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sensahin/sheaf/internal/api"
	"github.com/sensahin/sheaf/internal/core"
)

// Call is one tool call, already mapped to the Sheaf actions it performs.
// A call can perform several, such as a patch that edits three files.
type Call struct {
	Tool    string   // the agent's own tool name, for the queue summary
	Action  string   // core.ActionShell, ActionWrite, ...
	Targets []string // what the action touches; one check per target
	Cwd     string
}

// Verdict is what Sheaf decided about a call.
type Verdict struct {
	// Effect is allow or deny, or "" when Sheaf has no opinion and the
	// agent's own permission settings should apply.
	Effect string
	Reason string
	// Unresolved is set when the call needed a human and none answered in
	// time (or Sheaf could not be reached). Effect is then "".
	Unresolved bool
}

// Decide checks a call against the calling agent's policy and, when the
// policy asks, waits up to wait for the human's decision in the queue.
func Decide(ctx context.Context, client *api.Client, call Call, wait time.Duration) Verdict {
	var who core.Caller
	if err := client.Call(ctx, "whoami", nil, &who); err != nil {
		return Verdict{Unresolved: true, Reason: "Sheaf could not check this call: " + err.Error()}
	}
	if who.IsHuman() {
		// Not running as a Sheaf agent (no SHEAF_TOKEN): no opinion.
		return Verdict{}
	}
	targets := call.Targets
	if len(targets) == 0 {
		targets = []string{""}
	}
	// Strictest wins across targets: any deny denies, any ask asks.
	effect, reason := core.Allow, ""
	for _, t := range targets {
		var d core.Decision
		if err := client.Call(ctx, "policy.check", core.CheckRequest{Action: call.Action, Target: t, Cwd: call.Cwd}, &d); err != nil {
			return Verdict{Unresolved: true, Reason: "Sheaf could not check this call: " + err.Error()}
		}
		if d.Effect == core.Deny {
			return Verdict{Effect: "deny", Reason: "Sheaf policy denies this (" + d.Reason + ")"}
		}
		if d.Effect == core.Ask && effect == core.Allow {
			effect, reason = core.Ask, d.Reason
		}
	}
	if effect == core.Allow {
		return Verdict{}
	}

	detail := strings.Join(targets, "\n")
	summary := fmt.Sprintf("%s: %s", call.Tool, oneLine(detail, 120))
	var a core.Approval
	if err := client.Call(ctx, "approval.request", core.ApprovalRequest{Kind: core.ApprovalAction, Summary: summary, Detail: detail}, &a); err != nil {
		return Verdict{Unresolved: true, Reason: "Sheaf could not queue this call: " + err.Error()}
	}
	if err := client.Call(ctx, "approval.wait", api.WaitReq{ID: a.ID, WaitSeconds: int(wait / time.Second)}, &a); err != nil {
		return Verdict{Unresolved: true, Reason: "Sheaf lost track of " + a.ID + ": " + err.Error()}
	}
	switch a.Status {
	case core.ApprovalApproved:
		return Verdict{Effect: "allow", Reason: "approved in Sheaf (" + a.ID + ")" + note(a.Answer)}
	case core.ApprovalDenied:
		return Verdict{Effect: "deny", Reason: "denied in Sheaf (" + a.ID + ")" + note(a.Answer)}
	}
	return Verdict{Unresolved: true, Reason: "no decision in Sheaf yet (" + a.ID + "); " + reason}
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

// Adapter answers one agent program's pre-tool hook: it reads the agent's
// payload from r and returns what to print, empty for no opinion.
type Adapter func(ctx context.Context, client *api.Client, r io.Reader, wait time.Duration) ([]byte, error)

// Adapters are the agent programs `sheaf hook KIND` serves, by kind.
var Adapters = map[string]Adapter{
	"antigravity": Antigravity,
	"claude-code": ClaudeCode,
	"cline":       Cline,
	"codex":       Codex,
	"copilot":     Copilot,
	"cursor":      Cursor,
	"droid":       Droid,
	"gemini":      Gemini,
	"opencode":    OpenCode,
	"windsurf":    Windsurf,
}
