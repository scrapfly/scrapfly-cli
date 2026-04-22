package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	scrapfly "github.com/scrapfly/go-scrapfly"
	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"
)

func newCrawlCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "crawl",
		Short: "Start and inspect Crawler API jobs",
		Long: `Crawler API — recursive crawls with depth/page limits, path filters, and
streamed output.

Subcommands:
  start <url>       submit a crawl, print UUID immediately
  run <url>         submit + poll until DONE/FAILED/CANCELLED
  status <uuid>     fetch current state
  urls <uuid>       stream crawled URLs (visited|pending|failed|skipped)
  contents <uuid>   fetch per-URL content (bulk JSON or --plain single URL)
  artifact <uuid>   download WARC or HAR (requires -o/-O)
  cancel <uuid>     stop a running crawl`,
		Example: `  # Full synchronous crawl, markdown content
  scrapfly crawl run https://example.com --max-pages 20 --max-depth 2 \
    --content-format markdown

  # Start async, then poll + dump
  UUID=$(scrapfly crawl start https://example.com --max-pages 50 | jq -r .data.crawler_uuid)
  scrapfly crawl status "$UUID"
  scrapfly crawl urls "$UUID" --status visited
  scrapfly crawl contents "$UUID" --format markdown --limit 50

  # Download a WARC artifact
  scrapfly -o crawl.warc.gz crawl artifact "$UUID" --type warc`,
	}
	cmd.AddCommand(newCrawlStartCmd(flags))
	cmd.AddCommand(newCrawlStatusCmd(flags))
	cmd.AddCommand(newCrawlURLsCmd(flags))
	cmd.AddCommand(newCrawlContentCmd(flags))
	cmd.AddCommand(newCrawlCancelCmd(flags))
	cmd.AddCommand(newCrawlArtifactCmd(flags))
	cmd.AddCommand(newCrawlArtifactParseCmd(flags))
	cmd.AddCommand(newCrawlContentsBatchCmd(flags))
	cmd.AddCommand(newCrawlRunCmd(flags))
	cmd.AddCommand(newCrawlWatchCmd(flags))
	return cmd
}

type crawlStartFlags struct {
	pageLimit                int
	maxDepth                 int
	maxDuration              int
	maxAPICredit             int
	excludePaths             []string
	includePaths             []string
	ignoreBasePath           bool
	followExternal           bool
	allowedExternalDomains   []string
	followInternalSubdomains string
	allowedInternalSubs      []string
	delayMs                  int
	userAgent                string
	maxConcurrency           int
	renderingDelayMs         int
	useSitemaps              bool
	ignoreNoFollow           bool
	respectRobots            string
	cache                    bool
	cacheTTL                 int
	contentFormats           []string
	asp                      bool
	proxyPool                string
	country                  string
	webhookName              string
	webhookEvents            []string
	headerList               []string
	cacheClear               bool
	extractionRulesFile      string
}

func bindCrawlStartFlags(cmd *cobra.Command, f *crawlStartFlags) {
	cmd.Flags().IntVar(&f.pageLimit, "max-pages", 0, "max pages to crawl (0 = server default)")
	cmd.Flags().IntVar(&f.maxDepth, "max-depth", 0, "max crawl depth")
	cmd.Flags().IntVar(&f.maxDuration, "max-duration", 0, "max duration seconds (15-10800)")
	cmd.Flags().IntVar(&f.maxAPICredit, "max-api-credit", 0, "max API credit spend (0 = unlimited)")
	cmd.Flags().StringSliceVar(&f.excludePaths, "exclude", nil, "path glob to exclude (repeatable)")
	cmd.Flags().StringSliceVar(&f.includePaths, "include", nil, "path glob to include (repeatable, exclusive with --exclude)")
	cmd.Flags().BoolVar(&f.ignoreBasePath, "ignore-base-path", false, "ignore base path restriction")
	cmd.Flags().BoolVar(&f.followExternal, "follow-external", false, "follow external links")
	cmd.Flags().StringSliceVar(&f.allowedExternalDomains, "allowed-external-domain", nil, "whitelist external domain (repeatable)")
	cmd.Flags().StringVar(&f.followInternalSubdomains, "follow-internal-subdomains", "", "true|false (tri-state; empty = server default)")
	cmd.Flags().StringSliceVar(&f.allowedInternalSubs, "allowed-internal-subdomain", nil, "whitelist internal subdomain (repeatable)")
	cmd.Flags().IntVar(&f.delayMs, "delay", 0, "per-request delay ms (0-15000)")
	cmd.Flags().StringVar(&f.userAgent, "user-agent", "", "override user-agent")
	cmd.Flags().IntVar(&f.maxConcurrency, "max-concurrency", 0, "max concurrent crawls")
	cmd.Flags().IntVar(&f.renderingDelayMs, "rendering-delay", 0, "ms to wait after page load (0-25000)")
	cmd.Flags().BoolVar(&f.useSitemaps, "use-sitemaps", false, "seed crawl from sitemaps")
	cmd.Flags().BoolVar(&f.ignoreNoFollow, "ignore-no-follow", false, "ignore rel=nofollow")
	cmd.Flags().StringVar(&f.respectRobots, "respect-robots-txt", "", "true|false (tri-state; empty = server default true)")
	cmd.Flags().BoolVar(&f.cache, "cache", false, "enable caching on child scrapes")
	cmd.Flags().IntVar(&f.cacheTTL, "cache-ttl", 0, "cache TTL seconds (0-604800)")
	cmd.Flags().StringSliceVar(&f.contentFormats, "content-format", nil, "html|clean_html|markdown|text|json|extracted_data|page_metadata (repeatable)")
	cmd.Flags().BoolVar(&f.asp, "asp", false, "enable Anti-Scraping Protection for child scrapes")
	cmd.Flags().StringVar(&f.proxyPool, "proxy-pool", "", "proxy pool for child scrapes")
	cmd.Flags().StringVar(&f.country, "country", "", "proxy country for child scrapes")
	cmd.Flags().StringVar(&f.webhookName, "webhook", "", "webhook name")
	cmd.Flags().StringSliceVar(&f.webhookEvents, "webhook-event", nil, "webhook event (repeatable)")
	cmd.Flags().StringArrayVar(&f.headerList, "header", nil, "per-scrape request header key=value (repeatable)")
	cmd.Flags().BoolVar(&f.cacheClear, "cache-clear", false, "force cache refresh on each child scrape")
	cmd.Flags().StringVar(&f.extractionRulesFile, "extraction-rules-file", "", "path to JSON file with extraction rules map")
}

func buildCrawlerConfig(url string, f *crawlStartFlags) (*scrapfly.CrawlerConfig, error) {
	cfg := &scrapfly.CrawlerConfig{
		URL:                       url,
		PageLimit:                 f.pageLimit,
		MaxDepth:                  f.maxDepth,
		MaxDuration:               f.maxDuration,
		MaxAPICredit:              f.maxAPICredit,
		ExcludePaths:              f.excludePaths,
		IncludeOnlyPaths:          f.includePaths,
		IgnoreBasePathRestriction: f.ignoreBasePath,
		FollowExternalLinks:       f.followExternal,
		AllowedExternalDomains:    f.allowedExternalDomains,
		AllowedInternalSubdomains: f.allowedInternalSubs,
		Delay:                     f.delayMs,
		UserAgent:                 f.userAgent,
		MaxConcurrency:            f.maxConcurrency,
		RenderingDelay:            f.renderingDelayMs,
		UseSitemaps:               f.useSitemaps,
		IgnoreNoFollow:            f.ignoreNoFollow,
		Cache:                     f.cache,
		CacheTTL:                  f.cacheTTL,
		CacheClear:                f.cacheClear,
		ASP:                       f.asp,
		ProxyPool:                 f.proxyPool,
		Country:                   f.country,
		WebhookName:               f.webhookName,
	}
	for _, v := range f.contentFormats {
		cfg.ContentFormats = append(cfg.ContentFormats, scrapfly.CrawlerContentFormat(v))
	}
	for _, v := range f.webhookEvents {
		cfg.WebhookEvents = append(cfg.WebhookEvents, scrapfly.CrawlerWebhookEvent(v))
	}
	b, err := parseTriState(f.respectRobots)
	if err != nil {
		return nil, fmt.Errorf("--respect-robots-txt: %w", err)
	}
	cfg.RespectRobotsTxt = b
	b, err = parseTriState(f.followInternalSubdomains)
	if err != nil {
		return nil, fmt.Errorf("--follow-internal-subdomains: %w", err)
	}
	cfg.FollowInternalSubdomains = b
	if len(f.headerList) > 0 {
		h, err := parseKeyVals(f.headerList)
		if err != nil {
			return nil, fmt.Errorf("--header: %w", err)
		}
		cfg.Headers = h
	}
	if f.extractionRulesFile != "" {
		raw, err := os.ReadFile(f.extractionRulesFile)
		if err != nil {
			return nil, fmt.Errorf("read --extraction-rules-file: %w", err)
		}
		var rules map[string]interface{}
		if err := json.Unmarshal(raw, &rules); err != nil {
			return nil, fmt.Errorf("parse --extraction-rules-file: %w", err)
		}
		cfg.ExtractionRules = rules
	}
	return cfg, nil
}

func parseTriState(s string) (*bool, error) {
	switch s {
	case "":
		return nil, nil
	case "true", "1", "yes":
		return scrapfly.BoolPtr(true), nil
	case "false", "0", "no":
		return scrapfly.BoolPtr(false), nil
	}
	return nil, fmt.Errorf("expected true|false, got %q", s)
}

func newCrawlStartCmd(flags *rootFlags) *cobra.Command {
	var f crawlStartFlags
	cmd := &cobra.Command{
		Use:     "start <url>",
		Short:   "Start a new crawl and print the crawler UUID",
		Example: `  scrapfly crawl start https://example.com --max-pages 50 --max-depth 2`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := buildCrawlerConfig(args[0], &f)
			if err != nil {
				return err
			}
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			start, err := client.StartCrawl(cfg)
			if err != nil {
				return err
			}
			if flags.pretty {
				out.Pretty(os.Stdout, "✓ Started crawl %s for %s", start.CrawlerUUID, args[0])
				out.Pretty(os.Stdout, "")
				out.Pretty(os.Stdout, "To watch it live, try:  scrapfly crawl watch %s", start.CrawlerUUID)
				out.Pretty(os.Stdout, "To check once, try:     scrapfly crawl status %s", start.CrawlerUUID)
				return nil
			}
			return out.WriteSuccess(os.Stdout, false, "crawl.start", start)
		},
	}
	bindCrawlStartFlags(cmd, &f)
	return cmd
}

func newCrawlStatusCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "status <uuid>",
		Short: "Fetch status of a crawler job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			st, err := client.CrawlStatus(args[0])
			if err != nil {
				return err
			}
			// Compute a terminal-state string for easy shell scripting.
			terminal := "running"
			switch {
			case st.IsComplete():
				terminal = "done"
			case st.IsFailed():
				terminal = "failed"
			case st.IsCancelled():
				terminal = "cancelled"
			}
			if flags.pretty {
				stopReason := ""
				if st.State.StopReason != nil {
					stopReason = *st.State.StopReason
				}
				out.Pretty(os.Stdout, "%s terminal=%s visited=%d stop_reason=%q",
					args[0], terminal, st.State.URLsVisited, stopReason)
				return nil
			}
			// Wrap the SDK's status struct with our synthetic terminal field.
			return out.WriteSuccess(os.Stdout, false, "crawl.status", map[string]any{
				"terminal": terminal,
				"status":   st,
			})
		},
	}
}

func newCrawlURLsCmd(flags *rootFlags) *cobra.Command {
	var (
		status  string
		page    int
		perPage int
	)
	cmd := &cobra.Command{
		Use:   "urls <uuid>",
		Short: "List crawled URLs for a job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			res, err := client.CrawlURLs(args[0], &scrapfly.CrawlURLsOptions{
				Status: status, Page: page, PerPage: perPage,
			})
			if err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "crawl.urls", res)
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "visited|pending|failed|skipped")
	cmd.Flags().IntVar(&page, "page", 1, "page (1-based)")
	cmd.Flags().IntVar(&perPage, "per-page", 100, "page size")
	return cmd
}

func newCrawlContentCmd(flags *rootFlags) *cobra.Command {
	var (
		format string
		url    string
		limit  int
		offset int
		plain  bool
	)
	cmd := &cobra.Command{
		Use:   "contents <uuid>",
		Short: "Fetch crawled contents (bulk JSON or plain single-URL)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if format == "" {
				return fmt.Errorf("--format is required")
			}
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			if plain {
				if url == "" {
					return fmt.Errorf("--plain requires --url")
				}
				body, err := client.CrawlContentsPlain(args[0], url, scrapfly.CrawlerContentFormat(format))
				if err != nil {
					return err
				}
				fmt.Fprint(os.Stdout, body)
				return nil
			}
			res, err := client.CrawlContentsJSON(args[0], scrapfly.CrawlerContentFormat(format), &scrapfly.CrawlContentsOptions{
				URL: url, Limit: limit, Offset: offset,
			})
			if err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "crawl.contents", res)
		},
	}
	cmd.Flags().StringVar(&format, "format", "", "content format (html|clean_html|markdown|text|json|extracted_data|page_metadata)")
	cmd.Flags().StringVar(&url, "url", "", "filter to single URL")
	cmd.Flags().IntVar(&limit, "limit", 0, "max URLs in bulk mode (default server 10, max 50)")
	cmd.Flags().IntVar(&offset, "offset", 0, "offset in bulk mode")
	cmd.Flags().BoolVar(&plain, "plain", false, "return raw content for a single URL (requires --url)")
	return cmd
}

func newCrawlCancelCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <uuid>",
		Short: "Cancel a running crawl",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			if err := client.CrawlCancel(args[0]); err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "crawl.cancel", map[string]string{"uuid": args[0], "status": "cancelled"})
		},
	}
}

func newCrawlArtifactCmd(flags *rootFlags) *cobra.Command {
	var artifactType string
	cmd := &cobra.Command{
		Use:   "artifact <uuid>",
		Short: "Download a crawl artifact (warc|har) to --output",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			art, err := client.CrawlArtifact(args[0], scrapfly.CrawlerArtifactType(artifactType))
			if err != nil {
				return err
			}
			ext := artifactType
			if artifactType == "warc" {
				ext = "warc.gz"
			}
			dst, err := resolveOutputPath(flags, args[0], ext)
			if err != nil {
				return err
			}
			if dst == "" {
				return fmt.Errorf("-o/--output or -O/--output-dir is required")
			}
			if err := os.WriteFile(dst, art.Data, 0o644); err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "crawl.artifact", map[string]any{
				"uuid": args[0], "type": artifactType, "path": dst, "bytes": len(art.Data),
			})
		},
	}
	cmd.Flags().StringVar(&artifactType, "type", "warc", "warc|har")
	return cmd
}

func newCrawlContentsBatchCmd(flags *rootFlags) *cobra.Command {
	var (
		fromFile string
		formats  []string
	)
	cmd := &cobra.Command{
		Use:   "contents-batch <uuid> [url...]",
		Short: "Fetch content for up to 100 URLs in a single round-trip (CloudCrawlContentsBatch)",
		Long: `POST /crawl/{uuid}/contents/batch — returns a map of url → format → body.
URLs can be passed as positional args and/or read from --from-file.
Formats are repeatable via --format.`,
		Example: `  scrapfly crawl contents-batch <uuid> \
    https://example.com https://example.com/page --format markdown --format html`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			uuid := args[0]
			urls := append([]string{}, args[1:]...)
			if fromFile != "" {
				extra, err := readURLsFile(fromFile)
				if err != nil {
					return err
				}
				urls = append(urls, extra...)
			}
			if len(urls) == 0 {
				return fmt.Errorf("need at least one URL (positional or --from-file)")
			}
			if len(formats) == 0 {
				return fmt.Errorf("need at least one --format")
			}
			var f []scrapfly.CrawlerContentFormat
			for _, v := range formats {
				f = append(f, scrapfly.CrawlerContentFormat(v))
			}
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			res, err := client.CrawlContentsBatch(uuid, urls, f)
			if err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "crawl.contents.batch", res)
		},
	}
	cmd.Flags().StringVar(&fromFile, "from-file", "", "read URLs from file (one per line; `-` for stdin)")
	cmd.Flags().StringSliceVar(&formats, "format", nil, "content format (repeatable)")
	return cmd
}

func newCrawlRunCmd(flags *rootFlags) *cobra.Command {
	var (
		f            crawlStartFlags
		pollInterval time.Duration
		maxWait      time.Duration
	)
	cmd := &cobra.Command{
		Use:   "run <url>",
		Short: "Start a crawl and poll until terminal (DONE|FAILED|CANCELLED)",
		Example: `  scrapfly crawl run https://example.com \
    --max-pages 20 --content-format markdown --poll-interval 5s --max-wait 15m`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := buildCrawlerConfig(args[0], &f)
			if err != nil {
				return err
			}
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			start, err := client.StartCrawl(cfg)
			if err != nil {
				return err
			}
			deadline := time.Now().Add(maxWait)
			var last *scrapfly.CrawlerStatus
			for {
				st, err := client.CrawlStatus(start.CrawlerUUID)
				if err != nil {
					return err
				}
				last = st
				if st.IsComplete() || st.IsFailed() || st.IsCancelled() {
					break
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("crawl %s did not finish within %s (last state: %v)", start.CrawlerUUID, maxWait, st.State)
				}
				time.Sleep(pollInterval)
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "crawl.run", map[string]any{
				"uuid": start.CrawlerUUID, "status": last,
			})
		},
	}
	bindCrawlStartFlags(cmd, &f)
	cmd.Flags().DurationVar(&pollInterval, "poll-interval", 5*time.Second, "status poll interval")
	cmd.Flags().DurationVar(&maxWait, "max-wait", 30*time.Minute, "maximum wait before giving up")
	return cmd
}
