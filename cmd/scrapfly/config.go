package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"
)

// configFile is persisted at ~/.scrapfly/config.json. Fields are applied as
// defaults on every command that supports them; explicit flags always win.
type configFile struct {
	// Auth + connectivity.
	APIKey      string `json:"api_key,omitempty"`
	Host        string `json:"host,omitempty"`
	BrowserHost string `json:"browser_host,omitempty"`

	// Product defaults. *bool lets us distinguish "not set" from "false".
	ASP       *bool  `json:"asp,omitempty"`
	RenderJS  *bool  `json:"render_js,omitempty"`
	Country   string `json:"country,omitempty"`
	Format    string `json:"format,omitempty"`
	ProxyPool string `json:"proxy_pool,omitempty"`
	Cache     *bool  `json:"cache,omitempty"`
	Debug     *bool  `json:"debug,omitempty"`
}

// configKeys lists every settable key with its type (for validation + help).
var configKeys = map[string]string{
	"api-key":      "string",
	"host":         "string",
	"browser-host": "string",
	"asp":          "bool",
	"render-js":    "bool",
	"country":      "string",
	"format":       "string",
	"proxy-pool":   "string",
	"cache":        "bool",
	"debug":        "bool",
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

func boolPtr(v bool) *bool { return &v }

func setConfigField(c *configFile, key, value string) error {
	switch key {
	case "api-key":
		c.APIKey = value
	case "host":
		c.Host = value
	case "browser-host":
		c.BrowserHost = value
	case "country":
		c.Country = value
	case "format":
		c.Format = value
	case "proxy-pool":
		c.ProxyPool = value
	case "asp":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("asp expects true/false, got %q", value)
		}
		c.ASP = boolPtr(b)
	case "render-js":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("render-js expects true/false, got %q", value)
		}
		c.RenderJS = boolPtr(b)
	case "cache":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("cache expects true/false, got %q", value)
		}
		c.Cache = boolPtr(b)
	case "debug":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("debug expects true/false, got %q", value)
		}
		c.Debug = boolPtr(b)
	default:
		return fmt.Errorf("unknown config key %q (valid: %s)", key, validKeysHelp())
	}
	return nil
}

func unsetConfigField(c *configFile, key string) error {
	switch key {
	case "api-key":
		c.APIKey = ""
	case "host":
		c.Host = ""
	case "browser-host":
		c.BrowserHost = ""
	case "country":
		c.Country = ""
	case "format":
		c.Format = ""
	case "proxy-pool":
		c.ProxyPool = ""
	case "asp":
		c.ASP = nil
	case "render-js":
		c.RenderJS = nil
	case "cache":
		c.Cache = nil
	case "debug":
		c.Debug = nil
	default:
		return fmt.Errorf("unknown config key %q", key)
	}
	return nil
}

func validKeysHelp() string {
	keys := make([]string, 0, len(configKeys))
	for k := range configKeys {
		keys = append(keys, k)
	}
	return strings.Join(keys, ", ")
}

func newConfigCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage persistent CLI configuration (~/.scrapfly/config.json)",
		Long: `Persist API key, host, and product defaults (asp, render-js, country,
format, proxy-pool, cache, debug) so they apply to every command without
repeating flags.

Resolution order (highest wins):
  1. Explicit --flag on the command
  2. Environment variable (SCRAPFLY_API_KEY, etc.)
  3. ~/.scrapfly/config.json

Examples:
  scrapfly config set api-key scp-live-...
  scrapfly config set asp true
  scrapfly config set country us
  scrapfly config set format markdown
  scrapfly config view
  scrapfly config unset asp`,
	}
	cmd.AddCommand(newConfigSetCmd(flags))
	cmd.AddCommand(newConfigGetCmd(flags))
	cmd.AddCommand(newConfigUnsetCmd(flags))
	cmd.AddCommand(newConfigViewCmd(flags))
	cmd.AddCommand(newConfigClearCmd(flags))
	return cmd
}

func newConfigSetCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a config value",
		Long:  fmt.Sprintf("Valid keys: %s", validKeysHelp()),
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := loadConfig()
			if err != nil {
				return err
			}
			if err := setConfigField(c, args[0], args[1]); err != nil {
				return err
			}
			p, err := saveConfig(c)
			if err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "config.set", map[string]string{
				"key": args[0], "value": args[1], "path": p,
			})
		},
	}
}

func newConfigGetCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "Get a single config value",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := loadConfig()
			if err != nil {
				return err
			}
			val := getConfigField(c, args[0])
			if flags.pretty {
				out.Pretty(os.Stdout, "%s=%s", args[0], val)
				return nil
			}
			return out.WriteSuccess(os.Stdout, false, "config.get", map[string]string{
				"key": args[0], "value": val,
			})
		},
	}
}

func getConfigField(c *configFile, key string) string {
	switch key {
	case "api-key":
		return redact(c.APIKey)
	case "host":
		return c.Host
	case "browser-host":
		return c.BrowserHost
	case "country":
		return c.Country
	case "format":
		return c.Format
	case "proxy-pool":
		return c.ProxyPool
	case "asp":
		return boolPtrStr(c.ASP)
	case "render-js":
		return boolPtrStr(c.RenderJS)
	case "cache":
		return boolPtrStr(c.Cache)
	case "debug":
		return boolPtrStr(c.Debug)
	}
	return "(unknown key)"
}

func boolPtrStr(b *bool) string {
	if b == nil {
		return "(not set)"
	}
	return strconv.FormatBool(*b)
}

func newConfigUnsetCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "unset <key>",
		Short: "Remove a config value (revert to default)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := loadConfig()
			if err != nil {
				return err
			}
			if err := unsetConfigField(c, args[0]); err != nil {
				return err
			}
			p, err := saveConfig(c)
			if err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "config.unset", map[string]string{
				"key": args[0], "path": p,
			})
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
				"path":         p,
				"api_key":      redact(c.APIKey),
				"host":         c.Host,
				"browser_host": c.BrowserHost,
				"asp":          boolPtrStr(c.ASP),
				"render_js":    boolPtrStr(c.RenderJS),
				"country":      c.Country,
				"format":       c.Format,
				"proxy_pool":   c.ProxyPool,
				"cache":        boolPtrStr(c.Cache),
				"debug":        boolPtrStr(c.Debug),
			}
			if flags.pretty {
				out.Pretty(os.Stdout, "%s", p)
				for _, k := range []string{"api_key", "host", "browser_host", "asp", "render_js", "country", "format", "proxy_pool", "cache", "debug"} {
					v := view[k]
					if v == "" || v == "(not set)" {
						continue
					}
					out.Pretty(os.Stdout, "  %s = %v", k, v)
				}
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

// applyProductDefaults reads the config file and sets product-level defaults
// for any flag that wasn't explicitly passed on the command line. Called at
// the top of scrape/screenshot/crawl RunE.
func applyProductDefaults(cmd *cobra.Command, strings map[string]*string, bools map[string]*bool) {
	cfg, err := loadConfig()
	if err != nil {
		return
	}
	defaults := map[string]string{
		"country":    cfg.Country,
		"format":     cfg.Format,
		"proxy-pool": cfg.ProxyPool,
	}
	boolDefaults := map[string]*bool{
		"asp":       cfg.ASP,
		"render-js": cfg.RenderJS,
		"cache":     cfg.Cache,
		"debug":     cfg.Debug,
	}
	for flag, ptr := range strings {
		if ptr == nil {
			continue
		}
		if val, ok := defaults[flag]; ok && val != "" && !cmd.Flags().Changed(flag) {
			*ptr = val
		}
	}
	for flag, ptr := range bools {
		if ptr == nil {
			continue
		}
		if val, ok := boolDefaults[flag]; ok && val != nil && !cmd.Flags().Changed(flag) {
			*ptr = *val
		}
	}
}

func redact(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "***"
	}
	return key[:4] + "..." + key[len(key)-4:]
}
