// Package connector lets the person drive Sheaf from the Claude and ChatGPT
// apps, by serving Sheaf as a remote MCP server ("custom connector").
//
// The apps reach it from the internet, so it runs on its own listener that
// serves only this: the MCP endpoint at /mcp and the OAuth endpoints the apps
// use to sign in. The web queue and the API stay private.
//
// Sign-in is OAuth 2.1 with PKCE and dynamic client registration, with Sheaf
// as its own single-user authorization server. To approve a sign-in, the
// person types a one-time connect code that Sheaf sends to their Telegram or
// Slack, or that `sheaf connector code` prints on the server. The access token
// the app receives is a paired-device token: it shows in `sheaf device ls`
// and `sheaf device revoke` signs the app out.
package connector

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sensahin/sheaf/internal/core"
)

// Server is the connector's HTTP side.
type Server struct {
	Svc *core.Service
	// Launcher starts agents; nil when the daemon cannot.
	Launcher Launcher
	// BaseURL is the public HTTPS address the apps reach, without a trailing
	// slash, for example https://box.tail1234.ts.net:8443.
	BaseURL string
	// Home is $SHEAF_HOME, where registered apps are remembered.
	Home    string
	Version string
	// Notify sends the connect code to the person's chat channels and
	// reports whether any took it. Nil when there are none.
	Notify func(ctx context.Context, text string) bool
	// Now is the clock; tests replace it.
	Now func() time.Time

	once    sync.Once
	mu      sync.Mutex
	clients map[string]Client
	codes   map[string]authCode
	connect connectCode
}

// Launcher starts agents and shows their terminals.
type Launcher interface {
	StartTask(ctx context.Context, id, lead string) (core.StartResult, error)
	Screen(ctx context.Context, agent, task string, lines int) (core.Screen, error)
}

// Client is an app that registered itself (RFC 7591).
type Client struct {
	ID           string    `json:"client_id"`
	Name         string    `json:"client_name,omitempty"`
	RedirectURIs []string  `json:"redirect_uris"`
	CreatedAt    time.Time `json:"created_at"`
}

type authCode struct {
	client, redirect, challenge string
	expires                     time.Time
}

type connectCode struct {
	code     string
	made     time.Time
	expires  time.Time
	failures int
}

const (
	connectTTL  = 10 * time.Minute
	authCodeTTL = 5 * time.Minute
	maxFailures = 5
)

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) init() {
	s.once.Do(func() {
		s.clients = map[string]Client{}
		s.codes = map[string]authCode{}
		s.load()
	})
}

func (s *Server) statePath() string { return filepath.Join(s.Home, "connector.json") }

func (s *Server) load() {
	if s.Home == "" {
		return
	}
	b, err := os.ReadFile(s.statePath())
	if err != nil {
		return
	}
	var st struct {
		Clients map[string]Client `json:"clients"`
	}
	if json.Unmarshal(b, &st) == nil && st.Clients != nil {
		s.clients = st.Clients
	}
}

// save writes registered clients; the caller holds mu.
func (s *Server) save() error {
	if s.Home == "" {
		return nil
	}
	b, err := json.MarshalIndent(map[string]any{"clients": s.clients}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.statePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.statePath())
}

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// ConnectCode returns the current connect code, making a new one when there
// is none or it expired. Fresh reports whether it was just made.
func (s *Server) ConnectCode() (code string, fresh bool) {
	s.init()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.connect.code != "" && s.now().Before(s.connect.expires) && s.connect.failures < maxFailures {
		return s.connect.code, false
	}
	// Anyone who can reach the sign-in page can burn a code with wrong
	// guesses; making at most one a minute bounds both guessing and the
	// messages it sends you.
	if !s.connect.made.IsZero() && s.now().Sub(s.connect.made) < time.Minute && s.connect.code != "" {
		return s.connect.code, false
	}
	const digits = "0123456789"
	b := make([]byte, 8)
	rand.Read(b)
	for i := range b {
		b[i] = digits[int(b[i])%10]
	}
	s.connect = connectCode{code: string(b[:4]) + "-" + string(b[4:]), made: s.now(), expires: s.now().Add(connectTTL)}
	return s.connect.code, true
}

// checkConnect verifies a typed code. A code is single-use and dies after
// maxFailures wrong guesses.
func (s *Server) checkConnect(typed string) bool {
	typed = strings.TrimSpace(strings.ReplaceAll(typed, " ", ""))
	if len(typed) == 8 && !strings.Contains(typed, "-") {
		typed = typed[:4] + "-" + typed[4:]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := &s.connect
	if c.code == "" || s.now().After(c.expires) || c.failures >= maxFailures {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(typed), []byte(c.code)) != 1 {
		c.failures++
		return false
	}
	*c = connectCode{made: c.made}
	return true
}

// Handler serves the connector: OAuth metadata and endpoints, and /mcp.
func (s *Server) Handler() http.Handler {
	s.init()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.protectedResource)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/", s.protectedResource)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.authServerMeta)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server/", s.authServerMeta)
	mux.HandleFunc("GET /.well-known/openid-configuration", s.authServerMeta)
	mux.HandleFunc("POST /register", s.register)
	mux.HandleFunc("GET /authorize", s.authorizePage)
	mux.HandleFunc("POST /authorize", s.authorize)
	mux.HandleFunc("POST /token", s.token)
	mux.Handle("/mcp", s.mcpHandler())
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "Sheaf connector. Add %s/mcp as a custom connector in Claude or ChatGPT.\n", s.BaseURL)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func oauthErr(w http.ResponseWriter, code int, kind, desc string) {
	writeJSON(w, code, map[string]string{"error": kind, "error_description": desc})
}

func (s *Server) protectedResource(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"resource":                 s.BaseURL + "/mcp",
		"authorization_servers":    []string{s.BaseURL},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "Sheaf",
	})
}

func (s *Server) authServerMeta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"issuer":                                s.BaseURL,
		"authorization_endpoint":                s.BaseURL + "/authorize",
		"token_endpoint":                        s.BaseURL + "/token",
		"registration_endpoint":                 s.BaseURL + "/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none", "client_secret_post", "client_secret_basic"},
		"scopes_supported":                      []string{"sheaf"},
	})
}

// validRedirect allows HTTPS callbacks, and plain HTTP only on this machine
// (for desktop clients).
func validRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" || u.Host == "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	h := u.Hostname()
	return u.Scheme == "http" && (h == "localhost" || h == "127.0.0.1" || h == "::1")
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		oauthErr(w, 400, "invalid_client_metadata", "body must be JSON")
		return
	}
	if len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > 10 {
		oauthErr(w, 400, "invalid_redirect_uri", "give one to ten redirect_uris")
		return
	}
	for _, u := range req.RedirectURIs {
		if !validRedirect(u) {
			oauthErr(w, 400, "invalid_redirect_uri", "redirect_uris must be https (or http on localhost): "+u)
			return
		}
	}
	if len(req.Name) > 100 {
		req.Name = req.Name[:100]
	}
	c := Client{ID: "sc_" + randomString(16), Name: req.Name, RedirectURIs: req.RedirectURIs, CreatedAt: s.now().UTC()}
	s.mu.Lock()
	if len(s.clients) >= 200 {
		// Registration is open to anyone who can reach this address, so
		// bound it; the oldest registrations go first.
		var oldest string
		for id, x := range s.clients {
			if oldest == "" || x.CreatedAt.Before(s.clients[oldest].CreatedAt) {
				oldest = id
			}
		}
		delete(s.clients, oldest)
	}
	s.clients[c.ID] = c
	err := s.save()
	s.mu.Unlock()
	if err != nil {
		oauthErr(w, 500, "server_error", err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{
		"client_id": c.ID, "client_name": c.Name, "redirect_uris": c.RedirectURIs,
		"client_id_issued_at":        c.CreatedAt.Unix(),
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code"},
		"response_types":             []string{"code"},
	})
}

func (s *Server) client(id string) (Client, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.clients[id]
	return c, ok
}

type authParams struct {
	ClientID, RedirectURI, State, Challenge, Method, Scope, Resource string
}

func readAuthParams(v url.Values) authParams {
	return authParams{
		ClientID: v.Get("client_id"), RedirectURI: v.Get("redirect_uri"), State: v.Get("state"),
		Challenge: v.Get("code_challenge"), Method: v.Get("code_challenge_method"),
		Scope: v.Get("scope"), Resource: v.Get("resource"),
	}
}

// check validates the request; the returned message is shown on the page,
// since a bad client or redirect must not be redirected to.
func (s *Server) check(p authParams) (Client, string) {
	c, ok := s.client(p.ClientID)
	if !ok {
		return c, "This app is not registered with Sheaf. Remove the connector in the app and add it again."
	}
	allowed := false
	for _, u := range c.RedirectURIs {
		if u == p.RedirectURI {
			allowed = true
		}
	}
	if !allowed {
		return c, "The app's redirect address does not match its registration."
	}
	if p.Challenge == "" || p.Method != "S256" {
		return c, "The app must use PKCE with S256."
	}
	return c, ""
}

var page = template.Must(template.New("authorize").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Connect to Sheaf</title>
<style>
:root{--bg:#f6f3ec;--fg:#1f2a1f;--muted:#5b6658;--card:#fff;--accent:#2f6b3a;--err:#a33}
@media (prefers-color-scheme:dark){:root{--bg:#141813;--fg:#e9eee6;--muted:#a3ad9f;--card:#1d231c;--accent:#7fbf86;--err:#f08080}}
body{margin:0;background:var(--bg);color:var(--fg);font:16px/1.5 system-ui,-apple-system,sans-serif;display:flex;min-height:100vh;align-items:center;justify-content:center}
main{background:var(--card);max-width:420px;width:calc(100% - 32px);padding:28px;border-radius:14px;box-shadow:0 2px 16px #0002}
h1{font-size:22px;margin:0 0 8px}p{color:var(--muted);margin:8px 0}
input{font:inherit;font-size:24px;letter-spacing:4px;text-align:center;width:100%;box-sizing:border-box;padding:10px;margin:16px 0;border-radius:8px;border:1px solid var(--muted);background:transparent;color:var(--fg)}
button{font:inherit;font-weight:600;width:100%;padding:12px;border:0;border-radius:8px;background:var(--accent);color:var(--bg);cursor:pointer}
.err{color:var(--err)}b{color:var(--fg)}
</style></head><body><main>
<h1>Connect {{.App}} to Sheaf</h1>
{{if .Fatal}}<p class="err">{{.Fatal}}</p>{{else}}
<p><b>{{.App}}</b> wants to manage your agents: create tasks, message agents and decide approvals. You will be sent back to <b>{{.Host}}</b>.</p>
<p>{{.Where}}</p>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post" action="authorize">
{{range $k, $v := .Hidden}}<input type="hidden" name="{{$k}}" value="{{$v}}">{{end}}
<input name="connect_code" inputmode="numeric" autocomplete="one-time-code" placeholder="1234-5678" required autofocus>
<button type="submit">Connect</button>
</form>{{end}}
</main></body></html>`))

type pageData struct {
	App, Host, Where, Error, Fatal string
	CallbackOrigin                 string
	Hidden                         map[string]string
}

func (s *Server) renderPage(w http.ResponseWriter, code int, d pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	formAction := "'self'"
	if d.CallbackOrigin != "" {
		// Chrome also checks form-action on the redirect after the POST.
		// Permit only the callback origin already validated for this client.
		formAction += " " + d.CallbackOrigin
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action "+formAction+"; frame-ancestors 'none'")
	w.WriteHeader(code)
	page.Execute(w, d)
}

func (s *Server) pageFor(r *http.Request, p authParams, c Client) pageData {
	app := c.Name
	if app == "" {
		app = "An app"
	}
	host := p.RedirectURI
	origin := ""
	if u, err := url.Parse(p.RedirectURI); err == nil {
		host = u.Host
		origin = u.Scheme + "://" + u.Host
	}
	return pageData{App: app, Host: host, CallbackOrigin: origin, Hidden: map[string]string{
		"client_id": p.ClientID, "redirect_uri": p.RedirectURI, "state": p.State,
		"code_challenge": p.Challenge, "code_challenge_method": p.Method, "scope": p.Scope, "resource": p.Resource,
		"response_type": "code",
	}}
}

func (s *Server) authorizePage(w http.ResponseWriter, r *http.Request) {
	p := readAuthParams(r.URL.Query())
	c, bad := s.check(p)
	if bad != "" {
		s.renderPage(w, 400, pageData{App: "an app", Fatal: bad})
		return
	}
	if r.URL.Query().Get("response_type") != "code" {
		s.renderPage(w, 400, pageData{App: "an app", Fatal: "Only response_type=code is supported."})
		return
	}
	d := s.pageFor(r, p, c)
	code, fresh := s.ConnectCode()
	sent := false
	if s.Notify != nil {
		if fresh {
			msg := fmt.Sprintf("🔑 Connect code for %s: %s\n\nType it on the Sheaf sign-in page. It works once, for 10 minutes. If you are not connecting an app right now, ignore this.", d.App, code)
			sent = s.Notify(r.Context(), msg)
		} else {
			sent = true // it was sent when it was made
		}
	}
	if sent {
		d.Where = "Sheaf just sent you a connect code on Telegram or Slack. You can also run `sheaf connector code` on the server."
	} else {
		d.Where = "Get a connect code by running `sheaf connector code` on the server."
	}
	s.renderPage(w, 200, d)
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		s.renderPage(w, 400, pageData{App: "an app", Fatal: "Bad form."})
		return
	}
	p := readAuthParams(r.PostForm)
	c, bad := s.check(p)
	if bad != "" {
		s.renderPage(w, 400, pageData{App: "an app", Fatal: bad})
		return
	}
	if !s.checkConnect(r.PostForm.Get("connect_code")) {
		d := s.pageFor(r, p, c)
		d.Where = "Get a connect code by running `sheaf connector code` on the server, or from your Telegram or Slack."
		d.Error = "That code is wrong or expired. After five wrong tries a new code is needed."
		s.renderPage(w, 403, d)
		return
	}
	code := randomString(24)
	s.mu.Lock()
	for k, v := range s.codes {
		if s.now().After(v.expires) {
			delete(s.codes, k)
		}
	}
	s.codes[code] = authCode{client: c.ID, redirect: p.RedirectURI, challenge: p.Challenge, expires: s.now().Add(authCodeTTL)}
	s.mu.Unlock()
	u, _ := url.Parse(p.RedirectURI)
	q := u.Query()
	q.Set("code", code)
	if p.State != "" {
		q.Set("state", p.State)
	}
	q.Set("iss", s.BaseURL)
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		oauthErr(w, 400, "invalid_request", "bad form")
		return
	}
	f := r.PostForm
	if f.Get("grant_type") != "authorization_code" {
		oauthErr(w, 400, "unsupported_grant_type", "only authorization_code is supported")
		return
	}
	clientID := f.Get("client_id")
	if id, _, ok := r.BasicAuth(); ok && clientID == "" {
		clientID = id
	}
	s.mu.Lock()
	ac, ok := s.codes[f.Get("code")]
	delete(s.codes, f.Get("code")) // single use, even when the rest fails
	s.mu.Unlock()
	if !ok || s.now().After(ac.expires) || ac.client != clientID || ac.redirect != f.Get("redirect_uri") {
		oauthErr(w, 400, "invalid_grant", "the code is wrong, expired or was issued to another client")
		return
	}
	sum := sha256.Sum256([]byte(f.Get("code_verifier")))
	if subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(ac.challenge)) != 1 {
		oauthErr(w, 400, "invalid_grant", "PKCE verification failed")
		return
	}
	c, _ := s.client(clientID)
	name := "connector"
	if c.Name != "" {
		name = "connector: " + c.Name
	}
	if len(name) > 64 {
		name = name[:64]
	}
	_, tok, err := s.Svc.PairDevice(r.Context(), core.HumanCaller, name)
	if err != nil {
		oauthErr(w, 500, "server_error", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"access_token": tok, "token_type": "Bearer", "scope": "sheaf"})
}

type callerKey struct{}

// authenticate resolves the bearer token. Only a paired device (which is what
// sign-in issues) is accepted here, not the owner token or an agent's.
func (s *Server) authenticate(r *http.Request) (core.Caller, error) {
	h := r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(h, "Bearer ")
	if !ok || tok == "" {
		return core.Caller{}, errors.New("missing token")
	}
	c, err := s.Svc.Authenticate(r.Context(), tok)
	if err != nil {
		return core.Caller{}, err
	}
	if !c.IsHuman() || c.Device == "" {
		return core.Caller{}, errors.New("this token cannot be used here")
	}
	return c, nil
}

func (s *Server) unauthorized(w http.ResponseWriter, desc string) {
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata=%q, error="invalid_token", error_description=%q`,
		s.BaseURL+"/.well-known/oauth-protected-resource", desc))
	writeJSON(w, 401, map[string]string{"error": "invalid_token", "error_description": desc})
}
