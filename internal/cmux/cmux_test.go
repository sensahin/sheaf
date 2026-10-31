package cmux

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sensahin/sheaf/internal/core"
)

func TestDetectAndReport(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	os.WriteFile(filepath.Join(dir, "cmux"), []byte("#!/bin/sh\nprintf '%s|' \"$@\" >> "+log+"\necho >> "+log+"\n"), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	t.Setenv("CMUX_WORKSPACE_ID", "")
	if _, ok := Detect(); ok {
		t.Fatal("detected cmux outside cmux")
	}
	t.Setenv("CMUX_WORKSPACE_ID", "ws-1")
	t.Setenv("CMUX_SURFACE_ID", "sf-1")
	w, ok := Detect()
	if !ok {
		t.Fatal("not detected")
	}
	ctx := context.Background()
	w.Report(ctx, "builder", core.StatusReport{State: core.StateWorking, Note: "Writing tests"}, nil)
	w.Report(ctx, "builder", core.StatusReport{State: core.StateBlocked, Note: "needs permission: git push"}, nil)
	w.Release(ctx, "builder")

	b, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	want := []string{
		"set-status|sheaf-builder|builder: working · Writing tests|--workspace|ws-1|--color|#D9962B|",
		"set-status|sheaf-builder|builder: blocked · needs permission: git push|--workspace|ws-1|--color|#E5484D|",
		"notify|--title|Sheaf: builder needs you|--body|needs permission: git push|",
		"clear-status|sheaf-builder|--workspace|ws-1|",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s", b)
	}
}
