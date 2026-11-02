# Give agents GitHub, Vercel and Cloudflare access

Your agents on the server (Claude Code and Codex, set up with
[remote-server.md](remote-server.md)) can commit, push, open pull requests,
read deploy logs and create, update or delete resources on GitHub, Vercel
and Cloudflare. This page wires that up once. Everything runs as the user
the agents run as (`dev` below); nothing goes into a repository.

How it fits together:

- **Credentials** live in `~/.sheaf/env` (mode 0600, outside git). Every agent
  Sheaf starts gets them as environment variables, whether you start it from
  your phone, Telegram, the CLI, or Sheaf starts it on a handoff. `gh` and
  `vercel` can also use their own login files in `~/.config`.
- **Risky commands wait for you.** Sheaf's hook sees every shell command
  before it runs. Pushes, merges, production deploys, rollbacks, deletions,
  secret changes and remote database writes go to your queue, so you approve
  them from Telegram or the phone. Reading logs, listing resources, preview
  deploys and opening PRs run freely. [The full list](#what-waits-for-you).
- **Preferred path:** agents commit and push to a branch, open a PR, and the
  Vercel and Cloudflare Git integrations build previews and deploy `main`.
  Direct CLI deploys and changes are there when needed, behind approval.

## 0. Update Sheaf (once)

`sheaf env` and the platform rules need a Sheaf built after this page was
added:

```sh
go install github.com/sensahin/sheaf/cmd/sheaf@main && sudo install ~/go/bin/sheaf /usr/local/bin/sheaf && systemctl --user restart sheaf
```

Then give your existing agents the new approval rules (your own rules stay):

```sh
sheaf agent policy claude --add-defaults
sheaf agent policy codex --add-defaults
```

## 1. GitHub

You probably signed in during server setup. Check, as `dev`:

```sh
gh auth status
```

It should say "Logged in to github.com account ..." and list the token scopes.
Agents need `repo`, `workflow` (to change `.github/workflows`) and `read:org`.
Add the missing ones (it prints a code; open github.com/login/device on your
phone and enter it):

```sh
gh auth refresh -h github.com -s repo,workflow,read:org,admin:repo_hook
# add delete_repo too if agents may delete repositories (each one still waits for you)
```

Make `git push` over HTTPS use the same login, and set the commit author:

```sh
gh auth setup-git
git config --global user.name  "Your Name"
git config --global user.email "you@example.com"     # or your GitHub noreply address
cd ~/code/pluginscore && git remote -v                # should be https://github.com/...
# if it shows git@github.com:..., switch it:
# git remote set-url origin https://github.com/OWNER/pluginscore.git
git push --dry-run                                    # no password prompt = done
```

Not signed in at all? `gh auth login -h github.com -p https -w` (device code,
same as above), then the steps above.

Prefer a token you can scope to a few repositories? Create a fine-grained
token at github.com/settings/personal-access-tokens/new with access to the
chosen repositories and these permissions: Contents, Pull requests, Issues,
Workflows, Actions, Commit statuses, Deployments, Administration (only if
agents should change repo settings) set to Read and write; Metadata Read.
Then `sheaf env set GH_TOKEN` (paste it when asked) and `gh auth setup-git`.
`GH_TOKEN` wins over the `gh` login for agents.

## 2. Vercel

Install the CLI and sign in as `dev`:

```sh
sudo npm install -g vercel
vercel login            # prints a link and a code; open it on your phone
vercel whoami
```

Or use a token instead of the login (works the same for both agents and
does not expire unless you set it to): create one at
vercel.com/account/settings/tokens, scoped to your team, then

```sh
sheaf env set VERCEL_TOKEN          # paste the token; it is not echoed
```

The CLI reads `VERCEL_TOKEN`. On an older CLI that ignores it, agents add
`--token "$VERCEL_TOKEN"`.

Tell every worktree which project it is. `vercel link` writes a `.vercel`
folder that git ignores, so new task worktrees would not have it; the two IDs
below make every worktree linked:

```sh
cd ~/code/pluginscore
vercel link --yes                   # pick the existing project
cat .vercel/project.json            # shows orgId and projectId
sheaf env set VERCEL_ORG_ID         # paste orgId
sheaf env set VERCEL_PROJECT_ID     # paste projectId
```

Connect the GitHub repository in the Vercel project (Settings, Git) if it is
not already, so each push builds a preview and `main` deploys production.

What agents use: `vercel ls`, `vercel inspect URL --logs` (build log),
`vercel logs URL` (runtime log), `vercel env ls`, `vercel env pull`,
`vercel deploy` (preview), `vercel deploy --prod`, `vercel promote`,
`vercel rollback`, `vercel domains`, `vercel dns`, `vercel env add/rm`. The
production and delete commands wait for you.

## 3. Cloudflare

`wrangler login` needs a browser on the server, so use an API token. In the
Cloudflare dashboard: My Profile, API Tokens, Create Token, Create Custom
Token. For full operational access, add these permissions:

| Scope | Permission | Level |
|---|---|---|
| Account | Workers Scripts | Edit |
| Account | Workers KV Storage | Edit |
| Account | Workers R2 Storage | Edit |
| Account | D1 | Edit |
| Account | Cloudflare Pages | Edit |
| Account | Queues | Edit |
| Account | Workers Tail | Read |
| Account | Account Settings | Read |
| Zone | Zone | Read |
| Zone | DNS | Edit |
| Zone | Workers Routes | Edit |
| Zone | Cache Purge | Purge |
| Zone | Zone Settings | Edit |
| Zone | SSL and Certificates | Edit |
| User | User Details | Read |
| User | Memberships | Read |

Account Resources: your account. Zone Resources: all zones from that account.
Add more permissions later the same way (Edit token) if an agent reports a
403 for something you want it to do.

Your account ID is in the dashboard URL (`dash.cloudflare.com/<account id>/...`)
and on any zone's Overview page. Then, as `dev`:

```sh
sudo npm install -g wrangler
sheaf env set CLOUDFLARE_API_TOKEN    # paste the token
sheaf env set CLOUDFLARE_ACCOUNT_ID   # paste the account ID
set -a; . ~/.sheaf/env; set +a        # load them in this shell to test
wrangler whoami                       # shows your account and the token's permissions
```

For Git-based deploys, connect the repository in Workers & Pages (Workers
Builds or Pages, Settings, Builds) so `main` deploys and branches get
previews.

What agents use: `wrangler tail` (live logs), `wrangler deployments list`,
`wrangler kv`, `wrangler r2`, `wrangler d1`, `wrangler pages`,
`wrangler secret list`, the Cloudflare API with
`curl -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" https://api.cloudflare.com/client/v4/...`
for DNS and zone settings, and `wrangler deploy`, `wrangler rollback`,
deletes and secrets, which wait for you.

## 4. Let Codex use them

Codex's sandbox blocks writes outside the worktree, and these CLIs keep
state in your home directory. Codex may also hide variables whose names
contain `TOKEN`, `KEY` or `SECRET` from the commands it runs. Update its start
command (Claude Code needs nothing):

```sh
mkdir -p ~/.config ~/.cache ~/.npm ~/.local/share/com.vercel.cli
sheaf agent set codex --command "codex --sandbox workspace-write --ask-for-approval never \
  --add-dir $HOME/code/pluginscore/.git --add-dir $HOME/.config --add-dir $HOME/.cache \
  --add-dir $HOME/.npm --add-dir $HOME/.local/share/com.vercel.cli \
  -c sandbox_workspace_write.network_access=true \
  -c shell_environment_policy.ignore_default_excludes=true"
```

If a CLI still fails only under Codex with a permission or read-only error,
run Codex with `--sandbox danger-full-access` instead of `workspace-write`;
Sheaf's hook still sends risky commands to you.

## 5. Check from the agents themselves

Restart running agents so they pick up the new variables (Sheaf adds them
when it starts an agent). Then create a task for each, for example from
Telegram or the phone:

> Run `gh auth status`, `git push --dry-run`, `vercel whoami`, `vercel ls`
> and `wrangler whoami` in the repo and report which work.

`git push --dry-run` waits for your approval like a real push; approve it to
see the full path works.

## Managing credentials

```sh
sheaf env ls                        # names, values hidden
sheaf env set KEY                   # add or replace; value typed, not echoed
printf %s "$VALUE" | sheaf env set KEY
sheaf env rm KEY
sheaf env set KEY --agent codex     # only for one agent (~/.sheaf/agents/codex.env)
```

Rotating a token: create the new one, `sheaf env set` it, restart the agents,
then revoke the old one on the platform. Want them in your own SSH shell too?
Add `set -a; . ~/.sheaf/env; set +a` to `~/.bashrc`.

## What waits for you

On top of the earlier defaults (`git push`, `sudo`, secrets; force pushes and
`curl ... | sh` are refused), these go to your queue. A leading `*` means the
command is caught anywhere in a line, such as after `cd api &&` or `npx`.

- GitHub: `git push` anywhere in a line, `gh pr merge`, `gh repo delete`,
  `gh repo archive`, `gh repo edit --visibility`, `gh release create/delete`,
  `gh secret set/delete`, `gh variable delete`, `gh api ... -X DELETE`.
- Vercel: anything with `--prod`, `promote`, `rollback`, `remove`/`rm`,
  `project rm`, `env rm`, `env add ... production`, `domains rm`, `dns rm`,
  `alias set/rm`, `certs rm`.
- Cloudflare: `wrangler deploy`/`publish`/`rollback`/`versions deploy`,
  any `delete`, `secret put/bulk`, `pages deploy` to `main` or `production`,
  `d1 execute --remote`, `d1 migrations apply --remote`.
- Any `curl -X DELETE`.

See or change an agent's rules with `sheaf agent policy claude > rules.json`,
edit, then `sheaf agent policy claude --set rules.json`. When rules overlap
the strictest wins, so to let pushes to task branches through without asking,
remove the `git push*` and `*git push*` rules and add
`{"action":"shell","match":"*git push*main*","effect":"ask"}` to keep pushes
to `main` gated.
