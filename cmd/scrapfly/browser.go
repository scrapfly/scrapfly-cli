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
// caller opted in via --solve-captcha.
//
// Only the /unblock path needs this: CloudBrowserConfig carries SolveCaptcha
// (set in toConfig), but UnblockConfig has no such field, so the flag can only
// reach an unblock-minted session by riding the returned WSS URL. The API
// honors the query param and arms Antibot.captchaEnable on connect, so the
// solver still fires on the first page attach. Drop this once the SDK's
// UnblockConfig grows the field.
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
  salt        Print the project salt prefixed to your VNC password

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
				// --session is persistent on the command tree, not bound by
				// bindBrowserLaunchFlags, so every call site has to copy it in
				// itself (start, execute and agent already do). Without this
				// the session= pin never reaches the URL and Scrapfly
				// allocates a fresh browser instead of resuming.
				if sessionIDFlag != "" {
					launchCfg.session = sessionIDFlag
				}
				wsURL := client.CloudBrowser(launchCfg.toConfig())
				data := map[string]string{
					"ws_url":  wsURL,
					"session": launchCfg.session,
				}
				// The salted password is what an operator types; surface it
				// here so nobody has to derive it from the api key by hand.
				if launchCfg.enableVNC && launchCfg.vncPassword != "" {
					salt := client.CloudBrowserProjectSalt()
					vncURL, vncPassword := vncConnectInfo(wsURL, salt, launchCfg.vncPassword, "")
					data["project_salt"] = salt
					data["vnc_url"] = vncURL
					data["vnc_password"] = vncPassword
				}
				if flags.pretty {
					fmt.Fprintln(os.Stdout, wsURL)
					// stdout stays a bare pipeable URL in pretty mode.
					if vncURL, ok := data["vnc_url"]; ok {
						fmt.Fprintf(os.Stderr, "vnc %s\nvnc password %s\n", vncURL, data["vnc_password"])
					}
					return nil
				}
				return out.WriteSuccess(os.Stdout, false, "browser.ws", data)
			}
			// URL + no --unblock — guard the user.
			if !unblock {
				return fmt.Errorf("positional URL requires --unblock (Scrapfly /unblock endpoint). Omit the URL to just mint a CDP URL")
			}
			if err := errSessionShapingFlagsIgnored(&launchCfg, "--unblock"); err != nil {
				return err
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
	cmd.AddCommand(newBrowserSaltCmd(flags))

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
	lang         string
	languages    []string
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
	vault        string
	vaultKey     string
	enableVNC    bool
	vncPassword  string
	enableRTC    bool
	rtcUsername  string
	rtcPassword  string
	hitlNetworks []string
}

func bindBrowserLaunchFlags(cmd *cobra.Command, f *browserLaunchFlags) {
	cmd.Flags().StringVar(&f.proxyPool, "proxy-pool", "", "public_datacenter_pool|public_residential_pool")
	cmd.Flags().StringVar(&f.osSpoof, "os", "", "OS spoof")
	cmd.Flags().StringVar(&f.country, "country", "", "proxy country (ISO 3166-1 alpha-2)")
	cmd.Flags().StringVar(&f.lang, "lang", "", "browser UI language base tag, e.g. en (navigator.language; derived from country when unset)")
	cmd.Flags().StringSliceVar(&f.languages, "language", nil, "ordered Accept-Language preference, e.g. fr-FR (repeatable, max 3; derived from country when unset)")
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
	cmd.Flags().StringVar(&f.vault, "vault", "", "credential vault NAME; items are decrypted and pushed over CDP before the session yields")
	cmd.Flags().StringVar(&f.vaultKey, "vault-key", "", "base64 vault key you saved at vault creation. Scrapfly never stores it — without it the session fails with ERR::BROWSER::VAULT_KEY_INVALID")
	// VNC/RTC credentials are customer-owned: the server has no default and
	// rejects the session unless a password is supplied. The password sent here
	// is always the RAW one; which form a VIEWER types depends on the endpoint
	// (see newBrowserSaltCmd).
	cmd.Flags().BoolVar(&f.enableVNC, "enable-vnc", false, "expose the session over VNC for human takeover (requires --vnc-password)")
	cmd.Flags().StringVar(&f.vncPassword, "vnc-password", "", "VNC password (raw; the salted form a native client types is printed on connect)")
	cmd.Flags().BoolVar(&f.enableRTC, "enable-rtc", false, "expose the session over WebRTC live video (requires --rtc-password)")
	cmd.Flags().StringVar(&f.rtcUsername, "rtc-username", "", "WebRTC signaling username (server default: scrapfly)")
	cmd.Flags().StringVar(&f.rtcPassword, "rtc-password", "", "WebRTC signaling password")
	cmd.Flags().StringSliceVar(&f.hitlNetworks, "hitl-allowed-network", nil, "IP or CIDR trusted to attach to VNC/WebRTC without credentials (repeatable). Replaces the password requirement rather than adding to it")
}

func (f *browserLaunchFlags) toConfig() *scrapfly.CloudBrowserConfig {
	return &scrapfly.CloudBrowserConfig{
		ProxyPool:    f.proxyPool,
		OS:           f.osSpoof,
		Country:      f.country,
		Lang:         f.lang,
		Languages:    f.languages,
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
		SolveCaptcha: f.solveCaptcha,
		Vault:        f.vault,
		VaultKey:     f.vaultKey,
		EnableVNC:    f.enableVNC,
		VNCPassword:  f.vncPassword,
		EnableRTC:    f.enableRTC,
		RTCUsername:  f.rtcUsername,
		RTCPassword:  f.rtcPassword,

		HITLAllowedNetworks: f.hitlNetworks,
	}
}

// vncConnectInfo renders what a native VNC client needs to attach on the TCP
// endpoint (port 5901). Scrapfly prefixes the project salt server-side at
// allocation, so that endpoint requires "<salt>-<raw>". The WebSocket endpoint
// /run/<run_id>/vnc authenticates against the raw --vnc-password instead.
//
// The run id is only known once a session exists, so callers that have not
// connected yet pass "" and get a {run_id} placeholder.
//
// Duplicates go-scrapfly's CloudBrowserVNCPassword because go.mod pins a
// published SDK version that predates it. Drop this once the SDK ships.
// Callers must gate on enableVNC && vncPassword != "" (the server's own
// salting condition) so this never renders a credential for a session that
// has none.
func vncConnectInfo(wsURL, salt, rawPassword, runID string) (connectURL, password string) {
	// Hostname(), not Host: the CDP URL may carry a port (dev, self-hosted) and
	// appending :5901 to "host:8443" would produce a double-port authority.
	host := "browser.scrapfly.io"
	if u, err := url.Parse(wsURL); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	if runID == "" {
		runID = "{run_id}"
	}
	return fmt.Sprintf("vnc://%s@%s:5901", runID, host), salt + "-" + rawPassword
}

// wsURLSecretParams are the CDP URL query params that carry a credential.
// api_key was always there; the vault key and the HITL passwords joined it when
// those flags landed.
var wsURLSecretParams = []string{"api_key", "key", "vault_key", "vnc_password", "rtc_password"}

// redactWSURL masks credential params for human-facing output, so they do not
// land in terminal scrollback or CI logs.
func redactWSURL(wsURL string) string {
	u, err := url.Parse(wsURL)
	if err != nil {
		return "<unparseable ws url>"
	}
	q := u.Query()
	for _, p := range wsURLSecretParams {
		if q.Get(p) != "" {
			q.Set(p, "REDACTED")
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// errSessionShapingFlagsIgnored rejects launch flags that shape the session at
// ALLOCATION time when the session is not being allocated from those flags.
//
// /unblock takes its own config object (no VNC/RTC/vault fields in the SDK's
// UnblockConfig) and --ws attaches to a URL minted elsewhere, so in both cases
// the server has already allocated the browser by the time these flags exist.
// They cannot be applied retroactively: x11vnc is started at allocation, so
// re-sending enable_vnc on the CDP connect changes nothing. Failing here beats
// dropping them silently and leaving the caller waiting on a VNC endpoint that
// was never opened.
func errSessionShapingFlagsIgnored(f *browserLaunchFlags, how string) error {
	var ignored []string
	if f.enableVNC {
		ignored = append(ignored, "--enable-vnc")
	}
	if f.enableRTC {
		ignored = append(ignored, "--enable-rtc")
	}
	if f.vault != "" {
		ignored = append(ignored, "--vault")
	}
	if len(ignored) == 0 {
		return nil
	}
	return fmt.Errorf("%s cannot carry %s: the session is already allocated by then. Omit %s, or drop %s so the session is minted from the flags",
		how, strings.Join(ignored, " / "), how, strings.Join(ignored, " / "))
}

// projectSaltFor resolves the salt without a round-trip — it is sha256 of the
// api key.
//
// The key embedded in wsURL wins: that is the one the session was actually
// minted with, and on the --ws path it can differ from the locally configured
// key, which would silently yield a password no VNC client can use. Falls back
// to the configured key (and returns "" if there is none) for callers that
// have no URL yet.
func projectSaltFor(flags *rootFlags, wsURL string) string {
	if u, err := url.Parse(wsURL); err == nil {
		if key := u.Query().Get("api_key"); key != "" {
			return scrapfly.ProjectSalt(key)
		}
	}
	client, err := buildClient(flags)
	if err != nil {
		return ""
	}
	return client.CloudBrowserProjectSalt()
}

func newBrowserSaltCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "salt",
		Short: "Print the project salt prefixed to your VNC password",
		Long: `The project salt is sha256(api_key)[:8] — deterministic and computed locally,
identical to the X-Browser-Project-Salt response header.

Scrapfly prefixes it to your vnc_password when the session is allocated. Which
form a viewer types depends on the endpoint it attaches to:

  native client on the TCP mux (vnc://<run_id>@host:5901)  "<salt>-<password>"
  WebSocket endpoint (wss://host/run/<run_id>/vnc)         the raw password

The mux keys its DES challenge on the salted form so two customers who pick the
same password never collide on the wire; the WebSocket handler strips the salt
server-side before the same check. "browser start" prints the TCP URL and the
salted password that goes with it.`,
		Example: `  scrapfly browser salt --pretty`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			salt := client.CloudBrowserProjectSalt()
			if flags.pretty {
				fmt.Fprintln(os.Stdout, salt)
				return nil
			}
			return out.WriteSuccess(os.Stdout, false, "browser.salt", map[string]string{
				"project_salt": salt,
			})
		},
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
