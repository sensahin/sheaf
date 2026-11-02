package channels

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sensahin/sheaf/internal/core"
	"github.com/sensahin/sheaf/internal/store"
)

func newService(t *testing.T) *core.Service {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "sheaf.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return core.New(db, filepath.Join(dir, "worktrees"))
}

// fakeTelegram records Bot API calls and serves queued updates.
type fakeTelegram struct {
	mu      sync.Mutex
	calls   []tgCall
	updates chan update
	nextID  int64
}

type tgCall struct {
	Method string
	Body   map[string]any
}

func newFakeTelegram(t *testing.T) (*fakeTelegram, *httptest.Server) {
	f := &fakeTelegram{updates: make(chan update, 10), nextID: 100}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if !strings.HasPrefix(r.URL.Path, "/botTOKEN/") {
			http.Error(w, "bad token", 401)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(b, &body)
		var result any = true
		switch method {
		case "getUpdates":
			var ups []update
			select {
			case u := <-f.updates:
				ups = append(ups, u)
			case <-time.After(50 * time.Millisecond):
			case <-r.Context().Done():
			}
			result = ups
		case "sendMessage":
			f.mu.Lock()
			f.nextID++
			result = map[string]any{"message_id": f.nextID, "chat": map[string]any{"id": body["chat_id"]}}
			f.mu.Unlock()
		case "getMe":
			result = map[string]any{"id": 1, "username": "sheaf_test_bot"}
		}
		f.mu.Lock()
		f.calls = append(f.calls, tgCall{method, body})
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	}))
	t.Cleanup(ts.Close)
	return f, ts
}

// wait returns the first call to method matching pred, waiting up to 3s.
func (f *fakeTelegram) wait(t *testing.T, method string, pred func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for _, c := range f.calls {
			if c.Method == method && (pred == nil || pred(c.Body)) {
				f.mu.Unlock()
				return c.Body
			}
		}
		f.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no %s call", method)
	return nil
}

func textHas(s string) func(map[string]any) bool {
	return func(b map[string]any) bool { v, _ := b["text"].(string); return strings.Contains(v, s) }
}

const chat = 42

func startHub(t *testing.T, svc *core.Service) (*fakeTelegram, context.Context) {
	f, ts := newFakeTelegram(t)
	tg := NewTelegram(TelegramConfig{Token: "TOKEN", ChatID: chat, UserID: 7})
	tg.API().Base = ts.URL
	h := &Hub{Svc: svc, Channels: []Channel{tg}, Logf: t.Logf}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go h.Run(ctx)
	time.Sleep(100 * time.Millisecond) // let Run read the event head
	return f, ctx
}

func addAgent(t *testing.T, svc *core.Service, name string) core.Caller {
	t.Helper()
	if _, _, err := svc.AddAgent(context.Background(), core.HumanCaller, core.AgentSpec{Name: name, Kind: "claude-code"}); err != nil {
		t.Fatal(err)
	}
	return core.Caller{Name: name}
}

func msgFrom(user int64, text string, replyTo string) update {
	m := &tgMessage{MessageID: 9, From: &tgUser{ID: user}, Chat: tgChat{ID: chat, Type: "private"}, Text: text}
	if replyTo != "" {
		m.ReplyTo = &tgMessage{MessageID: 8, Text: replyTo}
	}
	return update{UpdateID: time.Now().UnixNano(), Message: m}
}

func TestTelegramApproveWithButton(t *testing.T) {
	svc := newService(t)
	agent := addAgent(t, svc, "claude")
	f, ctx := startHub(t, svc)

	a, err := svc.RequestApproval(ctx, agent, core.ApprovalRequest{Summary: "run migrations", Detail: "npm run migrate <prod>"})
	if err != nil {
		t.Fatal(err)
	}
	sent := f.wait(t, "sendMessage", textHas("needs your approval"))
	text := sent["text"].(string)
	if !strings.Contains(text, a.ID) || !strings.Contains(text, "&lt;prod&gt;") {
		t.Fatalf("notice text: %s", text)
	}
	kb, _ := json.Marshal(sent["reply_markup"])
	if !strings.Contains(string(kb), "approve:"+a.ID) {
		t.Fatalf("no approve button: %s", kb)
	}

	// Someone else pressing the button is ignored.
	f.updates <- update{UpdateID: 1, CallbackQuery: &callbackQuery{ID: "x", From: tgUser{ID: 666}, Data: "approve:" + a.ID,
		Message: &tgMessage{Chat: tgChat{ID: chat}}}}
	f.updates <- update{UpdateID: 2, CallbackQuery: &callbackQuery{ID: "cb", From: tgUser{ID: 7}, Data: "approve:" + a.ID,
		Message: &tgMessage{Chat: tgChat{ID: chat}}}}
	f.wait(t, "answerCallbackQuery", func(b map[string]any) bool { return b["callback_query_id"] == "cb" })
	got, _ := svc.GetApproval(ctx, core.HumanCaller, a.ID)
	if got.Status != core.ApprovalApproved {
		t.Fatalf("status %s", got.Status)
	}
	// The message loses its buttons and says what happened.
	f.wait(t, "editMessageText", textHas("Approved"))
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.Method == "answerCallbackQuery" && c.Body["callback_query_id"] == "x" {
			t.Fatal("answered a stranger's button press")
		}
	}
}

func TestTelegramAnswerQuestionByReply(t *testing.T) {
	svc := newService(t)
	agent := addAgent(t, svc, "codex")
	f, ctx := startHub(t, svc)

	a, err := svc.RequestApproval(ctx, agent, core.ApprovalRequest{Kind: core.ApprovalQuestion, Summary: "Which colour?"})
	if err != nil {
		t.Fatal(err)
	}
	sent := f.wait(t, "sendMessage", textHas("Which colour?"))
	f.updates <- msgFrom(7, "Green, please", sent["text"].(string))
	f.wait(t, "sendMessage", textHas("Answer sent to codex"))
	got, _ := svc.GetApproval(ctx, core.HumanCaller, a.ID)
	if got.Status != core.ApprovalAnswered || got.Answer != "Green, please" {
		t.Fatalf("approval %+v", got)
	}
}

func TestTelegramMessagesBothWays(t *testing.T) {
	svc := newService(t)
	agent := addAgent(t, svc, "claude")
	f, ctx := startHub(t, svc)

	m, err := svc.Send(ctx, agent, core.SendRequest{To: core.Human, Body: "Deployed the preview", Kind: core.KindMessage})
	if err != nil {
		t.Fatal(err)
	}
	sent := f.wait(t, "sendMessage", textHas("Deployed the preview"))
	// Delivered, so the agent's receipt says read.
	deadline := time.Now().Add(2 * time.Second)
	for {
		st, _ := svc.MessageStatus(ctx, agent, m.ID)
		if st.ReadAt != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("message not marked read after delivery")
		}
		time.Sleep(20 * time.Millisecond)
	}

	f.updates <- msgFrom(7, "Nice, now fix the footer", sent["text"].(string))
	f.wait(t, "sendMessage", textHas("Sent to claude"))
	in, _ := svc.Inbox(ctx, agent, core.InboxRequest{})
	if len(in) != 1 || in[0].Body != "Nice, now fix the footer" || in[0].From != core.Human {
		t.Fatalf("inbox %+v", in)
	}

	// A stranger writing to the bot is ignored.
	f.updates <- update{UpdateID: 99, Message: &tgMessage{From: &tgUser{ID: 666}, Chat: tgChat{ID: 666}, Text: "/agents"}}
	f.updates <- msgFrom(7, "@claude also check mobile", "")
	f.wait(t, "sendMessage", textHas("Sent to claude"))
	f.mu.Lock()
	for _, c := range f.calls {
		if c.Method == "sendMessage" && c.Body["chat_id"] == float64(666) {
			t.Fatal("answered a stranger")
		}
	}
	f.mu.Unlock()
}

func TestCommands(t *testing.T) {
	svc := newService(t)
	addAgent(t, svc, "claude")
	h := &Hub{Svc: svc}
	ctx := context.Background()

	r, _, err := h.Command(ctx, "telegram", "/task claude Redesign the about page\nKeep the logo.")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r, "Created t-") || !strings.Contains(r, "cannot start agents") {
		t.Fatalf("reply %q", r)
	}
	ts, _ := svc.ListTasks(ctx, core.TaskFilter{})
	if len(ts) != 1 || ts[0].Title != "Redesign the about page" || ts[0].Detail != "Keep the logo." || ts[0].Owner != "claude" {
		t.Fatalf("tasks %+v", ts)
	}
	if _, _, err := h.Command(ctx, "telegram", "/task nobody do it"); err == nil || !strings.Contains(err.Error(), "agents: claude") {
		t.Fatalf("unknown agent: %v", err)
	}
	if r, _, _ := h.Command(ctx, "telegram", "/tasks"); !strings.Contains(r, ts[0].ID) {
		t.Fatalf("tasks: %q", r)
	}
	if r, _, _ := h.Command(ctx, "telegram", "/agents"); !strings.Contains(r, "claude: idle") {
		t.Fatalf("agents: %q", r)
	}
	if r, _, _ := h.Command(ctx, "telegram", "hello?"); !strings.Contains(r, "/task AGENT") {
		t.Fatalf("help: %q", r)
	}
	// A reply on a task goes to its owner.
	if _, err := h.Reply(ctx, "telegram", ts[0].ID, "use the new font"); err != nil {
		t.Fatal(err)
	}
	in, _ := svc.Inbox(ctx, core.Caller{Name: "claude"}, core.InboxRequest{})
	if len(in) != 1 || in[0].Task != ts[0].ID {
		t.Fatalf("inbox %+v", in)
	}
}

func TestReplyToApprovalWithText(t *testing.T) {
	svc := newService(t)
	agent := addAgent(t, svc, "claude")
	h := &Hub{Svc: svc}
	ctx := context.Background()
	a1, _ := svc.RequestApproval(ctx, agent, core.ApprovalRequest{Summary: "push"})
	a2, _ := svc.RequestApproval(ctx, agent, core.ApprovalRequest{Summary: "delete branch"})
	if _, err := h.Reply(ctx, "telegram", a1.ID, "ok"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Reply(ctx, "telegram", a2.ID, "keep it, we need it for the release"); err != nil {
		t.Fatal(err)
	}
	g1, _ := svc.GetApproval(ctx, core.HumanCaller, a1.ID)
	g2, _ := svc.GetApproval(ctx, core.HumanCaller, a2.ID)
	if g1.Status != core.ApprovalApproved || g2.Status != core.ApprovalDenied || g2.Answer == "" {
		t.Fatalf("%+v %+v", g1, g2)
	}
}

func TestRefIn(t *testing.T) {
	for in, want := range map[string]string{
		"x needs approval\nReply · a-abcd2345 · t-zzzzzzzz": "a-abcd2345",
		"done · t-abcd2345": "t-abcd2345",
		"nothing here":      "",
		"m-0123abcd":        "m-0123abcd",
	} {
		if got := RefIn(in); got != want {
			t.Errorf("RefIn(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNoSlash(t *testing.T) {
	got := noSlash("• /task AGENT x\nBranch sheaf/t-1 (/queue)")
	if got != "• task AGENT x\nBranch sheaf/t-1 (queue)" {
		t.Fatalf("%q", got)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c, err := LoadConfig(dir)
	if err != nil || len(c.Names()) != 0 {
		t.Fatalf("%v %v", c, err)
	}
	c.Telegram = &TelegramConfig{Token: "t", ChatID: 1}
	if err := SaveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	c2, _ := LoadConfig(dir)
	if c2.Telegram == nil || c2.Telegram.ChatID != 1 || len(c2.Build()) != 1 {
		t.Fatalf("%+v", c2)
	}
}

func TestPairTelegram(t *testing.T) {
	f, ts := newFakeTelegram(t)
	api := &TelegramAPI{Token: "TOKEN", Base: ts.URL}
	said := make(chan string, 1)
	done := make(chan TelegramConfig, 1)
	go func() {
		cfg, err := PairTelegram(context.Background(), api, func(s string) { said <- s })
		if err != nil {
			t.Error(err)
		}
		done <- cfg
	}()
	msg := <-said
	i := strings.Index(msg, "?start=")
	code := strings.Fields(msg[i+len("?start="):])[0]
	// A /start without the code (someone who found the bot) is ignored.
	f.updates <- update{UpdateID: 1, Message: &tgMessage{From: &tgUser{ID: 666}, Chat: tgChat{ID: 666, Type: "private"}, Text: "/start"}}
	f.updates <- update{UpdateID: 2, Message: &tgMessage{From: &tgUser{ID: 7}, Chat: tgChat{ID: 70, Type: "private"}, Text: "/start " + code}}
	select {
	case cfg := <-done:
		if cfg.ChatID != 70 || cfg.UserID != 7 || cfg.Bot != "sheaf_test_bot" || cfg.Token != "TOKEN" {
			t.Fatalf("%+v", cfg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pairing did not finish")
	}
}
