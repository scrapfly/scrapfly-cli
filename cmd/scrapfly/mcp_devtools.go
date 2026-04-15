package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

func newMcpDevtoolsCmd(flags *rootFlags) *cobra.Command {
	var launchCfg browserLaunchFlags
	cmd := &cobra.Command{
		Use:   "devtools",
		Short: "Start Chrome DevTools MCP server connected to a Scrapfly browser",
		Long: `Mints a Scrapfly CDP URL and launches chrome-devtools-mcp via npx,
pointing it at the Scrapfly browser via --wsEndpoint. This gives any MCP
client the Chrome DevTools tool surface (navigation, scripting, network,
performance, emulation, screenshots, ...) backed by Scrapfly's anti-bot
network.

Requires Node.js (npx).

Equivalent to:
  npx chrome-devtools-mcp --wsEndpoint "$(scrapfly browser --pretty)" --acceptInsecureCerts`,
		Example: `  # Add to Claude Desktop config:
  # { "command": "scrapfly", "args": ["mcp", "devtools", "--country", "us"] }

  # Or run directly:
  scrapfly mcp devtools --country us --resolution 1920x1080`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := exec.LookPath("npx"); err != nil {
				return fmt.Errorf("npx not found (install Node.js to use the DevTools MCP bridge)")
			}
			launchCfg.enableMCP = true
			if sessionIDFlag != "" {
				launchCfg.session = sessionIDFlag
			}
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			wsURL := client.CloudBrowser(launchCfg.toConfig())
			fmt.Fprintf(os.Stderr, "[mcp devtools] CDP URL minted\n")

			npxArgs := []string{"chrome-devtools-mcp", "--wsEndpoint", wsURL}
			if flags.insecure {
				npxArgs = append(npxArgs, "--acceptInsecureCerts")
			}

			npx := exec.Command("npx", npxArgs...)
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
