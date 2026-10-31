package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sensahin/sheaf/internal/api"
	"github.com/sensahin/sheaf/internal/core"
)

// StatusStates are the states `sheaf hook status STATE` reports.
var StatusStates = []string{core.StateWorking, core.StateBlocked, core.StateIdle, core.StateDone, core.StateError}

// Status reports state for the calling agent from one of its own lifecycle
// hooks (a prompt submitted, a notification, the end of a turn). The agent's
// event payload on r is optional; a message in it becomes the status note,
// for example Claude Code's "Claude needs your permission to use Bash".
//
// Status hooks must never get in the agent's way, so this is best effort:
// outside Sheaf, or with the daemon down, it does nothing.
func Status(ctx context.Context, client *api.Client, state string, r io.Reader) error {
	valid := false
	for _, s := range StatusStates {
		valid = valid || s == state
	}
	if !valid {
		return fmt.Errorf("unknown state %q (want %s)", state, strings.Join(StatusStates, ", "))
	}
	var who core.Caller
	if err := client.Call(ctx, "whoami", nil, &who); err != nil || who.IsHuman() {
		return nil
	}
	rep := core.StatusReport{State: state, Note: eventMessage(r), Task: os.Getenv("SHEAF_TASK")}
	client.Call(ctx, "agent.report_status", rep, nil)
	return nil
}

// eventMessage pulls a human message out of an agent's event payload.
func eventMessage(r io.Reader) string {
	if r == nil {
		return ""
	}
	var m map[string]any
	if err := json.NewDecoder(io.LimitReader(r, 1<<20)).Decode(&m); err != nil {
		return ""
	}
	for _, k := range []string{"message", "notification", "body", "title"} {
		if s, ok := m[k].(string); ok && s != "" {
			return oneLine(s, 200)
		}
	}
	return ""
}
