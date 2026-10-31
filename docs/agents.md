# Agents

Sheaf connects to each coding agent in up to three ways:

- **Approvals.** The agent's pre-tool hook runs `sheaf hook KIND` before every
  tool call. Sheaf maps the call to an action (`shell`, `write`, `network`,
  `secret`, `tool`) and its targets, checks the agent's
  [policy](../README.md#policies), and answers in the agent's own format.
  Calls the policy marks `ask` wait in your queue for up to 11 minutes.
- **Status.** The agent's lifecycle hooks run `sheaf hook status STATE`, so
  Sheaf knows when it is working, blocked on you or idle, without the agent
  calling a tool. `sheaf run` adds status from the OSC 7501 sequences many
  agents print.
- **Messaging.** `sheaf mcp` is an MCP server on stdio. It gives the agent
  tools to message other agents, hand off tasks and ask you for approval (see
  [Tools agents get](../README.md#tools-agents-get)).

`sheaf setup KIND` in a project writes the hook and MCP files for one agent.
It creates files that don't exist and rewrites only files it owns, such as a
plugin it wrote before. When a file is yours already (your
`.claude/settings.json`, your `opencode.json`), it leaves it alone and prints
exactly what to add. `--print` shows everything without writing anything.

Hooks find the agent by `SHEAF_TOKEN`. `sheaf start AGENT` sets it, so the
config files hold no secret. An agent started without the token, such as one
launched from an IDE, passes through the hooks untouched.

## At a glance

| Agent | `sheaf setup` | Approvals | Status events |
|---|---|---|---|
| [Claude Code](#claude-code) | `claude-code` | allow, deny, or hand back to its prompt | working, blocked, idle |
| [Codex CLI](#codex-cli) | `codex` | allow or deny | working, idle |
| [OpenCode](#opencode) | `opencode` | deny | from `sheaf run` |
| [GitHub Copilot CLI](#github-copilot-cli) | `copilot` | allow, deny, or hand back to its prompt | working, blocked, idle |
| [Cursor](#cursor) | `cursor` | allow or deny | idle |
| [Gemini CLI, Qwen Code](#gemini-cli-and-qwen-code) | `gemini` | allow or deny | working, blocked, idle |
| [Factory Droid](#factory-droid) | `droid` | allow, deny, or hand back to its prompt | working, blocked, idle |
| [Google Antigravity](#google-antigravity) | `antigravity` | deny, or force its own prompt | from `sheaf run` |
| [Cline](#cline) | `cline` | deny | from `sheaf run` |
| [Windsurf](#windsurf) | `windsurf` | deny | from `sheaf run` |
| [goose](#goose) | `goose` | allow or deny | idle |
| [Amp](#amp) | `amp` | allow, deny, or ask in Amp | from `sheaf run` |
| [Kiro CLI](#kiro-cli) | `kiro` | deny | from `sheaf run` |
| [JetBrains Junie](#jetbrains-junie) | `junie` | none (no hooks yet) | from `sheaf run` |
| [Aider and others](#aider-and-anything-else) | none | none | from `sheaf run` |

"Hand back to its prompt" means an `ask` that you don't answer in time
returns to the agent's own permission prompt instead of failing. Where an
agent has no such answer, an unanswered call falls back to the agent's own
approval settings.

The Claude Code and OpenCode adapters have been run against the real
programs. The others follow each tool's published hook format and are tested
with recorded payloads. If your version behaves differently, open an issue
with the version and the hook payload.

## Claude Code

```sh
sheaf setup claude-code
```

Writes `.claude/settings.json` with a `PreToolUse` hook for every tool and
status hooks (`UserPromptSubmit` and `PostToolUse` → working, `Notification`
→ blocked, `Stop` → idle), and prints the `claude mcp add` command for the
MCP server. The notification text, such as "Claude needs your permission to
use Bash", becomes the agent's status note.

## Codex CLI

```sh
sheaf setup codex
```

Writes `.codex/hooks.json` (`PreToolUse`, plus `UserPromptSubmit` → working
and `Stop` → idle) and prints the `[mcp_servers.sheaf]` entry for
`~/.codex/config.toml`. Patches are checked file by file, and the strictest
file wins. Codex runs project hooks only after you trust them: run `/hooks` in
Codex once and approve Sheaf's. Codex has no "ask" answer, so an unanswered
call falls back to Codex's own approval policy.

## OpenCode

```sh
sheaf setup opencode
```

Writes `.opencode/plugins/sheaf.js`, a `tool.execute.before` plugin, and the
MCP entry in `opencode.json`. OpenCode plugins can only stop a call, so Sheaf
denies what your policy denies and leaves everything else to OpenCode's
permissions.

## GitHub Copilot CLI

```sh
sheaf setup copilot
```

Writes `.github/hooks/sheaf.json` (camelCase `preToolUse`, plus status
hooks) and prints the entry for `~/.copilot/mcp-config.json`.

## Cursor

```sh
sheaf setup cursor
```

Writes `.cursor/hooks.json` (`preToolUse`, and `stop` → idle) and
`.cursor/mcp.json`. Agents started from the Cursor IDE have no `SHEAF_TOKEN`,
so the hook ignores them; run `cursor-agent` through `sheaf start` to put it
under Sheaf.

## Gemini CLI and Qwen Code

```sh
sheaf setup gemini
```

Writes `.gemini/settings.json` with a `BeforeTool` hook, status hooks
(`BeforeAgent` → working, `Notification` → blocked, `AfterAgent` → idle) and
the MCP server. A deny reason reaches the model as the tool's error. Qwen Code
reads the same format from `.qwen/settings.json`: copy the file there.

## Factory Droid

```sh
sheaf setup droid
```

Writes `.factory/hooks.json` in Claude Code's hook format with Droid's tool
names, and the same status hooks as Claude Code.

## Google Antigravity

```sh
sheaf setup antigravity
```

Writes `.agents/hooks.json` and prints the entry for
`~/.gemini/config/mcp_config.json`. Some `agy` versions show their own prompt
even after Sheaf approves a call.

## Cline

```sh
sheaf setup cline
```

Writes the hook script `.clinerules/hooks/PreToolUse` and prints the MCP
entry to add under MCP Servers, Configure. A call Sheaf denies is cancelled;
anything else goes to Cline's own approval.

## Windsurf

```sh
sheaf setup windsurf
```

Writes `.windsurf/hooks.json` (`pre_run_command`, `pre_write_code`,
`pre_mcp_tool_use`) and `.windsurf/mcp_config.json`. Windsurf hooks block by
exit status, so a call Sheaf denies is stopped and anything else goes to
Windsurf's own approval.

## goose

```sh
sheaf setup goose
```

Writes a goose plugin in `.agents/plugins/sheaf/` with a `PreToolUse` hook
and `Stop` → idle, and prints the extension entry for
`~/.config/goose/config.yaml`.

## Amp

```sh
sheaf setup amp
```

Writes `.amp/plugins/sheaf.ts`, a `tool.call` plugin, and prints the MCP entry
for Amp's `settings.json`. An `ask` you don't answer in time becomes Amp's own
confirm dialog.

## Kiro CLI

```sh
sheaf setup kiro
```

Writes `.kiro/hooks/sheaf.json` and `.kiro/settings/mcp.json`. Kiro blocks by
exit status: a call Sheaf denies is stopped and anything else goes to Kiro's
approval. Kiro's hook format is new, so this adapter is the least proven; if
`kiro-cli` rejects the file, open an issue with your version.

## JetBrains Junie

```sh
sheaf setup junie
```

Writes `.junie/mcp/mcp.json`. Junie has no pre-tool hooks yet, so it gets
messaging, tasks and status, but Sheaf cannot hold its tool calls.

## Aider and anything else

Any agent works under `sheaf start` or `sheaf run`. It gets a task worktree,
status from its terminal and a place in `sheaf resume`. With MCP support, add
`sheaf mcp` as a stdio server and pass `SHEAF_TOKEN` for messaging.

## Terminals

`sheaf start` opens agents in tmux, or in place inside
[herdr](https://herdr.dev), [cmux](https://cmux.com) and Rex, and mirrors
their status into herdr's and cmux's sidebars. See the README sections on
[tmux](../README.md#tmux), [herdr](../README.md#herdr) and
[cmux and Rex](../README.md#cmux-and-rex).
