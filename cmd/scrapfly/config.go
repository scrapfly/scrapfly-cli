package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"
)

type configFile struct {
	APIKey string `json:"api_key,omitempty"`
	Host   string `json:"host,omitempty"`
}

func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".scrapfly", "config.json"), nil
}

func loadConfig() (*configFile, error) {
	p, err := configPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return &configFile{}, nil
	}
	if err != nil {
		return nil, err
	}
	var c configFile
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	return &c, nil
}

func saveConfig(c *configFile) (string, error) {
	p, err := configPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		return "", err
	}
	return p, nil
}

func newConfigCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage persistent CLI configuration (~/.scrapfly/config.json)",
		Long: `Persist the API key and host so you don't need to export them per session.

Resolution order for api-key and host (highest wins):
  1. --api-key / --host flag
  2. SCRAPFLY_API_KEY / SCRAPFLY_API_HOST env var
  3. ~/.scrapfly/config.json

The file is created with mode 0600 (user read/write only).`,
	}
	cmd.AddCommand(newConfigSetKeyCmd(flags))
	cmd.AddCommand(newConfigSetHostCmd(flags))
	cmd.AddCommand(newConfigViewCmd(flags))
	cmd.AddCommand(newConfigClearCmd(flags))
	return cmd
}

func newConfigSetKeyCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "set-key <api-key>",
		Short: "Store an API key in the config file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := loadConfig()
			if err != nil {
				return err
			}
			c.APIKey = args[0]
			p, err := saveConfig(c)
			if err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "config.set-key", map[string]string{"path": p})
		},
	}
}

func newConfigSetHostCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "set-host <url>",
		Short: "Store a default API host in the config file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := loadConfig()
			if err != nil {
				return err
			}
			c.Host = args[0]
			p, err := saveConfig(c)
			if err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "config.set-host", map[string]string{"path": p})
		},
	}
}

func newConfigViewCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "view",
		Short: "Print the current config (api-key is redacted)",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := loadConfig()
			if err != nil {
				return err
			}
			p, _ := configPath()
			view := map[string]any{
				"path":        p,
				"has_api_key": c.APIKey != "",
				"api_key":     redact(c.APIKey),
				"host":        c.Host,
			}
			if flags.pretty {
				out.Pretty(os.Stdout, "%s api_key=%s host=%q", p, view["api_key"], c.Host)
				return nil
			}
			return out.WriteSuccess(os.Stdout, false, "config.view", view)
		},
	}
}

func newConfigClearCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Delete the config file",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := configPath()
			if err != nil {
				return err
			}
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "config.clear", map[string]string{"path": p, "status": "removed"})
		},
	}
}

func redact(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "***"
	}
	return key[:4] + "…" + key[len(key)-4:]
}
