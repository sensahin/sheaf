package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/sensahin/sheaf/internal/core"
)

// SlackConfig is a Slack app in Socket Mode and the person it serves.
type SlackConfig struct {
	// BotToken (xoxb-) posts messages.
	BotToken string `json:"bot_token"`
	// AppToken (xapp-, scope connections:write) opens the Socket Mode
	// connection, so the server needs no public address.
	AppToken string `json:"app_token"`
	// User is the person's Slack user id; everyone else is ignored.
	User string `json:"user"`
	// Channel is the direct-message channel with the person.
	Channel string `json:"channel"`
}

// Slack talks to the person in a direct message with a Slack app.
type Slack struct {
	cfg SlackConfig
	api *SlackAPI

	mu   sync.Mutex
	refs map[string]string  // message ts -> the ref it shows
	sent map[string]sentMsg // approval id -> message (ts in html field)
}

// NewSlack returns the channel for cfg.
func NewSlack(cfg SlackConfig) *Slack {
	return &Slack{cfg: cfg, api: &SlackAPI{BotToken: cfg.BotToken, AppToken: cfg.AppToken},
		refs: map[string]string{}, sent: map[string]sentMsg{}}
}

// Name implements Channel.
func (s *Slack) Name() string { return "slack" }

// slackEsc escapes text for Slack's mrkdwn.
func slackEsc(t string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(t)
}

// FormatSlack renders a notice as Slack mrkdwn.
func FormatSlack(n Notice) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s *%s*\n", noticeIcon[n.Kind], slackEsc(n.Title))
	if n.TaskTitle != "" {
		fmt.Fprintf(&b, "_%s_\n", slackEsc(n.TaskTitle))
	}
	if n.Body != "" {
		b.WriteString("\n" + slackEsc(n.Body) + "\n")
	}
	if n.Detail != "" {
		fmt.Fprintf(&b, "```%s```\n", slackEsc(truncate(n.Detail, maxDetail)))
	}
	var foot []string
	switch {
	case n.Kind == NoticeQuestion:
		foot = append(foot, "Reply in thread to answer")
	case n.Decidable:
		foot = append(foot, "Reply in thread with a reason to deny")
	case n.Ref != "" || n.Task != "":
		foot = append(foot, "Reply in thread to message "+n.Agent)
	}
	if n.Ref != "" {
		foot = append(foot, n.Ref)
	}
	if n.Task != "" {
		foot = append(foot, n.Task)
	}
	if len(foot) > 0 {
		fmt.Fprintf(&b, "\n_%s_", strings.Join(foot, " · "))
	}
	return strings.TrimSpace(b.String())
}

func slackBlocks(text string, decide string) []any {
	blocks := []any{map[string]any{"type": "section", "text": map[string]string{"type": "mrkdwn", "text": text}}}
	if decide != "" {
		blocks = append(blocks, map[string]any{"type": "actions", "elements": []any{
			map[string]any{"type": "button", "style": "primary", "action_id": "approve", "value": decide,
				"text": map[string]string{"type": "plain_text", "text": "Approve"}},
			map[string]any{"type": "button", "style": "danger", "action_id": "deny", "value": decide,
				"text": map[string]string{"type": "plain_text", "text": "Deny"}},
		}})
	}
	return blocks
}

// Send implements Channel.
func (s *Slack) Send(ctx context.Context, n Notice) error {
	text := FormatSlack(n)
	if len(text) > 2900 { // a section block holds 3000 characters
		text = truncate(text, 2800)
	}
	decide := ""
	if n.Decidable {
		decide = n.Ref
	}
	ts, err := s.api.Post(ctx, s.cfg.Channel, "", n.Title, slackBlocks(text, decide))
	if err != nil {
		return err
	}
	ref := n.Ref
	if ref == "" {
		ref = n.Task
	}
	s.mu.Lock()
	if ref != "" {
		s.refs[ts] = ref
	}
	if strings.HasPrefix(n.Ref, "a-") {
		s.sent[n.Ref] = sentMsg{html: ts + "\x00" + text}
	}
	s.mu.Unlock()
	return nil
}

// Resolved implements Resolver.
func (s *Slack) Resolved(ctx context.Context, a core.Approval) {
	s.mu.Lock()
	m, ok := s.sent[a.ID]
	delete(s.sent, a.ID)
	s.mu.Unlock()
	if !ok {
		return
	}
	ts, text, _ := strings.Cut(m.html, "\x00")
	mark := map[string]string{core.ApprovalApproved: "✅ Approved", core.ApprovalDenied: "❌ Denied", core.ApprovalAnswered: "💬 Answered"}[a.Status]
	text += "\n\n*" + mark + "*"
	if a.Answer != "" {
		text += ": " + slackEsc(truncate(a.Answer, 500))
	}
	s.api.call(ctx, s.api.BotToken, "chat.update", map[string]any{
		"channel": s.cfg.Channel, "ts": ts, "text": mark, "blocks": slackBlocks(text, ""),
	}, nil)
}

// slackWords are commands that work without the slash, since Slack keeps
// "/word" for its own slash commands.
var slackWords = map[string]bool{"help": true, "queue": true, "tasks": true, "agents": true, "status": true,
	"task": true, "new": true, "msg": true, "tell": true, "approve": true, "deny": true, "answer": true, "screen": true}

// Run implements Channel: it holds a Socket Mode connection.
func (s *Slack) Run(ctx context.Context, h *Hub) error {
	return s.api.Socket(ctx, func(typ string, payload json.RawMessage) {
		switch typ {
		case "events_api":
			var p struct {
				Event slackEvent `json:"event"`
			}
			if json.Unmarshal(payload, &p) == nil {
				s.onMessage(ctx, h, p.Event)
			}
		case "interactive":
			s.onAction(ctx, h, payload)
		}
	})
}

type slackEvent struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	User        string `json:"user"`
	BotID       string `json:"bot_id"`
	Text        string `json:"text"`
	Channel     string `json:"channel"`
	ChannelType string `json:"channel_type"`
	TS          string `json:"ts"`
	ThreadTS    string `json:"thread_ts"`
}

func (s *Slack) onMessage(ctx context.Context, h *Hub, e slackEvent) {
	if e.Type != "message" || e.Subtype != "" || e.BotID != "" || e.User != s.cfg.User || e.Channel != s.cfg.Channel {
		return
	}
	text := slackUnescape(strings.TrimSpace(e.Text))
	if text == "" {
		return
	}
	var reply string
	var notices []Notice
	var err error
	if e.ThreadTS != "" && e.ThreadTS != e.TS {
		ref := s.refFor(ctx, e.ThreadTS)
		if ref == "" {
			reply = "That thread has nothing to answer. Reply in the thread of a message from an agent, or send help."
		} else {
			reply, err = h.Reply(ctx, s.Name(), ref, text)
		}
	} else {
		if w, _ := splitWord(text); slackWords[strings.ToLower(w)] {
			text = "/" + text
		}
		reply, notices, err = h.Command(ctx, s.Name(), text)
		reply = noSlash(reply) // commands work without the slash here
	}
	if err != nil {
		reply = errText(err)
	}
	if reply != "" {
		s.api.Post(ctx, s.cfg.Channel, e.ThreadTS, reply, nil)
	}
	for _, n := range notices {
		s.Send(ctx, n)
	}
}

// refFor finds what a thread's parent message refers to.
func (s *Slack) refFor(ctx context.Context, ts string) string {
	s.mu.Lock()
	ref := s.refs[ts]
	s.mu.Unlock()
	if ref != "" {
		return ref
	}
	// Sent before the daemon restarted: read the parent's footer.
	var out struct {
		Messages []struct {
			Text   string `json:"text"`
			Blocks []struct {
				Text struct {
					Text string `json:"text"`
				} `json:"text"`
			} `json:"blocks"`
		} `json:"messages"`
	}
	err := s.api.call(ctx, s.api.BotToken, "conversations.history", map[string]any{
		"channel": s.cfg.Channel, "latest": ts, "inclusive": true, "limit": 1,
	}, &out)
	if err != nil || len(out.Messages) == 0 {
		return ""
	}
	text := out.Messages[0].Text
	for _, b := range out.Messages[0].Blocks {
		text += "\n" + b.Text.Text
	}
	return RefIn(text)
}

func (s *Slack) onAction(ctx context.Context, h *Hub, payload json.RawMessage) {
	var p struct {
		Type string `json:"type"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
		Message struct {
			TS string `json:"ts"`
		} `json:"message"`
		Actions []struct {
			ActionID string `json:"action_id"`
			Value    string `json:"value"`
		} `json:"actions"`
	}
	if json.Unmarshal(payload, &p) != nil || p.Type != "block_actions" || p.User.ID != s.cfg.User || len(p.Actions) == 0 {
		return
	}
	act := p.Actions[0]
	if act.ActionID != "approve" && act.ActionID != "deny" {
		return
	}
	if _, err := h.Decide(ctx, s.Name(), act.Value, act.ActionID == "approve", ""); err != nil {
		s.api.Post(ctx, s.cfg.Channel, p.Message.TS, errText(err), nil)
	}
	// Success shows up as the message update from Resolved.
}

var slashRE = regexp.MustCompile(`(^|[\s(])/([a-z])`)

// noSlash drops the slash from commands in help text: Slack keeps "/word"
// for its own slash commands, so Sheaf's work without it there.
func noSlash(t string) string { return slashRE.ReplaceAllString(t, "$1$2") }

func slackUnescape(t string) string {
	return strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&").Replace(t)
}

// SlackAPI is a minimal Web API and Socket Mode client.
type SlackAPI struct {
	BotToken string
	AppToken string
	// Base is the API root; tests point it at a fake server.
	Base   string
	Client *http.Client
}

func (a *SlackAPI) call(ctx context.Context, token, method string, body, out any) error {
	base := a.Base
	if base == "" {
		base = "https://slack.com/api"
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/"+method, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)
	c := a.Client
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("slack %s: %w", method, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	var r struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("slack %s: HTTP %d", method, resp.StatusCode)
	}
	if !r.OK {
		return fmt.Errorf("slack %s: %s", method, r.Error)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// Post sends a message, in a thread when threadTS is set, and returns its ts.
func (a *SlackAPI) Post(ctx context.Context, channel, threadTS, text string, blocks []any) (string, error) {
	body := map[string]any{"channel": channel, "text": text, "unfurl_links": false}
	if threadTS != "" {
		body["thread_ts"] = threadTS
	}
	if blocks != nil {
		body["blocks"] = blocks
	}
	var out struct {
		TS string `json:"ts"`
	}
	err := a.call(ctx, a.BotToken, "chat.postMessage", body, &out)
	return out.TS, err
}

// Socket holds a Socket Mode connection, acknowledging every envelope and
// passing its payload to handle, until ctx ends or the connection drops.
func (a *SlackAPI) Socket(ctx context.Context, handle func(typ string, payload json.RawMessage)) error {
	var open struct {
		URL string `json:"url"`
	}
	if err := a.call(ctx, a.AppToken, "apps.connections.open", struct{}{}, &open); err != nil {
		return err
	}
	if _, err := url.Parse(open.URL); err != nil || open.URL == "" {
		return errors.New("slack apps.connections.open: no URL")
	}
	conn, _, err := websocket.Dial(ctx, open.URL, nil)
	if err != nil {
		return fmt.Errorf("slack socket: %w", err)
	}
	conn.SetReadLimit(4 << 20)
	defer conn.CloseNow()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("slack socket: %w", err)
		}
		var env struct {
			EnvelopeID string          `json:"envelope_id"`
			Type       string          `json:"type"`
			Payload    json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		if env.EnvelopeID != "" {
			ack, _ := json.Marshal(map[string]string{"envelope_id": env.EnvelopeID})
			if err := conn.Write(ctx, websocket.MessageText, ack); err != nil {
				return fmt.Errorf("slack socket: %w", err)
			}
		}
		if env.Type == "disconnect" {
			return nil // Slack asks for a reconnect now and then
		}
		go handle(env.Type, env.Payload)
	}
}

// PairSlack waits for the person to send the bot a one-time code in a
// direct message, then returns the config to save.
func PairSlack(ctx context.Context, api *SlackAPI, say func(string)) (SlackConfig, error) {
	var auth struct {
		User   string `json:"user"`
		UserID string `json:"user_id"`
		Team   string `json:"team"`
	}
	if err := api.call(ctx, api.BotToken, "auth.test", struct{}{}, &auth); err != nil {
		return SlackConfig{}, fmt.Errorf("checking the bot token: %w", err)
	}
	code := pairCode()
	say(fmt.Sprintf("App @%s is ready in %s. In Slack, open a direct message with @%s and send:\n\n  pair %s\n\nWaiting up to 10 minutes...", auth.User, auth.Team, auth.User, code))
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	found := make(chan SlackConfig, 1)
	errc := make(chan error, 1)
	go func() {
		for ctx.Err() == nil {
			err := api.Socket(ctx, func(typ string, payload json.RawMessage) {
				if typ != "events_api" {
					return
				}
				var p struct {
					Event slackEvent `json:"event"`
				}
				if json.Unmarshal(payload, &p) != nil {
					return
				}
				e := p.Event
				if e.Type != "message" || e.BotID != "" || e.ChannelType != "im" || strings.TrimSpace(e.Text) != "pair "+code {
					return
				}
				select {
				case found <- SlackConfig{BotToken: api.BotToken, AppToken: api.AppToken, User: e.User, Channel: e.Channel}:
				default:
				}
			})
			if err != nil {
				errc <- err
				return
			}
		}
	}()
	select {
	case cfg := <-found:
		api.Post(context.Background(), cfg.Channel, "", "Paired with Sheaf. Your agents will message you here once the daemon restarts.\n\n"+noSlash(Help), nil)
		return cfg, nil
	case err := <-errc:
		return SlackConfig{}, err
	case <-ctx.Done():
		return SlackConfig{}, errors.New("no pairing message arrived in 10 minutes; run the command again")
	}
}
