package desktop

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sensahin/sheaf/internal/api"
	"github.com/sensahin/sheaf/internal/config"
	"github.com/sensahin/sheaf/internal/core"
	"github.com/sensahin/sheaf/internal/store"
)

// daemon runs a real API server on a Unix socket under a short temp dir
// (macOS caps socket paths near 104 bytes).
func daemon(t *testing.T) config.Paths {
	t.Helper()
	dir, err := os.MkdirTemp("", "shd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	paths := config.At(dir)
	tok, err := paths.LoadOrCreateOwnerToken()
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(paths.DB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	svc := core.New(db, paths.WorktreeRoot)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := svc.SetOwnerToken(ctx, tok); err != nil {
		t.Fatal(err)
	}
	go api.NewServer(svc).ListenUnix(ctx, paths.Socket)
	for i := 0; !Running(paths.Socket); i++ {
		if i > 100 {
			t.Fatal("daemon did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return paths
}

func owner(t *testing.T, paths config.Paths) *api.Client {
	tok, err := paths.LoadOrCreateOwnerToken()
	if err != nil {
		t.Fatal(err)
	}
	return api.NewClient(paths.Socket, tok)
}

func TestDeviceTokenPairsOnceAndRepairsAfterRevoke(t *testing.T) {
	ctx := context.Background()
	paths := daemon(t)
	tok, err := DeviceToken(ctx, paths)
	if err != nil {
		t.Fatal(err)
	}
	again, err := DeviceToken(ctx, paths)
	if err != nil || again != tok {
		t.Fatalf("second call paired again: %v", err)
	}
	if st, _ := os.Stat(filepath.Join(paths.Home, "desktop.token")); st.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode %v", st.Mode())
	}
	var devs []core.Device
	if err := owner(t, paths).Call(ctx, "device.list", nil, &devs); err != nil || len(devs) != 1 || devs[0].Name != DeviceName {
		t.Fatalf("devices %+v %v", devs, err)
	}
	if err := owner(t, paths).Call(ctx, "device.revoke", api.IDReq{ID: devs[0].ID}, nil); err != nil {
		t.Fatal(err)
	}
	fresh, err := DeviceToken(ctx, paths)
	if err != nil || fresh == tok {
		t.Fatalf("did not re-pair after revoke: %v", err)
	}
}

func TestHandlerServesPageAndProxiesAPI(t *testing.T) {
	ctx := context.Background()
	paths := daemon(t)
	tok, err := DeviceToken(ctx, paths)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(Handler(paths.Socket))
	defer ts.Close()
	base := ts.URL

	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "Needs you") || resp.Header.Get("Content-Security-Policy") == "" {
		t.Fatalf("page: %d", resp.StatusCode)
	}

	call := func(token string) (int, string) {
		req, _ := http.NewRequest("POST", base+"/v1/whoami", strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := call(tok); code != 200 || !strings.Contains(body, `"human"`) {
		t.Fatalf("whoami through proxy: %d %s", code, body)
	}
	if code, _ := call("nope"); code != http.StatusForbidden {
		t.Fatalf("bad token got %d", code)
	}
	// Only POST reaches the daemon.
	resp, err = http.Get(base + "/v1/whoami")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatal("GET /v1 was proxied")
	}
}

func TestProxyReportsDaemonDown(t *testing.T) {
	ts := httptest.NewServer(Handler(filepath.Join(t.TempDir(), "none.sock")))
	defer ts.Close()
	resp, err := http.Post(ts.URL+"/v1/whoami", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var e struct{ Error string }
	json.NewDecoder(resp.Body).Decode(&e)
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(e.Error, "daemon") {
		t.Fatalf("%d %q", resp.StatusCode, e.Error)
	}
}

func TestAlertFor(t *testing.T) {
	ev := func(typ, actor string, data any) core.Event {
		b, _ := json.Marshal(data)
		return core.Event{Type: typ, Actor: actor, Data: b}
	}
	cases := []struct {
		ev    core.Event
		title string
		ok    bool
	}{
		{ev("approval.requested", "cc", map[string]string{"kind": "action", "summary": "rm -rf build"}), "cc needs your approval", true},
		{ev("approval.requested", "cc", map[string]string{"kind": "diff", "summary": "review"}), "cc wants a review", true},
		{ev("approval.requested", "cc", map[string]string{"kind": "question", "summary": "which db?"}), "cc has a question", true},
		{ev("message.sent", "cc", map[string]string{"to": "human", "kind": "handoff"}), "New handoff from cc", true},
		{ev("message.sent", "cc", map[string]string{"to": "codex"}), "", false},
		{ev("agent.status", "cc", map[string]string{"state": "blocked", "note": "needs a key"}), "cc is blocked", true},
		{ev("agent.status", "cc", map[string]string{"state": "working"}), "", false},
		{ev("task.created", "cc", nil), "", false},
	}
	for _, c := range cases {
		a, ok := AlertFor(c.ev)
		if ok != c.ok || a.Title != c.title {
			t.Errorf("%s %s: got %q %v", c.ev.Type, c.ev.Data, a.Title, ok)
		}
	}
}

func TestWatchNotifiesForNewQueueItems(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	paths := daemon(t)
	tok, err := DeviceToken(ctx, paths)
	if err != nil {
		t.Fatal(err)
	}
	var added api.AddAgentResp
	if err := owner(t, paths).Call(ctx, "agent.add", core.AgentSpec{Name: "cc"}, &added); err != nil {
		t.Fatal(err)
	}
	got := make(chan Alert, 4)
	go Watch(ctx, api.NewClient(paths.Socket, tok), func(a Alert) { got <- a })
	time.Sleep(200 * time.Millisecond) // let Watch read the head first
	agent := api.NewClient(paths.Socket, added.Token)
	if err := agent.Call(ctx, "approval.request", core.ApprovalRequest{Summary: "push to main"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case a := <-got:
		if a.Title != "cc needs your approval" || a.Body != "push to main" {
			t.Fatalf("%+v", a)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no notification")
	}
}

func TestNotifyCommandsKeepTextOutOfScripts(t *testing.T) {
	a := Alert{Title: `"; do shell script "x`, Body: "$(whoami)"}
	mac := notifyCommand("darwin", a)
	if n := len(mac.Args); mac.Args[n-2] != a.Title || mac.Args[n-1] != a.Body {
		t.Fatalf("osascript args %q", mac.Args)
	}
	win := notifyCommand("windows", a)
	if strings.Contains(strings.Join(win.Args, " "), a.Body) {
		t.Fatal("toast text spliced into the PowerShell command")
	}
	found := false
	for _, e := range win.Env {
		found = found || e == "SHEAF_ALERT_BODY="+a.Body
	}
	if !found {
		t.Fatal("toast text not passed in the environment")
	}
}
