package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/scrapfly/scrapfly-cli/internal/sessiond"
	"github.com/spf13/cobra"
)

func newBrowserReplayCmd(flags *rootFlags) *cobra.Command {
	var (
		delayMs int
		dryRun  bool
	)
	cmd := &cobra.Command{
		Use:   "replay <file.jsonl>",
		Short: "Replay a recorded browser session (from --record)",
		Long: `Reads a .jsonl file produced by 'browser start --record <file>' and
replays each action against the active session daemon. Each line is a
JSON Request (same format the daemon accepts).

Use --delay to add a pause between actions (milliseconds).
Use --dry-run to print each action without executing.`,
		Example: `  # Record a session
  scrapfly browser --session demo start --record session.jsonl &
  scrapfly browser navigate https://example.com
  scrapfly browser fill 'ai:username' admin
  scrapfly browser click 'ai:submit'
  scrapfly browser stop

  # Replay it
  scrapfly browser --session demo2 start &
  scrapfly browser replay session.jsonl --delay 500`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer f.Close()

			sid, ok := sessiond.Resolve(sessionIDFlag)
			if !ok && !dryRun {
				return fmt.Errorf("no active session (start one first, or use --dry-run)")
			}

			scanner := bufio.NewScanner(f)
			scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)
			step := 0
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				step++
				var req sessiond.Request
				if err := json.Unmarshal([]byte(line), &req); err != nil {
					return fmt.Errorf("line %d: invalid JSON: %w", step, err)
				}
				if dryRun {
					out.Pretty(os.Stdout, "[%d] %s %s %s", step, req.Action, req.URL+req.Ref+req.Selector, req.Text)
					continue
				}
				fmt.Fprintf(os.Stderr, "[replay %d] %s\n", step, req.Action)
				resp, err := sessiond.Send(sid, req)
				if err != nil {
					fmt.Fprintf(os.Stderr, "[replay %d] error: %v\n", step, err)
					continue
				}
				var data any
				_ = json.Unmarshal(resp.Data, &data)
				_ = out.WriteSuccess(os.Stdout, false, "browser.replay", map[string]any{
					"step": step, "action": req.Action, "result": data,
				})
				if delayMs > 0 {
					time.Sleep(time.Duration(delayMs) * time.Millisecond)
				}
			}
			return scanner.Err()
		},
	}
	cmd.Flags().IntVar(&delayMs, "delay", 0, "pause between actions (milliseconds)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print actions without executing")
	return cmd
}
