# Run Sheaf on a server

This guide sets Sheaf up on a Linux server, such as a Hetzner VPS, so Claude
Code and Codex work there around the clock while you drive them from your
phone or your Mac. You create a task from your phone; Sheaf starts an agent
on it in its own git branch; anything risky, and every finished diff, waits
in your queue; when one agent hands the task to the other for review, Sheaf
starts that one too.

```
 your phone / Mac ──HTTPS (Tailscale)──▶ sheaf daemon --headless  (systemd)
                                            │  starts and wakes agents
                                            ▼
                         tmux session "sheaf": claude:t-1  codex:t-1  ...
                                            │  each in its own worktree
                                            ▼
                               ~/code/pluginscore  ──git push──▶ GitHub ──▶ Vercel preview
```

The steps use Ubuntu 24.04 and the repository `pluginscore`; change the
names to yours. Commands starting with `sudo` run as root; everything else
runs as your normal user.

## 0. Pick the user agents run as

Agents run shell commands as the Linux user that runs Sheaf. If the server
also runs production jobs (cron jobs, workers, databases), give the agents a
separate user that cannot touch them:

```sh
sudo adduser dev                 # any name; this guide calls it dev
sudo usermod -aG sudo dev        # optional, for installing packages
sudo -iu dev                     # continue as dev
```

## 1. Install the tools

```sh
sudo apt update
sudo apt install -y git tmux curl ca-certificates build-essential
# Node.js, for Codex and for most web projects
curl -fsSL https://deb.nodesource.com/setup_22.x | sudo -E bash -
sudo apt install -y nodejs
```

## 2. Install Sheaf

Download the Linux build from the
[latest release](https://github.com/sensahin/sheaf/releases/latest) (Hetzner's
CX/CPX servers are `amd64`, CAX servers are `arm64`):

```sh
VERSION=$(curl -fsSL https://api.github.com/repos/sensahin/sheaf/releases/latest | grep -o '"tag_name": *"v[^"]*"' | grep -o 'v[0-9.]*')
ARCH=$(dpkg --print-architecture)          # amd64 or arm64
curl -fsSLO "https://github.com/sensahin/sheaf/releases/download/$VERSION/sheaf_${VERSION#v}_linux_${ARCH}.tar.gz"
tar xzf "sheaf_${VERSION#v}_linux_${ARCH}.tar.gz"
sudo install "sheaf_${VERSION#v}_linux_${ARCH}/sheaf" /usr/local/bin/sheaf
sheaf version
```

This guide needs v0.2.0 or later (`sheaf daemon --headless`). To run the
newest code instead, install Go 1.26 from [go.dev/dl](https://go.dev/dl/) and
run `go install github.com/sensahin/sheaf/cmd/sheaf@main`.

## 3. Install Claude Code and Codex, and sign in

```sh
curl -fsSL https://claude.ai/install.sh | bash    # Claude Code
sudo npm install -g @openai/codex                 # Codex
```

Sign in once over SSH. Both print a link you open on your Mac or phone:

```sh
claude            # then /login; or set ANTHROPIC_API_KEY instead
codex login --device-auth      # or: printenv OPENAI_API_KEY | codex login --with-api-key
```

Run `claude` once more and quit; it asks a few first-run questions only the
first time, and an agent nobody is watching should not meet them.

## 4. Get your repository onto the server

The server needs to push branches, so give it access to GitHub. The GitHub
CLI is the simplest, and lets agents open pull requests:

```sh
sudo apt install -y gh
gh auth login                    # GitHub.com, HTTPS, log in with a browser (open the link on your Mac)
gh auth setup-git
mkdir -p ~/code && cd ~/code
gh repo clone sensahin/pluginscore
cd pluginscore && npm ci         # or however the project installs
git config user.name "Your Name" && git config user.email you@example.com
```

Each task gets a fresh worktree, which has the repository's tracked files
only: no `node_modules` and no untracked `.env.local`. Agents run the install
themselves; for secrets the tests need, say in the task where to copy them
from (for example "run `cp ~/code/pluginscore/.env.local .` first"), or pull
them with `vercel env pull`.

Before each new task, Sheaf fetches and starts the task's branch from the
newest pushed commit of your checkout's branch, so you never need to `git
pull` on the server.

## 5. Start Sheaf as a service

```sh
sheaf init
mkdir -p ~/.config/systemd/user
sheaf service > ~/.config/systemd/user/sheaf.service
systemctl --user daemon-reload
systemctl --user enable --now sheaf
sudo loginctl enable-linger $USER   # keep it running when you log out of SSH
systemctl --user status sheaf
```

`sheaf service` writes a unit that runs `sheaf daemon --headless` with your
current `PATH` (so it finds `claude`, `codex`, `git` and `tmux`). Run it again
after you add the HTTPS address in step 7. Logs: `journalctl --user -u sheaf -f`.

`--headless` means nobody sits at this machine. The daemon then:

- starts an agent in tmux when you create a task with "Start now", or press
  "Start agent" on a task;
- starts an agent that is handed a task while it is not running (Claude
  handing to Codex for review starts Codex in the same worktree);
- tells a running, idle agent when new messages are in its inbox, by typing
  one line into its terminal ("You have 1 new message in your Sheaf inbox").
  The message itself stays on Sheaf's bus, logged and with a read receipt.

## 6. Add the agents and connect them to the repository

```sh
cd ~/code/pluginscore

sheaf agent add claude --kind claude-code --resume "claude --continue"
sheaf agent add codex --kind codex --resume "codex resume --last" \
  --command "codex --sandbox workspace-write --ask-for-approval never --add-dir $HOME/code/pluginscore/.git -c sandbox_workspace_write.network_access=true"

sheaf setup claude-code          # hooks + `claude mcp add sheaf`
sheaf setup codex                # hooks; prints the [mcp_servers.sheaf] lines
nano ~/.codex/config.toml        # paste those lines

sheaf repo add . --name pluginscore   # offer it in the phone's New task form
```

Why the Codex command: on a server nobody answers Codex's own approval
prompts, so Codex runs with `--ask-for-approval never` inside its
`workspace-write` sandbox, and Sheaf's hook decides the risky calls. The
sandbox needs the repository's `.git` directory (a worktree commits there)
and network access (installs, `git push`). Codex flags change between
versions; if Codex stops on a prompt, you will see it in the Terminal panel
(step 9). Claude Code needs no extra flags: Sheaf's hook answers its
permission checks.

What waits for you, by default: `git push`, `sudo`, and secrets. A force push
and `curl ... | sh` are refused. See the policy with `sheaf agent policy claude`.

## 7. Reach the web view from your phone and Mac

The daemon listens on `127.0.0.1:7517` only. Do not open that port in the
firewall; reach it through one of these.

**Tailscale (recommended).** A private network between your server, Mac and
phone, with HTTPS, free for personal use.

```sh
curl -fsSL https://tailscale.com/install.sh | sh
sudo tailscale up                       # open the link to add the server to your tailnet
sudo tailscale serve --bg 7517          # https://<server>.<tailnet>.ts.net -> 127.0.0.1:7517
tailscale serve status                  # shows the https:// address
```

Install Tailscale on your phone and Mac and sign in to the same account.
Then tell Sheaf its address, so pairing links point there:

```sh
sheaf service --public-url https://<server>.<tailnet>.ts.net > ~/.config/systemd/user/sheaf.service
systemctl --user daemon-reload && systemctl --user restart sheaf
```

**SSH tunnel (no extra software).** From your Mac:

```sh
ssh -N -L 7517:127.0.0.1:7517 dev@your-server
```

and open `http://localhost:7517`. This works only while the tunnel is open,
so it suits the Mac, not the phone.

**A public domain** behind Caddy or nginx with HTTPS works too, but then the
page is on the internet, protected only by device tokens. Prefer Tailscale.

## 8. Pair your phone and Mac

Over SSH:

```sh
sheaf pair --name phone     # prints a QR code: scan it with the phone's camera
sheaf pair --name mac       # prints a link: open it on the Mac
```

On the phone, "Add to Home Screen" makes it an app. Each device has its own
token; `sheaf device ls` lists them and `sheaf device revoke ID` cuts one off.

A paired device acts as you: it decides approvals, creates and starts tasks,
and can type into agents' terminals. It cannot add agents, change their
commands or policies, register repositories, or pair other devices; those
need SSH. Revoke a lost phone right away.

## 9. Day to day

**Give work.** Tasks → New task. Write the bug or feature as you would to a
colleague, pick the agent and `pluginscore`, keep "Own branch" and "Start
now" on, and Create. The agent starts on branch `sheaf/t-…` with your text as
its first prompt.

**Make Claude and Codex work together.** Say it in the task, for example:

> Fix the 500 on /pricing (started yesterday). Add a test. When it passes,
> commit and hand the task to codex for review. codex: review the diff,
> fix anything wrong, then request approval with kind diff.

Claude fixes and hands off; Sheaf starts Codex in the same worktree with
Claude's note; Codex reviews and asks you to approve the diff.

**Review.** Needs you shows the diff file by file. Approve, or deny with a
note; the agent reads your note in its inbox and carries on. Approve a
`git push` the same way. If the task said to open a pull request, the agent
runs `gh pr create`, and Vercel builds a preview of the branch, so you can
try the fix on your phone before merging.

**Talk to an agent.** Inbox → message `claude` or `codex`. A running agent
hears about it as soon as it is idle; one that is not running starts on the
task the message is about.

**See what an agent is doing.** Agents → Terminal shows the agent's screen,
refreshed every two seconds. If it stopped on a question of its own (a
folder-trust prompt, a confirmation), answer it there: move with ↑ ↓ and press ⏎, or type and Send
for Enter. Over SSH, `tmux attach -t sheaf` shows every agent; detach with
Ctrl-b d.

**When a task is done**, merge the pull request on GitHub and mark the task
done. Old worktrees live in `~/.sheaf/worktrees`; remove one with `git -C
~/code/pluginscore worktree remove ~/.sheaf/worktrees/t-…`.

## After a reboot

systemd starts the daemon again; the agents' tmux sessions are gone. Press
"Start agent" on each unfinished task, or over SSH run
`sheaf resume --tmux` to reopen every agent with its resume command.

## Troubleshooting

- **"this daemon cannot start agents"**: tmux is missing. `sudo apt install
  tmux`, then `systemctl --user restart sheaf`.
- **An agent window opens and closes at once**: the command is not on the
  service's `PATH`. Rerun `sheaf service ...` from a shell where `which
  claude codex` works, then `daemon-reload` and restart.
- **Folder-trust prompts**: each task is a new folder under
  `~/.sheaf/worktrees`, so before starting an agent there Sheaf marks the
  folder trusted in the agent's own settings (Claude Code's `~/.claude.json`,
  Codex's `~/.codex/config.toml`, Gemini's `trustedFolders.json`, Copilot's
  `trusted_folders`). If an agent still stops on a prompt, answer it in the
  Terminal panel: ↓ to move to the choice you want, then ⏎ (the highlighted
  choice is often the safe "No").
- **Codex ignores Sheaf's hooks**: Codex runs project hooks only in trusted
  folders, which Sheaf now sets for each task. If your Codex version also
  asks you to review new hooks, run `/hooks` in that agent's terminal and
  approve Sheaf's.
- **The QR code points at the wrong address**: set `--public-url` (step 7).
- **Nothing happens when you message an idle agent**: the agent's status
  comes from its hooks; check `sheaf agent ls` shows it `idle`, and that
  `sheaf setup` ran in the repository.
