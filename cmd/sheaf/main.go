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
	"runtime/debug"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/sensahin/sheaf/internal/api"
	"github.com/sensahin/sheaf/internal/config"
	"github.com/sensahin/sheaf/internal/core"
	"github.com/sensahin/sheaf/internal/herdr"
	"github.com/sensahin/sheaf/internal/hook"
	"github.com/sensahin/sheaf/internal/launch"
	"github.com/sensahin/sheaf/internal/mcpserver"
	agentrun "github.com/sensahin/sheaf/internal/run"
	"github.com/sensahin/sheaf/internal/setup"
	"github.com/sensahin/sheaf/internal/store"
	"rsc.io/qr"
)

// version is set by release builds with -ldflags "-X main.version=...".
var version = ""

func init() {
	if version != "" {
		return
	}
	version = "dev"
	// `go install ...@v0.1.0` records the module version.
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		version = bi.Main.Version
	}
}

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

Launching agents
  sheaf setup KIND [--print]       connect an agent program in this project
                                   (antigravity, claude-code, cline, codex,
                                   copilot, cursor, droid, gemini, junie,
                                   opencode, windsurf)
  sheaf start AGENT [--task T] [--here] [-- CMD]
                                   open the agent in a tmux window (or here),
                                   with its status tracked
  sheaf tmux-status                one line for tmux's status bar

For agents
  sheaf run [--agent A] [--task T] -- CMD...
                                   run an agent and report the status it
                                   prints (OSC 7501)
  sheaf mcp                        MCP server on stdio (needs SHEAF_TOKEN)
  sheaf hook KIND [--wait SECONDS]
                                   pre-tool hook that routes to your queue

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
	case "setup":
		return cmdSetup(rest)
	}

	c, err := client(paths)
	if err != nil {
		return err
	}
	switch cmd {
	case "status":
		return cmdStatus(ctx, c)
	case "agent":
		return cmdAgent(ctx, paths, c, rest)
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
	case "run":
		return cmdRun(ctx, paths, c, rest)
	case "start":
		return cmdStart(ctx, paths, c, rest)
	case "tmux-status":
		return cmdTmuxStatus(ctx, c)
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

func cmdRun(ctx context.Context, paths config.Paths, c *api.Client, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	task := fs.String("task", "", "task whose status follows the agent's")
	agent := fs.String("agent", "", "run as this agent, using its saved token instead of SHEAF_TOKEN")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: sheaf run [--agent A] [--task T] -- CMD [ARGS...]")
	}
	if *agent != "" {
		tok, err := paths.LoadAgentToken(*agent)
		if err != nil {
			return err
		}
		c.Token = tok
		// The agent's own Sheaf tools (sheaf mcp, the hook) find it here.
		os.Setenv("SHEAF_TOKEN", tok)
	}
	var who core.Caller
	if err := c.Call(ctx, "whoami", nil, &who); err != nil {
		return err
	}
	if who.IsHuman() {
		return fmt.Errorf("sheaf run reports status for an agent: set SHEAF_TOKEN to the agent's token")
	}
	report := func(ctx context.Context, r core.StatusReport) error {
		return c.Call(ctx, "agent.report_status", r, nil)
	}
	// Inside herdr, mirror the status into herdr's sidebar too, and give
	// herdr the command that brings this agent back after a restart.
	pane, inHerdr := herdr.Detect()
	if inHerdr {
		resume := herdrResume(ctx, c, who.Name, *task)
		toDaemon := report
		report = func(ctx context.Context, r core.StatusReport) error {
			err := toDaemon(ctx, r)
			pane.Report(ctx, who.Name, r, resume)
			resume = nil // once is enough
			return err
		}
	}
	// Signals go to the agent through its terminal; Sheaf just outlives it.
	signal.Ignore(os.Interrupt, syscall.SIGTERM)
	code, err := agentrun.Run(context.Background(), agentrun.Options{
		Command: fs.Args(),
		Env:     os.Environ(),
		Task:    *task,
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Report:  report,
	})
	if inHerdr {
		pane.Release(context.Background(), who.Name)
	}
	if err != nil {
		return err
	}
	os.Exit(code)
	return nil
}

// herdrResume is the command herdr runs to bring agent back in its pane:
// the agent's resume command, under `sheaf run` so Sheaf keeps tracking it.
// It is nil when the agent has no resume command.
func herdrResume(ctx context.Context, c *api.Client, agent, task string) []string {
	var agents []core.Agent
	if err := c.Call(ctx, "agent.list", nil, &agents); err != nil {
		return nil
	}
	for _, a := range agents {
		if a.Name == agent && a.ResumeCmd != "" {
			argv := []string{self(), "run", "--agent", agent}
			if task != "" {
				argv = append(argv, "--task", task)
			}
			return append(argv, "--", "sh", "-c", a.ResumeCmd)
		}
	}
	return nil
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

func cmdAgent(ctx context.Context, paths config.Paths, c *api.Client, args []string) error {
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
		if err := paths.SaveAgentToken(r.Agent.Name, r.Token); err != nil {
			return err
		}
		printAgentSetup(paths, r.Agent.Name, r.Agent.Kind, r.Token)
		return nil
	case "rm", "remove":
		if err := need(pos, 1, "agent rm NAME"); err != nil {
			return err
		}
		if err := c.Call(ctx, "agent.remove", api.NameReq{Name: pos[0]}, nil); err != nil {
			return err
		}
		return paths.RemoveAgentToken(pos[0])
	case "token":
		if err := need(pos, 1, "agent token NAME"); err != nil {
			return err
		}
		var r api.TokenResp
		if err := c.Call(ctx, "agent.rotate_token", api.NameReq{Name: pos[0]}, &r); err != nil {
			return err
		}
		if err := paths.SaveAgentToken(pos[0], r.Token); err != nil {
			return err
		}
		fmt.Println("The old token no longer works.")
		printAgentSetup(paths, pos[0], "", r.Token)
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

func printAgentSetup(paths config.Paths, name, kind, token string) {
	connect := "Connect your agent program to Sheaf once per project, for example:\n\n  sheaf setup claude-code    (see `sheaf setup` for the list)"
	if _, err := setup.For(kind, ""); err == nil {
		connect = "Connect " + kind + " to Sheaf once per project:\n\n  sheaf setup " + kind
	}
	fmt.Printf(`Agent %[1]q is ready. Start it with:

  sheaf start %[1]s [--task ID]

That runs it under "sheaf run", which hands it its token, so its own config
needs no secret. %[2]s

Starting it some other way? Give it this token as SHEAF_TOKEN (also saved,
owner-only, in %[3]s):

  SHEAF_TOKEN=%[4]s
`, name, connect, paths.AgentTokenPath(name), token)
}

func cmdSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	dir := fs.String("dir", ".", "project directory to write the config into")
	printOnly := fs.Bool("print", false, "print the config instead of writing it")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: sheaf setup KIND [--dir DIR] [--print]  (KIND: %s)", strings.Join(setup.Kinds(), ", "))
	}
	plan, err := setup.For(pos[0], self())
	if err != nil {
		return err
	}
	if *printOnly {
		for _, f := range plan.Files {
			fmt.Printf("# %s\n%s\n", f.Path, f.Content)
		}
	} else {
		results, err := setup.Apply(*dir, plan)
		if err != nil {
			return err
		}
		for _, r := range results {
			switch r.Status {
			case "skipped":
				fmt.Printf("%s already exists, so Sheaf left it alone. Add this to it:\n\n%s\n", r.Path, indent(r.Content))
			case "ok":
				fmt.Printf("%s is already set up.\n", r.Path)
			default:
				fmt.Printf("%s %s.\n", r.Status, r.Path)
			}
		}
	}
	for _, s := range plan.Steps {
		fmt.Printf("\n%s\n", indent(s))
	}
	return nil
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = "  " + l
		}
	}
	return strings.Join(lines, "\n") + "\n"
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
		spec := launch.Spec{Agent: it.Agent, Task: it.Task, Dir: it.Dir, Command: it.Command, Sheaf: self()}
		if !*tmux {
			fmt.Printf("# %s on %s: %s\ncd %s && %s\n\n", it.Agent, it.Task, it.Title, launch.Quote(it.Dir), spec.Shell())
			continue
		}
		where, err := launch.Tmux(spec, os.Getenv("TMUX") != "")
		if err != nil {
			return err
		}
		fmt.Printf("resumed %s on %s in %s\n", it.Agent, it.Task, where)
	}
	return nil
}

func self() string {
	p, err := os.Executable()
	if err != nil {
		return "sheaf"
	}
	return p
}

func cmdStart(ctx context.Context, paths config.Paths, c *api.Client, args []string) error {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	task := fs.String("task", "", "task to work on; the agent starts in its worktree")
	here := fs.Bool("here", false, "run in this terminal instead of a tmux window")
	resume := fs.Bool("resume", false, "use the agent's resume command")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return fmt.Errorf("usage: sheaf start AGENT [--task T] [--here] [--resume] [-- CMD...]")
	}
	name, rest := rest[0], rest[1:]
	// Flags may also follow the agent name.
	if err := fs.Parse(rest); err != nil {
		return err
	}
	rest = fs.Args()
	if len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:]
	}

	var a core.Agent
	if err := c.Call(ctx, "agent.get", api.NameReq{Name: name}, &a); err != nil {
		return err
	}
	if _, err := paths.LoadAgentToken(name); err != nil {
		return err
	}
	command := strings.Join(rest, " ")
	switch {
	case command != "":
	case *resume && a.ResumeCmd != "":
		command = a.ResumeCmd
	case launch.DefaultCommands[a.Kind] != "":
		command = launch.DefaultCommands[a.Kind]
	default:
		return fmt.Errorf("no command for agent %q (kind %q): pass one after --", name, a.Kind)
	}

	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	if *task != "" {
		var t core.Task
		if err := c.Call(ctx, "task.get", api.IDReq{ID: *task}, &t); err != nil {
			return err
		}
		if t.Owner != name {
			return fmt.Errorf("task %s is owned by %q, not %q; hand it off first", t.ID, t.Owner, name)
		}
		switch {
		case t.Worktree != "":
			dir = t.Worktree
		case t.Repo != "":
			dir = t.Repo
		}
	}

	spec := launch.Spec{Agent: name, Task: *task, Dir: dir, Command: command, Sheaf: self()}
	// In a herdr pane (and not in tmux inside it), the pane is the window:
	// run here, and `sheaf run` reports the status to herdr as well.
	_, inHerdr := herdr.Detect()
	inHerdr = inHerdr && os.Getenv("TMUX") == ""
	if *here || inHerdr || exec.Command("tmux", "-V").Run() != nil {
		if err := os.Chdir(dir); err != nil {
			return err
		}
		runArgs := []string{"--agent", name}
		if *task != "" {
			runArgs = append(runArgs, "--task", *task)
		}
		return cmdRun(ctx, paths, c, append(runArgs, "--", "sh", "-c", command))
	}
	where, err := launch.Tmux(spec, os.Getenv("TMUX") != "")
	if err != nil {
		return err
	}
	fmt.Printf("started %s in %s\n", name, where)
	return nil
}

// cmdTmuxStatus prints a short summary for tmux's status bar, for example
// set -g status-right '#(sheaf tmux-status)'. It prints nothing when there is
// nothing to say or the daemon is down, so the bar stays clean.
func cmdTmuxStatus(ctx context.Context, c *api.Client) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var q []core.Approval
	var agents []core.Agent
	if c.Call(ctx, "approval.list", api.ApprovalsReq{}, &q) != nil || c.Call(ctx, "agent.list", nil, &agents) != nil {
		return nil
	}
	fmt.Println(statusLine(len(q), agents))
	return nil
}

func statusLine(queued int, agents []core.Agent) string {
	var working, blocked int
	for _, a := range agents {
		switch a.State {
		case core.StateWorking:
			working++
		case core.StateBlocked, core.StateError:
			blocked++
		}
	}
	var parts []string
	if queued > 0 {
		parts = append(parts, fmt.Sprintf("#[fg=colour178,bold]%d need you#[default]", queued))
	}
	if blocked > 0 {
		parts = append(parts, fmt.Sprintf("%d blocked", blocked))
	}
	if working > 0 {
		parts = append(parts, fmt.Sprintf("%d working", working))
	}
	if len(parts) == 0 {
		return ""
	}
	return "sheaf: " + strings.Join(parts, " · ")
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
	hooks := hook.Adapters
	if len(args) == 0 || hooks[args[0]] == nil {
		return fmt.Errorf("usage: sheaf hook %s [--wait SECONDS]", strings.Join(setup.Kinds(), "|"))
	}
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	wait := fs.Int("wait", 600, "seconds to wait for your decision before falling back to the agent's own prompt")
	if _, err := parse(fs, args[1:]); err != nil {
		return err
	}
	out, err := hooks[args[0]](ctx, c, os.Stdin, time.Duration(*wait)*time.Second)
	var blocked *hook.Blocked
	if errors.As(err, &blocked) {
		// Agents that block by exit status read the reason from stderr.
		fmt.Fprintln(os.Stderr, blocked.Reason)
		os.Exit(2)
	}
	if err != nil {
		return err
	}
	if len(out) > 0 {
		os.Stdout.Write(append(out, '\n'))
	}
	return nil
}
