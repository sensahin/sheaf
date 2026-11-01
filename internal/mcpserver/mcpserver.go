// Package mcpserver gives agents Sheaf's tools over MCP.
//
// `sheaf mcp` runs this server on stdio. Each agent starts its own copy with
// its token in SHEAF_TOKEN, and every tool call is forwarded to the daemon as
// that agent, so the daemon, not the agent, decides what is allowed.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sensahin/sheaf/internal/api"
	"github.com/sensahin/sheaf/internal/core"
)

// Instructions are sent to the agent when it connects.
const Instructions = `You are one of several agents coordinated by Sheaf.
- Check your inbox at the start of a task and whenever you finish a step.
- Use report_status when you start, get blocked, or finish.
- Use handoff to pass a task to another agent (for example, for review); write a note they can act on without asking you. Sheaf wakes or starts them.
- Use request_approval before anything irreversible or outside your task, and for diffs that are ready for review.
- Message "human" for questions only the person can answer.`

// New returns an MCP server whose tools call the daemon through client.
func New(client *api.Client, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "sheaf", Version: version}, &mcp.ServerOptions{Instructions: Instructions})

	add(s, client, "whoami", "Return your agent name.", "whoami",
		func(struct{}) any { return nil })

	add(s, client, "list_agents", "List the agents in this sheaf with their state, so you know whom to message or hand off to.", "agent.list",
		func(struct{}) any { return nil })

	add(s, client, "send", "Send a message to another agent's inbox, or to \"human\". Use kind \"question\" when you expect a reply.", "message.send",
		func(r core.SendRequest) any { return r })

	add(s, client, "inbox", "Read your unread messages (handoffs, replies, decisions on your approval requests) and mark them read. Set wait_seconds to block until one arrives.", "message.inbox",
		func(r core.InboxRequest) any { return r })

	add(s, client, "message_status", "Check whether a message you sent has been read.", "message.status",
		func(r api.IDReq) any { return r })

	add(s, client, "report_status", "Tell Sheaf what you are doing: state is working, blocked, idle, done or error. With task set, the task moves too.", "agent.report_status",
		func(r core.StatusReport) any { return r })

	add(s, client, "create_task", "Create a task, owned by you unless you name another owner. Set repo and worktree=true to give it its own git worktree.", "task.create",
		func(r core.TaskSpec) any { return r })

	add(s, client, "list_tasks", "List tasks. Set owner to filter, active=true to hide finished ones.", "task.list",
		func(r core.TaskFilter) any { return r })

	add(s, client, "handoff", "Give a task you own to another agent. The note goes to their inbox: say what is done, what is left and where to look.", "task.handoff",
		func(r core.HandoffRequest) any { return r })

	add(s, client, "request_approval", "Ask the human to approve an action, review a task's diff (kind \"diff\"; the diff is filled in from the worktree), or answer a question. Returns an id; then call wait_for_approval.", "approval.request",
		func(r core.ApprovalRequest) any { return r })

	add(s, client, "wait_for_approval", "Wait for the human to decide an approval request, up to wait_seconds. Returns its status: pending, approved, denied or answered.", "approval.wait",
		func(r api.WaitReq) any { return r })

	add(s, client, "check_permission", "Ask whether your policy allows an action (shell, write, network, secret, tool) on a target. Returns allow, ask or deny.", "policy.check",
		func(r core.CheckRequest) any { return r })

	return s
}

// add registers a tool that forwards its typed input to an API method.
func add[In any](s *mcp.Server, client *api.Client, name, desc, method string, body func(In) any) {
	mcp.AddTool(s, &mcp.Tool{Name: name, Description: desc},
		func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
			var out json.RawMessage
			if err := client.Call(ctx, method, body(in), &out); err != nil {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil, nil
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(out)}}}, nil, nil
		})
}

// Run serves the MCP server on stdio until the client disconnects.
func Run(ctx context.Context, client *api.Client, version string) error {
	var who core.Caller
	if err := client.Call(ctx, "whoami", nil, &who); err != nil {
		return err
	}
	// An agent holding the owner's token could approve its own requests.
	if who.IsHuman() {
		return fmt.Errorf("sheaf mcp needs an agent's token in SHEAF_TOKEN, not yours; start the agent with `sheaf start`")
	}
	return New(client, version).Run(ctx, &mcp.StdioTransport{})
}
