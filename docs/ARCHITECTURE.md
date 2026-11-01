# Architecture

Sheaf is one Go binary, `sheaf`, that is both the daemon and its clients.

```
 agent (Claude Code, Codex, ...)          you
   │  MCP over stdio                        │  CLI
   ▼                                        ▼
 sheaf mcp ──┐                       sheaf queue / approve / ...
 sheaf hook ─┤  HTTP + bearer token over a Unix socket (0600)
             ▼
        sheaf daemon ── core ── SQLite (WAL): event log + state
             ▲
             │  same API + the web queue over TCP (127.0.0.1:7517 by default)
        phone or browser, paired with `sheaf pair`
                          └──── git worktrees, one per task
```

## Why these choices

**Go, single static binary.** The daemon must be light (herdr's ~41 MB idle
is the bar users compare against), start fast, and cross-compile to macOS,
Linux and Windows without a toolchain on the user's machine. Go gives that
with a small codebase and an official MCP SDK
(`github.com/modelcontextprotocol/go-sdk`).

**SQLite, pure Go (`modernc.org/sqlite`).** No cgo, so cross-compiling stays
trivial. WAL mode with `synchronous=NORMAL` survives process crashes, and every
state change commits in the same transaction as its event, so the state
tables and the log never disagree.

**A daemon, not a shared database file.** Agents never open the database.
They hold a token, and the daemon decides what each token may do. That
boundary is what makes the permission broker real: an agent in a sandbox
needs only the socket and its token, not your files.

**MCP as the agent interface.** Every major coding agent already speaks MCP,
so agents get Sheaf by adding one server, with no fork or wrapper. The
`inbox` tool can block (`wait_seconds`), so an idle agent waits for work
without polling its terminal.

**Hooks as the enforcement point.** MCP tools are opt-in for the agent; a
PreToolUse hook is not. `sheaf hook claude-code` checks every tool call
against the agent's policy and holds "ask" calls until you decide in the
queue. Adapters for other agents' hook systems follow the same shape: each
`sheaf hook KIND` maps the agent's payload to a `hook.Call` (an action and its
targets; a patch has one target per file), `hook.Decide` applies the policy
and the queue, and the adapter writes the answer in the agent's format.
`sheaf setup KIND` writes the agent's hook and MCP config. Claude Code, Codex,
OpenCode, Copilot CLI, Cursor, Gemini CLI, Factory Droid, Antigravity, Cline,
Windsurf, goose, Amp and Kiro work this way today; Windsurf and Kiro block by exit status, which an
adapter signals by returning `*hook.Blocked`. Junie gets MCP only.

**Worktrees before containers.** A worktree per task stops agents from
editing the same checkout, which is the most common collision, at zero cost.
Containers are planned as an optional isolation level per task.

**The web queue is the same API.** The daemon serves a static page (embedded
in the binary, no build step, nothing loaded from the network, strict CSP) that
calls `/v1` with a paired device's bearer token. Live updates are a long poll
on the event log. Pairing puts the token in the URL fragment, which browsers
never send to the server; the page moves it to local storage and clears the
address bar. Bearer tokens rather than cookies mean no CSRF surface.

**Headless dispatch, for servers.** With `--headless`, the daemon follows
its own event log (`internal/dispatch`). A task created with "start", or a
handoff or message to an agent that has no tmux window, opens the agent in a
window of the `sheaf` tmux session, in the task's worktree, with the task as
its first prompt (on the command line for agents that take one, in its inbox
for the rest). An agent that has a window and reports idle while it has
unread messages gets one line typed into its terminal telling it to read its
inbox; the messages stay on the bus. Each message rings once, and a freshly
started agent is left alone for 20 seconds. The same package reads a
window's screen (`tmux capture-pane`) and types into it, so the phone can
answer a prompt an agent stopped on. These run only the agent's registered
command, which only the owner token can set; a paired device can start and
type, but not change what runs. A device may create tasks only in
repositories registered with `sheaf repo add`.

**`sheaf run` for status.** OSC 7501 travels in the agent's own terminal
output, so the only place to read it is the pty. `sheaf run` starts the agent
in a pty, copies every byte through unchanged, and a streaming scanner picks
out the status sequences (split reads, BEL or ST terminators, and a size cap
on the body). Reports go to the daemon in the background, latest wins, so a
slow or stopped daemon never stalls the agent's terminal.

**The desktop app is the web queue in a native window.** `sheaf-desktop`
uses Wails, which wraps the system webview (WebKit on macOS and Linux,
WebView2 on Windows), so the app is a few megabytes and shares every line of
UI with the phone page. It serves the page from inside the app rather than
on a port, and forwards `/v1` to the daemon's socket without adding
credentials: the page uses a device token the app gets by pairing itself with
the owner token. The parts that need no native code (starting the daemon,
pairing, the proxy, notifications from the event log) live in
`internal/desktop` and are tested with the rest; only the window is in the
separate `desktop/` module, so `go test ./...` never needs cgo.

## Data model

| Table | Holds |
| --- | --- |
| `events` | Append-only log: `ts`, `actor`, `type`, `subject`, JSON `data` |
| `agents` | Name, kind, token hash (SHA-256), policy, start and resume commands, state |
| `tasks` | Title, detail, owner, parent, status, repo, worktree, branch, base commit |
| `messages` | Sender, recipient, task, kind (message, question, handoff, decision), body, read time |
| `approvals` | Agent, task, kind (action, diff, question), summary, detail, status, answer |
| `devices` | Paired phones and browsers: name, token hash, last seen, revoked time |
| `repos` | Repositories tasks may use from a paired device |

The daemon stores tokens only as hashes. The CLI keeps the owner token in
`$SHEAF_HOME/owner.token` and each agent's token in
`$SHEAF_HOME/agents/NAME.token` (all 0600), so `sheaf start` and
`sheaf resume` can launch agents without putting a token in tmux's arguments
or the process list. Agents that run as your user can read these files; real
separation between agents needs the container isolation on the roadmap.

## Rules the core enforces

- Only the owner token adds or removes agents, rotates their tokens, sets
  policies and pairs or revokes devices. Paired devices act as the owner for
  everything else, such as deciding approvals and messaging agents.
- Only a task's owner (or you) can move it or hand it off. A handoff changes
  the owner and delivers the note in one transaction.
- Agents see only their own inbox, the messages they sent, and their own
  approval requests. Only you can read the event log.
- Every permission check is logged with its decision and reason.
- Every decision is also delivered to the agent's inbox, so an agent that
  stopped waiting still hears back.

## Resume after a reboot

Agents are registered with a resume command (`claude --continue`,
`codex resume --last`, ...). Tasks that are open, working or blocked are in
the database, so after any restart `sheaf resume` prints, or with `--tmux`
opens, each agent's command in its task's worktree. The daemon reports how
many tasks can be resumed when it starts.

## Next

1. Confirm each adapter against the real agent (most are built from the
   agents' published hook formats) and keep them current as those formats
   change. Every adapter in the integration research is built, and so are
   the terminal adapters and the Homebrew formula.
2. An optional end-to-end encrypted relay so the web queue works away from
   home without a VPN. (The desktop app is built.)
3. Signed macOS app identity for the daemon, so TCC and Local Network
   consent stick.
4. Optional container isolation per task; network and secret brokering.
5. An open spec for the message bus.
