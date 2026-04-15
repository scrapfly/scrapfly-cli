package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

func newMcpPlaywrightCmd(flags *rootFlags) *cobra.Command {
	var launchCfg browserLaunchFlags
	cmd := &cobra.Command{
		Use:   "playwright",
		Short: "Start the Playwright MCP server connected to a Scrapfly browser",
		Long: `Mints a Scrapfly CDP URL and launches @playwright/mcp via npx,
pointing it at the Scrapfly browser. This gives any MCP client (Claude
Desktop, Cursor, Claude Code) the full Playwright tool surface (navigate,
click, fill, screenshot, pdf, ...) backed by Scrapfly's anti-bot network.

Requires Node.js (npx) on the machine.

Equivalent to running manually:
  npx @playwright/mcp --cdp-endpoint "$(scrapfly browser --pretty)"`,
		Example: `  # Add to Claude Desktop config:
  # { "command": "scrapfly", "args": ["mcp", "playwright", "--country", "us"] }

  # Or run directly:
  scrapfly mcp playwright --country us --resolution 1920x1080`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := exec.LookPath("npx"); err != nil {
				return fmt.Errorf("npx not found (install Node.js to use the Playwright MCP bridge)")
			}
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			wsURL := client.CloudBrowser(launchCfg.toConfig())
			fmt.Fprintf(os.Stderr, "[mcp playwright] CDP URL minted (browser flags applied)\n")

			npx := exec.Command("npx", "@playwright/mcp", "--cdp-endpoint", wsURL)
			npx.Stdin = os.Stdin
			npx.Stdout = os.Stdout
			npx.Stderr = os.Stderr
			npx.Env = os.Environ()
			if flags.insecure {
				npx.Env = append(npx.Env, "NODE_TLS_REJECT_UNAUTHORIZED=0")
			}
			return npx.Run()
		},
	}
	bindBrowserLaunchFlags(cmd, &launchCfg)
	return cmd
}
