package herdr

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sensahin/sheaf/internal/core"
)

func TestDetectAndReport(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "herdr")
	os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s|' \"$@\" >> "+log+"\necho >> "+log+"\n"), 0o755)

	t.Setenv("HERDR_ENV", "")
	if _, ok := Detect(); ok {
		t.Fatal("detected herdr outside herdr")
	}
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "w1:p2")
	t.Setenv("HERDR_BIN_PATH", bin)
	p, ok := Detect()
	if !ok || p.ID != "w1:p2" {
		t.Fatalf("detect: %+v %v", p, ok)
	}

	ctx := context.Background()
	if err := p.Report(ctx, "builder", core.StatusReport{State: core.StateBlocked, Note: "needs permission:\ngit push"}, []string{"sheaf", "run", "--agent", "builder", "--", "claude", "--continue"}); err != nil {
		t.Fatal(err)
	}
	if err := p.Report(ctx, "builder", core.StatusReport{State: core.StateDone}, nil); err != nil {
		t.Fatal(err)
	}
	if err := p.Release(ctx, "builder"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 3 {
		t.Fatalf("calls: %q", b)
	}
	first := strings.Split(strings.TrimSuffix(lines[0], "|"), "|")
	want := []string{"pane", "report-agent", "w1:p2", "--source", "sheaf", "--agent", "builder", "--state", "blocked", "--seq", first[10],
		"--message", "needs permission: git push", "--", "sheaf", "run", "--agent", "builder", "--", "claude", "--continue"}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("first call:\n got %q\nwant %q", first, want)
	}
	if !strings.Contains(lines[1], "--state|idle|") || strings.Contains(lines[1], "--|") {
		t.Fatalf("done should report idle with no resume: %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], "pane|release-agent|w1:p2|") {
		t.Fatalf("release: %q", lines[2])
	}
	// Sequence numbers grow.
	seq := func(l string) string { f := strings.Split(l, "|"); return f[len(f)-2] }
	if !(first[10] < seq(lines[2])) {
		t.Fatalf("seq did not grow: %s then %s", first[10], seq(lines[2]))
	}
}
