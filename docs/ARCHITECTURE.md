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
queue. Adapters for other agents' hook systems follow the same shape.

**Worktrees before containers.** A worktree per task stops agents from
editing the same checkout, which is the most common collision, at zero cost.
Containers are planned as an optional isolation level per task.

**The web queue is the same API.** The daemon serves a static page (embedded
in the binary, no build step, nothing loaded from the network, strict CSP) that
calls `/v1` with a paired device's bearer token. Live updates are a long poll
on the event log. Pairing puts the token in the URL fragment, which browsers
never send to the server; the page moves it to local storage and clears the
address bar. Bearer tokens rather than cookies mean no CSRF surface.

**`sheaf run` for status.** OSC 7501 travels in the agent's own terminal
output, so the only place to read it is the pty. `sheaf run` starts the agent
in a pty, copies every byte through unchanged, and a streaming scanner picks
out the status sequences (split reads, BEL or ST terminators, and a size cap
on the body). Reports go to the daemon in the background, latest wins, so a
slow or stopped daemon never stalls the agent's terminal.

## Data model

| Table | Holds |
| --- | --- |
| `events` | Append-only log: `ts`, `actor`, `type`, `subject`, JSON `data` |
| `agents` | Name, kind, token hash (SHA-256), policy, resume command, state |
| `tasks` | Title, owner, parent, status, repo, worktree, branch, base commit |
| `messages` | Sender, recipient, task, kind (message, question, handoff, decision), body, read time |
| `approvals` | Agent, task, kind (action, diff, question), summary, detail, status, answer |
| `devices` | Paired phones and browsers: name, token hash, last seen, revoked time |

Tokens are shown once and stored only as hashes. The owner token lives in
`$SHEAF_HOME/owner.token` (0600).

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

1. Native desktop app for the queue and diffs; an optional end-to-end
   encrypted relay so the web queue works away from home without a VPN.
2. Adapters: herdr, tmux, Rex and cmux panes; Codex and OpenCode hooks.
3. Signed macOS app identity for the daemon, so TCC and Local Network
   consent stick.
4. Optional container isolation per task; network and secret brokering.
5. An open spec for the message bus.
