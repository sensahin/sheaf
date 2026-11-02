# Talk to your agents from Telegram, Slack, Claude and ChatGPT

With channels, you no longer have to open the Sheaf queue. Agents message you
in apps you already use, and you answer there:

| Channel | Agents ping you | You approve / deny | You answer questions | You create tasks | Needs a public address |
|---|---|---|---|---|---|
| **Telegram** | yes | buttons, or reply | reply to the message | `/task claude ...` | no |
| **Slack** | yes | buttons, or reply in thread | reply in thread | `task claude ...` | no |
| **Claude app** (custom connector) | no, you ask | yes | yes | yes | yes (HTTPS) |
| **ChatGPT app** (developer mode) | no, you ask | yes | yes | yes | yes (HTTPS) |

Chat apps such as Claude and ChatGPT cannot start a conversation with you, so
they can't ping you. Use Telegram or Slack for alerts, and the Claude or
ChatGPT app for longer back-and-forth ("what is claude doing on the about page?
tell codex to review it").

Everything below runs on the machine where `sheaf daemon` runs (your server).
After any change, restart the daemon:

```sh
systemctl --user restart sheaf     # when it runs as a service (docs/remote-server.md)
```

## Telegram (recommended)

The bot long-polls Telegram, so your server can stay private. Only your own
chat can use it: messages from anyone else are ignored.

1. On your phone, open Telegram, talk to **@BotFather**, send `/newbot`, pick
   a name and a username ending in `bot`. It replies with a token like
   `123456789:AAH...`.
2. On the server:

   ```sh
   sheaf channel add telegram 123456789:AAH...
   ```

   It prints a `https://t.me/<your bot>?start=...` link. Open it on your phone
   and press **Start**. The one-time code in the link pairs the bot with your
   chat.
3. Restart the daemon, then check:

   ```sh
   systemctl --user restart sheaf
   sheaf channel test
   ```

### Using it

- **Approvals** arrive with **✅ Approve** and **❌ Deny** buttons. To deny
  and tell the agent why, reply to the message with your reason. A reply of
  `yes`/`ok` approves and `no` denies.
- **Questions** open a reply box. Your reply is the answer, and the agent
  carries on.
- **Messages** from an agent: reply to send your answer to that agent's inbox.
  Sheaf wakes the agent if it is idle.
- **Stuck on a terminal prompt.** An agent sometimes stops on its own
  prompt, such as Claude Code's "Do you want to proceed?" or a trust question,
  which never reaches the Sheaf queue. Claude Code reports this as
  *blocked*. When the daemon runs the agent in tmux, the notice shows the last
  lines of its terminal with **1 / 2 / 3 / Enter / Esc / Screen** buttons. Tap
  one to pick a menu option, or reply to type text and Enter. After each tap
  you get the new screen. `/screen claude` brings up the same view at any
  time.
- **Finished** notices (and blocked agents that aren't Sheaf-run): reply to
  message the agent about that task.
- Commands:

  ```
  /task claude Redesign the about page        create a task and start claude on it
  /task codex repo:pluginscore Review t-abc   pick the repository when you have several
  @claude also check mobile                   message an agent
  /queue      what is waiting for you (with buttons)
  /tasks      active tasks
  /agents     who is doing what
  /screen claude   the last lines of claude's terminal
  /approve ID   /deny ID [reason]   /answer ID text
  ```

  Any lines after the first line of `/task` become the task's description.

## Slack

Like the Telegram bot, it uses Socket Mode, so your server needs no public
address. It talks to you in a direct message with the app.

1. Go to <https://api.slack.com/apps> → **Create New App** → **From a
   manifest**, pick your workspace, choose JSON and paste:

   ```json
   {
     "display_information": { "name": "Sheaf" },
     "features": {
       "app_home": { "messages_tab_enabled": true, "messages_tab_read_only_enabled": false },
       "bot_user": { "display_name": "Sheaf", "always_online": true }
     },
     "oauth_config": { "scopes": { "bot": ["chat:write", "im:history", "im:read", "im:write"] } },
     "settings": {
       "event_subscriptions": { "bot_events": ["message.im"] },
       "interactivity": { "is_enabled": true },
       "socket_mode_enabled": true
     }
   }
   ```

2. **Basic Information** → **App-Level Tokens** → **Generate Token and
   Scopes**, add the scope `connections:write`, and copy the `xapp-...` token.
3. **Install App** → **Install to Workspace**, then copy the **Bot User OAuth
   Token** (`xoxb-...`).
4. On the server:

   ```sh
   sheaf channel add slack --bot-token xoxb-... --app-token xapp-...
   ```

   It tells you to send `pair <code>` to the app in a direct message. Open
   Slack, find **Sheaf** under Apps, and send the code.
5. Restart the daemon and run `sheaf channel test`.

Slack keeps `/word` for its own slash commands, so in Slack you type commands
without the slash: `task claude Redesign the about page`, `queue`, `tasks`,
`agents`. To answer something, reply in its thread.

## Claude and ChatGPT apps (connector)

Sheaf can act as a remote MCP server ("custom connector") for the Claude and
ChatGPT apps, on the web, desktop and phone. The connector has these tools:
`overview`, `list_tasks`, `get_task`, `create_task`, `message_agent`,
`list_approvals`, `decide`, `read_messages`, `agent_screen`, `answer_terminal`
(answer a prompt the agent is stopped on in its terminal) and `list_repos`.

The apps connect from their own cloud, so the connector needs a public HTTPS
address. It runs on its **own port** and serves only the connector and its
sign-in, never the web queue or the API. Every call needs a token that only
you can get by signing in (below).

### 1. Give it a public address with Tailscale Funnel

Claude connects to MCP and OAuth endpoints on standard HTTPS port 443.
If you already followed docs/remote-server.md, move the private web queue to
8443 before putting the connector on 443. This changes the dashboard address;
it does not make the dashboard public. Pair an existing device again at the
new dashboard address if its browser asks for a token.

```sh
sudo tailscale serve --https=443 off
sudo tailscale serve --bg --https=8443 http://127.0.0.1:7517
sudo tailscale funnel --bg --https=443 http://127.0.0.1:7518
tailscale funnel status          # connector: https://<server>.<tailnet>.ts.net
sheaf service --public-url https://<server>.<tailnet>.ts.net:8443 > ~/.config/systemd/user/sheaf.service
systemctl --user daemon-reload
```

If Tailscale says Funnel is not enabled for your tailnet, open the link it
prints and allow it. That is a one-time click in the Tailscale admin console.

Any other HTTPS reverse proxy (Caddy, Cloudflare Tunnel) works too. Point it at
`127.0.0.1:7518`.

### 2. Turn the connector on

```sh
sheaf connector enable --url https://<server>.<tailnet>.ts.net
systemctl --user restart sheaf
curl -s https://<server>.<tailnet>.ts.net/.well-known/oauth-authorization-server   # should print JSON
```

### 3. Add it to the app

- **Claude** (claude.ai or the Claude app): **Settings → Connectors → Add
  custom connector**. Name it `Sheaf` and enter the URL
  `https://<server>.<tailnet>.ts.net/mcp`. Choose **Register automatically**
  (DCR) for the OAuth client; Sheaf does not support CIMD.
- **ChatGPT**: **Settings → Apps & Connectors → Advanced settings**, turn on
  **Developer mode**, then **Create** a connector with the same URL and
  **OAuth** authentication. Developer mode depends on your ChatGPT plan.

Click **Connect**. A Sheaf sign-in page opens and asks for a **connect code**.
If Telegram or Slack is set up, the code has just been sent there. Otherwise
run this on the server:

```sh
sheaf connector code
```

Type the code and you are signed in. Then ask things like *"What are my agents
doing?"*, *"Create a task for claude to redesign the about page"*, or
*"Anything waiting for me?"*. The app asks you before it decides an approval.

The app's sign-in is a paired device. To sign it out:

```sh
sheaf device ls                  # the connector shows as "connector: Claude"
sheaf device revoke d-xxxxxxxx
```

### Security notes

- The sign-in page is public. Codes are 8 digits, work once, expire after 10
  minutes and die after 5 wrong tries. Sheaf makes at most one new code a
  minute.
- A connector token acts as you, with the rights of a paired phone. It can
  create tasks only in repositories you registered (`sheaf repo add`). It
  cannot mint tokens or change policies.
- `sheaf connector disable` and a restart close the port.

## ChatGPT Dot as a go-between

A Dot is ChatGPT's always-on agent. It works through ChatGPT's connectors, and
you can reach it in ChatGPT, Slack and Teams. If your Dot can use the Sheaf
connector above, you can give it a standing job such as *"watch my Sheaf queue
and tell me when something is waiting"*. This hasn't been tested, and OpenAI
has not said whether Dots can use custom connectors. Keep approvals with you:
tell the Dot never to call `decide` on its own. For dependable alerts, use
Telegram or Slack.

## Where it lives

- `~/.sheaf/channels.json` (mode 0600) holds the bot tokens, chat ids and the
  connector address. `sheaf channel ls` shows them, and
  `sheaf channel rm telegram` removes one.
- `~/.sheaf/connector.json` lists the apps that registered for sign-in.
- Messages an agent sends you count as read once a channel delivers them, so
  the agent's `message_status` shows that you got it.
