package main

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	scrapfly "github.com/scrapfly/go-scrapfly"
	"github.com/spf13/cobra"
)

func isStdoutTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func newCrawlWatchCmd(flags *rootFlags) *cobra.Command {
	var (
		interval   time.Duration
		exitStatus bool
	)
	cmd := &cobra.Command{
		Use:   "watch <uuid>",
		Short: "Poll a crawl and display a live stats panel until it reaches a terminal state",
		Long: `Watch a running crawl and continuously render its discovery stats
(visited / extracted / failed / skipped / queue depth / credits) until it
reaches DONE, FAILED, or CANCELLED. On a TTY the panel is redrawn in
place; on non-TTY stdout (CI, redirected output) each poll is appended
as a new frame so the log remains tailable.

Stopping the watch (Ctrl+C) does NOT stop the crawl — it keeps running
server-side.`,
		Example: `  scrapfly crawl watch 01HX7K4...
  scrapfly crawl watch 01HX7K4... --interval 5s --exit-status`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			uuid := args[0]
			client, err := buildClient(flags)
			if err != nil {
				return err
			}

			isTTY := isStdoutTTY()

			sig := make(chan os.Signal, 1)
			signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
			defer signal.Stop(sig)

			start := time.Now()
			lastLines := 0

			for {
				st, err := client.CrawlStatus(uuid)
				if err != nil {
					return err
				}
				panel := renderWatchPanel(uuid, st, time.Since(start), interval)

				if isTTY && lastLines > 0 {
					fmt.Fprintf(os.Stdout, "\033[%dA\033[J", lastLines)
				}
				fmt.Fprint(os.Stdout, panel)
				lastLines = strings.Count(panel, "\n")

				if st.IsComplete() || st.IsFailed() || st.IsCancelled() {
					renderWatchTerminal(os.Stdout, uuid, st, time.Since(start))
					if exitStatus && (st.IsFailed() || st.IsCancelled()) {
						os.Exit(1)
					}
					return nil
				}

				select {
				case <-sig:
					fmt.Fprintln(os.Stdout, "\nStopped watching. Crawl continues server-side.")
					return nil
				case <-time.After(interval):
				}
			}
		},
	}
	cmd.Flags().DurationVar(&interval, "interval", 3*time.Second, "poll interval (e.g. 3s, 10s)")
	cmd.Flags().BoolVar(&exitStatus, "exit-status", false, "exit with non-zero status if the crawl failed or was cancelled")
	return cmd
}

func renderWatchPanel(uuid string, st *scrapfly.CrawlerStatus, elapsed, poll time.Duration) string {
	var b strings.Builder
	sep := strings.Repeat("─", 50)

	fmt.Fprintf(&b, "Crawl %s\n", uuid)
	fmt.Fprintf(&b, "%s\n", sep)
	fmt.Fprintf(&b, "State       %-16s elapsed %s\n", st.Status, fmtDuration(elapsed))
	fmt.Fprintf(&b, "Visited     %-16d extracted %d\n", st.State.URLsVisited, st.State.URLsExtracted)
	fmt.Fprintf(&b, "Queue       %-16s failed %d  skipped %d\n",
		fmt.Sprintf("%d pending", st.State.URLsToCrawl), st.State.URLsFailed, st.State.URLsSkipped)
	fmt.Fprintf(&b, "Credits     %d used\n", st.State.APICreditUsed)
	fmt.Fprintf(&b, "%s\n", sep)
	fmt.Fprintf(&b, "Polling every %s. Ctrl+C to stop watching (crawl continues).\n", poll)
	return b.String()
}

func renderWatchTerminal(w *os.File, uuid string, st *scrapfly.CrawlerStatus, elapsed time.Duration) {
	mark := "✓"
	label := "DONE"
	switch {
	case st.IsFailed():
		mark = "✗"
		label = "FAILED"
	case st.IsCancelled():
		mark = "⚠"
		label = "CANCELLED"
	}
	stopReason := ""
	if st.State.StopReason != nil {
		stopReason = *st.State.StopReason
	}
	fmt.Fprintf(w, "\n%s Crawl %s %s in %s\n", mark, uuid, label, fmtDuration(elapsed))
	fmt.Fprintf(w, "  visited=%d extracted=%d failed=%d skipped=%d  credits=%d\n",
		st.State.URLsVisited, st.State.URLsExtracted, st.State.URLsFailed, st.State.URLsSkipped, st.State.APICreditUsed)
	if stopReason != "" {
		fmt.Fprintf(w, "  stop_reason=%s\n", stopReason)
	}
	if st.IsComplete() {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "To list visited URLs, try:  scrapfly crawl urls %s --status visited\n", uuid)
		fmt.Fprintf(w, "To fetch one page, try:     scrapfly crawl contents %s --url <URL> --format html --plain\n", uuid)
		fmt.Fprintf(w, "To download WARC, try:      scrapfly -o crawl.warc.gz crawl artifact %s --type warc\n", uuid)
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Note: markdown/text/clean_html are only available if the crawl was started with --content-format <fmt>.")
	}
}

func fmtDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	s := int((d % time.Minute) / time.Second)
	if h > 0 {
		return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
