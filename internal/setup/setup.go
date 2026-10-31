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
	// Mode is the file's permission bits; 0 means 0644.
	Mode os.FileMode
	// Owned files are Sheaf's own and are rewritten freely. Others are the
	// agent's config: Sheaf creates them but never rewrites them.
	Owned bool
}

// Plan is what connecting one agent program takes.
type Plan struct {
	Kind string
	// MCPOnly plans connect messaging and status but no hook: the agent
	// has no pre-tool hook to enforce policy with.
	MCPOnly bool
	Files   []File
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
	"copilot":     copilot,
	"cursor":      cursor,
	"gemini":      gemini,
	"droid":       droid,
	"antigravity": antigravity,
	"cline":       cline,
	"windsurf":    windsurf,
	"junie":       junie,
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

func copilot(sheaf string) Plan {
	cmd := sheaf + " hook copilot"
	return Plan{
		Kind: "copilot",
		Files: []File{{
			Path: ".github/hooks/sheaf.json",
			Content: jsonText(map[string]any{"version": 1, "hooks": map[string]any{
				"preToolUse": []any{map[string]any{"type": "command", "bash": cmd, "timeoutSec": HookTimeout}},
			}}),
			Marker: "hook copilot",
			Owned:  true,
		}},
		Steps: []string{
			"Add Sheaf's MCP server to ~/.copilot/mcp-config.json:\n\n" +
				jsonText(map[string]any{"mcpServers": map[string]any{"sheaf": map[string]any{
					"type": "local", "command": sheaf, "args": []string{"mcp"}, "tools": []string{"*"},
				}}}),
		},
	}
}

func cursor(sheaf string) Plan {
	return Plan{
		Kind: "cursor",
		Files: []File{
			{
				Path: ".cursor/hooks.json",
				Content: jsonText(map[string]any{"version": 1, "hooks": map[string]any{
					"preToolUse": []any{map[string]any{"command": sheaf + " hook cursor", "timeout": HookTimeout}},
				}}),
				Marker: "hook cursor",
			},
			{
				Path: ".cursor/mcp.json",
				Content: jsonText(map[string]any{"mcpServers": map[string]any{"sheaf": map[string]any{
					"command": sheaf, "args": []string{"mcp"},
				}}}),
				Marker: `"sheaf"`,
			},
		},
		Steps: []string{"In the Cursor IDE, agents started from the editor have no SHEAF_TOKEN, so the hook stays out of their way. Run agents through `sheaf start` (cursor-agent) to put them under Sheaf."},
	}
}

func gemini(sheaf string) Plan {
	return Plan{
		Kind: "gemini",
		Files: []File{{
			Path: ".gemini/settings.json",
			Content: jsonText(map[string]any{
				"hooks": map[string]any{"BeforeTool": []any{map[string]any{
					"matcher": ".*",
					"hooks": []any{map[string]any{
						"type": "command", "name": "sheaf", "command": sheaf + " hook gemini",
						"timeout": HookTimeout * 1000, // milliseconds
					}},
				}}},
				"mcpServers": map[string]any{"sheaf": map[string]any{
					"command": sheaf, "args": []string{"mcp"},
					"env": map[string]string{"SHEAF_TOKEN": "$SHEAF_TOKEN", "SHEAF_HOME": "$SHEAF_HOME"},
				}},
			}),
			Marker: "hook gemini",
		}},
		Steps: []string{"Qwen Code reads the same format from .qwen/settings.json: copy the file there to use it."},
	}
}

func droid(sheaf string) Plan {
	h := map[string]any{"type": "command", "command": sheaf + " hook droid", "timeout": HookTimeout}
	return Plan{
		Kind: "droid",
		Files: []File{{
			// A standalone hooks.json keys events directly.
			Path: ".factory/hooks.json",
			Content: jsonText(map[string]any{
				"PreToolUse": []any{map[string]any{"matcher": "*", "hooks": []any{h}}},
			}),
			Marker: "hook droid",
		}},
		Steps: []string{"droid mcp add sheaf \"" + sheaf + " mcp\"   (or add Sheaf as an MCP server with /mcp in Droid)"},
	}
}

func antigravity(sheaf string) Plan {
	h := map[string]any{"type": "command", "command": sheaf + " hook antigravity", "timeout": HookTimeout}
	return Plan{
		Kind: "antigravity",
		Files: []File{{
			// Antigravity's hooks.json groups hooks under a name per tool.
			Path: ".agents/hooks.json",
			Content: jsonText(map[string]any{"sheaf": map[string]any{
				"PreToolUse": []any{map[string]any{"matcher": "*", "hooks": []any{h}}},
			}}),
			Marker: "hook antigravity",
		}},
		Steps: []string{
			"Add Sheaf's MCP server to ~/.gemini/config/mcp_config.json:\n\n" +
				jsonText(map[string]any{"mcpServers": map[string]any{"sheaf": map[string]any{
					"command": sheaf, "args": []string{"mcp"},
				}}}),
		},
	}
}

func cline(sheaf string) Plan {
	return Plan{
		Kind: "cline",
		Files: []File{{
			// Cline runs an executable named after the hook.
			Path:    ".clinerules/hooks/PreToolUse",
			Content: "#!/bin/sh\n# Sheaf: check Cline's tool calls against your Sheaf policy and queue.\nexec " + shellQuote(sheaf) + " hook cline\n",
			Marker:  "hook cline",
			Mode:    0o755,
			Owned:   true,
		}},
		Steps: []string{"Add Sheaf to Cline's MCP servers (MCP Servers > Configure):\n\n" +
			jsonText(map[string]any{"mcpServers": map[string]any{"sheaf": map[string]any{
				"command": sheaf, "args": []string{"mcp"},
			}}})},
	}
}

func windsurf(sheaf string) Plan {
	entry := func() []any {
		return []any{map[string]any{"command": sheaf + " hook windsurf", "show_output": false}}
	}
	return Plan{
		Kind: "windsurf",
		Files: []File{
			{
				Path: ".windsurf/hooks.json",
				Content: jsonText(map[string]any{"hooks": map[string]any{
					"pre_run_command":  entry(),
					"pre_write_code":   entry(),
					"pre_mcp_tool_use": entry(),
				}}),
				Marker: "hook windsurf",
			},
			{
				Path: ".windsurf/mcp_config.json",
				Content: jsonText(map[string]any{"mcpServers": map[string]any{"sheaf": map[string]any{
					"command": sheaf, "args": []string{"mcp"},
				}}}),
				Marker: `"sheaf"`,
			},
		},
		Steps: []string{"Windsurf hooks can only block: a call Sheaf denies is stopped, anything else goes to Windsurf's own approval."},
	}
}

func junie(sheaf string) Plan {
	return Plan{
		Kind:    "junie",
		MCPOnly: true,
		Files: []File{{
			Path: ".junie/mcp/mcp.json",
			Content: jsonText(map[string]any{"mcpServers": map[string]any{"sheaf": map[string]any{
				"command": sheaf, "args": []string{"mcp"},
			}}}),
			Marker: `"sheaf"`,
		}},
		Steps: []string{"Junie has no pre-tool hooks yet, so Sheaf gives it messaging, tasks and status, but cannot hold its tool calls for approval."},
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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
			if err := write(p, f.Content, f.Mode); err != nil {
				return out, err
			}
			out = append(out, Result{Path: f.Path, Status: "created"})
		case err != nil:
			return out, err
		case string(old) == f.Content:
			out = append(out, Result{Path: f.Path, Status: "ok"})
		case f.Owned:
			if err := write(p, f.Content, f.Mode); err != nil {
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

func write(path, content string, mode os.FileMode) error {
	if mode == 0 {
		mode = 0o644
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		return err
	}
	return os.Chmod(path, mode) // WriteFile keeps an existing file's mode
}
