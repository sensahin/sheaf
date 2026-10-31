package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sensahin/sheaf/internal/api"
	"github.com/sensahin/sheaf/internal/core"
	"github.com/sensahin/sheaf/internal/hook"
	"github.com/sensahin/sheaf/internal/store"
)

// daemon starts a real daemon on a Unix socket and returns a client factory.
func daemon(t *testing.T) func(token string) *api.Client {
	t.Helper()
	// Unix socket paths are limited to ~100 bytes, so avoid t.TempDir's long names.
	dir, err := os.MkdirTemp("", "sheaf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	db, err := store.Open(filepath.Join(dir, "sheaf.db"))
	if err != nil {
		t.Fatal(err)
	}
	svc := core.New(db, filepath.Join(dir, "worktrees"))
	if err := svc.SetOwnerToken(context.Background(), "owner"); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "s.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		api.NewServer(svc).ListenUnix(ctx, sock)
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done; db.Close() })
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return func(token string) *api.Client { return api.NewClient(sock, token) }
}

func connect(t *testing.T, c *api.Client) *mcp.ClientSession {
	t.Helper()
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := New(c, "test").Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "agent"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any, out any) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if res.IsError {
		t.Fatalf("%s: %s", tool, text)
	}
	if out != nil {
		if err := json.Unmarshal([]byte(text), out); err != nil {
			t.Fatalf("%s: %v: %s", tool, err, text)
		}
	}
}

func TestTwoAgentsHandOffThroughMCP(t *testing.T) {
	ctx := context.Background()
	client := daemon(t)
	human := client("owner")

	var planner, builder api.AddAgentResp
	if err := human.Call(ctx, "agent.add", core.AgentSpec{Name: "planner"}, &planner); err != nil {
		t.Fatal(err)
	}
	if err := human.Call(ctx, "agent.add", core.AgentSpec{Name: "builder"}, &builder); err != nil {
		t.Fatal(err)
	}
	p := connect(t, client(planner.Token))
	b := connect(t, client(builder.Token))

	tools, err := p.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) < 10 {
		t.Fatalf("tools: %v %v", tools, err)
	}

	var who core.Caller
	call(t, b, "whoami", nil, &who)
	if who.Name != "builder" {
		t.Fatalf("whoami = %q", who.Name)
	}

	// The planner creates a task and hands it to the builder.
	var task core.Task
	call(t, p, "create_task", map[string]any{"title": "add rate limiting"}, &task)
	call(t, p, "handoff", map[string]any{"task": task.ID, "to": "builder", "note": "use a token bucket in middleware/limit.go"}, nil)

	var inbox []core.Message
	call(t, b, "inbox", map[string]any{"wait_seconds": 2}, &inbox)
	if len(inbox) != 1 || inbox[0].Kind != core.KindHandoff || !strings.Contains(inbox[0].Body, "token bucket") {
		t.Fatalf("builder inbox: %+v", inbox)
	}
	call(t, b, "report_status", map[string]any{"state": "working", "task": task.ID, "note": "writing middleware"}, nil)

	// The builder asks to push; the human approves from the CLI side.
	var ap core.Approval
	call(t, b, "request_approval", map[string]any{"summary": "push branch sheaf/" + task.ID, "task": task.ID}, &ap)
	go func() {
		time.Sleep(100 * time.Millisecond)
		human.Call(ctx, "approval.decide", core.DecideRequest{ID: ap.ID, Status: core.ApprovalApproved}, nil)
	}()
	call(t, b, "wait_for_approval", map[string]any{"id": ap.ID, "wait_seconds": 5}, &ap)
	if ap.Status != core.ApprovalApproved {
		t.Fatalf("approval: %+v", ap)
	}

	// The planner cannot decide approvals or read the audit log.
	if err := client(planner.Token).Call(ctx, "approval.decide", core.DecideRequest{ID: ap.ID, Status: core.ApprovalDenied}, nil); err == nil {
		t.Fatal("agent decided an approval")
	}
	if err := client(planner.Token).Call(ctx, "events.list", api.EventsReq{}, nil); err == nil {
		t.Fatal("agent read the event log")
	}
	if err := client("forged").Call(ctx, "whoami", nil, nil); err == nil {
		t.Fatal("forged token accepted")
	}
}

func TestClaudeCodeHook(t *testing.T) {
	ctx := context.Background()
	client := daemon(t)
	human := client("owner")
	var builder api.AddAgentResp
	if err := human.Call(ctx, "agent.add", core.AgentSpec{Name: "builder"}, &builder); err != nil {
		t.Fatal(err)
	}
	agent := client(builder.Token)

	run := func(tool, input string) map[string]string {
		t.Helper()
		payload := `{"tool_name":"` + tool + `","tool_input":` + input + `,"cwd":"/tmp"}`
		out, err := hook.ClaudeCode(ctx, agent, bytes.NewBufferString(payload), 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if len(out) == 0 {
			return nil
		}
		var o struct {
			H map[string]string `json:"hookSpecificOutput"`
		}
		json.Unmarshal(out, &o)
		return o.H
	}

	if got := run("Bash", `{"command":"go test ./..."}`); got != nil {
		t.Fatalf("allowed command produced output: %v", got)
	}
	if got := run("Read", `{"file_path":"/etc/passwd"}`); got != nil {
		t.Fatalf("read produced output: %v", got)
	}
	if got := run("Bash", `{"command":"git push --force origin main"}`); got["permissionDecision"] != "deny" {
		t.Fatalf("force push: %v", got)
	}

	// "ask" lands in the human's queue; approving it lets the call through.
	go func() {
		for i := 0; i < 50; i++ {
			var q []core.Approval
			human.Call(ctx, "approval.list", api.ApprovalsReq{}, &q)
			if len(q) > 0 {
				human.Call(ctx, "approval.decide", core.DecideRequest{ID: q[0].ID, Status: core.ApprovalApproved}, nil)
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	if got := run("Bash", `{"command":"git push origin feature"}`); got["permissionDecision"] != "allow" {
		t.Fatalf("approved push: %v", got)
	}

	// With nobody answering, it falls back to Claude Code's own prompt.
	if got := run("Bash", `{"command":"sudo reboot"}`); got["permissionDecision"] != "ask" {
		t.Fatalf("unanswered: %v", got)
	}
}

func TestCodexAndOpenCodeHooks(t *testing.T) {
	ctx := context.Background()
	client := daemon(t)
	human := client("owner")
	var builder api.AddAgentResp
	if err := human.Call(ctx, "agent.add", core.AgentSpec{Name: "builder", Kind: "codex"}, &builder); err != nil {
		t.Fatal(err)
	}
	pol := core.DefaultPolicy()
	pol.Rules = append(pol.Rules, core.Rule{Action: core.ActionWrite, Match: "/etc/*", Effect: core.Ask})
	if err := human.Call(ctx, "agent.set_policy", api.PolicyReq{Name: "builder", Policy: pol}, nil); err != nil {
		t.Fatal(err)
	}
	agent := client(builder.Token)

	codex := func(tool, input string, wait time.Duration) string {
		t.Helper()
		payload := `{"tool_name":"` + tool + `","tool_input":` + input + `,"cwd":"/tmp","hook_event_name":"PreToolUse"}`
		out, err := hook.Codex(ctx, agent, bytes.NewBufferString(payload), wait)
		if err != nil {
			t.Fatal(err)
		}
		if len(out) == 0 {
			return ""
		}
		var o struct {
			H map[string]string `json:"hookSpecificOutput"`
		}
		json.Unmarshal(out, &o)
		return o.H["permissionDecision"]
	}
	if got := codex("Bash", `{"command":"go test ./..."}`, time.Second); got != "" {
		t.Fatalf("allowed: %q", got)
	}
	if got := codex("Bash", `{"command":"git push --force origin main"}`, time.Second); got != "deny" {
		t.Fatalf("force push: %q", got)
	}
	// Codex cannot "ask": an unanswered call gets no opinion, not "ask".
	if got := codex("Bash", `{"command":"sudo reboot"}`, time.Second); got != "" {
		t.Fatalf("unanswered: %q", got)
	}
	// A patch touching a guarded file waits for the human, even when the
	// patch's other files are fine.
	go func() {
		for i := 0; i < 50; i++ {
			var q []core.Approval
			human.Call(ctx, "approval.list", api.ApprovalsReq{}, &q)
			for _, a := range q {
				if strings.Contains(a.Summary, "/etc/hosts") {
					human.Call(ctx, "approval.decide", core.DecideRequest{ID: a.ID, Status: core.ApprovalDenied, Answer: "no"}, nil)
					return
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	patch, _ := json.Marshal(map[string]string{"command": "*** Begin Patch\n*** Update File: README.md\n+x\n*** Update File: /etc/hosts\n+x\n*** End Patch"})
	if got := codex("apply_patch", string(patch), 5*time.Second); got != "deny" {
		t.Fatalf("patch: %q", got)
	}

	opencode := func(tool, args string) hook.OpenCodeOutput {
		t.Helper()
		out, err := hook.OpenCode(ctx, agent, bytes.NewBufferString(`{"tool":"`+tool+`","args":`+args+`,"cwd":"/tmp"}`), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		var o hook.OpenCodeOutput
		if err := json.Unmarshal(out, &o); err != nil {
			t.Fatal(err)
		}
		return o
	}
	if o := opencode("bash", `{"command":"go build"}`); o.Decision != "" {
		t.Fatalf("opencode allowed: %+v", o)
	}
	if o := opencode("bash", `{"command":"git push --force"}`); o.Decision != "deny" || o.Reason == "" {
		t.Fatalf("opencode force push: %+v", o)
	}
	if o := opencode("read", `{"filePath":"/etc/passwd"}`); o.Decision != "" {
		t.Fatalf("opencode read: %+v", o)
	}
}

// TestEveryAdapterAnswersInItsOwnFormat feeds each agent's payload for a
// denied command and checks the answer is that agent's deny.
func TestEveryAdapterAnswersInItsOwnFormat(t *testing.T) {
	ctx := context.Background()
	client := daemon(t)
	var a api.AddAgentResp
	if err := client("owner").Call(ctx, "agent.add", core.AgentSpec{Name: "a"}, &a); err != nil {
		t.Fatal(err)
	}
	agent := client(a.Token)
	cmd := "git push --force origin main"
	cases := map[string]struct{ in, deny, allow string }{
		"claude-code": {`{"tool_name":"Bash","tool_input":{"command":"CMD"},"cwd":"/tmp"}`, `"permissionDecision":"deny"`, ""},
		"codex":       {`{"tool_name":"Bash","tool_input":{"command":"CMD"},"cwd":"/tmp"}`, `"permissionDecision":"deny"`, ""},
		"copilot":     {`{"toolName":"bash","toolArgs":{"command":"CMD"},"cwd":"/tmp","timestamp":1}`, `"permissionDecision":"deny"`, ""},
		"cursor":      {`{"tool_name":"Shell","tool_input":{"command":"CMD"},"cwd":"/tmp","hook_event_name":"preToolUse"}`, `"permission":"deny"`, "{}"},
		"gemini":      {`{"tool_name":"run_shell_command","tool_input":{"command":"CMD"},"cwd":"/tmp","hook_event_name":"BeforeTool"}`, `"decision":"deny"`, ""},
		"droid":       {`{"tool_name":"Execute","tool_input":{"command":"CMD"},"cwd":"/tmp"}`, `"permissionDecision":"deny"`, ""},
		"antigravity": {`{"toolCall":{"name":"run_command","args":{"CommandLine":"CMD"}},"workspacePaths":["/tmp"]}`, `"decision":"deny"`, ""},
		"cline":       {`{"clineVersion":"3.40","hookName":"PreToolUse","workspaceRoots":["/tmp"],"preToolUse":{"toolName":"execute_command","parameters":{"command":"CMD"}}}`, `"cancel":true`, `{"cancel":false}`},
		"goose":       {`{"event":"PreToolUse","tool_name":"developer__shell","tool_input":{"command":"CMD"},"working_dir":"/tmp"}`, `"decision":"block"`, ""},
		"amp":         {`{"tool":"Bash","input":{"cmd":"CMD"},"cwd":"/tmp"}`, `"decision":"deny"`, `{"decision":""}`},
		"opencode":    {`{"tool":"bash","args":{"command":"CMD"},"cwd":"/tmp"}`, `"decision":"deny"`, `{"decision":""}`},
	}
	// Windsurf blocks by exit status, so its deny is an error.
	ws := `{"agent_action_name":"pre_run_command","tool_info":{"command_line":"CMD","cwd":"/tmp"}}`
	if _, err := hook.Windsurf(ctx, agent, strings.NewReader(strings.ReplaceAll(ws, "CMD", cmd)), time.Second); err == nil {
		t.Error("windsurf: force push not blocked")
	}
	if out, err := hook.Windsurf(ctx, agent, strings.NewReader(strings.ReplaceAll(ws, "CMD", "go test")), time.Second); err != nil || len(out) != 0 {
		t.Errorf("windsurf allow: %q %v", out, err)
	}
	for kind, adapter := range hook.Adapters {
		if kind == "windsurf" {
			continue
		}
		tc, ok := cases[kind]
		if !ok {
			t.Errorf("no test case for adapter %s", kind)
			continue
		}
		out, err := adapter(ctx, agent, strings.NewReader(strings.ReplaceAll(tc.in, "CMD", cmd)), time.Second)
		if err != nil || !strings.Contains(string(out), tc.deny) {
			t.Errorf("%s deny: %s %v", kind, out, err)
		}
		out, err = adapter(ctx, agent, strings.NewReader(strings.ReplaceAll(tc.in, "CMD", "go test ./...")), time.Second)
		if err != nil || string(out) != tc.allow {
			t.Errorf("%s allow: got %q want %q (%v)", kind, out, tc.allow, err)
		}
	}
}
