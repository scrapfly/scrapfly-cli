package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"
)

// dashboardAPIKeyURL is the dashboard page where users can copy or regenerate
// their project API key. Kept in sync with the `dashboard_overview_project`
// route in apps/scrapfly/web-app/src/HttpWorker/WebWorker.php.
const dashboardAPIKeyURL = "https://scrapfly.io/dashboard/project"

func newAuthCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Interactive authentication (login / logout / whoami)",
		Long: `Manage CLI authentication.

  auth login    Open the dashboard in the browser, paste the API key back, and
                persist it to ~/.scrapfly/config.json after a live validation
                against the Scrapfly API.

  auth logout   Remove the stored API key (host setting is preserved).

  auth whoami   Verify the current credentials and print the account envelope.`,
	}
	cmd.AddCommand(newAuthLoginCmd(flags))
	cmd.AddCommand(newAuthLogoutCmd(flags))
	cmd.AddCommand(newAuthWhoamiCmd(flags))
	return cmd
}

func newAuthLoginCmd(flags *rootFlags) *cobra.Command {
	var (
		noBrowser bool
		keyFlag   string
	)
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Interactive API key setup (opens browser by default)",
		Long: `Launch an interactive login that:
  1. Opens the Scrapfly dashboard API-key page in your default browser.
  2. Waits for you to paste the key back into the terminal.
  3. Validates the key against the API and saves it to ~/.scrapfly/config.json.

Headless environments (no TTY, SSH without forwarding, containers, CI):
  The command detects the missing terminal, prints the dashboard URL, and
  reads the key from stdin. You can also pipe the key in:

    echo "$SCRAPFLY_API_KEY" | scrapfly auth login

  or pass it directly:

    scrapfly auth login --key scp-live-xxxxxxxx

Flags:
  --no-browser  Skip the browser-open attempt and just print the URL.
  --key <KEY>   Skip the interactive prompt entirely (useful for scripts).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAuthLogin(flags, keyFlag, noBrowser)
		},
	}
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "don't attempt to launch a browser, just print the URL")
	cmd.Flags().StringVar(&keyFlag, "key", "", "skip the prompt and use this API key directly")
	return cmd
}

func newAuthLogoutCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove the stored API key from ~/.scrapfly/config.json",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if cfg.APIKey == "" {
				return out.WriteSuccess(os.Stdout, flags.pretty, "auth.logout", map[string]any{"status": "already_logged_out"})
			}
			cfg.APIKey = ""
			p, err := saveConfig(cfg)
			if err != nil {
				return err
			}
			if flags.pretty {
				out.Pretty(os.Stdout, "logged out (api_key cleared in %s)", p)
				return nil
			}
			return out.WriteSuccess(os.Stdout, false, "auth.logout", map[string]any{"path": p, "status": "logged_out"})
		},
	}
}

func newAuthWhoamiCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Verify the stored credentials and print account info",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(flags)
		},
	}
}

func runAuthLogin(flags *rootFlags, preset string, noBrowser bool) error {
	url := dashboardURLForHost(resolveHost(flags))

	// Fast path: --key flag short-circuits the prompt. Still validate.
	if preset != "" {
		return finalizeLogin(flags, strings.TrimSpace(preset))
	}

	interactive := isInteractive()

	// Try to open the browser in interactive mode. A failure is non-fatal:
	// we always fall back to printing the URL so the user can open it
	// manually (headless servers, SSH without X forwarding, etc).
	browserOpened := false
	if interactive && !noBrowser {
		if err := openBrowser(url); err == nil {
			browserOpened = true
		}
	}

	// Banner goes to stderr so it never contaminates a piped JSON envelope
	// on stdout (we still write the final success envelope to stdout).
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  Scrapfly CLI — Login")
	fmt.Fprintln(os.Stderr, "  ────────────────────")
	if browserOpened {
		fmt.Fprintln(os.Stderr, "  Opened your browser to:")
	} else if !interactive {
		fmt.Fprintln(os.Stderr, "  No interactive terminal detected. Open this URL in a browser:")
	} else {
		fmt.Fprintln(os.Stderr, "  Open this URL in a browser (no browser launched):")
	}
	fmt.Fprintln(os.Stderr, "    "+url)
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  Copy your project API key (format: scp-live-…) and paste it below.")
	fmt.Fprintln(os.Stderr, "")

	key, err := readAPIKey(interactive)
	if err != nil {
		return err
	}
	return finalizeLogin(flags, key)
}

// finalizeLogin validates the key against the /account endpoint (using the
// currently resolved host) and persists it. Using Account() instead of a
// dedicated Verify call doubles as both a validation check and a retrieval of
// useful context (account id, project, plan, quota) that we echo back to the
// user so they can confirm they logged into the right account before anything
// is written to disk.
func finalizeLogin(flags *rootFlags, key string) error {
	if key == "" {
		return errors.New("no API key provided")
	}
	// Build a client with the freshly supplied key, bypassing the normal
	// resolution chain so we validate *this* key, not whatever is already
	// stored.
	probe := *flags
	probe.apiKey = key
	client, err := buildClient(&probe)
	if err != nil {
		return err
	}
	account, err := client.Account()
	if err != nil {
		return fmt.Errorf("validate api key via /account: %w", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	cfg.APIKey = key
	path, err := saveConfig(cfg)
	if err != nil {
		return err
	}

	usage := account.Subscription.Usage.Scrape
	payload := map[string]any{
		"path":    path,
		"api_key": redact(key),
		"status":  "logged_in",
		"account": map[string]any{
			"account_id": account.Account.AccountID,
			"project":    account.Project.Name,
			"plan":       account.Subscription.PlanName,
			"scrape": map[string]any{
				"used":      usage.Current,
				"limit":     usage.Limit,
				"remaining": usage.Remaining,
			},
			"concurrency": map[string]any{
				"usage": usage.ConcurrentUsage,
				"limit": usage.ConcurrentLimit,
			},
		},
	}
	if flags.pretty {
		out.Pretty(os.Stdout,
			"logged in as account=%s project=%q plan=%s scrape=%d/%d (remaining %d) — api_key=%s saved to %s",
			account.Account.AccountID, account.Project.Name, account.Subscription.PlanName,
			usage.Current, usage.Limit, usage.Remaining,
			redact(key), path)
		return nil
	}
	return out.WriteSuccess(os.Stdout, false, "auth.login", payload)
}

// readAPIKey reads one line from stdin. In interactive mode we show a prompt
// on stderr; in non-interactive mode (pipe, CI) we just consume stdin silently
// so scripts like `echo "$KEY" | scrapfly auth login` work.
func readAPIKey(interactive bool) (string, error) {
	if interactive {
		fmt.Fprint(os.Stderr, "  API key › ")
	}
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read api key from stdin: %w", err)
	}
	return strings.TrimSpace(line), nil
}

// isInteractive returns true when stdin is attached to a terminal (as opposed
// to a pipe or redirected file). Uses the stdlib `os.ModeCharDevice` trick
// rather than pulling in x/term so we stay dependency-free.
func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// openBrowser launches the OS default browser pointing at url. Returns the
// underlying exec error so callers can decide whether to fall back to the
// print-URL path.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	default:
		// Linux/BSDs: xdg-open is the de-facto standard. If it's missing
		// (minimal containers, headless servers) we return the error and
		// the caller falls back to printing the URL.
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

// dashboardURLForHost derives the dashboard URL from the currently configured
// API host. For the default api.scrapfly.io we point at scrapfly.io; for any
// custom/dev host we swap the "api." prefix for "scrapfly.io" equivalents and
// keep unknown hosts on their base domain so self-hosted stacks still work.
func dashboardURLForHost(apiHost string) string {
	apiHost = strings.TrimSuffix(apiHost, "/")
	if apiHost == "" || apiHost == "https://api.scrapfly.io" {
		return dashboardAPIKeyURL
	}
	// Map api.<x> → <x> for the common {prod, dev, home} pairings.
	if strings.Contains(apiHost, "://api.") {
		base := strings.Replace(apiHost, "://api.", "://", 1)
		return base + "/dashboard/project"
	}
	// Fall back to appending the dashboard path directly.
	return apiHost + "/dashboard/project"
}
