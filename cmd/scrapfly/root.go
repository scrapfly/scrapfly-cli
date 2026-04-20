package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"
)

var version = "0.3.2"

type rootFlags struct {
	apiKey      string
	host        string
	browserHost string
	insecure    bool
	timeout     time.Duration
	pretty      bool
	outputPath  string
	outputDir   string
}

// knownSubcommands lists top-level subcommand names (and aliases) used by the
// root shortcut to tell "scrapfly https://..." from "scrapfly scrape ...".
var knownSubcommands = map[string]struct{}{
	"scrape": {}, "scraper": {}, "screenshot": {}, "extract": {},
	"crawl": {}, "account": {}, "status": {}, "config": {},
	"browser": {}, "agent": {}, "selector": {}, "mcp": {},
	"update": {}, "version": {}, "help": {}, "completion": {}, "__complete": {},
}

// rewriteURLShortcut inserts "scrape" before the first URL-looking arg, as
// long as no known subcommand appears earlier. Lets users write
// `scrapfly --pretty https://example.com` instead of `scrapfly scrape ...`.
//
// Must be careful NOT to treat flag values as positional URLs: when the
// previous arg looks like a flag (`--host`, `-h`, ...) and has no `=`,
// the URL belongs to that flag, not the shortcut.
func rewriteURLShortcut(args []string) []string {
	for i, a := range args {
		lc := strings.ToLower(a)
		if !(strings.HasPrefix(lc, "http://") || strings.HasPrefix(lc, "https://")) {
			continue
		}
		// Skip if this URL is the value of a preceding --flag (no `=`).
		if i > 0 {
			prev := args[i-1]
			if strings.HasPrefix(prev, "-") && !strings.Contains(prev, "=") {
				continue
			}
		}
		for _, prev := range args[:i] {
			if _, isCmd := knownSubcommands[strings.ToLower(prev)]; isCmd {
				return args
			}
		}
		out := make([]string, 0, len(args)+1)
		out = append(out, args[:i]...)
		out = append(out, "scrape")
		out = append(out, args[i:]...)
		return out
	}
	return args
}

func execute(args []string) error {
	var flags rootFlags
	var showStatus bool

	root := &cobra.Command{
		Use:   "scrapfly",
		Short: "Scrapfly command-line client for web scraping, screenshots, extraction and crawling",
		Long: `Scrapfly CLI — drive the Scrapfly product APIs from the terminal.

Authentication:
  Set SCRAPFLY_API_KEY in the environment, or pass --api-key on any command.
  Dev/self-hosted stacks: --host https://... (or SCRAPFLY_API_HOST) and
  --insecure to skip TLS verification.

Output:
  Default is a JSON envelope {success, product, data|error} on stdout and
  exit code 0/1. --pretty prints a one-line human summary instead. Binary
  products (screenshots, crawl artifacts) require -o <file> or -O <dir>.

Products:
  scrape      Web Scraping API     — single URL, rendering, ASP, extraction.
  screenshot  Screenshot API       — dedicated image capture.
  extract     Extraction API       — AI/template-based data extraction.
  crawl       Crawler API          — recursive crawls with status polling.
  account     Account API          — plan, usage, quota.
  browser     Browser API          — CDP sessions, unblock, extensions, execute.

Examples:
  # Scrape with JS render + ASP, markdown output
  scrapfly scrape https://web-scraping.dev/products \
    --render-js --asp --country us --format markdown

  # Screenshot saved into a directory
  scrapfly -O ./shots screenshot https://example.com --resolution 1920x1080

  # Pipe scrape body into extract (Scrapfly-fetched + AI extraction)
  scrapfly scrape https://web-scraping.dev/product/1 --render-js --proxified \
    | scrapfly extract --content-type text/html --prompt "name, price, sku"

  # Crawl 20 pages synchronously
  scrapfly crawl run https://example.com --max-pages 20 --content-format markdown`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			// Non-blocking update check that fires at most once per
			// updateCheckCacheTTL (~24h). Emits a one-line stderr hint
			// when a newer release is available. Skip when:
			//   - user is already running `scrapfly update ...` (noise)
			//   - running MCP stdio transport (stderr could confuse hosts)
			//   - SCRAPFLY_NO_UPDATE_CHECK=1 (explicit opt-out, for CI)
			if os.Getenv("SCRAPFLY_NO_UPDATE_CHECK") == "1" {
				return
			}
			name := cmd.Name()
			if name == "update" || name == "mcp" || name == "version" {
				// version runs its own nag so we don't duplicate.
				return
			}
			// Also skip for any command whose parent chain includes mcp.
			for p := cmd.Parent(); p != nil; p = p.Parent() {
				if p.Name() == "mcp" {
					return
				}
			}
			if nag := maybeUpdateNag(); nag != "" {
				fmt.Fprintln(os.Stderr, nag)
			}
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if showStatus {
				return runStatus(&flags)
			}
			return cmd.Help()
		},
	}
	root.SetVersionTemplate("scrapfly {{.Version}}\n")

	root.PersistentFlags().StringVar(&flags.apiKey, "api-key", "", "Scrapfly API key (overrides SCRAPFLY_API_KEY)")
	root.PersistentFlags().StringVar(&flags.host, "host", "", "API host (overrides SCRAPFLY_API_HOST, default https://api.scrapfly.io)")
	root.PersistentFlags().StringVar(&flags.browserHost, "browser-host", "", "Browser host (overrides SCRAPFLY_BROWSER_HOST, default https://browser.scrapfly.io)")
	root.PersistentFlags().BoolVar(&flags.insecure, "insecure", false, "skip TLS verification (dev stacks only)")
	root.PersistentFlags().DurationVar(&flags.timeout, "timeout", 150*time.Second, "per-request timeout")
	root.PersistentFlags().BoolVar(&flags.pretty, "pretty", false, "human-readable output instead of JSON")
	root.PersistentFlags().StringVarP(&flags.outputPath, "output", "o", "", "write primary payload to this file path (mutually exclusive with --output-dir)")
	root.PersistentFlags().StringVarP(&flags.outputDir, "output-dir", "O", "", "write primary payload into this directory with an auto-generated filename")
	root.PersistentFlags().BoolVar(&showStatus, "status", false, "print CLI + auth + usage status and exit (runs before subcommand)")

	root.AddCommand(newScrapeCmd(&flags))
	root.AddCommand(newScreenshotCmd(&flags))
	root.AddCommand(newExtractCmd(&flags))
	root.AddCommand(newCrawlCmd(&flags))
	root.AddCommand(newAccountCmd(&flags))
	root.AddCommand(newStatusCmd(&flags))
	root.AddCommand(newConfigCmd(&flags))
	root.AddCommand(newBrowserCmd(&flags))
	root.AddCommand(newAgentCmd(&flags))
	root.AddCommand(newSelectorCmd(&flags))
	root.AddCommand(newMcpCmd(&flags))
	root.AddCommand(newUpdateCmd(&flags))
	root.AddCommand(newVersionCmd())

	root.SetArgs(rewriteURLShortcut(args))
	if err := root.Execute(); err != nil {
		_ = out.WriteError(os.Stderr, flags.pretty, "", err)
		return err
	}
	return nil
}
