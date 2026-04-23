package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	scrapfly "github.com/scrapfly/go-scrapfly"
	"github.com/scrapfly/scrapfly-cli/internal/cdp"
	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"
)

// action is the on-the-wire command format, one JSON per line.
//
// Supported actions (model-facing vocabulary — keep stable):
//
//	{"action":"open","url":"https://..."}
//	{"action":"snapshot"}
//	{"action":"click","ref":"e5"}
//	{"action":"type","ref":"e7","text":"hello"}
//	{"action":"scroll","direction":"down","amount":500,"ref":"e3"}
//	{"action":"screenshot","fullpage":false}
//	{"action":"eval","js":"document.title"}
//	{"action":"close"}
type action struct {
	Action    string  `json:"action"`
	URL       string  `json:"url,omitempty"`
	Ref       string  `json:"ref,omitempty"`
	Text      string  `json:"text,omitempty"`
	JS        string  `json:"js,omitempty"`
	Direction string  `json:"direction,omitempty"`
	Amount    float64 `json:"amount,omitempty"`
	FullPage  bool    `json:"fullpage,omitempty"`
}

func newBrowserExecuteCmd(flags *rootFlags) *cobra.Command {
	var (
		wsURL      string
		scriptPath string
		targetURL  string
		unblock    bool
		keepOpen   bool
		shotDir    string
		launchArgs browserLaunchFlags
	)
	cmd := &cobra.Command{
		Use:   "execute",
		Short: "Drive a browser session over CDP with a Playwright-MCP-style action set",
		Long: `Connect to a Browser CDP session and run actions. One JSON per line
on stdout (envelope per action). The vocabulary is deliberately small so LLM
tool-use loops can drive the browser directly:

  open / snapshot / click / type / scroll / screenshot / eval / close

Inputs (mutually exclusive):
  --ws <url>        connect to an existing CDP WebSocket
  --url <url>       call /unblock first, then attach (--unblock for alias)
  (neither)         build a new CDP URL from browser flags, attach lazily

Driver modes:
  --script <file>   execute one action per line from file (jsonl)
  (stdin is a tty)  interactive REPL (prompt-per-line)
  (stdin piped)     non-interactive: read actions from stdin

Refs returned by "snapshot" ("e1", "e2", ...) are valid until the next snapshot.
Take a snapshot before click/type/scroll-on-ref.

Screenshots: if -O/--output-dir is set, PNG bytes are written to an auto-named
file and only the path comes back in the envelope. Otherwise base64 in the
envelope.`,
		Example: `  # REPL against an unblocked URL
  scrapfly browser execute --url https://web-scraping.dev/products

  # Script one-shot
  printf '%s\n%s\n%s\n' '{"action":"open","url":"https://example.com"}' \
    '{"action":"snapshot"}' '{"action":"screenshot"}' \
    | scrapfly browser execute`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			// Resolve CDP endpoint. Only build the SDK client when we actually
			// need it (unblock or CDP URL generation).
			var (
				sessionID string
				runID     string
			)
			switch {
			case wsURL != "":
				// caller-supplied URL — no pre-navigation, no SDK needed.
			case targetURL != "" || unblock:
				if targetURL == "" {
					return fmt.Errorf("--unblock requires --url")
				}
				client, err := buildClient(flags)
				if err != nil {
					return err
				}
				res, err := client.CloudBrowserUnblock(scrapfly.UnblockConfig{URL: targetURL})
				if err != nil {
					return err
				}
				wsURL = appendSolveCaptchaParam(res.WSURL, launchArgs.solveCaptcha)
				sessionID = res.SessionID
				runID = res.RunID
			default:
				client, err := buildClient(flags)
				if err != nil {
					return err
				}
				wsURL = appendSolveCaptchaParam(client.CloudBrowser(launchArgs.toConfig()), launchArgs.solveCaptcha)
			}

			cdpClient, err := cdp.Dial(ctx, wsURL)
			if err != nil {
				return err
			}
			defer cdpClient.Close()

			sess, err := cdp.Attach(ctx, cdpClient)
			if err != nil {
				return err
			}
			if !keepOpen {
				defer sess.Detach(context.Background())
			}

			// Emit a "ready" envelope so callers know the attach succeeded.
			_ = out.WriteSuccess(os.Stdout, false, "browser.execute.ready", map[string]any{
				"ws_url":     wsURL,
				"session_id": sessionID,
				"run_id":     runID,
				"target_id":  sess.TargetID,
			})

			// Choose action source.
			var src io.Reader
			isInteractive := false
			switch {
			case scriptPath != "":
				f, err := os.Open(scriptPath)
				if err != nil {
					return err
				}
				defer f.Close()
				src = f
			default:
				// stdin (interactive or piped)
				src = os.Stdin
				if info, err := os.Stdin.Stat(); err == nil && (info.Mode()&os.ModeCharDevice) != 0 {
					isInteractive = true
				}
			}

			scanner := bufio.NewScanner(src)
			scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)
			for {
				if isInteractive {
					fmt.Fprint(os.Stderr, "browser> ")
				}
				if !scanner.Scan() {
					break
				}
				line := strings.TrimSpace(scanner.Text())
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				if err := runOne(ctx, sess, line, flags, shotDir); err != nil {
					_ = out.WriteError(os.Stdout, false, "browser.execute", err)
				}
			}
			return scanner.Err()
		},
	}

	cmd.Flags().StringVar(&wsURL, "ws", "", "connect to an existing CDP WebSocket URL")
	cmd.Flags().StringVar(&targetURL, "url", "", "call /unblock on this URL, then attach")
	cmd.Flags().BoolVar(&unblock, "unblock", false, "alias: pre-navigate via /unblock (requires --url)")
	cmd.Flags().StringVar(&scriptPath, "script", "", "execute actions from a .jsonl file (one action per line)")
	cmd.Flags().BoolVar(&keepOpen, "keep-open", false, "don't close the tab when execute exits")
	cmd.Flags().StringVar(&shotDir, "screenshot-dir", "", "write screenshots into this directory (default: inline base64)")
	bindBrowserLaunchFlags(cmd, &launchArgs)
	// session comes from the persistent --session on the browser command;
	// copy it into launchArgs in PreRun so CDP URL generation picks it up.
	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		launchArgs.session = sessionIDFlag
		return nil
	}

	return cmd
}

func runOne(ctx context.Context, s *cdp.Session, line string, flags *rootFlags, shotDir string) error {
	var a action
	if err := json.Unmarshal([]byte(line), &a); err != nil {
		return fmt.Errorf("invalid action JSON: %w", err)
	}
	switch a.Action {
	case "open":
		res, err := s.Open(ctx, a.URL)
		if err != nil {
			return err
		}
		return out.WriteSuccess(os.Stdout, false, "browser.open", res)

	case "snapshot":
		nodes, err := s.Snapshot(ctx)
		if err != nil {
			return err
		}
		return out.WriteSuccess(os.Stdout, false, "browser.snapshot", map[string]any{"nodes": nodes})

	case "click":
		res, err := s.Click(ctx, a.Ref)
		if err != nil {
			return err
		}
		return out.WriteSuccess(os.Stdout, false, "browser.click", res)

	case "type", "fill":
		res, err := s.Fill(ctx, a.Ref, a.Text)
		if err != nil {
			return err
		}
		return out.WriteSuccess(os.Stdout, false, "browser."+a.Action, res)

	case "scroll":
		amount := a.Amount
		if amount == 0 {
			amount = 500
		}
		res, err := s.Scroll(ctx, a.Direction, amount, a.Ref)
		if err != nil {
			return err
		}
		return out.WriteSuccess(os.Stdout, false, "browser.scroll", res)

	case "screenshot":
		png, err := s.Screenshot(ctx, a.FullPage)
		if err != nil {
			return err
		}
		if shotDir != "" {
			// reuse resolveOutputPath via a synthetic rootFlags copy.
			fake := *flags
			fake.outputPath = ""
			fake.outputDir = shotDir
			dst, err := resolveOutputPath(&fake, s.TargetID, "png")
			if err != nil {
				return err
			}
			if err := os.WriteFile(dst, png, 0o644); err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, false, "browser.screenshot", map[string]any{
				"path": dst, "bytes": len(png),
			})
		}
		return out.WriteSuccess(os.Stdout, false, "browser.screenshot", map[string]any{
			"image_base64": base64Std(png),
			"bytes":        len(png),
		})

	case "eval":
		v, err := s.Eval(ctx, a.JS)
		if err != nil {
			return err
		}
		return out.WriteSuccess(os.Stdout, false, "browser.eval", map[string]any{"value": v})

	case "close":
		return s.Detach(ctx)

	default:
		return fmt.Errorf("unknown action %q", a.Action)
	}
}

func base64Std(b []byte) string {
	// Inline import avoided elsewhere; use stdlib here.
	return stdBase64(b)
}
