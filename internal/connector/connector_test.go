package connector

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sensahin/sheaf/internal/core"
	"github.com/sensahin/sheaf/internal/store"
)

func setup(t *testing.T) (*Server, *httptest.Server, *core.Service) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "sheaf.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	svc := core.New(db, filepath.Join(dir, "worktrees"))
	s := &Server{Svc: svc, Home: dir, Version: "test"}
	ts := httptest.NewServer(nil)
	s.BaseURL = ts.URL
	ts.Config.Handler = s.Handler()
	t.Cleanup(ts.Close)
	return s, ts, svc
}

func noRedirect() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func register(t *testing.T, base, redirect string) string {
	t.Helper()
	resp, err := http.Post(base+"/register", "application/json",
		strings.NewReader(`{"client_name":"Claude","redirect_uris":["`+redirect+`"]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 201 {
		t.Fatalf("register: %d %v", resp.StatusCode, out)
	}
	return out["client_id"].(string)
}

// signIn runs the whole OAuth flow and returns the access token.
func signIn(t *testing.T, s *Server, base string) string {
	t.Helper()
	const redirect = "https://claude.ai/api/mcp/auth_callback"
	client := register(t, base, redirect)
	verifier := "verifier-verifier-verifier-verifier-verifier-1234"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	q := url.Values{"response_type": {"code"}, "client_id": {client}, "redirect_uri": {redirect},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {"xyz"}}

	resp, err := http.Get(base + "/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("authorize page: %d", resp.StatusCode)
	}

	// A wrong code is refused.
	form := url.Values{}
	for k, v := range q {
		form[k] = v
	}
	form.Set("connect_code", "0000-0000")
	resp, _ = noRedirect().PostForm(base+"/authorize", form)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("wrong code: %d", resp.StatusCode)
	}

	code, _ := s.ConnectCode()
	form.Set("connect_code", strings.ReplaceAll(code, "-", ""))
	resp, err = noRedirect().PostForm(base+"/authorize", form)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 302 {
		t.Fatalf("authorize: %d", resp.StatusCode)
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if !strings.HasPrefix(loc.String(), redirect) || loc.Query().Get("state") != "xyz" {
		t.Fatalf("redirect %s", loc)
	}
	authCode := loc.Query().Get("code")

	// The connect code was used up.
	if s.checkConnect(code) {
		t.Fatal("connect code worked twice")
	}

	tok := url.Values{"grant_type": {"authorization_code"}, "code": {authCode}, "redirect_uri": {redirect},
		"client_id": {client}, "code_verifier": {"wrong"}}
	resp, _ = http.PostForm(base+"/token", tok)
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("bad verifier accepted: %d", resp.StatusCode)
	}
	// The code was burned by the failed attempt; run authorize again.
	code, _ = s.ConnectCode()
	form.Set("connect_code", code)
	resp, _ = noRedirect().PostForm(base+"/authorize", form)
	resp.Body.Close()
	loc, _ = url.Parse(resp.Header.Get("Location"))
	tok.Set("code", loc.Query().Get("code"))
	tok.Set("code_verifier", verifier)
	resp, err = http.PostForm(base+"/token", tok)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 200 || out["token_type"] != "Bearer" {
		t.Fatalf("token: %d %v", resp.StatusCode, out)
	}
	return out["access_token"].(string)
}

type bearer struct {
	tok  string
	base http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.tok)
	return b.base.RoundTrip(r)
}

func TestMetadataAndUnauthorized(t *testing.T) {
	_, ts, _ := setup(t)
	resp, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 || !strings.Contains(resp.Header.Get("WWW-Authenticate"), "/.well-known/oauth-protected-resource") {
		t.Fatalf("unauthenticated: %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
	for _, p := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-authorization-server"} {
		resp, err := http.Get(ts.URL + p)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("%s: %v %v", p, resp, err)
		}
		resp.Body.Close()
	}
	// Plain HTTP callbacks are refused unless on localhost.
	resp, _ = http.Post(ts.URL+"/register", "application/json", strings.NewReader(`{"redirect_uris":["http://evil.example/cb"]}`))
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("http redirect registered: %d", resp.StatusCode)
	}
}

func TestAuthorizeFormPermitsRegisteredCallback(t *testing.T) {
	_, ts, _ := setup(t)
	const redirect = "https://claude.ai/api/mcp/auth_callback"
	client := register(t, ts.URL, redirect)
	q := url.Values{"response_type": {"code"}, "client_id": {client}, "redirect_uri": {redirect},
		"code_challenge": {"challenge"}, "code_challenge_method": {"S256"}}
	for _, valid := range []bool{true, false} {
		if !valid {
			q.Set("redirect_uri", "https://unregistered.example/callback")
		}
		resp, err := http.Get(ts.URL + "/authorize?" + q.Encode())
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		policy := resp.Header.Get("Content-Security-Policy")
		if valid {
			if resp.StatusCode != 200 || !strings.Contains(policy, "form-action 'self' https://claude.ai;") {
				t.Fatalf("registered OAuth callback must be allowed through a form redirect: %d %q", resp.StatusCode, policy)
			}
		} else if resp.StatusCode != 400 || !strings.Contains(policy, "form-action 'self';") || strings.Contains(policy, "unregistered.example") {
			t.Fatalf("unregistered callback widened the form policy: %d %q", resp.StatusCode, policy)
		}
	}
}

func TestSignInAndUseTools(t *testing.T) {
	ctx := context.Background()
	s, ts, svc := setup(t)
	if _, _, err := svc.AddAgent(ctx, core.HumanCaller, core.AgentSpec{Name: "claude", Kind: "claude-code"}); err != nil {
		t.Fatal(err)
	}
	a, err := svc.RequestApproval(ctx, core.Caller{Name: "claude"}, core.ApprovalRequest{Kind: core.ApprovalQuestion, Summary: "Which colour?"})
	if err != nil {
		t.Fatal(err)
	}

	tok := signIn(t, s, ts.URL)
	// The token is a paired device, so it can be listed and revoked.
	ds, _ := svc.ListDevices(ctx, core.HumanCaller)
	if len(ds) != 1 || !strings.Contains(ds[0].Name, "Claude") {
		t.Fatalf("devices %+v", ds)
	}

	cl := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{tok, http.DefaultTransport}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	tools, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) < 8 {
		t.Fatalf("%d tools", len(tools.Tools))
	}
	call := func(name string, args any) string {
		t.Helper()
		r, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		text := r.Content[0].(*mcp.TextContent).Text
		if r.IsError {
			t.Fatalf("%s: %s", name, text)
		}
		return text
	}
	if out := call("overview", map[string]any{}); !strings.Contains(out, a.ID) || !strings.Contains(out, `"claude"`) {
		t.Fatalf("overview: %s", out)
	}
	out := call("create_task", map[string]any{"owner": "claude", "title": "Redesign about page"})
	if !strings.Contains(out, "cannot start agents") {
		t.Fatalf("create_task: %s", out)
	}
	call("decide", map[string]any{"id": a.ID, "decision": "answer", "text": "green"})
	got, _ := svc.GetApproval(ctx, core.HumanCaller, a.ID)
	if got.Status != core.ApprovalAnswered || got.Answer != "green" {
		t.Fatalf("approval %+v", got)
	}
	call("message_agent", map[string]any{"agent": "claude", "body": "thanks"})
	in, _ := svc.Inbox(ctx, core.Caller{Name: "claude"}, core.InboxRequest{})
	if len(in) != 2 { // the decision and the message
		t.Fatalf("inbox %+v", in)
	}

	// Revoking the device signs the app out.
	svc.RevokeDevice(ctx, core.HumanCaller, ds[0].ID)
	req, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("revoked token: %d", resp.StatusCode)
	}
}

func TestOwnerAndAgentTokensRefused(t *testing.T) {
	ctx := context.Background()
	_, ts, svc := setup(t)
	svc.SetOwnerToken(ctx, "owner-secret")
	_, agentTok, _ := svc.AddAgent(ctx, core.HumanCaller, core.AgentSpec{Name: "claude"})
	for _, tok := range []string{"owner-secret", agentTok} {
		req, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, _ := http.DefaultClient.Do(req)
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatalf("token %q: %d", tok[:5], resp.StatusCode)
		}
	}
}

func TestConnectCodeLocksAfterFailures(t *testing.T) {
	now := time.Now()
	s := &Server{Now: func() time.Time { return now }}
	code, fresh := s.ConnectCode()
	if !fresh {
		t.Fatal("first code not fresh")
	}
	for i := 0; i < maxFailures; i++ {
		s.checkConnect("9999-9999")
	}
	if s.checkConnect(code) {
		t.Fatal("code still works after too many failures")
	}
	if _, fresh := s.ConnectCode(); fresh {
		t.Fatal("new code within a minute")
	}
	now = now.Add(2 * time.Minute)
	if c2, fresh := s.ConnectCode(); !fresh || c2 == "" {
		t.Fatal("no new code after lockout")
	}
}
