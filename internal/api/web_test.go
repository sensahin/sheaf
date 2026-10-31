package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sensahin/sheaf/internal/core"
	"github.com/sensahin/sheaf/internal/store"
)

func TestWebQueue(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "sheaf.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := core.New(db, dir)
	svc.SetOwnerToken(ctx, "owner")
	srv := NewServer(svc)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	srv.WebURL = ts.URL

	for path, want := range map[string]string{"/": "Pair this device", "/app.js": "approval.decide", "/style.css": "--band", "/manifest.webmanifest": "standalone"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(b), want) {
			t.Fatalf("GET %s: %d", path, resp.StatusCode)
		}
		if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "script-src 'self'") {
			t.Fatalf("GET %s: no CSP", path)
		}
	}

	post := func(token, method, body string) (int, map[string]any) {
		req, _ := http.NewRequest("POST", ts.URL+"/v1/"+method, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	if code, _ := post("", "approval.list", "{}"); code != http.StatusForbidden {
		t.Fatalf("unauthenticated call: %d", code)
	}
	code, pair := post("owner", "device.pair", `{"name":"phone"}`)
	if code != 200 {
		t.Fatalf("pair: %d %v", code, pair)
	}
	url, _ := pair["url"].(string)
	tok, _ := pair["token"].(string)
	if !strings.HasPrefix(url, ts.URL+"/#t=") || !strings.HasSuffix(url, tok) {
		t.Fatalf("pair url %q", url)
	}
	if code, who := post(tok, "whoami", ""); code != 200 || who["name"] != "human" {
		t.Fatalf("device whoami: %d %v", code, who)
	}
	if code, _ := post(tok, "device.pair", `{}`); code != http.StatusForbidden {
		t.Fatalf("device paired a device: %d", code)
	}
	if code, head := post(tok, "events.head", ""); code != 200 || head["id"].(float64) < 1 {
		t.Fatalf("events.head: %d %v", code, head)
	}
}
