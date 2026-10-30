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

> Status: early. The core runtime works and is tested; the desktop app,
> the phone view and adapters for other terminals are next. See
> [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Quick start

```sh
go install github.com/sensahin/sheaf/cmd/sheaf@latest

sheaf init
sheaf daemon &                      # or run it under launchd / systemd

sheaf agent add planner --kind claude-code --resume "claude --continue"
sheaf agent add builder --kind codex --resume "codex resume --last"
```

`sheaf agent add` prints the agent's token and the lines to paste into the
agent's config. For Claude Code:

```sh
claude mcp add sheaf --env SHEAF_TOKEN=sha_... -- sheaf mcp
```

and, to route its permission prompts through your Sheaf queue, a PreToolUse
hook in `.claude/settings.json`:

```json
{"hooks": {"PreToolUse": [{"matcher": "*", "hooks": [
  {"type": "command", "command": "SHEAF_TOKEN=sha_... sheaf hook claude-code"}
]}]}}
```

Any MCP client works the same way: run `sheaf mcp` on stdio with
`SHEAF_TOKEN` in its environment.

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
sheaf init | daemon | status
sheaf agent add|ls|rm|token|policy
sheaf task new|ls|show|status|done|handoff
sheaf send | inbox
sheaf queue | show | approve | deny | answer
sheaf resume [--tmux] | log [--follow]
sheaf mcp | hook claude-code
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
```

## Contributing

Issues and pull requests are welcome, and we answer them.
