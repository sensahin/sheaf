// Package setup writes the config that connects an agent program to Sheaf:
// its MCP server entry and its pre-tool hook.
package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	// Commands are run by `sheaf setup` when their program is installed,
	// and printed for you to run otherwise.
	Commands []Command
}

// Command is one command `sheaf setup` can run for you.
type Command struct {
	Args []string
	// Check succeeds when the command's work is already done.
	Check []string
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
	"goose":       goose,
	"amp":         amp,
	"kiro":        kiro,
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

// StatusTimeout bounds a status hook, in seconds: it only posts one report.
const StatusTimeout = 10

func preToolUse(command string, extra map[string]any, status map[string]string, sheaf string) string {
	h := map[string]any{"type": "command", "command": command, "timeout": HookTimeout}
	for k, v := range extra {
		h[k] = v
	}
	events := map[string]any{
		"PreToolUse": []any{map[string]any{"matcher": "*", "hooks": []any{h}}},
	}
	for ev, g := range statusGroups(sheaf, status, StatusTimeout) {
		events[ev] = g
	}
	return jsonText(map[string]any{"hooks": events})
}

// statusGroups builds Claude-shaped hook groups that report a status when an
// agent event fires: event name -> Sheaf state.
func statusGroups(sheaf string, status map[string]string, timeout int) map[string]any {
	out := map[string]any{}
	for ev, state := range status {
		out[ev] = []any{map[string]any{"hooks": []any{map[string]any{
			"type": "command", "command": sheaf + " hook status " + state, "timeout": timeout,
		}}}}
	}
	return out
}

// claudeStatus is the status reporting for agents with Claude Code's events:
// a prompt starts work, a notification means it waits on you (a permission
// prompt or a question), a finished tool call means it is back at work, and
// the end of a turn leaves it idle.
var claudeStatus = map[string]string{
	"UserPromptSubmit": "working",
	"PostToolUse":      "working",
	"Notification":     "blocked",
	"Stop":             "idle",
}

func claudeCode(sheaf string) Plan {
	cmd := sheaf + " hook claude-code"
	return Plan{
		Kind:  "claude-code",
		Files: []File{{Path: ".claude/settings.json", Content: preToolUse(cmd, nil, claudeStatus, sheaf), Marker: "hook claude-code"}},
		Commands: []Command{{
			Args:  []string{"claude", "mcp", "add", "--scope", "user", "sheaf", "--", sheaf, "mcp"},
			Check: []string{"claude", "mcp", "get", "sheaf"},
		}},
	}
}

func codex(sheaf string) Plan {
	cmd := sheaf + " hook codex"
	return Plan{
		Kind: "codex",
		Files: []File{{
			Path: ".codex/hooks.json",
			Content: preToolUse(cmd, map[string]any{"statusMessage": "Checking with Sheaf"},
				map[string]string{"UserPromptSubmit": "working", "Stop": "idle"}, sheaf),
			Marker: "hook codex",
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
				"preToolUse":          []any{map[string]any{"type": "command", "bash": cmd, "timeoutSec": HookTimeout}},
				"userPromptSubmitted": []any{map[string]any{"type": "command", "bash": sheaf + " hook status working", "timeoutSec": StatusTimeout}},
				"notification":        []any{map[string]any{"type": "command", "bash": sheaf + " hook status blocked", "timeoutSec": StatusTimeout}},
				"agentStop":           []any{map[string]any{"type": "command", "bash": sheaf + " hook status idle", "timeoutSec": StatusTimeout}},
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
					"stop":       []any{map[string]any{"command": sheaf + " hook status idle", "timeout": StatusTimeout}},
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
				"hooks": geminiHooks(sheaf),
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

func geminiHooks(sheaf string) map[string]any {
	// Gemini's timeouts are in milliseconds.
	hooks := statusGroups(sheaf, map[string]string{
		"BeforeAgent": "working", "Notification": "blocked", "AfterAgent": "idle",
	}, StatusTimeout*1000)
	hooks["BeforeTool"] = []any{map[string]any{
		"matcher": ".*",
		"hooks": []any{map[string]any{
			"type": "command", "name": "sheaf", "command": sheaf + " hook gemini",
			"timeout": HookTimeout * 1000,
		}},
	}}
	return hooks
}

func droid(sheaf string) Plan {
	h := map[string]any{"type": "command", "command": sheaf + " hook droid", "timeout": HookTimeout}
	return Plan{
		Kind: "droid",
		Files: []File{{
			// A standalone hooks.json keys events directly.
			Path: ".factory/hooks.json",
			Content: jsonText(func() map[string]any {
				events := statusGroups(sheaf, claudeStatus, StatusTimeout)
				events["PreToolUse"] = []any{map[string]any{"matcher": "*", "hooks": []any{h}}}
				return events
			}()),
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

func goose(sheaf string) Plan {
	return Plan{
		Kind: "goose",
		Files: []File{
			{
				Path:    ".agents/plugins/sheaf/plugin.json",
				Content: jsonText(map[string]any{"name": "sheaf", "description": "Routes goose's tool calls through your Sheaf policy and queue."}),
				Marker:  `"sheaf"`,
				Owned:   true,
			},
			{
				Path: ".agents/plugins/sheaf/hooks/hooks.json",
				Content: jsonText(map[string]any{"hooks": map[string]any{
					"PreToolUse": []any{map[string]any{"hooks": []any{map[string]any{
						"type": "command", "command": sheaf + " hook goose", "timeout": HookTimeout,
					}}}},
					"Stop": statusGroups(sheaf, map[string]string{"Stop": "idle"}, StatusTimeout)["Stop"],
				}}),
				Marker: "hook goose",
				Owned:  true,
			},
		},
		Steps: []string{"Add Sheaf as a goose extension in ~/.config/goose/config.yaml:\n\n" +
			"extensions:\n" +
			"  sheaf:\n" +
			"    name: sheaf\n" +
			"    type: stdio\n" +
			"    cmd: " + tomlString(sheaf) + "\n" +
			"    args: [mcp]\n" +
			"    enabled: true\n" +
			"    timeout: 660"},
	}
}

func amp(sheaf string) Plan {
	return Plan{
		Kind:  "amp",
		Files: []File{{Path: ".amp/plugins/sheaf.ts", Content: ampPlugin(sheaf), Marker: "hook amp", Owned: true}},
		Steps: []string{"Add Sheaf's MCP server to Amp's settings.json:\n\n" +
			jsonText(map[string]any{"amp.mcpServers": map[string]any{"sheaf": map[string]any{
				"command": sheaf, "args": []string{"mcp"},
			}}})},
	}
}

func ampPlugin(sheaf string) string {
	return `// Sheaf for Amp: checks each tool call against your Sheaf policy and sends
// the ones that need you to your Sheaf queue by running ` + "`sheaf hook amp`" + `.
// Written by ` + "`sheaf setup amp`" + `.
const SHEAF = ` + string(must(json.Marshal(sheaf))) + `

export default function (amp) {
  amp.on("tool.call", async (event, ctx) => {
    // Outside Sheaf (no SHEAF_TOKEN) stay out of the way.
    if (!process.env.SHEAF_TOKEN) return { action: "allow" }
    const payload = JSON.stringify({ tool: event.tool, input: event.input, cwd: process.cwd() })
    let verdict = {}
    try {
      const proc = Bun.spawn([SHEAF, "hook", "amp"], { stdin: new Blob([payload]), stdout: "pipe", stderr: "ignore" })
      const out = await new Response(proc.stdout).text()
      if ((await proc.exited) === 0) verdict = JSON.parse(out)
    } catch {
      // Sheaf unreachable: fall through to allow, as without Sheaf.
    }
    if (verdict.decision === "deny") {
      return { action: "reject-and-continue", message: verdict.reason || "Sheaf denied this call" }
    }
    if (verdict.decision === "ask") {
      const ok = await ctx.ui.confirm({
        title: "Sheaf: allow " + event.tool + "?",
        message: verdict.reason || "Nobody answered in Sheaf yet.",
        confirmButtonText: "Allow",
      })
      if (!ok) return { action: "reject-and-continue", message: "You declined this in Amp." }
    }
    return { action: "allow" }
  })
}
`
}

func kiro(sheaf string) Plan {
	hook := func(name, trigger, matcher, command string, timeout int) map[string]any {
		h := map[string]any{
			"name": name, "trigger": trigger, "enabled": true, "timeout": timeout,
			"action": map[string]any{"type": "command", "command": command},
		}
		if matcher != "" {
			h["matcher"] = matcher
		}
		return h
	}
	return Plan{
		Kind: "kiro",
		Files: []File{
			{
				Path: ".kiro/hooks/sheaf.json",
				Content: jsonText(map[string]any{"version": "v1", "hooks": []any{
					hook("sheaf-policy", "PreToolUse", "*", sheaf+" hook kiro", HookTimeout),
					hook("sheaf-working", "UserPromptSubmit", "", sheaf+" hook status working", StatusTimeout),
					hook("sheaf-idle", "Stop", "", sheaf+" hook status idle", StatusTimeout),
				}}),
				Marker: "hook kiro",
				Owned:  true,
			},
			{
				Path: ".kiro/settings/mcp.json",
				Content: jsonText(map[string]any{"mcpServers": map[string]any{"sheaf": map[string]any{
					"command": sheaf, "args": []string{"mcp"},
				}}}),
				Marker: `"sheaf"`,
			},
		},
		Steps: []string{"Kiro blocks a tool by the hook's exit status: a call Sheaf denies is stopped, anything else goes to Kiro's own approval. Kiro's hook format is new; if `kiro-cli` rejects the file, tell us your version."},
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

// Apply writes plan's files under dir. It creates missing files and rewrites
// Sheaf's own. An existing agent config that already mentions Sheaf is "ok".
// Any other JSON config is "merged": Sheaf adds its entries next to yours,
// keeps every value you set, and saves the original as PATH.sheaf-backup
// first. A config Sheaf cannot merge is "skipped", with what to add by hand.
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
		case strings.HasSuffix(f.Path, ".json"):
			merged, ok := mergeJSON(old, f.Content)
			if !ok {
				out = append(out, Result{Path: f.Path, Status: "skipped", Content: f.Content})
				continue
			}
			st, err := os.Stat(p)
			if err != nil {
				return out, err
			}
			if err := write(p+".sheaf-backup", string(old), st.Mode().Perm()); err != nil {
				return out, err
			}
			if err := write(p, merged, st.Mode().Perm()); err != nil {
				return out, err
			}
			out = append(out, Result{Path: f.Path, Status: "merged"})
		default:
			out = append(out, Result{Path: f.Path, Status: "skipped", Content: f.Content})
		}
	}
	return out, nil
}

// mergeJSON adds Sheaf's config to an existing JSON object: objects merge
// key by key, Sheaf's array entries are appended unless an equal entry is
// there, and wherever both set a plain value, yours wins. It fails, leaving
// the file to you, if either side is not a JSON object (such as a file with
// comments).
func mergeJSON(old []byte, add string) (string, bool) {
	var dst, src map[string]any
	if decodeObject(old, &dst) != nil || dst == nil || decodeObject([]byte(add), &src) != nil {
		return "", false
	}
	b, err := json.MarshalIndent(mergeValue(dst, src), "", "  ")
	if err != nil {
		return "", false
	}
	return string(b) + "\n", true
}

// decodeObject keeps numbers as written, so merging never rounds them.
func decodeObject(b []byte, v *map[string]any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.More() {
		return errors.New("trailing data")
	}
	return nil
}

func mergeValue(dst, src any) any {
	switch s := src.(type) {
	case map[string]any:
		d, ok := dst.(map[string]any)
		if !ok {
			return dst
		}
		for k, v := range s {
			if old, ok := d[k]; ok {
				d[k] = mergeValue(old, v)
			} else {
				d[k] = v
			}
		}
		return d
	case []any:
		d, ok := dst.([]any)
		if !ok {
			return dst
		}
	next:
		for _, v := range s {
			for _, have := range d {
				if reflect.DeepEqual(have, v) {
					continue next
				}
			}
			d = append(d, v)
		}
		return d
	}
	return dst
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

// PrepareWorktree marks a task worktree as trusted for the agent (see Trust)
// and writes the agent's project config into it, so
// an agent started there finds its Sheaf hooks even though the worktree is a
// fresh checkout without the main checkout's untracked config. Files the
// repository tracks are left as they are. The files it writes are listed in
// the repository's info/exclude, so the agent never commits them. Unknown
// kinds are a no-op.
func PrepareWorktree(dir, kind, sheaf string) ([]string, error) {
	plan, err := For(kind, sheaf)
	if err != nil {
		return nil, nil
	}
	var files []File
	for _, f := range plan.Files {
		if exec.Command("git", "-C", dir, "ls-files", "--error-unmatch", "--", f.Path).Run() == nil {
			continue // tracked: the repository decides
		}
		files = append(files, f)
	}
	plan.Files = files
	// Nobody may be at the keyboard to answer the agent's folder-trust
	// prompt. Failing to mark it is not fatal: the prompt still works.
	Trust(kind, dir)
	res, err := Apply(dir, plan)
	if err != nil {
		return nil, err
	}
	var wrote []string
	for _, r := range res {
		if r.Status == "created" || r.Status == "updated" || r.Status == "merged" {
			wrote = append(wrote, r.Path)
		}
	}
	return wrote, exclude(dir, files)
}

// exclude lists paths in the repository's shared info/exclude file.
func exclude(dir string, files []File) error {
	if len(files) == 0 {
		return nil
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return nil // not a git checkout
	}
	path := filepath.Join(strings.TrimSpace(string(out)), "info", "exclude")
	old, _ := os.ReadFile(path)
	have := map[string]bool{}
	for _, l := range strings.Split(string(old), "\n") {
		have[strings.TrimSpace(l)] = true
	}
	var add strings.Builder
	for _, f := range files {
		for _, p := range []string{"/" + filepath.ToSlash(f.Path), "/" + filepath.ToSlash(f.Path) + ".sheaf-backup"} {
			if !have[p] {
				add.WriteString(p + "\n")
				have[p] = true
			}
		}
	}
	if add.Len() == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer fh.Close()
	prefix := ""
	if len(old) > 0 && !strings.HasSuffix(string(old), "\n") {
		prefix = "\n"
	}
	_, err = fh.WriteString(prefix + "# Sheaf: agent config written into task worktrees\n" + add.String())
	return err
}
