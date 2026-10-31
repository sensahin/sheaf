// Package setup writes the config that connects an agent program to Sheaf:
// its MCP server entry and its pre-tool hook.
package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// HookTimeout is how long agents should let `sheaf hook` run, in seconds:
// the hook's own 600-second wait for your decision, plus slack.
const HookTimeout = 660

// File is one config file, relative to the project directory.
type File struct {
	Path    string
	Content string
	// Marker is text that shows the file is already set up for Sheaf.
	Marker string
	// Owned files are Sheaf's own and are rewritten freely. Others are the
	// agent's config: Sheaf creates them but never rewrites them.
	Owned bool
}

// Plan is what connecting one agent program takes.
type Plan struct {
	Kind  string
	Files []File
	// Steps are commands or notes for what Sheaf does not write itself.
	Steps []string
}

// Kinds lists the agent programs Sheaf can set up.
func Kinds() []string {
	k := make([]string, 0, len(plans))
	for name := range plans {
		k = append(k, name)
	}
	sort.Strings(k)
	return k
}

var plans = map[string]func(sheaf string) Plan{
	"claude-code": claudeCode,
	"codex":       codex,
	"opencode":    openCode,
}

// For returns the plan for kind, with sheaf as the path to the binary.
func For(kind, sheaf string) (Plan, error) {
	f, ok := plans[kind]
	if !ok {
		return Plan{}, fmt.Errorf("no setup for %q (have %s)", kind, strings.Join(Kinds(), ", "))
	}
	return f(sheaf), nil
}

func jsonText(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	enc.Encode(v)
	return b.String()
}

func preToolUse(command string, extra map[string]any) string {
	h := map[string]any{"type": "command", "command": command, "timeout": HookTimeout}
	for k, v := range extra {
		h[k] = v
	}
	return jsonText(map[string]any{"hooks": map[string]any{
		"PreToolUse": []any{map[string]any{"matcher": "*", "hooks": []any{h}}},
	}})
}

func claudeCode(sheaf string) Plan {
	cmd := sheaf + " hook claude-code"
	return Plan{
		Kind:  "claude-code",
		Files: []File{{Path: ".claude/settings.json", Content: preToolUse(cmd, nil), Marker: "hook claude-code"}},
		Steps: []string{"claude mcp add --scope user sheaf -- " + sheaf + " mcp"},
	}
}

func codex(sheaf string) Plan {
	cmd := sheaf + " hook codex"
	return Plan{
		Kind: "codex",
		Files: []File{{
			Path:    ".codex/hooks.json",
			Content: preToolUse(cmd, map[string]any{"statusMessage": "Checking with Sheaf"}),
			Marker:  "hook codex",
		}},
		Steps: []string{
			"Add Sheaf's MCP server to ~/.codex/config.toml:\n\n" +
				"[mcp_servers.sheaf]\n" +
				"command = " + tomlString(sheaf) + "\n" +
				"args = [\"mcp\"]\n" +
				"env_vars = [\"SHEAF_TOKEN\", \"SHEAF_HOME\"]",
			"Codex runs project hooks only once you trust them: run /hooks in Codex and approve Sheaf's.",
		},
	}
}

func openCode(sheaf string) Plan {
	return Plan{
		Kind: "opencode",
		Files: []File{
			{Path: ".opencode/plugins/sheaf.js", Content: openCodePlugin(sheaf), Marker: "hook opencode", Owned: true},
			{
				Path: "opencode.json",
				Content: jsonText(map[string]any{
					"$schema": "https://opencode.ai/config.json",
					"mcp": map[string]any{"sheaf": map[string]any{
						"type": "local", "command": []string{sheaf, "mcp"}, "enabled": true,
					}},
				}),
				Marker: `"sheaf"`,
			},
		},
	}
}

func tomlString(s string) string {
	b, _ := json.Marshal(s) // JSON string escapes are valid TOML basic strings
	return string(b)
}

func openCodePlugin(sheaf string) string {
	return `// Sheaf for OpenCode: checks each tool call against your Sheaf policy and
// sends the ones that need you to your Sheaf queue. Written by ` + "`sheaf setup opencode`" + `.
const SHEAF = ` + string(must(json.Marshal(sheaf))) + `

export const Sheaf = async ({ $, directory }) => ({
  "tool.execute.before": async (input, output) => {
    // Outside Sheaf (no SHEAF_TOKEN) the hook has no opinion; skip the call.
    if (!process.env.SHEAF_TOKEN) return
    const payload = JSON.stringify({ tool: input.tool, args: output.args, cwd: directory })
    const res = await $` + "`${SHEAF} hook opencode < ${new Response(payload)}`" + `.quiet().nothrow()
    if (res.exitCode !== 0) return // Sheaf unreachable: OpenCode's own permissions apply
    let verdict
    try {
      verdict = JSON.parse(res.stdout.toString())
    } catch {
      return
    }
    if (verdict.decision === "deny") {
      throw new Error(verdict.reason || "Sheaf denied this call")
    }
  },
})
`
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

// Result says what Apply did with one file.
type Result struct {
	Path   string
	Status string // "created", "updated", "ok" (already set up), "skipped"
	// Content to add by hand, for a skipped file.
	Content string
}

// Apply writes plan's files under dir. It creates missing files, rewrites
// Sheaf's own, and leaves an existing agent config alone: one that already
// mentions Sheaf is "ok", any other is "skipped" with what to add.
func Apply(dir string, plan Plan) ([]Result, error) {
	var out []Result
	for _, f := range plan.Files {
		p := filepath.Join(dir, f.Path)
		old, err := os.ReadFile(p)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if err := write(p, f.Content); err != nil {
				return out, err
			}
			out = append(out, Result{Path: f.Path, Status: "created"})
		case err != nil:
			return out, err
		case string(old) == f.Content:
			out = append(out, Result{Path: f.Path, Status: "ok"})
		case f.Owned:
			if err := write(p, f.Content); err != nil {
				return out, err
			}
			out = append(out, Result{Path: f.Path, Status: "updated"})
		case strings.Contains(string(old), f.Marker):
			out = append(out, Result{Path: f.Path, Status: "ok"})
		default:
			out = append(out, Result{Path: f.Path, Status: "skipped", Content: f.Content})
		}
	}
	return out, nil
}

func write(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
