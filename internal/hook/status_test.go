package hook

import (
	"strings"
	"testing"
)

func TestNotificationStatusDistinguishesIdleFromPermission(t *testing.T) {
	for _, tc := range []struct{ payload, state, note string }{
		{`{"notification_type":"idle_prompt","message":"Claude is waiting for your input"}`, "idle", ""},
		{`{"notification_type":"permission_prompt","message":"Approve Bash"}`, "blocked", "Approve Bash"},
		{`{"notification_type":"elicitation_dialog","message":"Answer the question"}`, "blocked", "Answer the question"},
		{`{"notification_type":"auth_success"}`, "", ""},
		{`{"notification_type":"plugin_notification"}`, "", ""},
		{`{"message":"Legacy notification"}`, "blocked", "Legacy notification"},
		{`not JSON`, "blocked", ""},
	} {
		t.Run(tc.payload, func(t *testing.T) {
			state, note := statusEvent("blocked", strings.NewReader(tc.payload))
			if state != tc.state || note != tc.note {
				t.Fatalf("got %q %q", state, note)
			}
		})
	}
}
