package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/scrapfly/scrapfly-cli/internal/cdp"
	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"
)

func newBrowserMcpCmd(flags *rootFlags) *cobra.Command {
	var launchCfg browserLaunchFlags
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Start a browser with built-in MCP enabled and print the MCP endpoint URL",
		Long: `Allocates a Scrapfly Cloud Browser with Chromium's built-in MCP server
enabled (enable_mcp=true). Connects via CDP to keep the session alive,
then prints the streamable-HTTP MCP endpoint URL.

Point your MCP client (Claude Desktop, Cursor, Claude Code) at the
printed URL. The browser stays alive until you Ctrl-C this process.

No Node.js, no Playwright, no intermediary.`,
		Example: `  # Start and get the endpoint
  scrapfly browser mcp --country us --resolution 1920x1080

  # Use the printed URL in Claude Desktop config:
  # { "url": "https://browser.scrapfly.io/mcp?api_key=..." }`,
		RunE: func(cmd *cobra.Command, args []string) error {
			launchCfg.enableMCP = true
			// Copy the persistent --session flag into the launch config so the
			// session name appears in the CDP URL → allocation → Redis.
			if sessionIDFlag != "" {
				launchCfg.session = sessionIDFlag
			}

			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			wsURL := client.CloudBrowser(launchCfg.toConfig())

			sfClient := client

			// Derive a fallback MCP endpoint from the WSS URL. The real
			// endpoint comes from the sessions API after allocation.
			derivedEndpoint, err := deriveMCPEndpoint(wsURL)
			if err != nil {
				return err
			}
			mcpEndpoint := derivedEndpoint

			// Connect via CDP to keep the session alive.
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			fmt.Fprintf(os.Stderr, "[browser mcp] connecting to keep session alive...\n")
			cdpClient, err := cdp.Dial(ctx, wsURL)
			if err != nil {
				return fmt.Errorf("cdp dial: %w", err)
			}
			defer cdpClient.Close()

			sess, err := cdp.Attach(ctx, cdpClient)
			if err != nil {
				_ = cdpClient.Close()
				return fmt.Errorf("cdp attach: %w", err)
			}
			defer sess.Detach(context.Background())

			// Poll the sessions API for the real mcp_endpoint returned by the
			// allocation (agent populates it when enable_mcp=true). Fall back
			// to the derived URL if the API doesn't return one yet.
			if launchCfg.session != "" {
				for i := 0; i < 10; i++ {
					sessions, err := sfClient.CloudBrowserSessions()
					if err == nil {
						if list, ok := sessions["sessions"].([]any); ok {
							for _, s := range list {
								if m, ok := s.(map[string]any); ok {
									if sid, _ := m["session_id"].(string); sid == launchCfg.session {
										if ep, _ := m["mcp_endpoint"].(string); ep != "" {
											mcpEndpoint = ep
											break
										}
									}
								}
							}
						}
					}
					if mcpEndpoint != derivedEndpoint {
						break
					}
					time.Sleep(500 * time.Millisecond)
				}
			}

			fmt.Fprintf(os.Stderr, "[browser mcp] session alive. MCP endpoint ready.\n")
			fmt.Fprintf(os.Stderr, "[browser mcp] Ctrl-C to stop.\n\n")

			if flags.pretty {
				out.Pretty(os.Stdout, "%s", mcpEndpoint)
			} else {
				_ = out.WriteSuccess(os.Stdout, false, "browser.mcp", map[string]string{
					"mcp_endpoint": mcpEndpoint,
					"ws_url":       wsURL,
				})
			}

			// Block until signal.
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			select {
			case <-sigCh:
				fmt.Fprintf(os.Stderr, "\n[browser mcp] shutting down...\n")
			case <-ctx.Done():
			}
			return nil
		},
	}
	bindBrowserLaunchFlags(cmd, &launchCfg)
	return cmd
}

// deriveMCPEndpoint converts a WSS CDP URL to the Chromium MCP HTTP endpoint.
// wss://host?params -> https://host/mcp?params (minus enable_mcp param itself)
func deriveMCPEndpoint(wsURL string) (string, error) {
	u, err := url.Parse(wsURL)
	if err != nil {
		return "", err
	}
	// Switch scheme.
	switch {
	case strings.HasPrefix(u.Scheme, "wss"):
		u.Scheme = "https"
	case strings.HasPrefix(u.Scheme, "ws"):
		u.Scheme = "http"
	}
	u.Path = "/mcp"
	// Remove enable_mcp from query (it was only needed for allocation).
	q := u.Query()
	q.Del("enable_mcp")
	u.RawQuery = q.Encode()
	return u.String(), nil
}
