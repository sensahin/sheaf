// Package channels lets agents reach the person where they already are, and
// lets the person answer there: a chat app such as Telegram or Slack.
//
// The Hub follows the event log. Every event that needs the person (an
// approval, an agent's question or message, a blocked agent, a finished task)
// becomes a Notice sent to every configured channel. What the person sends
// back (a button press, a reply, a command) comes in through Hub.Command,
// Hub.Reply and Hub.Decide, so every channel speaks the same language and acts
// with the same rights as a paired phone.
package channels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sensahin/sheaf/internal/core"
)

// Notice kinds.
const (
	NoticeApproval = "approval" // an action needs approving
	NoticeDiff     = "diff"     // a task's changes are ready for review
	NoticeQuestion = "question" // an approval of kind question
	NoticeMessage  = "message"  // an agent messaged the person
	NoticeBlocked  = "blocked"  // an agent reported it is blocked
	NoticeDone     = "done"     // an agent finished a task
	NoticeInfo     = "info"     // anything else Sheaf has to say
)

// Notice is something the person should see.
type Notice struct {
	Kind  string
	Agent string
	Task  string
	// TaskTitle is the task's title, when there is a task.
	TaskTitle string
	// Title is the one-line headline.
	Title string
	// Body is the summary, question or message text.
	Body string
	// Detail is the command or diff, shown in a code block.
	Detail string
	// Ref is what a reply to this notice answers: an approval id (a-...) or a
	// message id (m-...). Empty when there is nothing to answer.
	Ref string
	// Decidable is set for approvals, which get Approve and Deny buttons.
	Decidable bool
	// Terminal is set when the agent is stopped on a prompt in its own
	// terminal (a permission dialog, a menu, an input box). Detail holds the
	// screen; replies are typed into the terminal and the channel offers key
	// buttons. Ref is then a TermRef.
	Terminal bool
}

// TermRef names an agent's terminal in a notice footer, so a reply can find
// it: term:AGENT or term:AGENT:TASK.
func TermRef(agent, task string) string {
	if task == "" {
		return "term:" + agent
	}
	return "term:" + agent + ":" + task
}

// ParseTermRef splits a TermRef.
func ParseTermRef(ref string) (agent, task string, ok bool) {
	rest, ok := strings.CutPrefix(ref, "term:")
	if !ok || rest == "" {
		return "", "", false
	}
	agent, task, _ = strings.Cut(rest, ":")
	return agent, task, true
}

// TermKeys are the buttons a terminal notice offers, as label and key. Digits
// pick an option in a numbered menu; the rest are tmux key names.
var TermKeys = [][2]string{{"1", "1"}, {"2", "2"}, {"3", "3"}, {"⏎ Enter", "Enter"}, {"Esc", "Escape"}, {"🔄 Screen", "screen"}}

// Channel is one way to reach the person.
type Channel interface {
	// Name is a short label such as "telegram".
	Name() string
	// Send delivers a notice.
	Send(ctx context.Context, n Notice) error
	// Run receives the person's replies and commands until ctx ends, calling
	// the hub for each.
	Run(ctx context.Context, h *Hub) error
}

// Resolver is a channel that can update a notice it sent once the approval
// was decided anywhere, for example to remove its buttons.
type Resolver interface {
	Resolved(ctx context.Context, a core.Approval)
}

// Launcher starts agents and shows their terminals; the daemon's is
// dispatch.Dispatcher. Nil when the daemon cannot start agents.
type Launcher interface {
	StartTask(ctx context.Context, id, lead string) (core.StartResult, error)
	Screen(ctx context.Context, agent, task string, lines int) (core.Screen, error)
	Type(ctx context.Context, agent, task, text string, enter bool) error
	Key(ctx context.Context, agent, task, key string) error
}

// Hub connects channels to the coordination service.
type Hub struct {
	Svc      *core.Service
	Dispatch Launcher
	Channels []Channel
	// Logf reports problems; nil is quiet.
	Logf func(format string, a ...any)
}

func (h *Hub) logf(format string, a ...any) {
	if h.Logf != nil {
		h.Logf(format, a...)
	}
}

// caller is the person, acting through a channel. It has the rights of a
// paired device: it can decide, message and create tasks in registered
// repositories, but not mint tokens or loosen policies.
func caller(channel string) core.Caller {
	return core.Caller{Name: core.Human, Device: "channel:" + channel}
}

// Run starts every channel and forwards notices until ctx ends.
func (h *Hub) Run(ctx context.Context) error {
	if len(h.Channels) == 0 {
		return nil
	}
	for _, c := range h.Channels {
		go func(c Channel) {
			for ctx.Err() == nil {
				if err := c.Run(ctx, h); err != nil && ctx.Err() == nil {
					h.logf("%s: %v; retrying in 10s", c.Name(), err)
					sleep(ctx, 10*time.Second)
				}
			}
		}(c)
	}
	after, err := h.Svc.LastEventID(ctx)
	if err != nil {
		return err
	}
	for ctx.Err() == nil {
		evs, err := h.Svc.Events(ctx, after, 200, 30*time.Second)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			h.logf("channels: reading events: %v", err)
			sleep(ctx, 5*time.Second)
			continue
		}
		for _, ev := range evs {
			after = ev.ID
			h.handle(ctx, ev)
		}
	}
	return nil
}

// handle turns one event into notices.
func (h *Hub) handle(ctx context.Context, ev core.Event) {
	if ev.Type == "approval.decided" {
		a, err := h.Svc.GetApproval(ctx, core.HumanCaller, ev.Subject)
		if err != nil {
			return
		}
		for _, c := range h.Channels {
			if r, ok := c.(Resolver); ok {
				r.Resolved(ctx, a)
			}
		}
		return
	}
	n, ok, err := h.NoticeFor(ctx, ev)
	if err != nil {
		h.logf("channels: %s %s: %v", ev.Type, ev.Subject, err)
		return
	}
	if !ok {
		return
	}
	if h.Broadcast(ctx, n) > 0 && ev.Type == "message.sent" {
		// Delivered: the agent's read receipt should say so.
		if err := h.Svc.MarkRead(ctx, core.HumanCaller, ev.Subject); err != nil {
			h.logf("channels: marking %s read: %v", ev.Subject, err)
		}
	}
}

// Broadcast sends a notice to every channel and returns how many took it.
func (h *Hub) Broadcast(ctx context.Context, n Notice) int {
	sent := 0
	for _, c := range h.Channels {
		if err := c.Send(ctx, n); err != nil {
			h.logf("%s: sending %q: %v", c.Name(), n.Title, err)
			continue
		}
		sent++
	}
	return sent
}

func eventData(ev core.Event) map[string]any {
	var d map[string]any
	json.Unmarshal(ev.Data, &d)
	return d
}

// NoticeFor returns the notice for an event, if the event needs the person.
func (h *Hub) NoticeFor(ctx context.Context, ev core.Event) (Notice, bool, error) {
	d := eventData(ev)
	str := func(k string) string { v, _ := d[k].(string); return v }
	switch ev.Type {
	case "approval.requested":
		a, err := h.Svc.GetApproval(ctx, core.HumanCaller, ev.Subject)
		if err != nil {
			return Notice{}, false, err
		}
		if a.Status != core.ApprovalPending {
			return Notice{}, false, nil
		}
		return h.approvalNotice(ctx, a), true, nil
	case "message.sent":
		if str("to") != core.Human {
			return Notice{}, false, nil
		}
		m, err := h.Svc.MessageStatus(ctx, core.HumanCaller, ev.Subject)
		if err != nil {
			return Notice{}, false, err
		}
		n := Notice{Kind: NoticeMessage, Agent: m.From, Task: m.Task, Body: m.Body, Ref: m.ID,
			Title: m.From + " sent you a message"}
		if m.Kind == core.KindQuestion {
			n.Kind = NoticeQuestion
			n.Title = m.From + " asks"
		}
		h.addTitle(ctx, &n)
		return n, true, nil
	case "message.read":
		if ev.Actor == core.Human {
			return Notice{}, false, nil
		}
		var last *core.Message
		ids, _ := d["ids"].([]any)
		for _, id := range ids {
			s, _ := id.(string)
			m, err := h.Svc.MessageStatus(ctx, core.HumanCaller, s)
			if err == nil && m.From == core.Human && m.To == ev.Actor {
				last = &m
			}
		}
		if last == nil {
			return Notice{}, false, nil
		}
		return Notice{Kind: NoticeInfo, Agent: ev.Actor, Task: last.Task, Ref: last.ID,
			Title: ev.Actor + " read your message", Body: "Its response will appear here when it replies through Sheaf."}, true, nil
	case "agent.status":
		state, task, note := str("state"), str("task"), str("note")
		n := Notice{Agent: ev.Actor, Task: task, Body: note}
		switch state {
		case core.StateBlocked:
			// The actionable question/approval already has its own notice.
			// A second terminal notice would route replies to the wrong place.
			pending, err := h.Svc.ListApprovals(ctx, core.ApprovalPending)
			if err != nil {
				return Notice{}, false, err
			}
			for _, a := range pending {
				if a.Agent == ev.Actor && (task == "" || a.Task == task) {
					return Notice{}, false, nil
				}
			}
			n.Kind, n.Title = NoticeBlocked, ev.Actor+" is blocked"
			h.attachScreen(ctx, &n)
		case core.StateDone:
			if task == "" {
				return Notice{}, false, nil
			}
			n.Kind, n.Title = NoticeDone, ev.Actor+" finished a task"
		case core.StateError:
			n.Kind, n.Title = NoticeBlocked, ev.Actor+" hit an error"
		default:
			return Notice{}, false, nil
		}
		h.addTitle(ctx, &n)
		return n, true, nil
	}
	return Notice{}, false, nil
}

// screenWait lets a prompt finish drawing before it is captured.
var screenWait = 800 * time.Millisecond

// attachScreen shows a blocked agent's terminal in the notice, when Sheaf
// runs it: the agent is often stopped on its own prompt (a permission
// dialog, a trust question) that never reached the queue.
func (h *Hub) attachScreen(ctx context.Context, n *Notice) {
	if h.Dispatch == nil {
		return
	}
	sleep(ctx, screenWait)
	s, err := h.Dispatch.Screen(ctx, n.Agent, n.Task, 60)
	if err != nil {
		return // not running in Sheaf's tmux
	}
	n.Terminal = true
	n.Ref = TermRef(n.Agent, n.Task)
	n.Detail = screenTail(s.Text, 18)
	if n.Body == "" {
		n.Body = "It is waiting in its terminal. Tap a key, or reply to type an answer."
	}
}

// screenTail returns the last n non-blank-trailing lines of a screen.
func screenTail(text string, n int) string {
	lines := strings.Split(strings.TrimRight(text, "\n "), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// Press presses a TermKeys key (or "screen" to just look) in the terminal a
// TermRef names, and returns the screen afterwards.
func (h *Hub) Press(ctx context.Context, channel, ref, key string) (string, error) {
	agent, task, ok := ParseTermRef(ref)
	if !ok {
		return "", fmt.Errorf("%w: not a terminal", core.ErrInvalid)
	}
	if h.Dispatch == nil {
		return "", errors.New("this daemon cannot reach terminals (no tmux)")
	}
	switch key {
	case "screen":
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		if err := h.Dispatch.Type(ctx, agent, task, key, false); err != nil {
			return "", err
		}
	default:
		if err := h.Dispatch.Key(ctx, agent, task, key); err != nil {
			return "", err
		}
	}
	if key != "screen" {
		sleep(ctx, screenWait)
	}
	s, err := h.Dispatch.Screen(ctx, agent, task, 60)
	if err != nil {
		return "", err
	}
	return screenTail(s.Text, 18), nil
}

func (h *Hub) approvalNotice(ctx context.Context, a core.Approval) Notice {
	n := Notice{Agent: a.Agent, Task: a.Task, Body: a.Summary, Detail: a.Detail, Ref: a.ID}
	switch a.Kind {
	case core.ApprovalQuestion:
		n.Kind, n.Title = NoticeQuestion, a.Agent+" has a question"
	case core.ApprovalDiff:
		n.Kind, n.Title, n.Decidable = NoticeDiff, a.Agent+" wants a review", true
	default:
		n.Kind, n.Title, n.Decidable = NoticeApproval, a.Agent+" needs your approval", true
	}
	h.addTitle(ctx, &n)
	return n
}

func (h *Hub) addTitle(ctx context.Context, n *Notice) {
	if n.Task == "" {
		return
	}
	if t, err := h.Svc.GetTask(ctx, n.Task); err == nil {
		n.TaskTitle = t.Title
	}
}

// Pending returns notices for everything waiting in the queue, so a channel
// can show it on demand.
func (h *Hub) Pending(ctx context.Context) ([]Notice, error) {
	as, err := h.Svc.ListApprovals(ctx, core.ApprovalPending)
	if err != nil {
		return nil, err
	}
	out := make([]Notice, 0, len(as))
	for _, a := range as {
		out = append(out, h.approvalNotice(ctx, a))
	}
	return out, nil
}

// Decide approves or denies an approval from a button press.
func (h *Hub) Decide(ctx context.Context, channel, id string, approve bool, note string) (core.Approval, error) {
	status := core.ApprovalDenied
	if approve {
		status = core.ApprovalApproved
	}
	return h.Svc.Decide(ctx, caller(channel), core.DecideRequest{ID: id, Status: status, Answer: note})
}

// refRE finds a Sheaf id in the text of a notice the person replied to.
var refRE = regexp.MustCompile(`\b([amt]-[0-9a-z]{8})\b`)

// termRE finds a TermRef, which wins over everything else in a footer.
var termRE = regexp.MustCompile(`\bterm:[a-z0-9][a-z0-9_-]{0,31}(:t-[0-9a-z]{8})?`)

// RefIn returns the last approval, message or task id in text: the one a
// notice names in its footer.
func RefIn(text string) string {
	if t := termRE.FindAllString(text, -1); len(t) > 0 {
		return t[len(t)-1]
	}
	all := refRE.FindAllString(text, -1)
	// Prefer approvals and messages, which are what a reply answers.
	for i := len(all) - 1; i >= 0; i-- {
		if !strings.HasPrefix(all[i], "t-") {
			return all[i]
		}
	}
	if len(all) > 0 {
		return all[len(all)-1]
	}
	return ""
}

var (
	yesWords = map[string]bool{"yes": true, "y": true, "ok": true, "okay": true, "approve": true, "approved": true, "lgtm": true, "go": true, "👍": true, "✅": true, "sure": true}
	noWords  = map[string]bool{"no": true, "n": true, "deny": true, "denied": true, "stop": true, "👎": true, "❌": true}
)

// Reply answers the thing ref names with text: a question gets the text as
// its answer; an approval is approved by "yes"/"ok" and denied by "no" or by
// any other text, which goes to the agent as the reason; a message or task
// gets the text sent to its agent.
func (h *Hub) Reply(ctx context.Context, channel, ref, text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("%w: empty reply", core.ErrInvalid)
	}
	c := caller(channel)
	switch {
	case strings.HasPrefix(ref, "term:"):
		agent, task, _ := ParseTermRef(ref)
		if h.Dispatch == nil {
			return "", errors.New("this daemon cannot reach terminals (no tmux)")
		}
		if err := h.Dispatch.Type(ctx, agent, task, text, true); err != nil {
			return "", err
		}
		return "Typed into " + agent + "'s terminal.", nil
	case strings.HasPrefix(ref, "a-"):
		a, err := h.Svc.GetApproval(ctx, c, ref)
		if err != nil {
			return "", err
		}
		if a.Status != core.ApprovalPending {
			// Already decided: the reply is still worth passing on.
			if _, err := h.Svc.Send(ctx, c, core.SendRequest{To: a.Agent, Task: a.Task, Body: text}); err != nil {
				return "", err
			}
			return fmt.Sprintf("%s was already %s, so I sent your reply to %s as a message.", a.ID, a.Status, a.Agent), nil
		}
		word := strings.ToLower(strings.Trim(text, " .!"))
		d := core.DecideRequest{ID: a.ID}
		switch {
		case a.Kind == core.ApprovalQuestion:
			d.Status, d.Answer = core.ApprovalAnswered, text
		case yesWords[word]:
			d.Status = core.ApprovalApproved
		case noWords[word]:
			d.Status = core.ApprovalDenied
		default:
			d.Status, d.Answer = core.ApprovalDenied, text
		}
		if _, err := h.Svc.Decide(ctx, c, d); err != nil {
			return "", err
		}
		switch d.Status {
		case core.ApprovalAnswered:
			return "Answer sent to " + a.Agent + ".", nil
		case core.ApprovalApproved:
			return "Approved. " + a.Agent + " carries on.", nil
		}
		if d.Answer != "" {
			return "Denied, and your note went to " + a.Agent + ".", nil
		}
		return "Denied.", nil
	case strings.HasPrefix(ref, "m-"):
		m, err := h.Svc.MessageStatus(ctx, c, ref)
		if err != nil {
			return "", err
		}
		to := m.From
		if to == core.Human {
			to = m.To
		}
		return h.message(ctx, c, to, m.Task, text)
	case strings.HasPrefix(ref, "t-"):
		t, err := h.Svc.GetTask(ctx, ref)
		if err != nil {
			return "", err
		}
		if t.Owner == "" {
			return "", fmt.Errorf("%w: task %s has no owner to tell", core.ErrInvalid, t.ID)
		}
		return h.message(ctx, c, t.Owner, t.ID, text)
	}
	return "", fmt.Errorf("%w: nothing to reply to", core.ErrInvalid)
}

func (h *Hub) message(ctx context.Context, c core.Caller, agent, task, text string) (string, error) {
	m, err := h.Svc.Send(ctx, c, core.SendRequest{To: agent, Task: task, Body: text})
	if err != nil {
		return "", err
	}
	ack := "Queued for " + agent + " · " + m.ID + "."
	a, err := h.Svc.GetAgent(ctx, agent)
	if err == nil && a.State == core.StateWorking {
		ack += " It is working; it will read this when its current turn finishes."
	} else if err == nil && a.State == core.StateBlocked {
		ack += " It is waiting for input. Answer its question or approval here; use /screen " + agent + " for a terminal prompt."
	}
	return ack, nil
}

// Help is the command list every channel shows.
const Help = `Talk to your agents:
• Reply to any message from an agent to answer it. On an approval, "yes" approves, "no" denies, anything else denies and tells the agent why.
• /task AGENT what to do: create a task and start AGENT on it. Add repo:NAME when you have more than one repository. Lines after the first become the task's description.
• @AGENT message, or /msg AGENT message: message an agent.
• /approve ID, /deny ID [reason], /answer ID text
• /queue: what is waiting for you
• /tasks: active tasks
• /agents: who is doing what
• /screen AGENT: the last lines of an agent's terminal`

// Command runs a command or message the person typed (not a reply) and
// returns what to answer. Notices for /queue are returned separately so the
// channel can show their buttons.
func (h *Hub) Command(ctx context.Context, channel, text string) (string, []Notice, error) {
	text = strings.TrimSpace(text)
	c := caller(channel)
	if strings.HasPrefix(text, "@") {
		agent, rest := splitWord(text[1:])
		if rest == "" {
			return "", nil, fmt.Errorf("%w: write the message after @%s", core.ErrInvalid, agent)
		}
		r, err := h.message(ctx, c, agent, "", rest)
		return r, nil, err
	}
	if !strings.HasPrefix(text, "/") {
		return "I didn't catch that. Reply to an agent's message to answer it, or:\n\n" + Help, nil, nil
	}
	cmd, rest := splitWord(text[1:])
	// Telegram appends @botname to commands in groups.
	cmd, _, _ = strings.Cut(strings.ToLower(cmd), "@")
	switch cmd {
	case "start", "help":
		return Help, nil, nil
	case "queue", "q":
		ns, err := h.Pending(ctx)
		if err != nil {
			return "", nil, err
		}
		if len(ns) == 0 {
			return "Nothing is waiting for you.", nil, nil
		}
		return fmt.Sprintf("%d item(s) waiting:", len(ns)), ns, nil
	case "tasks":
		r, err := h.tasksText(ctx)
		return r, nil, err
	case "agents", "status":
		r, err := h.agentsText(ctx)
		return r, nil, err
	case "task", "new":
		r, err := h.newTask(ctx, c, rest)
		return r, nil, err
	case "msg", "send", "tell":
		agent, body := splitWord(rest)
		if agent == "" || body == "" {
			return "", nil, fmt.Errorf("%w: use /msg AGENT message", core.ErrInvalid)
		}
		r, err := h.message(ctx, c, strings.TrimPrefix(agent, "@"), "", body)
		return r, nil, err
	case "approve", "yes":
		id, note := splitWord(rest)
		if id == "" {
			return "", nil, fmt.Errorf("%w: use /approve ID", core.ErrInvalid)
		}
		if _, err := h.Decide(ctx, channel, id, true, note); err != nil {
			return "", nil, err
		}
		return "Approved " + id + ".", nil, nil
	case "deny", "no":
		id, note := splitWord(rest)
		if id == "" {
			return "", nil, fmt.Errorf("%w: use /deny ID [reason]", core.ErrInvalid)
		}
		if _, err := h.Decide(ctx, channel, id, false, note); err != nil {
			return "", nil, err
		}
		return "Denied " + id + ".", nil, nil
	case "answer":
		id, body := splitWord(rest)
		if id == "" || body == "" {
			return "", nil, fmt.Errorf("%w: use /answer ID text", core.ErrInvalid)
		}
		r, err := h.Reply(ctx, channel, id, body)
		return r, nil, err
	case "screen":
		agent, _ := splitWord(rest)
		agent = strings.TrimPrefix(agent, "@")
		r, err := h.screen(ctx, agent)
		if err != nil {
			return "", nil, err
		}
		return "", []Notice{{Kind: NoticeInfo, Agent: agent, Title: agent + "'s terminal", Detail: screenTail(r, 18),
			Ref: TermRef(agent, ""), Terminal: true}}, nil
	}
	return "Unknown command /" + cmd + ".\n\n" + Help, nil, nil
}

func splitWord(s string) (string, string) {
	s = strings.TrimSpace(s)
	i := strings.IndexAny(s, " \t\n")
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimSpace(s[i:])
}

func (h *Hub) newTask(ctx context.Context, c core.Caller, rest string) (string, error) {
	agent, body := splitWord(rest)
	agent = strings.TrimPrefix(agent, "@")
	if agent == "" || body == "" {
		return "", fmt.Errorf("%w: use /task AGENT what to do", core.ErrInvalid)
	}
	if _, err := h.Svc.GetAgent(ctx, agent); err != nil {
		names, _ := h.agentNames(ctx)
		return "", fmt.Errorf("%w: no agent %q (agents: %s)", core.ErrInvalid, agent, strings.Join(names, ", "))
	}
	// repo:NAME may appear anywhere on the first line.
	first, detail, _ := strings.Cut(body, "\n")
	var repoName string
	var words []string
	for _, w := range strings.Fields(first) {
		if v, ok := strings.CutPrefix(w, "repo:"); ok {
			repoName = v
			continue
		}
		words = append(words, w)
	}
	title := strings.Join(words, " ")
	if title == "" {
		return "", fmt.Errorf("%w: the task needs a title", core.ErrInvalid)
	}
	repo, err := h.pickRepo(ctx, repoName)
	if err != nil {
		return "", err
	}
	spec := core.TaskSpec{Title: title, Detail: strings.TrimSpace(detail), Owner: agent}
	if repo != "" {
		spec.Repo, spec.Worktree = repo, true
	}
	t, err := h.Svc.CreateTask(ctx, c, spec)
	if err != nil {
		return "", err
	}
	msg := fmt.Sprintf("Created %s for %s: %s", t.ID, agent, t.Title)
	if t.Branch != "" {
		msg += "\nBranch " + t.Branch
	}
	if h.Dispatch == nil {
		return msg + "\nThis daemon cannot start agents (no tmux), so start it on the server.", nil
	}
	if _, err := h.Dispatch.StartTask(ctx, t.ID, ""); err != nil {
		return msg + "\nStarting " + agent + " failed: " + err.Error(), nil
	}
	return msg + "\n" + agent + " is on it.", nil
}

// pickRepo resolves repo:NAME, or the only registered repository.
func (h *Hub) pickRepo(ctx context.Context, name string) (string, error) {
	repos, err := h.Svc.ListRepos(ctx)
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
		return "", fmt.Errorf("%w: no repository %q (registered: %s)", core.ErrInvalid, name, strings.Join(names, ", "))
	}
	switch len(repos) {
	case 0:
		return "", nil
	case 1:
		return repos[0].Path, nil
	}
	return "", fmt.Errorf("%w: say which repository with repo:NAME (one of %s)", core.ErrInvalid, strings.Join(names, ", "))
}

func (h *Hub) agentNames(ctx context.Context) ([]string, error) {
	as, err := h.Svc.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, a := range as {
		out = append(out, a.Name)
	}
	return out, nil
}

func (h *Hub) tasksText(ctx context.Context) (string, error) {
	ts, err := h.Svc.ListTasks(ctx, core.TaskFilter{Active: true})
	if err != nil {
		return "", err
	}
	if len(ts) == 0 {
		return "No active tasks. Create one with /task AGENT what to do.", nil
	}
	var b strings.Builder
	b.WriteString("Active tasks:\n")
	for _, t := range ts {
		fmt.Fprintf(&b, "• %s %s, %s: %s\n", t.ID, t.Owner, t.Status, t.Title)
	}
	return strings.TrimSpace(b.String()), nil
}

func (h *Hub) agentsText(ctx context.Context) (string, error) {
	as, err := h.Svc.ListAgents(ctx)
	if err != nil {
		return "", err
	}
	if len(as) == 0 {
		return "No agents yet. Add one on the server with `sheaf agent add`.", nil
	}
	sort.Slice(as, func(i, j int) bool { return as[i].Name < as[j].Name })
	pending, _ := h.Svc.ListApprovals(ctx, core.ApprovalPending)
	var b strings.Builder
	for _, a := range as {
		fmt.Fprintf(&b, "• %s: %s", a.Name, a.State)
		if a.Note != "" {
			fmt.Fprintf(&b, ", %s", a.Note)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "%d item(s) waiting for you.", len(pending))
	return b.String(), nil
}

func (h *Hub) screen(ctx context.Context, agent string) (string, error) {
	if agent == "" {
		return "", fmt.Errorf("%w: use /screen AGENT", core.ErrInvalid)
	}
	if h.Dispatch == nil {
		return "", errors.New("this daemon cannot show terminals (no tmux)")
	}
	s, err := h.Dispatch.Screen(ctx, agent, "", 30)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(s.Text, "\n "), nil
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
