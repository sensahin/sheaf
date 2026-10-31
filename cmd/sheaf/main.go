// Command sheaf is the Sheaf daemon and CLI.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/sensahin/sheaf/internal/api"
	"github.com/sensahin/sheaf/internal/config"
	"github.com/sensahin/sheaf/internal/core"
	"github.com/sensahin/sheaf/internal/hook"
	"github.com/sensahin/sheaf/internal/mcpserver"
	"github.com/sensahin/sheaf/internal/store"
	"rsc.io/qr"
)

var version = "0.1.0-dev"

const usage = `sheaf: many agents, one agreement.

Setup
  sheaf init                       create ~/.sheaf and your owner token
  sheaf daemon [--web ADDR|off]    run the coordination daemon and web queue
  sheaf agent add NAME [--kind K] [--resume CMD]
  sheaf agent ls | rm NAME | token NAME | policy NAME [--set FILE]

Work
  sheaf task new TITLE [--owner A] [--parent T] [--repo DIR] [--worktree]
  sheaf task ls [--all] [--owner A]
  sheaf task status ID STATUS      open, working, blocked, done, cancelled
  sheaf task handoff ID TO NOTE
  sheaf send TO MESSAGE [--task T] [--question]
  sheaf inbox [--all] [--wait SECONDS]

Your queue
  sheaf pair [--name phone]        open the queue on your phone (QR code)
  sheaf device ls | revoke ID
  sheaf queue [--all]              what needs you
  sheaf show ID                    one item, with its diff or command
  sheaf approve ID [NOTE]
  sheaf deny ID [NOTE]
  sheaf answer ID TEXT

Recovery and audit
  sheaf resume [--tmux]            restart agents on their unfinished tasks
  sheaf log [--follow] [--after N]

For agents
  sheaf mcp                        MCP server on stdio (needs SHEAF_TOKEN)
  sheaf hook claude-code [--wait SECONDS]
                                   PreToolUse hook that routes to your queue

State lives in $SHEAF_HOME (default ~/.sheaf).
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "sheaf:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(usage)
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	paths, err := config.Resolve()
	if err != nil {
		return err
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version":
		fmt.Println("sheaf", version)
		return nil
	case "init":
		return cmdInit(ctx, paths)
	case "daemon":
		return cmdDaemon(ctx, paths, rest)
	}

	c, err := client(paths)
	if err != nil {
		return err
	}
	switch cmd {
	case "status":
		return cmdStatus(ctx, c)
	case "agent":
		return cmdAgent(ctx, c, rest)
	case "task":
		return cmdTask(ctx, c, rest)
	case "send":
		return cmdSend(ctx, c, rest)
	case "inbox":
		return cmdInbox(ctx, c, rest)
	case "queue", "gleanings":
		return cmdQueue(ctx, c, rest)
	case "show":
		return cmdShow(ctx, c, rest)
	case "approve":
		return cmdDecide(ctx, c, rest, core.ApprovalApproved)
	case "deny":
		return cmdDecide(ctx, c, rest, core.ApprovalDenied)
	case "answer":
		return cmdDecide(ctx, c, rest, core.ApprovalAnswered)
	case "resume":
		return cmdResume(ctx, c, rest)
	case "log":
		return cmdLog(ctx, c, rest)
	case "mcp":
		return mcpserver.Run(ctx, c, version)
	case "hook":
		return cmdHook(ctx, c, rest)
	case "pair":
		return cmdPair(ctx, c, rest)
	case "device":
		return cmdDevice(ctx, c, rest)
	}
	return fmt.Errorf("unknown command %q (see `sheaf help`)", cmd)
}

// client authenticates as SHEAF_TOKEN if set (an agent), else as the owner.
func client(paths config.Paths) (*api.Client, error) {
	tok := os.Getenv("SHEAF_TOKEN")
	if tok == "" {
		b, err := os.ReadFile(paths.OwnerToken)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		tok = strings.TrimSpace(string(b))
	}
	return api.NewClient(paths.Socket, tok), nil
}

// parse parses flags that may appear before, between or after positional
// arguments, and returns the positionals.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func need(pos []string, n int, use string) error {
	if len(pos) < n {
		return fmt.Errorf("usage: sheaf %s", use)
	}
	return nil
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func table() *tabwriter.Writer { return tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0) }

func ago(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return t.Local().Format("Jan 2")
}

func cmdInit(ctx context.Context, paths config.Paths) error {
	if err := paths.Ensure(); err != nil {
		return err
	}
	tok, err := paths.LoadOrCreateOwnerToken()
	if err != nil {
		return err
	}
	db, err := store.Open(paths.DB)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := core.New(db, paths.WorktreeRoot).SetOwnerToken(ctx, tok); err != nil {
		return err
	}
	fmt.Printf("Sheaf is set up in %s.\n\nNext:\n  sheaf daemon                         (leave it running)\n  sheaf agent add builder --kind claude-code --resume \"claude --continue\"\n", paths.Home)
	return nil
}

func cmdDaemon(ctx context.Context, paths config.Paths, args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	web := fs.String("web", defaultWeb, `address for the web queue, or "off"; use 0.0.0.0:7517 or a Tailscale address to reach it from your phone`)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if err := paths.Ensure(); err != nil {
		return err
	}
	tok, err := paths.LoadOrCreateOwnerToken()
	if err != nil {
		return err
	}
	db, err := store.Open(paths.DB)
	if err != nil {
		return err
	}
	defer db.Close()
	svc := core.New(db, paths.WorktreeRoot)
	if err := svc.SetOwnerToken(ctx, tok); err != nil {
		return err
	}
	if err := svc.MarkStarted(ctx, version); err != nil {
		return err
	}
	plan, err := svc.ResumePlan(ctx)
	if err != nil {
		return err
	}
	pending, err := svc.ListApprovals(ctx, core.ApprovalPending)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "sheaf %s listening on %s\n", version, paths.Socket)
	if len(plan) > 0 {
		fmt.Fprintf(os.Stderr, "%d unfinished task(s) can be resumed: run `sheaf resume`\n", len(plan))
	}
	if len(pending) > 0 {
		fmt.Fprintf(os.Stderr, "%d item(s) waiting in your queue: run `sheaf queue`\n", len(pending))
	}
	srv := api.NewServer(svc)
	if *web == "off" {
		return srv.ListenUnix(ctx, paths.Socket)
	}
	l, err := net.Listen("tcp", *web)
	if err != nil {
		return fmt.Errorf("web queue: %w (pass --web off to disable it)", err)
	}
	srv.WebURL = webURL(l.Addr().(*net.TCPAddr))
	fmt.Fprintf(os.Stderr, "web queue at %s (pair a device with `sheaf pair`)\n", srv.WebURL)
	if !l.Addr().(*net.TCPAddr).IP.IsLoopback() {
		fmt.Fprintln(os.Stderr, "warning: the web queue is reachable from your network over plain HTTP; prefer a private network such as Tailscale")
	}
	errc := make(chan error, 2)
	go func() { errc <- srv.ListenTCP(ctx, l) }()
	go func() { errc <- srv.ListenUnix(ctx, paths.Socket) }()
	// Either listener failing takes the daemon down; ctx stops the other.
	return <-errc
}

const defaultWeb = "127.0.0.1:7517"

// webURL is the address a phone should open: a wildcard listen address is
// replaced by this machine's first private IPv4 address.
func webURL(a *net.TCPAddr) string {
	host := a.IP.String()
	if a.IP.IsUnspecified() {
		host = "localhost"
		if addrs, err := net.InterfaceAddrs(); err == nil {
			for _, ad := range addrs {
				if ipn, ok := ad.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() {
					host = ipn.IP.String()
					break
				}
			}
		}
	}
	return "http://" + net.JoinHostPort(host, fmt.Sprint(a.Port))
}

func cmdPair(ctx context.Context, c *api.Client, args []string) error {
	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	name := fs.String("name", "phone", "a name for the device")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	var r api.PairResp
	if err := c.Call(ctx, "device.pair", api.PairReq{Name: *name}, &r); err != nil {
		return err
	}
	if r.URL == "" {
		return fmt.Errorf("paired %s, but the daemon is not serving the web queue; restart it without --web off", r.Device.ID)
	}
	fmt.Printf("Scan this on %s, or open the link. It works only for this device.\n\n", *name)
	if err := printQR(os.Stdout, r.URL); err != nil {
		return err
	}
	fmt.Printf("\n%s\n\nRevoke it any time with: sheaf device revoke %s\n", r.URL, r.Device.ID)
	return nil
}

// printQR draws a QR code with half-block characters, two rows per line.
func printQR(w io.Writer, text string) error {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return err
	}
	const quiet = 2
	black := func(x, y int) bool {
		x, y = x-quiet, y-quiet
		return x >= 0 && y >= 0 && x < code.Size && y < code.Size && code.Black(x, y)
	}
	n := code.Size + 2*quiet
	var b strings.Builder
	for y := 0; y < n; y += 2 {
		for x := 0; x < n; x++ {
			top, bot := black(x, y), black(x, y+1)
			// Light modules are drawn, so the code reads on dark terminals.
			switch {
			case !top && !bot:
				b.WriteString("█")
			case !top:
				b.WriteString("▀")
			case !bot:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString("\n")
	}
	_, err = io.WriteString(w, b.String())
	return err
}

func cmdDevice(ctx context.Context, c *api.Client, args []string) error {
	if len(args) == 0 {
		args = []string{"ls"}
	}
	switch args[0] {
	case "ls", "list":
		var ds []core.Device
		if err := c.Call(ctx, "device.list", nil, &ds); err != nil {
			return err
		}
		w := table()
		fmt.Fprintln(w, "DEVICE\tNAME\tPAIRED\tLAST SEEN\tSTATUS")
		for _, d := range ds {
			seen, status := "never", "active"
			if d.LastSeen != nil {
				seen = ago(*d.LastSeen)
			}
			if d.RevokedAt != nil {
				status = "revoked"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", d.ID, d.Name, ago(d.CreatedAt), seen, status)
		}
		return w.Flush()
	case "revoke", "rm":
		if err := need(args[1:], 1, "device revoke ID"); err != nil {
			return err
		}
		return c.Call(ctx, "device.revoke", api.IDReq{ID: args[1]}, nil)
	}
	return fmt.Errorf("unknown device command %q", args[0])
}

func cmdStatus(ctx context.Context, c *api.Client) error {
	var who core.Caller
	if err := c.Call(ctx, "whoami", nil, &who); err != nil {
		return err
	}
	var agents []core.Agent
	if err := c.Call(ctx, "agent.list", nil, &agents); err != nil {
		return err
	}
	fmt.Printf("You are %s.\n\n", who.Name)
	w := table()
	fmt.Fprintln(w, "AGENT\tKIND\tSTATE\tNOTE\tUPDATED")
	for _, a := range agents {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", a.Name, a.Kind, a.State, a.Note, ago(a.UpdatedAt))
	}
	w.Flush()
	if who.Name == core.Human {
		var q []core.Approval
		if err := c.Call(ctx, "approval.list", api.ApprovalsReq{}, &q); err != nil {
			return err
		}
		fmt.Printf("\n%d item(s) need you.\n", len(q))
	}
	return nil
}

func cmdAgent(ctx context.Context, c *api.Client, args []string) error {
	if len(args) == 0 {
		args = []string{"ls"}
	}
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	kind := fs.String("kind", "", "agent program: claude-code, codex, opencode, ...")
	resume := fs.String("resume", "", "command that resumes the agent, run in its task directory")
	set := fs.String("set", "", "policy JSON file to apply")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	switch args[0] {
	case "ls", "list":
		var agents []core.Agent
		if err := c.Call(ctx, "agent.list", nil, &agents); err != nil {
			return err
		}
		w := table()
		fmt.Fprintln(w, "AGENT\tKIND\tSTATE\tNOTE\tRESUME")
		for _, a := range agents {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", a.Name, a.Kind, a.State, a.Note, a.ResumeCmd)
		}
		return w.Flush()
	case "add":
		if err := need(pos, 1, "agent add NAME [--kind K] [--resume CMD]"); err != nil {
			return err
		}
		var r api.AddAgentResp
		if err := c.Call(ctx, "agent.add", core.AgentSpec{Name: pos[0], Kind: *kind, ResumeCmd: *resume}, &r); err != nil {
			return err
		}
		printAgentSetup(r.Agent.Name, r.Token)
		return nil
	case "rm", "remove":
		if err := need(pos, 1, "agent rm NAME"); err != nil {
			return err
		}
		return c.Call(ctx, "agent.remove", api.NameReq{Name: pos[0]}, nil)
	case "token":
		if err := need(pos, 1, "agent token NAME"); err != nil {
			return err
		}
		var r api.TokenResp
		if err := c.Call(ctx, "agent.rotate_token", api.NameReq{Name: pos[0]}, &r); err != nil {
			return err
		}
		fmt.Println("The old token no longer works.")
		printAgentSetup(pos[0], r.Token)
		return nil
	case "policy":
		if err := need(pos, 1, "agent policy NAME [--set FILE]"); err != nil {
			return err
		}
		if *set != "" {
			b, err := os.ReadFile(*set)
			if err != nil {
				return err
			}
			var p core.Policy
			if err := json.Unmarshal(b, &p); err != nil {
				return err
			}
			return c.Call(ctx, "agent.set_policy", api.PolicyReq{Name: pos[0], Policy: p}, nil)
		}
		var a core.Agent
		if err := c.Call(ctx, "agent.get", api.NameReq{Name: pos[0]}, &a); err != nil {
			return err
		}
		printJSON(a.Policy)
		return nil
	}
	return fmt.Errorf("unknown agent command %q", args[0])
}

func printAgentSetup(name, token string) {
	self, err := os.Executable()
	if err != nil {
		self = "sheaf"
	}
	fmt.Printf(`Agent %q is ready. Its token is shown only once:

  SHEAF_TOKEN=%s

Claude Code: connect the tools and route permission checks to your queue
  claude mcp add sheaf --env SHEAF_TOKEN=%s -- %s mcp
  and add to .claude/settings.json:
  {"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"SHEAF_TOKEN=%s %s hook claude-code"}]}]}}

Any other MCP client: run "%s mcp" on stdio with SHEAF_TOKEN in its environment.
`, name, token, token, self, token, self, self)
}

func cmdTask(ctx context.Context, c *api.Client, args []string) error {
	if len(args) == 0 {
		args = []string{"ls"}
	}
	fs := flag.NewFlagSet("task", flag.ContinueOnError)
	owner := fs.String("owner", "", "owning agent")
	parent := fs.String("parent", "", "parent task id")
	repo := fs.String("repo", "", "path in a git repository")
	wt := fs.Bool("worktree", false, "give the task its own git worktree")
	all := fs.Bool("all", false, "include finished tasks")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	switch args[0] {
	case "ls", "list":
		var tasks []core.Task
		if err := c.Call(ctx, "task.list", core.TaskFilter{Owner: *owner, Active: !*all}, &tasks); err != nil {
			return err
		}
		w := table()
		fmt.Fprintln(w, "TASK\tSTATUS\tOWNER\tTITLE\tWORKTREE")
		for _, t := range tasks {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", t.ID, t.Status, t.Owner, t.Title, t.Worktree)
		}
		return w.Flush()
	case "new", "create":
		if err := need(pos, 1, "task new TITLE [--owner A] [--repo DIR] [--worktree]"); err != nil {
			return err
		}
		if *wt && *repo == "" {
			*repo = "."
		}
		if *repo != "" {
			if abs, err := filepath.Abs(*repo); err == nil {
				*repo = abs
			}
		}
		var t core.Task
		if err := c.Call(ctx, "task.create", core.TaskSpec{Title: strings.Join(pos, " "), Owner: *owner, Parent: *parent, Repo: *repo, Worktree: *wt}, &t); err != nil {
			return err
		}
		fmt.Println(t.ID)
		if t.Worktree != "" {
			fmt.Printf("worktree %s on branch %s\n", t.Worktree, t.Branch)
		}
		return nil
	case "show":
		if err := need(pos, 1, "task show ID"); err != nil {
			return err
		}
		var t core.Task
		if err := c.Call(ctx, "task.get", api.IDReq{ID: pos[0]}, &t); err != nil {
			return err
		}
		printJSON(t)
		return nil
	case "status":
		if err := need(pos, 2, "task status ID STATUS"); err != nil {
			return err
		}
		return c.Call(ctx, "task.set_status", api.TaskStatusReq{ID: pos[0], Status: pos[1]}, nil)
	case "done":
		if err := need(pos, 1, "task done ID"); err != nil {
			return err
		}
		return c.Call(ctx, "task.set_status", api.TaskStatusReq{ID: pos[0], Status: core.TaskDone}, nil)
	case "handoff":
		if err := need(pos, 3, "task handoff ID TO NOTE"); err != nil {
			return err
		}
		return c.Call(ctx, "task.handoff", core.HandoffRequest{Task: pos[0], To: pos[1], Note: strings.Join(pos[2:], " ")}, nil)
	}
	return fmt.Errorf("unknown task command %q", args[0])
}

func cmdSend(ctx context.Context, c *api.Client, args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	task := fs.String("task", "", "related task id")
	q := fs.Bool("question", false, "expect a reply")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "send TO MESSAGE [--task T] [--question]"); err != nil {
		return err
	}
	kind := core.KindMessage
	if *q {
		kind = core.KindQuestion
	}
	var m core.Message
	if err := c.Call(ctx, "message.send", core.SendRequest{To: pos[0], Body: strings.Join(pos[1:], " "), Task: *task, Kind: kind}, &m); err != nil {
		return err
	}
	fmt.Println(m.ID)
	return nil
}

func cmdInbox(ctx context.Context, c *api.Client, args []string) error {
	fs := flag.NewFlagSet("inbox", flag.ContinueOnError)
	all := fs.Bool("all", false, "include read messages")
	wait := fs.Int("wait", 0, "seconds to wait for a message")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	var msgs []core.Message
	if err := c.Call(ctx, "message.inbox", core.InboxRequest{All: *all, WaitSeconds: *wait}, &msgs); err != nil {
		return err
	}
	if len(msgs) == 0 {
		fmt.Println("No new messages.")
	}
	for _, m := range msgs {
		head := fmt.Sprintf("%s  %s from %s, %s", m.ID, m.Kind, m.From, ago(m.CreatedAt))
		if m.Task != "" {
			head += ", task " + m.Task
		}
		fmt.Printf("%s\n  %s\n\n", head, strings.ReplaceAll(m.Body, "\n", "\n  "))
	}
	return nil
}

func cmdQueue(ctx context.Context, c *api.Client, args []string) error {
	fs := flag.NewFlagSet("queue", flag.ContinueOnError)
	all := fs.Bool("all", false, "include decided items")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	status := ""
	if *all {
		status = "all"
	}
	var items []core.Approval
	if err := c.Call(ctx, "approval.list", api.ApprovalsReq{Status: status}, &items); err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Println("Nothing needs you.")
		return nil
	}
	w := table()
	fmt.Fprintln(w, "ID\tKIND\tAGENT\tTASK\tSTATUS\tSUMMARY\tASKED")
	for _, a := range items {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", a.ID, a.Kind, a.Agent, a.Task, a.Status, a.Summary, ago(a.CreatedAt))
	}
	return w.Flush()
}

func cmdShow(ctx context.Context, c *api.Client, args []string) error {
	if err := need(args, 1, "show ID"); err != nil {
		return err
	}
	var a core.Approval
	if err := c.Call(ctx, "approval.get", api.IDReq{ID: args[0]}, &a); err != nil {
		return err
	}
	fmt.Printf("%s  %s from %s (%s)\n%s\n", a.ID, a.Kind, a.Agent, a.Status, a.Summary)
	if a.Task != "" {
		fmt.Println("task", a.Task)
	}
	if a.Detail != "" {
		fmt.Printf("\n%s\n", a.Detail)
	}
	if a.Answer != "" {
		fmt.Printf("\nanswer: %s\n", a.Answer)
	}
	return nil
}

func cmdDecide(ctx context.Context, c *api.Client, args []string, status string) error {
	if err := need(args, 1, status+" ID [NOTE]"); err != nil {
		return err
	}
	var a core.Approval
	if err := c.Call(ctx, "approval.decide", core.DecideRequest{ID: args[0], Status: status, Answer: strings.Join(args[1:], " ")}, &a); err != nil {
		return err
	}
	fmt.Printf("%s %s. %s will see it in its inbox.\n", a.ID, a.Status, a.Agent)
	return nil
}

func cmdResume(ctx context.Context, c *api.Client, args []string) error {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	tmux := fs.Bool("tmux", false, "open each agent in a tmux window")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	var plan []core.ResumeItem
	if err := c.Call(ctx, "task.resume_plan", nil, &plan); err != nil {
		return err
	}
	if len(plan) == 0 {
		fmt.Println("Nothing to resume.")
		return nil
	}
	// One window per agent: an agent resumes into its most recent task.
	seen := map[string]bool{}
	for _, it := range plan {
		if seen[it.Agent] {
			continue
		}
		seen[it.Agent] = true
		if !*tmux {
			fmt.Printf("# %s on %s: %s\ncd %q && %s\n\n", it.Agent, it.Task, it.Title, it.Dir, it.Command)
			continue
		}
		if err := tmuxWindow(it); err != nil {
			return err
		}
		fmt.Printf("resumed %s on %s in tmux session \"sheaf\"\n", it.Agent, it.Task)
	}
	return nil
}

func tmuxWindow(it core.ResumeItem) error {
	if exec.Command("tmux", "has-session", "-t", "sheaf").Run() != nil {
		return exec.Command("tmux", "new-session", "-d", "-s", "sheaf", "-n", it.Agent, "-c", it.Dir, it.Command).Run()
	}
	return exec.Command("tmux", "new-window", "-t", "sheaf", "-n", it.Agent, "-c", it.Dir, it.Command).Run()
}

func cmdLog(ctx context.Context, c *api.Client, args []string) error {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	follow := fs.Bool("follow", false, "keep printing new events")
	after := fs.Int64("after", 0, "start after this event id")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	last := *after
	for {
		var evs []core.Event
		wait := 0
		if *follow {
			wait = 60
		}
		if err := c.Call(ctx, "events.list", api.EventsReq{After: last, Limit: 500, WaitSeconds: wait}, &evs); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		for _, e := range evs {
			fmt.Printf("%6d  %s  %-8s %-22s %-12s %s\n", e.ID, e.TS[:19], e.Actor, e.Type, e.Subject, e.Data)
			last = e.ID
		}
		if !*follow && len(evs) < 500 {
			return nil
		}
	}
}

func cmdHook(ctx context.Context, c *api.Client, args []string) error {
	if len(args) == 0 || args[0] != "claude-code" {
		return fmt.Errorf("usage: sheaf hook claude-code [--wait SECONDS]")
	}
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	wait := fs.Int("wait", 600, "seconds to wait for your decision before falling back to the agent's own prompt")
	if _, err := parse(fs, args[1:]); err != nil {
		return err
	}
	out, err := hook.ClaudeCode(ctx, c, os.Stdin, time.Duration(*wait)*time.Second)
	if err != nil {
		return err
	}
	if len(out) > 0 {
		os.Stdout.Write(append(out, '\n'))
	}
	return nil
}
