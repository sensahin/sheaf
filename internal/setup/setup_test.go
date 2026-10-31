package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlansAreValid(t *testing.T) {
	for _, kind := range Kinds() {
		p, err := For(kind, "/opt/bin/sheaf")
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range p.Files {
			if !strings.Contains(f.Content, f.Marker) {
				t.Errorf("%s %s: content lacks its own marker %q", kind, f.Path, f.Marker)
			}
			if strings.HasSuffix(f.Path, ".json") {
				var v any
				if err := json.Unmarshal([]byte(f.Content), &v); err != nil {
					t.Errorf("%s %s: invalid JSON: %v", kind, f.Path, err)
				}
			}
			if !strings.Contains(f.Content, "/opt/bin/sheaf") {
				t.Errorf("%s %s: does not use the sheaf path", kind, f.Path)
			}
		}
	}
	if _, err := For("nope", "sheaf"); err == nil {
		t.Fatal("unknown kind accepted")
	}
}

func TestApplyNeverRewritesAgentConfig(t *testing.T) {
	dir := t.TempDir()
	p, _ := For("opencode", "sheaf")
	res, err := Apply(dir, p)
	if err != nil || res[0].Status != "created" || res[1].Status != "created" {
		t.Fatalf("first apply: %+v %v", res, err)
	}
	res, _ = Apply(dir, p)
	if res[0].Status != "ok" || res[1].Status != "ok" {
		t.Fatalf("second apply: %+v", res)
	}

	// The user's own opencode.json without Sheaf in it is left untouched;
	// Sheaf's plugin file is rewritten.
	cfg := filepath.Join(dir, "opencode.json")
	mine := `{"model": "anthropic/claude"}`
	os.WriteFile(cfg, []byte(mine), 0o644)
	os.WriteFile(filepath.Join(dir, ".opencode/plugins/sheaf.js"), []byte("old"), 0o644)
	res, _ = Apply(dir, p)
	if res[0].Status != "updated" || res[1].Status != "skipped" || res[1].Content == "" {
		t.Fatalf("third apply: %+v", res)
	}
	if b, _ := os.ReadFile(cfg); string(b) != mine {
		t.Fatalf("opencode.json rewritten: %s", b)
	}
}
