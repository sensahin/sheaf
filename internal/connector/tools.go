package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sensahin/sheaf/internal/core"
)

// Instructions tell the chat app's model how to use Sheaf.
const Instructions = `Sheaf coordinates the user's coding agents (for example Claude Code and Codex) running on their server.
- Start with overview to see agents, active tasks and what is waiting for the user.
- create_task gives an agent work and starts it. Write a clear title and put the full brief in detail; the agent reads it as its prompt.
- Agents ask for approvals and answers through the queue: list_approvals, then decide with the user's decision. Never approve or deny without the user saying so.
- Agents' messages to the user are in read_messages. To answer one, message_agent.
- get_task shows a task's story (handoff notes, questions, reports); agent_screen shows an agent's terminal when it seems stuck.`

func (s *Server) mcpHandler() http.Handler {
	h := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		c, _ := r.Context().Value(callerKey{}).(core.Caller)
		return s.newMCP(c)
	}, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		// Requests arrive through a proxy (Tailscale Funnel) on localhost
		// with the public host name; the bearer token is the protection.
		DisableLocalhostProtection: true,
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := s.authenticate(r)
		if err != nil {
			s.unauthorized(w, err.Error())
			return
		}
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerKey{}, c)))
	})
}

type (
	noArgs        struct{}
	listTasksArgs struct {
		All   bool   `json:"all,omitempty" jsonschema:"include finished and cancelled tasks"`
		Owner string `json:"owner,omitempty" jsonschema:"only tasks owned by this agent"`
	}
	idArgs struct {
		ID string `json:"id" jsonschema:"the id, for example t-abcd1234"`
	}
	createTaskArgs struct {
		Owner   string `json:"owner" jsonschema:"the agent that does the task (see overview for names)"`
		Title   string `json:"title" jsonschema:"a short title"`
		Detail  string `json:"detail,omitempty" jsonschema:"the full brief: what to do, acceptance criteria, links"`
		Repo    string `json:"repo,omitempty" jsonschema:"repository name or path from list_repos; may be left out when only one is registered"`
		NoStart bool   `json:"no_start,omitempty" jsonschema:"create the task without starting the agent"`
	}
	messageArgs struct {
		Agent string `json:"agent" jsonschema:"the agent to message"`
		Body  string `json:"body" jsonschema:"the message"`
		Task  string `json:"task,omitempty" jsonschema:"the task it is about"`
	}
	approvalsArgs struct {
		Status string `json:"status,omitempty" jsonschema:"pending (default), approved, denied, answered or all"`
	}
	decideArgs struct {
		ID       string `json:"id" jsonschema:"the approval id, a-..."`
		Decision string `json:"decision" jsonschema:"approve, deny or answer"`
		Text     string `json:"text,omitempty" jsonschema:"the answer to a question, or a note for the agent"`
	}
	readArgs struct {
		All  bool `json:"all,omitempty" jsonschema:"include messages already read"`
		Peek bool `json:"peek,omitempty" jsonschema:"leave unread messages unread"`
	}
	screenArgs struct {
		Agent string `json:"agent" jsonschema:"the agent"`
		Lines int    `json:"lines,omitempty" jsonschema:"how many lines, default 40"`
	}
)

// newMCP returns the tools, acting as c.
func (s *Server) newMCP(c core.Caller) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "sheaf", Title: "Sheaf", Version: s.Version}, &mcp.ServerOptions{Instructions: Instructions})
	svc := s.Svc

	tool(srv, "overview", "See your agents (state and what they are doing), active tasks, and how many items wait for you.", true,
		func(ctx context.Context, _ noArgs) (any, error) {
			agents, err := svc.ListAgents(ctx)
			if err != nil {
				return nil, err
			}
			tasks, err := svc.ListTasks(ctx, core.TaskFilter{Active: true})
			if err != nil {
				return nil, err
			}
			pending, err := svc.ListApprovals(ctx, core.ApprovalPending)
			if err != nil {
				return nil, err
			}
			unread, err := svc.Inbox(ctx, core.HumanCaller, core.InboxRequest{Peek: true})
			if err != nil {
				return nil, err
			}
			for i := range pending {
				pending[i].Detail = clip(pending[i].Detail, 400)
			}
			return map[string]any{"agents": agents, "active_tasks": tasks, "waiting_for_you": pending, "unread_messages": len(unread)}, nil
		})

	tool(srv, "list_tasks", "List tasks, newest first. Active ones only unless all is set.", true,
		func(ctx context.Context, a listTasksArgs) (any, error) {
			return svc.ListTasks(ctx, core.TaskFilter{Owner: a.Owner, Active: !a.All})
		})

	tool(srv, "get_task", "Read a task with its recent messages (handoff notes, questions, reports) and its owner's state.", true,
		func(ctx context.Context, a idArgs) (any, error) {
			t, err := svc.GetTask(ctx, a.ID)
			if err != nil {
				return nil, err
			}
			msgs, err := svc.TaskMessages(ctx, t.ID, 30)
			if err != nil {
				return nil, err
			}
			out := map[string]any{"task": t, "messages": msgs}
			if t.Owner != "" {
				if ag, err := svc.GetAgent(ctx, t.Owner); err == nil {
					out["owner"] = map[string]string{"name": ag.Name, "state": ag.State, "note": ag.Note}
				}
			}
			return out, nil
		})

	tool(srv, "list_repos", "List the repositories tasks can use.", true,
		func(ctx context.Context, _ noArgs) (any, error) { return svc.ListRepos(ctx) })

	tool(srv, "create_task", "Create a task for an agent, in its own git worktree, and start the agent on it.", false,
		func(ctx context.Context, a createTaskArgs) (any, error) {
			repo, err := s.pickRepo(ctx, a.Repo)
			if err != nil {
				return nil, err
			}
			spec := core.TaskSpec{Title: a.Title, Detail: a.Detail, Owner: a.Owner}
			if repo != "" {
				spec.Repo, spec.Worktree = repo, true
			}
			t, err := svc.CreateTask(ctx, c, spec)
			if err != nil {
				return nil, err
			}
			out := map[string]any{"task": t}
			switch {
			case a.NoStart || t.Owner == "":
			case s.Launcher == nil:
				out["start_error"] = "this server cannot start agents (no tmux)"
			default:
				if res, err := s.Launcher.StartTask(ctx, t.ID, ""); err != nil {
					out["start_error"] = err.Error()
				} else {
					out["started"] = res
				}
			}
			return out, nil
		})

	tool(srv, "message_agent", "Send a message to an agent's inbox. Sheaf tells the agent when it is idle.", false,
		func(ctx context.Context, a messageArgs) (any, error) {
			return svc.Send(ctx, c, core.SendRequest{To: a.Agent, Body: a.Body, Task: a.Task})
		})

	tool(srv, "list_approvals", "List what agents are waiting on you for: actions to approve, diffs to review, questions to answer.", true,
		func(ctx context.Context, a approvalsArgs) (any, error) {
			as, err := svc.ListApprovals(ctx, a.Status)
			if err != nil {
				return nil, err
			}
			for i := range as {
				as[i].Detail = clip(as[i].Detail, 6000)
			}
			return as, nil
		})

	tool(srv, "decide", "Decide an approval the user has decided: approve, deny (text explains why), or answer a question (text is the answer). Only on the user's explicit instruction.", false,
		func(ctx context.Context, a decideArgs) (any, error) {
			d := core.DecideRequest{ID: a.ID, Answer: a.Text}
			switch strings.ToLower(a.Decision) {
			case "approve", "approved", "yes":
				d.Status = core.ApprovalApproved
			case "deny", "denied", "no":
				d.Status = core.ApprovalDenied
			case "answer", "answered":
				d.Status = core.ApprovalAnswered
			default:
				return nil, errors.New("decision must be approve, deny or answer")
			}
			return svc.Decide(ctx, c, d)
		})

	tool(srv, "read_messages", "Read agents' messages to the user, oldest first, and mark them read.", false,
		func(ctx context.Context, a readArgs) (any, error) {
			return svc.Inbox(ctx, core.HumanCaller, core.InboxRequest{All: a.All, Peek: a.Peek})
		})

	tool(srv, "agent_screen", "Show the last lines of an agent's terminal, to see why it is stuck.", true,
		func(ctx context.Context, a screenArgs) (any, error) {
			if s.Launcher == nil {
				return nil, errors.New("this server cannot show terminals (no tmux)")
			}
			if a.Lines <= 0 || a.Lines > 200 {
				a.Lines = 40
			}
			return s.Launcher.Screen(ctx, a.Agent, "", a.Lines)
		})
	return srv
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("\n… (%d more bytes)", len(s)-n)
}

func (s *Server) pickRepo(ctx context.Context, name string) (string, error) {
	repos, err := s.Svc.ListRepos(ctx)
	if err != nil {
		return "", err
	}
	var names []string
	for _, r := range repos {
		if name != "" && (r.Name == name || r.Path == name) {
			return r.Path, nil
		}
		names = append(names, r.Name)
	}
	if name != "" {
		return "", fmt.Errorf("no repository %q; registered: %s", name, strings.Join(names, ", "))
	}
	switch len(repos) {
	case 0:
		return "", nil
	case 1:
		return repos[0].Path, nil
	}
	return "", fmt.Errorf("say which repository: %s", strings.Join(names, ", "))
}

// tool registers a tool whose result is the JSON of what f returns.
func tool[In any](s *mcp.Server, name, desc string, readOnly bool, f func(context.Context, In) (any, error)) {
	t := &mcp.Tool{Name: name, Description: desc, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly}}
	mcp.AddTool(s, t, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		out, err := f(ctx, in)
		if err != nil {
			msg := err.Error()
			for _, p := range []string{"invalid: ", "forbidden: ", "not found: "} {
				msg = strings.TrimPrefix(msg, p)
			}
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}, nil, nil
		}
		b, err := json.MarshalIndent(out, "", " ")
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil, nil
	})
}
