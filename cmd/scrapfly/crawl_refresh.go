package main

import (
	"fmt"
	"os"

	scrapfly "github.com/scrapfly/go-scrapfly"
	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"
)

func newCrawlRefreshCmd(flags *rootFlags) *cobra.Command {
	var (
		enable   bool
		disable  bool
		interval int
	)
	cmd := &cobra.Command{
		Use:   "refresh <uuid>",
		Short: "Re-scrape a crawl in place now, or change its refresh schedule",
		Long: `A refresh re-scrapes the crawl's own URLs under the same UUID: same
` + "`/crawl/{uuid}/contents`" + `, same artifacts, same search index. Only pages whose
content actually changed are re-indexed, and pages that disappeared are
dropped, so anything already pointing at this crawl keeps working.

With no flags the command runs one refresh immediately. With --enable,
--disable or --interval it changes the schedule instead and starts nothing:
the two are separate actions because a schedule change should not silently
bill a full re-scrape.

Explicit boolean values are honored: --enable=false turns scheduling off;
--disable=false turns it on. The two flags are mutually exclusive.

A refresh bills the pages it re-scrapes, exactly like the original crawl. What
unchanged pages save is the embedding and the index write. The interval floor
is 3600s and the ceiling 7776000s (90 days).

A refreshed crawl's content is not immutable: the same URL can return
different bytes after a refresh run.`,
		Example: `  # Run one refresh right now
  scrapfly crawl refresh 01HX7K4...

  # Refresh daily from now on
  scrapfly crawl refresh 01HX7K4... --enable --interval 86400

  # Stop refreshing; the interval is kept for when it is turned back on
  scrapfly crawl refresh 01HX7K4... --disable`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			enableSet := cmd.Flags().Changed("enable")
			disableSet := cmd.Flags().Changed("disable")
			if enableSet && disableSet {
				return fmt.Errorf("%w: --enable and --disable are mutually exclusive", scrapfly.ErrCrawlerConfig)
			}
			client, err := buildClient(flags)
			if err != nil {
				return err
			}

			settingsChanged := enableSet || disableSet || cmd.Flags().Changed("interval")
			if !settingsChanged {
				state, err := client.CrawlRefreshNow(args[0])
				if err != nil {
					return err
				}
				if flags.pretty {
					printCrawlRefreshState(args[0], state)
					return nil
				}
				return out.WriteSuccess(os.Stdout, false, "crawl.refresh", state)
			}

			settings := scrapfly.CrawlRefreshSettings{}
			if enableSet {
				settings.Enabled = scrapfly.BoolPtr(enable)
			}
			if disableSet {
				settings.Enabled = scrapfly.BoolPtr(!disable)
			}
			// Only send an interval the caller actually typed: a zero would
			// otherwise wipe the period the crawl keeps while disabled.
			if cmd.Flags().Changed("interval") {
				settings.IntervalSeconds = &interval
			}

			state, err := client.CrawlRefreshSettings(args[0], settings)
			if err != nil {
				return err
			}
			if flags.pretty {
				printCrawlRefreshState(args[0], state)
				return nil
			}
			return out.WriteSuccess(os.Stdout, false, "crawl.refresh", state)
		},
	}
	cmd.Flags().BoolVar(&enable, "enable", false, "turn auto-refresh on instead of running one now")
	cmd.Flags().BoolVar(&disable, "disable", false, "turn auto-refresh off instead of running one now")
	cmd.Flags().IntVar(&interval, "interval", 0, "seconds between refresh runs, 3600-7776000")
	return cmd
}

func newCrawlRefreshHistoryCmd(flags *rootFlags) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "refresh-history <uuid>",
		Short: "Show a crawl's refresh timeline",
		Long: `One row per refresh run, newest last. The server keeps the 50 most recent
runs; older rows are trimmed rather than paged, because the timeline exists to
show recent activity.

A row where added, updated and removed are all zero means the site stood still
and the run cost no re-indexing.`,
		Example: `  scrapfly crawl refresh-history 01HX7K4...
  scrapfly crawl refresh-history 01HX7K4... --limit 10 --pretty`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			history, err := client.CrawlRefreshHistory(args[0], limit)
			if err != nil {
				return err
			}
			if flags.pretty {
				if len(history) == 0 {
					out.Pretty(os.Stdout, "no refresh has run yet")
					return nil
				}
				for _, entry := range history {
					out.Pretty(os.Stdout, "%s  gen=%d  +%d ~%d -%d  =%d  failed=%d  %dms  search=%s",
						entry.At, entry.Generation, entry.Added, entry.Updated, entry.Removed,
						entry.Unchanged, entry.Failed, entry.DurationMs, entry.SearchStatus)
					if entry.Error != "" {
						out.Pretty(os.Stdout, "   error: %s", entry.Error)
					}
				}
				return nil
			}
			return out.WriteSuccess(os.Stdout, false, "crawl.refresh_history", map[string]any{
				"crawler_uuid": args[0],
				"history":      history,
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "keep only the last N rows (0 = everything the server kept)")
	return cmd
}

func printCrawlRefreshState(uuid string, state *scrapfly.CrawlerRefreshState) {
	out.Pretty(os.Stdout, "%s  enabled=%v status=%s generation=%d",
		uuid, state.Enabled, state.Status, state.Generation)
	if state.IntervalSeconds > 0 {
		out.Pretty(os.Stdout, "interval=%ds", state.IntervalSeconds)
	}
	if state.LastRunAt != "" {
		out.Pretty(os.Stdout, "last run: %s", state.LastRunAt)
	}
	if state.NextRunAt != "" {
		out.Pretty(os.Stdout, "next run: %s", state.NextRunAt)
	}
	if state.Error != "" {
		out.Pretty(os.Stdout, "error: %s", state.Error)
	}
	if last := state.LastRun(); last != nil {
		out.Pretty(os.Stdout, "last diff: +%d ~%d -%d (=%d unchanged, %d failed)",
			last.Added, last.Updated, last.Removed, last.Unchanged, last.Failed)
	}
}
