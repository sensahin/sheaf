package channels

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Config is $SHEAF_HOME/channels.json: the channels this Sheaf talks
// through. It holds bot tokens, so it is written readable only by its owner.
type Config struct {
	Telegram *TelegramConfig `json:"telegram,omitempty"`
	Slack    *SlackConfig    `json:"slack,omitempty"`
	// Connector serves Sheaf to the Claude and ChatGPT apps.
	Connector *ConnectorConfig `json:"connector,omitempty"`
}

// ConnectorConfig is where the remote MCP connector listens and the public
// HTTPS address that reaches it.
type ConnectorConfig struct {
	Listen string `json:"listen"`
	URL    string `json:"url"`
}

// ConfigPath is where the channel config lives under home.
func ConfigPath(home string) string { return filepath.Join(home, "channels.json") }

// LoadConfig reads the config; a missing file is an empty config.
func LoadConfig(home string) (Config, error) {
	var c Config
	b, err := os.ReadFile(ConfigPath(home))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}

// SaveConfig writes the config with owner-only permissions.
func SaveConfig(home string, c Config) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := ConfigPath(home) + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, ConfigPath(home))
}

// Names lists the configured channels.
func (c Config) Names() []string {
	var out []string
	if c.Telegram != nil {
		out = append(out, "telegram")
	}
	if c.Slack != nil {
		out = append(out, "slack")
	}
	return out
}

// Build returns the configured channels.
func (c Config) Build() []Channel {
	var out []Channel
	if c.Telegram != nil {
		out = append(out, NewTelegram(*c.Telegram))
	}
	if c.Slack != nil {
		out = append(out, NewSlack(*c.Slack))
	}
	return out
}
