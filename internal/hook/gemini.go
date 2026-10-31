package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/sensahin/sheaf/internal/api"
	"github.com/sensahin/sheaf/internal/core"
)

// Gemini CLI (and forks such as Qwen Code): a BeforeTool hook in
// settings.json. The answer is {"decision": allow|deny, "reason"}; on deny
// the reason reaches the model as the tool's error. There is no "ask", so an
// unanswered call gets no opinion and Gemini's own confirmation applies.

type geminiOutput struct {
	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

var urlRe = regexp.MustCompile(`https?://[^\s"'<>]+`)

func geminiCall(in PreToolUseInput) (Call, bool) {
	a := fields(in.ToolInput)
	c := Call{Tool: in.ToolName, Cwd: in.Cwd}
	switch in.ToolName {
	case "run_shell_command":
		c.Action, c.Targets = core.ActionShell, []string{str(a, "command")}
	case "write_file", "replace", "edit":
		c.Action, c.Targets = core.ActionWrite, []string{str(a, "file_path", "absolute_path", "path")}
	case "web_fetch":
		// web_fetch takes a prompt with the URLs in it: check each host.
		c.Action = core.ActionNetwork
		for _, u := range urlRe.FindAllString(str(a, "prompt", "url"), -1) {
			c.Targets = append(c.Targets, host(u))
		}
		if len(c.Targets) == 0 {
			return Call{}, false
		}
	case "read_file", "read_many_files", "list_directory", "glob", "search_file_content", "grep",
		"save_memory", "google_web_search", "write_todos":
		return Call{}, false
	default:
		if isSheafTool(in.ToolName) {
			return Call{}, false
		}
		c.Action, c.Targets = core.ActionTool, []string{in.ToolName}
	}
	return c, true
}

// Gemini answers a Gemini CLI BeforeTool hook.
func Gemini(ctx context.Context, client *api.Client, r io.Reader, wait time.Duration) ([]byte, error) {
	var in PreToolUseInput
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return nil, fmt.Errorf("reading hook input: %w", err)
	}
	call, ok := geminiCall(in)
	if !ok {
		return nil, nil
	}
	v := Decide(ctx, client, call, wait)
	if v.Effect == "" {
		return nil, nil
	}
	return json.Marshal(geminiOutput{v.Effect, v.Reason})
}
