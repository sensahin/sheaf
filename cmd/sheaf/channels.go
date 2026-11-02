package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sensahin/sheaf/internal/api"
	"github.com/sensahin/sheaf/internal/channels"
	"github.com/sensahin/sheaf/internal/config"
)

const restartHint = `Restart the daemon to use it:
  systemctl --user restart sheaf      (if it runs as a service)
or stop and start ` + "`sheaf daemon`" + ` yourself. Then check with: sheaf channel test`

func cmdChannel(ctx context.Context, paths config.Paths, args []string) error {
	if len(args) == 0 {
		args = []string{"ls"}
	}
	cfg, err := channels.LoadConfig(paths.Home)
	if err != nil {
		return err
	}
	say := func(s string) { fmt.Println(s) }
	switch args[0] {
	case "ls", "list":
		if len(cfg.Names()) == 0 && cfg.Connector == nil {
			fmt.Println("No channels yet. Add one with `sheaf channel add telegram TOKEN` (see docs/channels.md).")
			return nil
		}
		if cfg.Telegram != nil {
			fmt.Printf("telegram  @%s, chat %d\n", cfg.Telegram.Bot, cfg.Telegram.ChatID)
		}
		if cfg.Slack != nil {
			fmt.Printf("slack     DM %s with user %s\n", cfg.Slack.Channel, cfg.Slack.User)
		}
		if cfg.Connector != nil {
			fmt.Printf("connector %s/mcp (listening on %s)\n", cfg.Connector.URL, cfg.Connector.Listen)
		}
		return nil
	case "add":
		if len(args) < 2 {
			return errors.New("usage: sheaf channel add telegram TOKEN | slack --bot-token T --app-token T")
		}
		switch args[1] {
		case "telegram":
			fs := flag.NewFlagSet("channel add telegram", flag.ContinueOnError)
			tokenFlag := fs.String("token", "", "the bot token from @BotFather")
			pos, err := parse(fs, args[2:])
			if err != nil {
				return err
			}
			token := *tokenFlag
			if token == "" && len(pos) > 0 {
				token = pos[0]
			}
			if token == "" {
				token = os.Getenv("TELEGRAM_BOT_TOKEN")
			}
			if token == "" {
				return errors.New("give the bot token from @BotFather: sheaf channel add telegram 123456:ABC...")
			}
			tg, err := channels.PairTelegram(ctx, &channels.TelegramAPI{Token: strings.TrimSpace(token)}, say)
			if err != nil {
				return err
			}
			cfg.Telegram = &tg
			if err := channels.SaveConfig(paths.Home, cfg); err != nil {
				return err
			}
			fmt.Printf("\nPaired with your Telegram chat. Saved to %s.\n\n%s\n", channels.ConfigPath(paths.Home), restartHint)
			return nil
		case "slack":
			fs := flag.NewFlagSet("channel add slack", flag.ContinueOnError)
			bot := fs.String("bot-token", os.Getenv("SLACK_BOT_TOKEN"), "the app's Bot User OAuth Token (xoxb-...)")
			app := fs.String("app-token", os.Getenv("SLACK_APP_TOKEN"), "an app-level token with connections:write (xapp-...)")
			if _, err := parse(fs, args[2:]); err != nil {
				return err
			}
			if !strings.HasPrefix(*bot, "xoxb-") || !strings.HasPrefix(*app, "xapp-") {
				return errors.New("give both tokens: --bot-token xoxb-... --app-token xapp-... (see docs/channels.md)")
			}
			sl, err := channels.PairSlack(ctx, &channels.SlackAPI{BotToken: *bot, AppToken: *app}, say)
			if err != nil {
				return err
			}
			cfg.Slack = &sl
			if err := channels.SaveConfig(paths.Home, cfg); err != nil {
				return err
			}
			fmt.Printf("\nPaired with your Slack DM. Saved to %s.\n\n%s\n", channels.ConfigPath(paths.Home), restartHint)
			return nil
		}
		return fmt.Errorf("unknown channel %q: telegram or slack", args[1])
	case "rm", "remove":
		if err := need(args[1:], 1, "channel rm telegram|slack"); err != nil {
			return err
		}
		switch args[1] {
		case "telegram":
			cfg.Telegram = nil
		case "slack":
			cfg.Slack = nil
		default:
			return fmt.Errorf("unknown channel %q", args[1])
		}
		if err := channels.SaveConfig(paths.Home, cfg); err != nil {
			return err
		}
		fmt.Println("Removed. Restart the daemon to stop using it.")
		return nil
	case "test":
		c, err := client(paths)
		if err != nil {
			return err
		}
		var out map[string]int
		if err := c.Call(ctx, "channel.test", api.TextReq{Text: strings.Join(args[1:], " ")}, &out); err != nil {
			return err
		}
		fmt.Printf("Delivered to %d channel(s). Check your phone.\n", out["delivered"])
		return nil
	}
	return fmt.Errorf("unknown channel command %q", args[0])
}

func cmdConnector(ctx context.Context, paths config.Paths, args []string) error {
	if len(args) == 0 {
		args = []string{"status"}
	}
	cfg, err := channels.LoadConfig(paths.Home)
	if err != nil {
		return err
	}
	switch args[0] {
	case "enable":
		fs := flag.NewFlagSet("connector enable", flag.ContinueOnError)
		u := fs.String("url", "", "the public HTTPS address that reaches the connector, e.g. https://box.tail1234.ts.net:8443")
		listen := fs.String("listen", defaultConnector, "the local address the connector listens on")
		if _, err := parse(fs, args[1:]); err != nil {
			return err
		}
		if !strings.HasPrefix(*u, "https://") {
			return errors.New("give the public address: sheaf connector enable --url https://HOST:8443 (see docs/channels.md)")
		}
		cfg.Connector = &channels.ConnectorConfig{Listen: *listen, URL: strings.TrimRight(*u, "/")}
		if err := channels.SaveConfig(paths.Home, cfg); err != nil {
			return err
		}
		fmt.Printf("Connector enabled on %s.\n\n%s\n\nThen add this URL as a custom connector in Claude or ChatGPT:\n  %s/mcp\n",
			*listen, strings.Replace(restartHint, "sheaf channel test", "curl -s "+cfg.Connector.URL+"/.well-known/oauth-authorization-server", 1), cfg.Connector.URL)
		return nil
	case "disable":
		cfg.Connector = nil
		if err := channels.SaveConfig(paths.Home, cfg); err != nil {
			return err
		}
		fmt.Println("Connector disabled. Restart the daemon. Signed-in apps keep a device token until you run `sheaf device revoke ID` (see `sheaf device ls`).")
		return nil
	case "code":
		c, err := client(paths)
		if err != nil {
			return err
		}
		var out map[string]string
		if err := c.Call(ctx, "connector.code", nil, &out); err != nil {
			return err
		}
		fmt.Printf("Connect code: %s\nType it on the Sheaf sign-in page within 10 minutes. It works once.\n", out["code"])
		return nil
	case "status":
		if cfg.Connector == nil {
			fmt.Println("The connector is off. Turn it on with `sheaf connector enable --url https://...` (see docs/channels.md).")
			return nil
		}
		fmt.Printf("Connector: %s/mcp (listening on %s)\n", cfg.Connector.URL, cfg.Connector.Listen)
		return nil
	}
	return fmt.Errorf("unknown connector command %q", args[0])
}
