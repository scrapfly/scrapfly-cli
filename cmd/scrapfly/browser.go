package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	scrapfly "github.com/scrapfly/go-scrapfly"
	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/scrapfly/scrapfly-cli/internal/sessiond"
	"github.com/spf13/cobra"
)

// appendSolveCaptchaParam adds ?solve_captcha=true to a CDP WSS URL when the
// caller opted in via --solve-captcha. This is a temporary shim: the Go SDK
// version pinned in go.mod (v0.3.3) does not yet expose a SolveCaptcha field
// on CloudBrowserConfig, but the Scrapfly API already honors the query param
// and arms Antibot.captchaEnable on session start. Once the SDK ships the
// field, delete this helper and set cfg.SolveCaptcha = f.solveCaptcha in
// toConfig() instead.
func appendSolveCaptchaParam(wsURL string, solveCaptcha bool) string {
	if !solveCaptcha {
		return wsURL
	}
	u, err := url.Parse(wsURL)
	if err != nil {
		// Fall back to raw concat on parse failure — the Scrapfly URL always
		// has a query string, so appending &solve_captcha=true is safe even
		// when url.Parse misbehaves on an edge-case scheme.
		if strings.Contains(wsURL, "?") {
			return wsURL + "&solve_captcha=true"
		}
		return wsURL + "?solve_captcha=true"
	}
	q := u.Query()
	q.Set("solve_captcha", "true")
	u.RawQuery = q.Encode()
	return u.String()
}

func newBrowserCmd(flags *rootFlags) *cobra.Command {
	var (
		unblock    bool
		navigateTO int
		launchCfg  browserLaunchFlags
	)
	cmd := &cobra.Command{
		Use:   "browser [url]",
		Short: "Browser: mint a CDP URL, unblock a target, or manage sessions",
		Long: `Control Scrapfly Browser — a remote Chromium you connect to over CDP (WebSocket).

Usage:
  scrapfly browser                   Print a CDP WSS URL (session lazy-starts on connect)
  scrapfly browser <url> --unblock   POST /unblock → ws_url + session_id + run_id
  scrapfly browser <sub> ...         Subcommands for session/extension management

Subcommands:
  execute     Drive a session with a small action vocabulary
  list        List active sessions
  close       Stop a session
  playback    Fetch debug playback metadata
  video       Download session recording (webm; requires -o/-O)
  extensions  Manage browser extensions (list|get|upload|delete)

The WSS URL includes your API key as a query param — treat as a secret.`,
		Example: `  # Mint a CDP URL (no target pre-navigated)
  scrapfly browser --resolution 1920x1080 --pretty

  # Unblock a URL and get an attach-ready session
  scrapfly browser https://example.com --unblock --country us

  # Re-use a prior session (requires SDK support for "session" param)
  scrapfly browser https://example.com --unblock --session sess_abc123`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			// No URL → print a CDP WSS URL (ws-mode).
			if len(args) == 0 {
				wsURL := appendSolveCaptchaParam(client.CloudBrowser(launchCfg.toConfig()), launchCfg.solveCaptcha)
				if flags.pretty {
					fmt.Fprintln(os.Stdout, wsURL)
					return nil
				}
				return out.WriteSuccess(os.Stdout, false, "browser.ws", map[string]string{
					"ws_url":  wsURL,
					"session": launchCfg.session,
				})
			}
			// URL + no --unblock — guard the user.
			if !unblock {
				return fmt.Errorf("positional URL requires --unblock (Scrapfly /unblock endpoint). Omit the URL to just mint a CDP URL")
			}
			res, err := client.CloudBrowserUnblock(scrapfly.UnblockConfig{
				URL:            args[0],
				Country:        launchCfg.country,
				Timeout:        navigateTO,
				BrowserTimeout: launchCfg.timeout,
				// NOTE: SDK v0.2.0's UnblockConfig does not expose the `session`
				// field documented in the /unblock API. Track SDK update and
				// wire --session through once available.
			})
			if err != nil {
				return err
			}
			// --solve-captcha is applied to the returned CDP URL rather than
			// the /unblock payload itself (SDK v0.3.3's UnblockConfig has no
			// solve_captcha field yet). The Cloud Browser API arms
			// Antibot.captchaEnable when the client connects with the param,
			// so the effect is the same — the solver fires on the first page
			// attach of the post-unblock CDP session.
			res.WSURL = appendSolveCaptchaParam(res.WSURL, launchCfg.solveCaptcha)
			if flags.pretty {
				out.Pretty(os.Stdout, "session=%s run=%s ws=%s", res.SessionID, res.RunID, res.WSURL)
				return nil
			}
			return out.WriteSuccess(os.Stdout, false, "browser.unblock", res)
		},
	}

	// Unblock-specific flags.
	cmd.Flags().BoolVar(&unblock, "unblock", false, "call POST /unblock for the positional URL (required when a URL is given)")
	cmd.Flags().IntVar(&navigateTO, "navigate-timeout", 0, "unblock: navigation timeout seconds (max 300, default 60)")
	// Shared browser config (same surface as CDP URL builder + /unblock request).
	// --session-timeout from launch flags maps to unblock's browser_timeout.
	bindBrowserLaunchFlags(cmd, &launchCfg)

	// --session is a PERSISTENT flag so it applies to all subcommands
	// (start/stop/status + action verbs). Takes precedence over launchCfg.session.
	cmd.PersistentFlags().StringVar(&sessionIDFlag, "session", "", "session id (daemon socket at ~/.scrapfly/sessions/<id>.sock)")

	cmd.AddCommand(newBrowserExecuteCmd(flags))
	cmd.AddCommand(newBrowserListCmd(flags))
	cmd.AddCommand(newBrowserCloseCmd(flags))
	cmd.AddCommand(newBrowserPlaybackCmd(flags))
	cmd.AddCommand(newBrowserVideoCmd(flags))
	cmd.AddCommand(newBrowserExtensionsCmd(flags))

	// Session daemon + action subcommands.
	cmd.AddCommand(newBrowserStartCmd(flags))
	cmd.AddCommand(newBrowserStopCmd(flags))
	cmd.AddCommand(newBrowserStatusCmd(flags))
	cmd.AddCommand(newBrowserNavigateCmd(flags))
	cmd.AddCommand(newBrowserSnapshotCmd(flags))
	cmd.AddCommand(newBrowserClickCmd(flags))
	cmd.AddCommand(newBrowserFillCmd(flags))
	cmd.AddCommand(newBrowserClickAICmd(flags))
	cmd.AddCommand(newBrowserFillAICmd(flags))
	cmd.AddCommand(newBrowserWaitCmd(flags))
	cmd.AddCommand(newBrowserContentCmd(flags))
	cmd.AddCommand(newBrowserSlideCmd(flags))
	cmd.AddCommand(newBrowserScrollCmd(flags))
	cmd.AddCommand(newBrowserScreenshotSessionCmd(flags))
	cmd.AddCommand(newBrowserEvalCmd(flags))
	return cmd
}

type browserLaunchFlags struct {
	proxyPool    string
	osSpoof      string
	country      string
	session      string
	timeout      int
	blockImages  bool
	blockStyles  bool
	blockFonts   bool
	blockMedia   bool
	screenshot   bool
	cache        bool
	blacklist    bool
	debug        bool
	solveCaptcha bool
	resolution   string
	extensions   []string
	browserBrand string
	byopProxy    string
}

func bindBrowserLaunchFlags(cmd *cobra.Command, f *browserLaunchFlags) {
	cmd.Flags().StringVar(&f.proxyPool, "proxy-pool", "", "public_datacenter_pool|public_residential_pool")
	cmd.Flags().StringVar(&f.osSpoof, "os", "", "OS spoof")
	cmd.Flags().StringVar(&f.country, "country", "", "proxy country (ISO 3166-1 alpha-2)")
	// NOTE: --session is NOT bound here because the browser command tree
	// exposes it as a persistent flag (sessionIDFlag) shared across start /
	// stop / status / action subcommands. Callers outside the browser tree
	// (e.g. agent) bind their own --session flag and copy it into f.session.
	cmd.Flags().IntVar(&f.timeout, "session-timeout", 0, "session timeout seconds — max 1800 (Unblock) / default 900; controls how long Scrapfly keeps the browser alive. Keep high when using `browser start` across many calls.")
	cmd.Flags().BoolVar(&f.blockImages, "block-images", false, "block image resources")
	cmd.Flags().BoolVar(&f.blockStyles, "block-styles", false, "block CSS")
	cmd.Flags().BoolVar(&f.blockFonts, "block-fonts", false, "block font resources")
	cmd.Flags().BoolVar(&f.blockMedia, "block-media", false, "block audio/video")
	cmd.Flags().BoolVar(&f.screenshot, "screenshot", false, "enable screenshot capability")
	cmd.Flags().BoolVar(&f.cache, "cache", false, "enable cache")
	cmd.Flags().BoolVar(&f.blacklist, "blacklist", false, "enable blacklist enforcement")
	cmd.Flags().BoolVar(&f.debug, "debug", false, "enable debug recording (playback/video)")
	cmd.Flags().BoolVar(&f.solveCaptcha, "solve-captcha", false, "arm Scrapium's built-in captcha solver (Turnstile, DataDome, reCAPTCHA, geetest). Billed per solve. See https://scrapfly.io/docs/cloud-browser-api/captcha-solver")
	cmd.Flags().StringVar(&f.resolution, "resolution", "", "viewport e.g. 1920x1080")
	cmd.Flags().StringSliceVar(&f.extensions, "extension", nil, "extension id to attach (repeatable)")
	cmd.Flags().StringVar(&f.browserBrand, "browser-brand", "", "chrome|edge|brave|opera")
	cmd.Flags().StringVar(&f.byopProxy, "byop-proxy", "", "bring-your-own proxy URL (Custom plan)")
}

func (f *browserLaunchFlags) toConfig() *scrapfly.CloudBrowserConfig {
	return &scrapfly.CloudBrowserConfig{
		ProxyPool:    f.proxyPool,
		OS:           f.osSpoof,
		Country:      f.country,
		Session:      f.session,
		Timeout:      f.timeout,
		BlockImages:  f.blockImages,
		BlockStyles:  f.blockStyles,
		BlockFonts:   f.blockFonts,
		BlockMedia:   f.blockMedia,
		Screenshot:   f.screenshot,
		Cache:        f.cache,
		Blacklist:    f.blacklist,
		Debug:        f.debug,
		Resolution:   f.resolution,
		Extensions:   f.extensions,
		BrowserBrand: f.browserBrand,
		BYOPProxy:    f.byopProxy,
	}
}

func newBrowserListCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List active Browser sessions",
		Example: `  scrapfly browser list --pretty`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			res, err := client.CloudBrowserSessions()
			if err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "browser.list", res)
		},
	}
}

func newBrowserCloseCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "close [session-id]",
		Short: "Full cleanup: shut down the daemon AND release Scrapfly's remote browser",
		Long: `Use this at the end of a scenario to release all resources immediately:
  1. Send "shutdown" to the local session daemon (if running).
  2. Call POST /session/{id}/stop so Scrapfly can return the browser to the
     pool without waiting for the session timeout.
  3. Clear the active-session marker.

If <session-id> is omitted, resolves via --session / SCRAPFLY_SESSION /
~/.scrapfly/sessions/.current.`,
		Example: `  scrapfly browser close          # uses the active session
  scrapfly browser close sess_abc`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Resolve session id.
			sid := ""
			if len(args) == 1 {
				sid = args[0]
			} else {
				resolved, ok := sessiond.Resolve(sessionIDFlag)
				if !ok {
					return fmt.Errorf("no session id (pass it as arg, or use --session / SCRAPFLY_SESSION)")
				}
				sid = resolved
			}

			// Record whether the CLI ever knew about this session locally —
			// used to turn a remote 404 into "no such session" vs.
			// "already stopped".
			sockPath, _, _ := sessiond.PathsFor(sid)
			_, sockErr := os.Stat(sockPath)
			_, metaErr := sessiond.LoadMeta(sid)
			knownLocally := sockErr == nil || metaErr == nil

			// 1. Shut down the daemon if alive.
			daemonStatus := "not running"
			if sockErr == nil {
				// A mid-response conn close is expected because the daemon
				// exits immediately after writing the response; treat any
				// completed send attempt as "shutdown sent".
				_, _ = sessiond.Send(sid, sessiond.Request{Action: "shutdown"})
				daemonStatus = "shutdown sent"
			}

			// 2. Release the remote browser. 404 means "already gone" —
			// success if we previously knew this session, otherwise the id
			// was just wrong.
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			remoteStatus := "stopped"
			if err := client.CloudBrowserSessionStop(sid); err != nil {
				switch {
				case strings.Contains(err.Error(), "status 404") && knownLocally:
					remoteStatus = "already stopped"
				case strings.Contains(err.Error(), "status 404"):
					remoteStatus = "no such session"
				default:
					remoteStatus = "error: " + err.Error()
				}
			}

			// 3. Clear the active-session marker regardless.
			_ = sessiond.ClearCurrent(sid)

			return out.WriteSuccess(os.Stdout, flags.pretty, "browser.close", map[string]any{
				"session_id":  sid,
				"daemon":      daemonStatus,
				"remote_stop": remoteStatus,
			})
		},
	}
	return cmd
}

func newBrowserPlaybackCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "playback <run-id>",
		Short: "Fetch playback metadata for a debug run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			res, err := client.CloudBrowserPlayback(args[0])
			if err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "browser.playback", res)
		},
	}
}

func newBrowserVideoCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "video <run-id>",
		Short: "Download the session recording (webm); requires -o or -O",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			data, err := client.CloudBrowserVideo(args[0])
			if err != nil {
				return err
			}
			dst, err := resolveOutputPath(flags, args[0], "webm")
			if err != nil {
				return err
			}
			if dst == "" {
				return fmt.Errorf("-o/--output or -O/--output-dir is required (binary payload)")
			}
			if err := os.WriteFile(dst, data, 0o644); err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "browser.video", map[string]any{
				"run_id": args[0], "path": dst, "bytes": len(data),
			})
		},
	}
}

func newBrowserExtensionsCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "extensions",
		Short: "Manage Browser extensions (list|get|upload|delete)",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List all extensions on the account",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			res, err := client.CloudBrowserExtensionList()
			if err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "browser.extensions.list", res)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "get <extension-id>",
		Short: "Show details for an extension",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			res, err := client.CloudBrowserExtensionGet(args[0])
			if err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "browser.extensions.get", res)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:     "upload <path>",
		Short:   "Upload a .zip or .crx extension",
		Example: `  scrapfly browser extensions upload ./ublock.zip`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			res, err := client.CloudBrowserExtensionUpload(args[0])
			if err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "browser.extensions.upload", res)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "delete <extension-id>",
		Short: "Delete an extension",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			res, err := client.CloudBrowserExtensionDelete(args[0])
			if err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "browser.extensions.delete", res)
		},
	})
	return cmd
}
