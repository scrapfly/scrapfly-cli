package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	scrapfly "github.com/scrapfly/go-scrapfly"
	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/scrapfly/scrapfly-cli/internal/sessiond"
	"github.com/spf13/cobra"
)

// session flag value shared across action subcommands. Bound via a persistent
// flag on the browser command so callers write `scrapfly browser --session abc click e5`.
var sessionIDFlag string

func newBrowserStartCmd(flags *rootFlags) *cobra.Command {
	var (
		targetURL string
		wsURL     string
		unblock   bool
		launchCfg browserLaunchFlags
	)
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start a persistent browser session daemon (foreground)",
		Long: `Holds a Scrapfly Browser CDP connection open and serves actions over a local
Unix socket at ~/.scrapfly/sessions/<id>.sock. Other "scrapfly browser <action>"
invocations talk to this daemon so that tabs, cookies, and AXTree refs
persist across CLI calls.

Runs in the foreground; background it with & / systemd / tmux as you prefer.`,
		Example: `  scrapfly browser --session demo start --url https://web-scraping.dev/login --unblock &`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if sessionIDFlag == "" {
				return fmt.Errorf("--session <id> is required")
			}
			// Record as the active session so subsequent CLI calls can
			// omit --session. Cleared by stop (or on process exit below).
			if err := sessiond.SetCurrent(sessionIDFlag); err != nil {
				fmt.Fprintf(os.Stderr, "[session %s] warning: could not record active session: %v\n", sessionIDFlag, err)
			}
			defer sessiond.ClearCurrent(sessionIDFlag)
			// Resolve ws URL.
			if wsURL == "" {
				client, err := buildClient(flags)
				if err != nil {
					return err
				}
				switch {
				case targetURL != "" && unblock:
					res, err := client.CloudBrowserUnblock(scrapfly.UnblockConfig{URL: targetURL, Country: launchCfg.country})
					if err != nil {
						return err
					}
					wsURL = res.WSURL
				case targetURL != "" && !unblock:
					return fmt.Errorf("--url requires --unblock")
				default:
					// Copy sessionIDFlag into the launch config so Scrapfly
					// pins the browser session identity.
					launchCfg.session = sessionIDFlag
					wsURL = client.CloudBrowser(launchCfg.toConfig())
				}
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stop := make(chan os.Signal, 1)
			signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-stop
				cancel()
			}()

			fmt.Fprintf(os.Stderr, "[session %s] connecting %s\n", sessionIDFlag, wsURL)
			return sessiond.Serve(ctx, sessionIDFlag, wsURL, func(sock string) {
				fmt.Fprintf(os.Stderr, "[session %s] ready; socket=%s\n", sessionIDFlag, sock)
			})
		},
	}
	cmd.Flags().StringVar(&targetURL, "url", "", "pre-navigate via /unblock (requires --unblock)")
	cmd.Flags().StringVar(&wsURL, "ws", "", "attach to a pre-minted WS URL instead of building one")
	cmd.Flags().BoolVar(&unblock, "unblock", false, "use /unblock for --url (anti-bot bypass)")
	bindBrowserLaunchFlags(cmd, &launchCfg)
	return cmd
}

func newBrowserStopCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the running session daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			sid, ok := sessiond.Resolve(sessionIDFlag)
			if !ok {
				return fmt.Errorf("no active session (use --session <id>, SCRAPFLY_SESSION, or run `browser start` first)")
			}
			_, err := sessiond.Send(sid, sessiond.Request{Action: "shutdown"})
			// Clear marker even on error so a stale .current doesn't hide a
			// later start.
			_ = sessiond.ClearCurrent(sid)
			if err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "browser.stop", map[string]string{
				"session": sid, "status": "shutting down",
			})
		},
	}
}

func newBrowserStatusCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show running-session info (pid, ws_url, started_at)",
		RunE: func(cmd *cobra.Command, args []string) error {
			sid, ok := sessiond.Resolve(sessionIDFlag)
			if !ok {
				return fmt.Errorf("no active session (use --session <id>, SCRAPFLY_SESSION, or run `browser start` first)")
			}
			meta, err := sessiond.LoadMeta(sid)
			if err != nil {
				return fmt.Errorf("no metadata for session %q (never started?): %w", sid, err)
			}
			alive := true
			if _, err := sessiond.Send(sid, sessiond.Request{Action: "ping"}); err != nil {
				alive = false
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "browser.status", map[string]any{
				"session_id": meta.SessionID,
				"ws_url":     meta.WSURL,
				"pid":        meta.PID,
				"started_at": meta.StartedAt,
				"alive":      alive,
			})
		},
	}
}

// isRefLocatorStr mirrors cdp.isRefLocator (not exported). True when the
// locator looks like "e<digits>" — used to keep ref-path semantics for
// ai-resolved results even when --selector-type says otherwise.
func isRefLocatorStr(s string) bool {
	if len(s) < 2 || s[0] != 'e' {
		return false
	}
	for _, c := range s[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// runAction is the thin wrapper every action subcommand uses: send request →
// unwrap response → emit envelope. Pretty mode emits one-line summaries.
func runAction(flags *rootFlags, product string, req sessiond.Request) error {
	sid, ok := sessiond.Resolve(sessionIDFlag)
	if !ok {
		return fmt.Errorf("no active session (use --session <id>, SCRAPFLY_SESSION, or run `browser --session <id> start` first)")
	}
	resp, err := sessiond.Send(sid, req)
	if err != nil {
		return err
	}
	var data any
	_ = json.Unmarshal(resp.Data, &data)
	if flags.pretty {
		switch product {
		case "browser.snapshot":
			if m, ok := data.(map[string]any); ok {
				if nodes, ok := m["nodes"].([]any); ok {
					out.Pretty(os.Stdout, "%s: %d nodes", product, len(nodes))
					return nil
				}
			}
		case "browser.eval":
			if m, ok := data.(map[string]any); ok {
				b, _ := json.Marshal(m["value"])
				out.Pretty(os.Stdout, "%s: %s", product, string(b))
				return nil
			}
		}
		b, _ := json.Marshal(data)
		out.Pretty(os.Stdout, "%s: %s", product, string(b))
		return nil
	}
	return out.WriteSuccess(os.Stdout, false, product, data)
}

func newBrowserNavigateCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "navigate <url>",
		Aliases: []string{"open", "goto"},
		Short:   "Navigate the session's tab to URL",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAction(flags, "browser.navigate", sessiond.Request{Action: "navigate", URL: args[0]})
		},
	}
}

func newBrowserSnapshotCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "snapshot",
		Short: "Capture the accessibility tree; returns refs e1..eN valid until next snapshot",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAction(flags, "browser.snapshot", sessiond.Request{Action: "snapshot"})
		},
	}
}

func newBrowserClickCmd(flags *rootFlags) *cobra.Command {
	var selType string
	cmd := &cobra.Command{
		Use:   "click <locator>",
		Short: "Click an element — locator is an AXTree ref (e3), CSS selector, XPath, AX node id, or ai:<description>",
		Long: `Locator resolution order:
  1. "ai:<text>" → the LLM picks a ref from the current AXTree snapshot.
  2. "e\d+" → AXTree ref from the last snapshot (uses Input.dispatchMouseEvent).
  3. --selector-type=xpath <expr>          → Antibot selector {type:xpath}
  4. --selector-type=axNodeId <ax_node_id> → Antibot selector {type:axNodeId}
  5. anything else                         → Antibot selector {type:css} (default, human-like click)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			locator, err := maybeResolveAI(args[0])
			if err != nil {
				return err
			}
			req := sessiond.Request{Action: "click", Ref: locator}
			if selType != "" && selType != "css" && !isRefLocatorStr(locator) {
				req.Selector = locator
				req.SelectorType = selType
				req.Action = "click_selector"
			}
			return runAction(flags, "browser.click", req)
		},
	}
	cmd.Flags().StringVar(&selType, "selector-type", "css", "css|xpath|axNodeId (default css; overrides auto-detect)")
	return cmd
}

func newBrowserFillCmd(flags *rootFlags) *cobra.Command {
	var (
		selType string
		clear   bool
		wpm     float64
	)
	cmd := &cobra.Command{
		Use:     "fill <locator> <value>",
		Aliases: []string{"type"},
		Short:   "Focus an element and insert text (Antibot.fill: human-like WPM + clear)",
		Long: `Locator:
  - "ai:<text>"  → LLM-resolved from the current AXTree snapshot.
  - "e\d+"       → AXTree ref from the last snapshot (plain Input.insertText path).
  - CSS/XPath/axNodeId via --selector-type goes through Antibot.fill on Scrapfly browsers.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			locator, err := maybeResolveAI(args[0])
			if err != nil {
				return err
			}
			req := sessiond.Request{Action: "fill", Ref: locator, Text: args[1], Clear: clear, WPM: wpm}
			if selType != "" && selType != "css" && !isRefLocatorStr(locator) {
				req.Selector = locator
				req.SelectorType = selType
				req.Action = "fill_selector"
			}
			return runAction(flags, "browser.fill", req)
		},
	}
	cmd.Flags().StringVar(&selType, "selector-type", "css", "css|xpath|axNodeId (default css; overrides auto-detect)")
	cmd.Flags().BoolVar(&clear, "clear", true, "clear existing text before typing (Antibot path only)")
	cmd.Flags().Float64Var(&wpm, "wpm", 0, "typing speed in words per minute (0 = default)")
	return cmd
}

func newBrowserWaitCmd(flags *rootFlags) *cobra.Command {
	var (
		selType   string
		timeoutMs int
		visible   bool
	)
	cmd := &cobra.Command{
		Use:   "wait <selector>",
		Short: "Wait for an element to appear (Antibot.waitForElement — native, no JS polling)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAction(flags, "browser.wait", sessiond.Request{
				Action: "wait", Selector: args[0], SelectorType: selType,
				TimeoutMs: timeoutMs, Visible: visible,
			})
		},
	}
	cmd.Flags().StringVar(&selType, "selector-type", "css", "css|xpath|axNodeId")
	cmd.Flags().IntVar(&timeoutMs, "timeout-ms", 0, "timeout in ms (default 10000)")
	cmd.Flags().BoolVar(&visible, "visible", false, "also require visibility (not display:none / opacity:0)")
	return cmd
}

func newBrowserContentCmd(flags *rootFlags) *cobra.Command {
	var (
		skipIframes bool
		raw         bool
	)
	cmd := &cobra.Command{
		Use:   "content",
		Short: "Return the fully rendered page HTML with iframes inlined (Page.getRenderedContent)",
		RunE: func(cmd *cobra.Command, args []string) error {
			renderIframes := !skipIframes
			req := sessiond.Request{Action: "content", RenderIframes: &renderIframes}
			sid, ok := sessiond.Resolve(sessionIDFlag)
			if !ok {
				return fmt.Errorf("no active session")
			}
			resp, err := sessiond.Send(sid, req)
			if err != nil {
				return err
			}
			var payload struct {
				Content string `json:"content"`
				Type    string `json:"type"`
			}
			if err := json.Unmarshal(resp.Data, &payload); err != nil {
				return err
			}
			if raw || flags.outputPath != "" {
				if flags.outputPath != "" {
					if err := os.WriteFile(flags.outputPath, []byte(payload.Content), 0o644); err != nil {
						return err
					}
					return out.WriteSuccess(os.Stdout, flags.pretty, "browser.content", map[string]any{
						"path": flags.outputPath, "bytes": len(payload.Content), "type": payload.Type,
					})
				}
				fmt.Fprint(os.Stdout, payload.Content)
				return nil
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "browser.content", map[string]any{
				"content": payload.Content, "type": payload.Type, "bytes": len(payload.Content),
			})
		},
	}
	cmd.Flags().BoolVar(&skipIframes, "skip-iframes", false, "don't inline iframe content (faster)")
	cmd.Flags().BoolVar(&raw, "raw", false, "write raw HTML to stdout (no JSON envelope)")
	return cmd
}

func newBrowserSlideCmd(flags *rootFlags) *cobra.Command {
	var (
		target   string
		distance float64
		selType  string
	)
	cmd := &cobra.Command{
		Use:   "slide <source-selector>",
		Short: "Slider/drag primitive for captchas (Antibot.clickAndSlide)",
		Long: `Press-and-hold the source element, slide to a target, release. Pass either
--target <selector> OR --distance <px>.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if target == "" && distance == 0 {
				return fmt.Errorf("need --target or --distance")
			}
			return runAction(flags, "browser.slide", sessiond.Request{
				Action: "slide", Selector: args[0], SelectorType: selType,
				TargetSelector: target, Distance: distance,
			})
		},
	}
	cmd.Flags().StringVar(&target, "target", "", "target selector (CSS) — slide to this element")
	cmd.Flags().Float64Var(&distance, "distance", 0, "fallback: slide by this many px when --target is empty")
	cmd.Flags().StringVar(&selType, "selector-type", "css", "css|xpath|axNodeId for the source selector")
	return cmd
}

func newBrowserScrollCmd(flags *rootFlags) *cobra.Command {
	var (
		amount float64
		ref    string
	)
	cmd := &cobra.Command{
		Use:   "scroll <direction>",
		Short: "Scroll up|down|left|right (optionally anchored to an element)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := maybeResolveAI(ref)
			if err != nil {
				return err
			}
			return runAction(flags, "browser.scroll", sessiond.Request{
				Action: "scroll", Direction: args[0], Amount: amount, Ref: resolved,
			})
		},
	}
	cmd.Flags().Float64Var(&amount, "amount", 500, "scroll distance in px")
	cmd.Flags().StringVar(&ref, "ref", "", "anchor element — ref, CSS selector, or ai:<description>")
	return cmd
}

func newBrowserScreenshotSessionCmd(flags *rootFlags) *cobra.Command {
	var fullpage bool
	cmd := &cobra.Command{
		Use:   "screenshot",
		Short: "PNG of the session viewport; writes to -o/-O or inline base64",
		RunE: func(cmd *cobra.Command, args []string) error {
			sid, ok := sessiond.Resolve(sessionIDFlag)
			if !ok {
				return fmt.Errorf("no active session (use --session <id> or start one first)")
			}
			resp, err := sessiond.Send(sid, sessiond.Request{Action: "screenshot", FullPage: fullpage})
			if err != nil {
				return err
			}
			var payload struct {
				PNG   []byte `json:"png"`
				Bytes int    `json:"bytes"`
			}
			if err := json.Unmarshal(resp.Data, &payload); err != nil {
				return err
			}
			dst, err := resolveOutputPath(flags, sid, "png")
			if err != nil {
				return err
			}
			if dst != "" {
				if err := os.WriteFile(dst, payload.PNG, 0o644); err != nil {
					return err
				}
				return out.WriteSuccess(os.Stdout, flags.pretty, "browser.screenshot", map[string]any{
					"path": dst, "bytes": len(payload.PNG),
				})
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "browser.screenshot", map[string]any{
				"image_base64": stdBase64(payload.PNG), "bytes": len(payload.PNG),
			})
		},
	}
	cmd.Flags().BoolVar(&fullpage, "fullpage", false, "capture beyond viewport")
	return cmd
}

func newBrowserEvalCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "eval <js>",
		Short: "Evaluate a JS expression in the session's page",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAction(flags, "browser.eval", sessiond.Request{Action: "eval", JS: args[0]})
		},
	}
}
