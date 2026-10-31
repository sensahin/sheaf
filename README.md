<p align="center">
  <img src="assets/brand/sheaf-wordmark-light.svg" alt="Sheaf" height="64">
</p>

<p align="center"><b>Many agents. One agreement.</b></p>

Sheaf is a local-first coordination layer for AI coding agents. It sits under
whatever terminal you already use (tmux, herdr, Rex, cmux, a plain shell) and
gives your agents what a terminal multiplexer can't:

- **Real messaging.** Agents send each other messages, questions and
  handoffs through MCP tools. Nothing is typed into another agent's terminal,
  and nothing lands in your half-written prompt. Every message has a read
  receipt.
- **Sandboxes and permissions.** Each task gets its own git worktree. Each
  agent gets its own token and policy. A `git push`, a `sudo` or a write
  outside the task's worktree waits for you; a force push is refused.
- **Work that survives a reboot.** Every message, handoff, status change and
  decision is written to an append-only event log in SQLite before it takes
  effect. After a crash or reboot, `sheaf resume` restarts each agent on its
  unfinished task, in the right directory.
- **One queue for what needs you.** Approvals, questions and diffs from every
  agent land in one place: `sheaf queue`.

Sheaf is open source (Apache-2.0) and needs no account. Your agents and their
messages never leave your machine.

> Status: early. The core runtime and the web queue work and are tested;
> the desktop app, a relay for reaching your queue from anywhere and adapters
> for other terminals are next. See
> [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Install

Download the archive for your system from
[Releases](https://github.com/sensahin/sheaf/releases), check it against
`checksums.txt`, and put `sheaf` on your PATH. It is a single binary with no
dependencies. Or, with Go installed:

With Homebrew (macOS and Linux):

```sh
brew tap sensahin/sheaf https://github.com/sensahin/sheaf
brew install sheaf
brew services start sheaf     # runs `sheaf daemon` at login
```

Until the first release is out, build the latest code with Homebrew instead
(it installs Go for the build): `brew install --HEAD sensahin/sheaf/sheaf`.

Or download a binary from [Releases](https://github.com/sensahin/sheaf/releases),
or build from source:

```sh
go install github.com/sensahin/sheaf/cmd/sheaf@latest
```

macOS and Linux are fully supported. Windows builds are experimental:
`sheaf run` is not available there yet.

## Quick start

```sh

sheaf init
sheaf daemon &                      # or run it under launchd / systemd

sheaf agent add planner --kind claude-code --resume "claude --continue"
sheaf agent add builder --kind codex --resume "codex resume --last"
```

Connect each agent program once per project. This writes its pre-tool hook
(so its permission prompts go to your Sheaf queue) and its MCP server entry
(so it can message other agents):

```sh
sheaf setup claude-code    # .claude/settings.json + `claude mcp add`
sheaf setup codex          # .codex/hooks.json + ~/.codex/config.toml entry
sheaf setup opencode       # .opencode/plugins/sheaf.js + opencode.json
sheaf setup copilot        # .github/hooks/sheaf.json + MCP entry to add
sheaf setup cursor         # .cursor/hooks.json + .cursor/mcp.json
sheaf setup gemini         # .gemini/settings.json (hook + MCP)
sheaf setup droid          # .factory/hooks.json
sheaf setup antigravity    # .agents/hooks.json + MCP entry to add
sheaf setup cline          # .clinerules/hooks/PreToolUse + MCP entry to add
sheaf setup windsurf       # .windsurf/hooks.json + .windsurf/mcp_config.json
sheaf setup goose          # .agents/plugins/sheaf/ + extension entry to add
sheaf setup amp            # .amp/plugins/sheaf.ts + MCP entry to add
sheaf setup kiro           # .kiro/hooks/sheaf.json + .kiro/settings/mcp.json
sheaf setup junie          # .junie/mcp/mcp.json (MCP only)
```

`sheaf setup` creates missing files and never rewrites a config you already
have; it prints what to add instead. `--print` shows everything without
writing.

Then start agents through Sheaf:

```sh
sheaf start planner                   # a tmux window named "planner"
sheaf start builder --task t-7xa6x441 # in that task's worktree
```

`sheaf start` opens each agent in its own tmux window (or in the current
terminal with `--here`, or when tmux isn't installed), already wrapped in
`sheaf run`, so its status is tracked from the first second. The agent gets
its token from Sheaf, so its config holds no secret. Started outside Sheaf,
an agent needs `SHEAF_TOKEN`, which `sheaf agent add` prints; without it the
hooks stay out of the way.

| Agent | Approvals | Notes |
|---|---|---|
| Claude Code | allow, deny, or hand back to its prompt | PreToolUse hook |
| Codex CLI | allow or deny | PreToolUse hook; patches are checked file by file. Codex has no "ask" answer, so an unanswered call falls back to Codex's own approval policy. Trust the hook once with `/hooks`. |
| OpenCode | deny | `tool.execute.before` plugin; an unanswered call falls back to OpenCode's own permissions |
| GitHub Copilot CLI | allow, deny, or hand back to its prompt | camelCase `preToolUse` hook |
| Cursor (cursor-agent, IDE) | allow or deny | `preToolUse` hook; unanswered falls back to Cursor. Editor-started agents have no `SHEAF_TOKEN`, so the hook ignores them. |
| Gemini CLI, Qwen Code | allow or deny | `BeforeTool` hook; a deny reason reaches the model as the tool's error |
| Factory Droid | allow, deny, or hand back to its prompt | PreToolUse hook (Claude Code format, Droid's tool names) |
| Google Antigravity | deny, or force its own prompt | PreToolUse hook; some agy versions prompt even after an approval |
| Cline | deny | `PreToolUse` hook script; unanswered falls back to Cline's approval |
| Windsurf (Cascade) | deny | `pre_run_command`, `pre_write_code`, `pre_mcp_tool_use` hooks, which block by exit status |
| goose | allow or deny | `PreToolUse` hook in a goose plugin; unanswered falls back to goose |
| Amp | allow, deny, or ask in Amp | `tool.call` plugin; an unanswered call becomes an Amp confirm dialog |
| JetBrains Junie | messaging and status only | MCP; Junie has no pre-tool hooks yet |
| Kiro CLI | deny | `PreToolUse` hook in `.kiro/hooks/`, which blocks by exit status; Kiro's hook format is new, so this one is the least proven |
| Aider, anything else | status and worktrees | `sheaf start` / `sheaf run`; Aider has no hooks or MCP |
| Anything with MCP | messaging and status only | run `sheaf mcp` on stdio |

The Claude Code and OpenCode adapters have been run against the real
programs. The others follow each tool's published hook format and are covered
by recorded-payload tests; report a mismatch if your version differs.

## A session

```sh
$ sheaf task new "Add rate limiting" --owner planner --worktree
t-7xa6x441
worktree ~/.sheaf/worktrees/t-7xa6x441 on branch sheaf/t-7xa6x441
```

The planner works in that worktree, then calls the `handoff` tool:

```
handoff(task="t-7xa6x441", to="builder",
        note="Plan is in PLAN.md. Token bucket in middleware/limit.go; start with step 2.")
```

The builder finds the note in its `inbox`, owns the task now, and when it
tries to `git push`, the push waits in your queue:

```sh
$ sheaf queue
ID          KIND    AGENT    TASK        STATUS   SUMMARY                          ASKED
a-3k9d2m1x  action  builder              pending  Bash: git push origin sheaf/...  12s ago

$ sheaf approve a-3k9d2m1x "feature branch only"
a-3k9d2m1x approved. builder will see it in its inbox.
```

When the work is ready, the builder asks for review with
`request_approval(kind="diff", task=...)`, and `sheaf show` prints the diff
from the task's worktree.

## tmux

After a reboot, `sheaf resume --tmux` reopens every agent that had
unfinished work, each in its task's worktree with its resume command (such as
`claude --continue`). Put the queue in your status bar:

```tmux
set -g status-right '#(sheaf tmux-status) %H:%M'
set -g status-interval 5
```

It shows something like `sheaf: 2 need you · 1 blocked · 3 working`, and
nothing when all is quiet.

## herdr

Inside a [herdr](https://herdr.dev) pane, `sheaf start AGENT` runs the agent
right in that pane, and `sheaf run` mirrors its status into herdr's sidebar
(working, blocked with the reason, idle). It also hands herdr the agent's
resume command, wrapped in `sheaf run`, so when herdr brings the pane back
after a restart, Sheaf keeps tracking the agent. Sheaf's queue, worktrees and
messaging work the same as anywhere else.

## cmux and Rex

In a [cmux](https://cmux.com) workspace, `sheaf start AGENT` runs the agent in
the current pane. Its status shows as an entry in the workspace's sidebar
(`builder: blocked · needs permission: git push`), and cmux sends a
notification when the agent needs you. In a Rex pane, `sheaf start` also runs
in place; Rex has no status API yet, so status lives in Sheaf
(`sheaf status`, the web queue, `sheaf tmux-status`).

## Status from agents' own hooks

`sheaf setup` also wires each agent's lifecycle events to
`sheaf hook status STATE`, so Sheaf knows what the agent is doing without
the agent calling a tool:

| Agent | working | blocked (needs you) | idle |
|---|---|---|---|
| Claude Code, Factory Droid | prompt submitted, tool finished | notification | turn ended |
| Codex | prompt submitted | | turn ended |
| Copilot CLI | prompt submitted | notification | agent stopped |
| Gemini CLI | before agent | notification | after agent |
| Cursor, goose | | | stop |

The notification text ("Claude needs your permission to use Bash") becomes
the status note. Run under `sheaf start`, the report also follows the task.

## Status without tool calls

Claude Code, Codex, Amp, goose, opencode and others already print their
status as OSC 7501 sequences. Start an agent through `sheaf run` and Sheaf
reads them on the way to your terminal, so the queue shows it as working,
blocked (and why) or done without the agent calling any Sheaf tool:

```sh
SHEAF_TOKEN=sha_... sheaf run --task t-7xa6x441 -- claude
```

The agent's output reaches your terminal unchanged, so `sheaf run` also works
inside tmux, herdr or any other terminal. With `--task`, the task's status
follows the agent's.

## Desktop app

The desktop app is a window for the same queue, built for a big screen:
diffs file by file with line numbers, your agents, their tasks (mark them
done, cancel or reopen), and messages, including new ones you start. It shows
a desktop notification when an agent asks for approval or a review, asks a
question, messages you or reports that it is blocked.

Download it from [Releases](https://github.com/sensahin/sheaf/releases):
`Sheaf_VERSION_macos_universal.zip` (Apple Silicon and Intel),
`sheaf-desktop_VERSION_linux_ARCH.tar.gz` (needs WebKitGTK 4.1, as on Ubuntu
22.04 or later) or `sheaf-desktop_VERSION_windows_amd64.zip` (needs the
WebView2 runtime that Windows 10 and 11 ship). Each one includes the `sheaf`
CLI.

The app starts the daemon if it is not running and pairs itself as the device
`desktop`, so there is nothing to set up. Closing the window leaves the daemon
running for your agents. The page is served inside the app with no network
port; it talks to the daemon over its socket with its own device token, which
`sheaf device revoke` cuts off like any other.

The macOS build is signed ad hoc, not yet with a Developer ID, so the first
time, open it with right-click, Open, or run
`xattr -d com.apple.quarantine /Applications/Sheaf.app`.

## Your queue on your phone

The daemon also serves the queue as a small web page, on `127.0.0.1:7517` by
default. To use it from your phone, pair the phone:

```sh
sheaf daemon --web 0.0.0.0:7517     # or your Tailscale address
sheaf pair --name phone             # prints a QR code and a link
```

Scan the code and you get the queue, with diffs, approve, deny and answer
buttons, your agents' status and their messages to you. It updates live and
can be added to the home screen. Each paired device has its own token, so
`sheaf device revoke ID` cuts off a lost phone without touching anything
else. A paired device can decide approvals and message agents, but cannot add
agents, change policies or pair more devices.

The page is plain HTTP. Use it on a private network such as Tailscale, not on
public Wi-Fi.

## Tools agents get

| Tool | What it does |
| --- | --- |
| `whoami`, `list_agents` | Who am I, who else is here and what are they doing |
| `send`, `inbox`, `message_status` | Messages and questions, with read receipts; `inbox` can block until something arrives |
| `report_status` | working, blocked, idle or done, optionally moving a task |
| `create_task`, `list_tasks` | Tasks, optionally each with its own git worktree |
| `handoff` | Give a task to another agent, with a note, in one step |
| `request_approval`, `wait_for_approval` | Ask you to approve an action, review a diff or answer a question |
| `check_permission` | Ask the broker before acting |

## Commands

```
sheaf init | daemon [--web ADDR|off] | status
sheaf agent add|ls|rm|token|policy
sheaf task new|ls|show|status|done|handoff
sheaf send | inbox
sheaf queue | show | approve | deny | answer
sheaf pair | device ls|revoke
sheaf log [--follow]
sheaf setup KIND | start AGENT | resume [--tmux] | tmux-status
sheaf run -- CMD | mcp | hook claude-code|codex|opencode
```

## Policies

Each agent has a policy: rules that match an action (`shell`, `write`,
`network`, `secret`, `tool`) and a glob, with an effect of `allow`, `ask` or
`deny`. The strictest matching rule wins. `sheaf agent policy NAME` shows the
default; `sheaf agent policy NAME --set policy.json` replaces it.

```json
{
  "default": "allow",
  "rules": [
    {"action": "shell", "match": "git push --force*", "effect": "deny"},
    {"action": "shell", "match": "git push*", "effect": "ask"},
    {"action": "shell", "match": "sudo *", "effect": "ask"},
    {"action": "secret", "match": "*", "effect": "ask"}
  ]
}
```

On top of the rules, a write outside the agent's current task worktree always
needs your approval.

## Building

```sh
go build ./cmd/sheaf
go test ./...
scripts/build-release.sh v0.1.0   # all platforms, into dist/
```

The desktop app lives in its own module, `desktop/`, because it links the
system webview with cgo ([Wails](https://wails.io)). On Linux, install
`libgtk-3-dev` and `libwebkit2gtk-4.1-dev` first.

```sh
cd desktop && go build -tags desktop,production,webkit2_41 .   # Linux
cd desktop && go build -tags desktop,production .              # macOS, Windows
scripts/build-desktop.sh v0.1.0   # this platform's package, into dist/
```

Pushing a `v*` tag builds every platform and publishes a GitHub release, with
the desktop app built on macOS, Linux (x86 and Arm) and Windows runners.

## Contributing

Issues and pull requests are welcome, and we answer them.
