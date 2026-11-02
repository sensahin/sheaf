package channels

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sensahin/sheaf/internal/core"
)

// TelegramConfig is a bot and the one chat it serves.
type TelegramConfig struct {
	Token string `json:"token"`
	// ChatID is the private chat with the person; messages from anywhere else
	// are ignored.
	ChatID int64 `json:"chat_id"`
	// UserID is the person's Telegram user id, checked on every update.
	UserID int64 `json:"user_id"`
	// Bot is the bot's username, for messages that link to it.
	Bot string `json:"bot,omitempty"`
}

// Telegram talks to the person through a Telegram bot. It long-polls the Bot
// API, so the server needs no public address.
type Telegram struct {
	cfg TelegramConfig
	api *TelegramAPI

	mu   sync.Mutex
	sent map[string]sentMsg // approval id -> the message that shows it
}

type sentMsg struct {
	id   int64
	html string
}

// NewTelegram returns the channel for cfg.
func NewTelegram(cfg TelegramConfig) *Telegram {
	return &Telegram{cfg: cfg, api: &TelegramAPI{Token: cfg.Token}, sent: map[string]sentMsg{}}
}

// Name implements Channel.
func (t *Telegram) Name() string { return "telegram" }

// API exposes the client, for tests.
func (t *Telegram) API() *TelegramAPI { return t.api }

// maxText keeps a message under Telegram's 4096-character limit, with room
// for the headline and footer.
const maxDetail = 2500

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 { // do not split a UTF-8 sequence
		cut--
	}
	return s[:cut] + "\n… (" + strconv.Itoa(len(s)-cut) + " more bytes in the Sheaf queue)"
}

var noticeIcon = map[string]string{
	NoticeApproval: "🔐", NoticeDiff: "🔍", NoticeQuestion: "❓", NoticeMessage: "💬",
	NoticeBlocked: "⛔", NoticeDone: "✅", NoticeInfo: "ℹ️",
}

// FormatHTML renders a notice as Telegram HTML.
func FormatHTML(n Notice) string {
	var b strings.Builder
	esc := html.EscapeString
	fmt.Fprintf(&b, "%s <b>%s</b>\n", noticeIcon[n.Kind], esc(n.Title))
	if n.TaskTitle != "" {
		fmt.Fprintf(&b, "<i>%s</i>\n", esc(n.TaskTitle))
	}
	if n.Body != "" {
		b.WriteString("\n" + esc(n.Body) + "\n")
	}
	if n.Detail != "" {
		fmt.Fprintf(&b, "\n<pre>%s</pre>\n", esc(truncate(n.Detail, maxDetail)))
	}
	var foot []string
	switch {
	case n.Kind == NoticeQuestion:
		foot = append(foot, "Reply to answer")
	case n.Decidable:
		foot = append(foot, "Reply with a reason to deny")
	case n.Ref != "" || n.Task != "":
		foot = append(foot, "Reply to message "+n.Agent)
	}
	if n.Ref != "" {
		foot = append(foot, n.Ref)
	}
	if n.Task != "" {
		foot = append(foot, n.Task)
	}
	if len(foot) > 0 {
		fmt.Fprintf(&b, "\n<i>%s</i>", esc(strings.Join(foot, " · ")))
	}
	return strings.TrimSpace(b.String())
}

func decideKeyboard(id string) *inlineKeyboard {
	return &inlineKeyboard{InlineKeyboard: [][]inlineButton{{
		{Text: "✅ Approve", CallbackData: "approve:" + id},
		{Text: "❌ Deny", CallbackData: "deny:" + id},
	}}}
}

// Send implements Channel.
func (t *Telegram) Send(ctx context.Context, n Notice) error {
	text := FormatHTML(n)
	msg := sendMessage{ChatID: t.cfg.ChatID, Text: text, ParseMode: "HTML", LinkPreview: &linkPreview{Disabled: true}}
	if n.Decidable && n.Ref != "" {
		msg.ReplyMarkup = decideKeyboard(n.Ref)
	}
	if n.Kind == NoticeQuestion {
		// Open the reply box straight away.
		msg.ReplyMarkup = &forceReply{ForceReply: true, Placeholder: "Answer " + n.Agent}
	}
	m, err := t.api.SendMessage(ctx, msg)
	if err != nil {
		return err
	}
	if strings.HasPrefix(n.Ref, "a-") {
		t.mu.Lock()
		t.sent[n.Ref] = sentMsg{m.MessageID, text}
		t.mu.Unlock()
	}
	return nil
}

// Resolved implements Resolver: the approval's message loses its buttons and
// says how it was decided.
func (t *Telegram) Resolved(ctx context.Context, a core.Approval) {
	t.mu.Lock()
	s, ok := t.sent[a.ID]
	delete(t.sent, a.ID)
	t.mu.Unlock()
	if !ok {
		return
	}
	mark := map[string]string{core.ApprovalApproved: "✅ Approved", core.ApprovalDenied: "❌ Denied", core.ApprovalAnswered: "💬 Answered"}[a.Status]
	if mark == "" {
		mark = a.Status
	}
	text := s.html + "\n\n<b>" + mark + "</b>"
	if a.Answer != "" {
		text += ": " + html.EscapeString(truncate(a.Answer, 500))
	}
	t.api.call(ctx, "editMessageText", map[string]any{
		"chat_id": t.cfg.ChatID, "message_id": s.id, "text": text, "parse_mode": "HTML",
		"link_preview_options": linkPreview{Disabled: true},
	}, nil)
}

// Run implements Channel: it long-polls for the person's messages.
func (t *Telegram) Run(ctx context.Context, h *Hub) error {
	var offset int64
	for ctx.Err() == nil {
		ups, err := t.api.GetUpdates(ctx, offset, 50)
		if err != nil {
			return err
		}
		for _, u := range ups {
			offset = u.UpdateID + 1
			t.handle(ctx, h, u)
		}
	}
	return nil
}

// allowed reports whether an update comes from the paired person.
func (t *Telegram) allowed(chat, user int64) bool {
	return chat == t.cfg.ChatID && (t.cfg.UserID == 0 || user == t.cfg.UserID)
}

func (t *Telegram) handle(ctx context.Context, h *Hub, u update) {
	if q := u.CallbackQuery; q != nil {
		if q.Message == nil || !t.allowed(q.Message.Chat.ID, q.From.ID) {
			return
		}
		action, id, _ := strings.Cut(q.Data, ":")
		var reply string
		if _, err := h.Decide(ctx, t.Name(), id, action == "approve", ""); err != nil {
			reply = errText(err)
		} else if action == "approve" {
			reply = "Approved"
		} else {
			reply = "Denied"
		}
		t.api.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": q.ID, "text": reply}, nil)
		return
	}
	m := u.Message
	if m == nil || m.From == nil || !t.allowed(m.Chat.ID, m.From.ID) || strings.TrimSpace(m.Text) == "" {
		return
	}
	var reply string
	var notices []Notice
	var err error
	if r := m.ReplyTo; r != nil && !strings.HasPrefix(m.Text, "/") {
		ref := RefIn(r.Text)
		if ref == "" {
			reply = "That message has nothing to answer. Reply to a message from an agent, or send /help."
		} else {
			reply, err = h.Reply(ctx, t.Name(), ref, m.Text)
		}
	} else {
		reply, notices, err = h.Command(ctx, t.Name(), m.Text)
	}
	if err != nil {
		reply = errText(err)
	}
	if reply != "" {
		t.api.SendMessage(ctx, sendMessage{ChatID: m.Chat.ID, Text: reply, ReplyTo: &replyParams{MessageID: m.MessageID}})
	}
	for _, n := range notices {
		t.Send(ctx, n)
	}
}

func errText(err error) string {
	s := err.Error()
	for _, p := range []string{"invalid: ", "forbidden: "} {
		s = strings.TrimPrefix(s, p)
	}
	return "⚠️ " + s
}

// TelegramAPI is a minimal Bot API client.
type TelegramAPI struct {
	Token string
	// Base is the API root; tests point it at a fake server.
	Base   string
	Client *http.Client
}

type tgResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
}

func (a *TelegramAPI) call(ctx context.Context, method string, body, out any) error {
	base := a.Base
	if base == "" {
		base = "https://api.telegram.org"
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/bot"+a.Token+"/"+method, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c := a.Client
	if c == nil {
		c = &http.Client{Timeout: 70 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		// The URL holds the token; never let it reach a log.
		var ue interface{ Unwrap() error }
		if errors.As(err, &ue) {
			err = ue.Unwrap()
		}
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	defer resp.Body.Close()
	var r tgResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&r); err != nil {
		return fmt.Errorf("telegram %s: HTTP %d", method, resp.StatusCode)
	}
	if !r.OK {
		return fmt.Errorf("telegram %s: %s", method, r.Description)
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

type (
	tgUser struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
		Name     string `json:"first_name"`
	}
	tgChat struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	}
	tgMessage struct {
		MessageID int64      `json:"message_id"`
		From      *tgUser    `json:"from"`
		Chat      tgChat     `json:"chat"`
		Text      string     `json:"text"`
		ReplyTo   *tgMessage `json:"reply_to_message"`
	}
	callbackQuery struct {
		ID      string     `json:"id"`
		From    tgUser     `json:"from"`
		Message *tgMessage `json:"message"`
		Data    string     `json:"data"`
	}
	update struct {
		UpdateID      int64          `json:"update_id"`
		Message       *tgMessage     `json:"message"`
		CallbackQuery *callbackQuery `json:"callback_query"`
	}
	inlineButton struct {
		Text         string `json:"text"`
		CallbackData string `json:"callback_data"`
	}
	inlineKeyboard struct {
		InlineKeyboard [][]inlineButton `json:"inline_keyboard"`
	}
	forceReply struct {
		ForceReply  bool   `json:"force_reply"`
		Placeholder string `json:"input_field_placeholder,omitempty"`
	}
	linkPreview struct {
		Disabled bool `json:"is_disabled"`
	}
	replyParams struct {
		MessageID int64 `json:"message_id"`
	}
	sendMessage struct {
		ChatID      int64        `json:"chat_id"`
		Text        string       `json:"text"`
		ParseMode   string       `json:"parse_mode,omitempty"`
		ReplyMarkup any          `json:"reply_markup,omitempty"`
		ReplyTo     *replyParams `json:"reply_parameters,omitempty"`
		LinkPreview *linkPreview `json:"link_preview_options,omitempty"`
	}
)

// GetMe returns the bot's user.
func (a *TelegramAPI) GetMe(ctx context.Context) (tgUser, error) {
	var u tgUser
	return u, a.call(ctx, "getMe", struct{}{}, &u)
}

// GetUpdates long-polls for updates after offset.
func (a *TelegramAPI) GetUpdates(ctx context.Context, offset int64, timeout int) ([]update, error) {
	var ups []update
	err := a.call(ctx, "getUpdates", map[string]any{
		"offset": offset, "timeout": timeout, "allowed_updates": []string{"message", "callback_query"},
	}, &ups)
	return ups, err
}

// SendMessage sends a message.
func (a *TelegramAPI) SendMessage(ctx context.Context, m sendMessage) (tgMessage, error) {
	var out tgMessage
	if len(m.Text) > 4096 {
		m.Text = truncate(m.Text, 4000)
		m.ParseMode = "" // a cut can break HTML tags
	}
	return out, a.call(ctx, "sendMessage", m, &out)
}

// PairTelegram connects a bot to the person's chat: it prints a link that
// opens the bot with a one-time code, waits for that /start, and returns the
// config to save. say prints instructions.
func PairTelegram(ctx context.Context, api *TelegramAPI, say func(string)) (TelegramConfig, error) {
	me, err := api.GetMe(ctx)
	if err != nil {
		return TelegramConfig{}, fmt.Errorf("checking the bot token: %w", err)
	}
	// A webhook blocks getUpdates.
	api.call(ctx, "deleteWebhook", map[string]any{"drop_pending_updates": true}, nil)
	code := pairCode()
	say(fmt.Sprintf("Bot @%s is ready. On your phone, open this link and press Start:\n\n  https://t.me/%s?start=%s\n\nWaiting up to 10 minutes...", me.Username, me.Username, code))
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	var offset int64
	for {
		ups, err := api.GetUpdates(ctx, offset, 25)
		if err != nil {
			if ctx.Err() != nil {
				return TelegramConfig{}, errors.New("no /start arrived in 10 minutes; run the command again")
			}
			return TelegramConfig{}, err
		}
		for _, u := range ups {
			offset = u.UpdateID + 1
			m := u.Message
			if m == nil || m.From == nil || m.Chat.Type != "private" {
				continue
			}
			if strings.TrimSpace(m.Text) != "/start "+code {
				continue
			}
			cfg := TelegramConfig{Token: api.Token, ChatID: m.Chat.ID, UserID: m.From.ID, Bot: me.Username}
			// Acknowledge the updates so the daemon does not see them again.
			api.GetUpdates(ctx, offset, 0)
			api.SendMessage(ctx, sendMessage{ChatID: m.Chat.ID, Text: "Paired with Sheaf. Your agents will message you here once the daemon restarts.\n\n" + Help})
			return cfg, nil
		}
	}
}

func pairCode() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, 10)
	rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}
