package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	scrapfly "github.com/scrapfly/go-scrapfly"
	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"
)

func newScrapeCmd(flags *rootFlags) *cobra.Command {
	var (
		method             string
		headerList         []string
		cookieList         []string
		body               string
		bodyFile           string
		country            string
		lang               []string
		osSpoof            string
		browserBrand       string
		geolocation        string
		renderJS           bool
		waitForSelector    string
		renderingWait      int
		renderingStage     string
		jsFile             string
		jsScenarioFile     string
		autoScroll         bool
		asp                bool
		cache              bool
		cacheTTL           int
		cacheClear         bool
		session            string
		sessionStickyProxy bool
		format             string
		formatOptions      []string
		extractionPrompt   string
		extractionModel    string
		extractionTemplate string
		proxyPool          string
		timeoutMs          int
		costBudget         int
		tags               []string
		debug              bool
		ssl                bool
		dns                bool
		correlationID      string
		proxified          bool
		screenshotFull     bool
		onlyMainContent    bool
		timing             bool
		fromFile           string
		concurrency        int
		webhook            string
		retry              bool
		dataFile           string
		extTemplateFile    string
		screenshotNamed    []string
		screenshotFlags    []string
		contentOnly        bool
	)

	cmd := &cobra.Command{
		Use:     "scrape <url> [more-urls...]",
		Aliases: []string{"scraper"},
		Short:   "Scrape one or more URLs via Scrapfly Web Scraping API",
		Long: `Scrape a single URL. Returns the scrape envelope (content, status, headers,
browser data, cost) as JSON, or a human summary with --pretty.

Flag groups:
  Rendering:       --render-js --wait-for-selector --rendering-wait --auto-scroll
  Anti-bot:        --asp --cost-budget --proxy-pool --country
  Output format:   --format raw|markdown|clean_html|text [--format-option ...]
  Extraction:      --extraction-prompt | --extraction-model | --extraction-template
  Request shape:   --method --header k=v --cookie n=v --body | --body-file
  Session/cache:   --session --cache --cache-ttl`,
		Example: `  # Basic markdown scrape
  scrapfly scrape https://web-scraping.dev/products --format markdown

  # JS-rendered, ASP-bypassed, US proxy
  scrapfly scrape https://example.com --render-js --asp --country us

  # POST with JSON body and custom headers
  scrapfly scrape https://httpbin.dev/post --method POST \
    --header content-type=application/json --body '{"hello":"world"}'

  # AI extraction inline
  scrapfly scrape https://example.com/product \
    --render-js --extraction-prompt "product name, price, sku"

  # Pipe scrape body into extract (two-step: fetch then extract)
  scrapfly scrape https://web-scraping.dev/product/1 --render-js --proxified \
    | scrapfly extract --content-type text/html \
        --url https://web-scraping.dev/product/1 \
        --prompt "product name, price, sku, description"`,
		Args: cobra.MinimumNArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			if onlyMainContent && format == "" {
				format = string(scrapfly.FormatMarkdown)
			}
			if screenshotFull {
				renderJS = true
			}

			// Batch mode kicks in when >1 arg or --from-file is set. We emit
			// one JSON envelope per line (ndjson) on stdout — easy for agents
			// to pipe into jq / xargs.
			urls := append([]string{}, args...)
			if fromFile != "" {
				fromURLs, err := readURLsFile(fromFile)
				if err != nil {
					return err
				}
				urls = append(urls, fromURLs...)
			}
			if len(urls) == 0 {
				// No URL + no stdin batch: show the full help so the user
				// sees flags + examples instead of a terse JSON error.
				return cmd.Help()
			}

			cfg := &scrapfly.ScrapeConfig{
				URL:                urls[0],
				Webhook:            webhook,
				Retry:              retry,
				Method:             scrapfly.HttpMethod(strings.ToUpper(method)),
				Country:            country,
				Lang:               lang,
				OS:                 osSpoof,
				BrowserBrand:       browserBrand,
				Geolocation:        geolocation,
				RenderJS:           renderJS,
				WaitForSelector:    waitForSelector,
				RenderingWait:      renderingWait,
				RenderingStage:     renderingStage,
				AutoScroll:         autoScroll,
				ASP:                asp,
				Cache:              cache,
				CacheTTL:           cacheTTL,
				CacheClear:         cacheClear,
				Session:            session,
				SessionStickyProxy: sessionStickyProxy,
				Format:             scrapfly.Format(format),
				ExtractionPrompt:   extractionPrompt,
				ExtractionModel:    scrapfly.ExtractionModel(extractionModel),
				ExtractionTemplate: extractionTemplate,
				ProxyPool:          scrapfly.ProxyPool(proxyPool),
				Timeout:            timeoutMs,
				CostBudget:         costBudget,
				Tags:               tags,
				Debug:              debug,
				SSL:                ssl,
				DNS:                dns,
				CorrelationID:      correlationID,
				ProxifiedResponse:  proxified,
			}

			for _, o := range formatOptions {
				cfg.FormatOptions = append(cfg.FormatOptions, scrapfly.FormatOption(o))
			}
			if screenshotFull {
				if cfg.Screenshots == nil {
					cfg.Screenshots = map[string]string{}
				}
				cfg.Screenshots["main"] = "fullpage"
			}
			if len(screenshotNamed) > 0 {
				m, err := parseKeyVals(screenshotNamed)
				if err != nil {
					return fmt.Errorf("invalid --screenshot-named: %w", err)
				}
				if cfg.Screenshots == nil {
					cfg.Screenshots = map[string]string{}
				}
				for k, v := range m {
					cfg.Screenshots[k] = v
				}
			}
			for _, f := range screenshotFlags {
				cfg.ScreenshotFlags = append(cfg.ScreenshotFlags, scrapfly.ScreenshotFlag(f))
			}
			if dataFile != "" {
				raw, err := os.ReadFile(dataFile)
				if err != nil {
					return fmt.Errorf("read --data-file: %w", err)
				}
				var m map[string]interface{}
				if err := json.Unmarshal(raw, &m); err != nil {
					return fmt.Errorf("parse --data-file (JSON object expected): %w", err)
				}
				cfg.Data = m
			}
			if extTemplateFile != "" {
				raw, err := os.ReadFile(extTemplateFile)
				if err != nil {
					return fmt.Errorf("read --extraction-template-file: %w", err)
				}
				var m map[string]interface{}
				if err := json.Unmarshal(raw, &m); err != nil {
					return fmt.Errorf("parse --extraction-template-file: %w", err)
				}
				cfg.ExtractionEphemeralTemplate = m
			}

			headers, err := parseKeyVals(headerList)
			if err != nil {
				return fmt.Errorf("invalid --header: %w", err)
			}
			cfg.Headers = headers
			cookies, err := parseKeyVals(cookieList)
			if err != nil {
				return fmt.Errorf("invalid --cookie: %w", err)
			}
			cfg.Cookies = cookies

			if body != "" && bodyFile != "" {
				return fmt.Errorf("--body and --body-file are mutually exclusive")
			}
			if bodyFile != "" {
				raw, err := os.ReadFile(bodyFile)
				if err != nil {
					return fmt.Errorf("read --body-file: %w", err)
				}
				cfg.Body = string(raw)
			} else if body != "" {
				cfg.Body = body
			}

			if jsFile != "" {
				raw, err := os.ReadFile(jsFile)
				if err != nil {
					return fmt.Errorf("read --js-file: %w", err)
				}
				cfg.JS = string(raw)
			}
			if jsScenarioFile != "" {
				raw, err := os.ReadFile(jsScenarioFile)
				if err != nil {
					return fmt.Errorf("read --js-scenario-file: %w", err)
				}
				var steps []map[string]any
				if err := json.Unmarshal(raw, &steps); err != nil {
					return fmt.Errorf("parse --js-scenario-file (JSON array expected): %w", err)
				}
				cfg.JSScenario = steps
			}

			client, err := buildClient(flags)
			if err != nil {
				return err
			}

			// Batch path: ConcurrentScrape emits ndjson envelopes.
			if len(urls) > 1 {
				if proxified {
					return fmt.Errorf("--proxified is a single-URL stream; don't combine with batch scraping")
				}
				return runBatchScrape(client, cfg, urls, concurrency)
			}

			started := time.Now()

			// --proxified streams the raw upstream body (no JSON envelope).
			// Use this when piping to extract or another tool.
			if proxified {
				cfg.ProxifiedResponse = true
				resp, err := client.ScrapeProxified(cfg)
				if err != nil {
					return err
				}
				defer resp.Body.Close()
				sink := io.Writer(os.Stdout)
				if flags.outputPath != "" {
					f, err := os.Create(flags.outputPath)
					if err != nil {
						return err
					}
					defer f.Close()
					sink = f
				}
				_, err = io.Copy(sink, resp.Body)
				return err
			}

			result, err := client.Scrape(cfg)
			if err != nil {
				return err
			}
			elapsed := time.Since(started)
			if timing {
				fmt.Fprintf(os.Stderr, "scrape %s took %s\n", urls[0], elapsed.Round(time.Millisecond))
			}
			if contentOnly {
				_, err := os.Stdout.WriteString(result.Result.Content)
				return err
			}
			if flags.pretty {
				r := result.Result
				out.Pretty(os.Stdout, "%s %d bytes=%d format=%s cost=%d took=%s",
					r.Status, r.StatusCode, len(r.Content), r.Format, result.Context.Cost.Total,
					elapsed.Round(time.Millisecond))
				return nil
			}
			if flags.outputPath != "" {
				// For text-like formats, write just the content body. For binary/base64 blobs,
				// also decode if the server flagged it.
				content := result.Result.Content
				var data []byte = []byte(content)
				if result.Result.Format == "binary" {
					if decoded, decErr := base64.StdEncoding.DecodeString(content); decErr == nil {
						data = decoded
					}
				}
				if err := os.WriteFile(flags.outputPath, data, 0o644); err != nil {
					return err
				}
			}
			return out.WriteSuccess(os.Stdout, false, "scrape", result)
		},
	}

	cmd.Flags().StringVar(&method, "method", "GET", "HTTP method")
	cmd.Flags().StringArrayVar(&headerList, "header", nil, "request header key=value (repeatable)")
	cmd.Flags().StringArrayVar(&cookieList, "cookie", nil, "request cookie name=value (repeatable)")
	cmd.Flags().StringVar(&body, "body", "", "raw request body")
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "read request body from file")

	cmd.Flags().StringVar(&country, "country", "", "proxy country (ISO 3166-1 alpha-2, e.g. us, uk)")
	cmd.Flags().StringSliceVar(&lang, "lang", nil, "Accept-Language values (e.g. en-US,fr-FR)")
	cmd.Flags().StringVar(&osSpoof, "os", "", "spoof OS in user-agent")
	cmd.Flags().StringVar(&browserBrand, "browser-brand", "", "chrome|edge|brave|opera")
	cmd.Flags().StringVar(&geolocation, "geolocation", "", "lat,lng geolocation spoof")

	cmd.Flags().BoolVar(&renderJS, "render-js", false, "render page with headless browser")
	cmd.Flags().StringVar(&waitForSelector, "wait-for-selector", "", "CSS selector to wait for (requires --render-js)")
	cmd.Flags().IntVar(&renderingWait, "rendering-wait", 0, "extra wait (ms) after load (requires --render-js)")
	cmd.Flags().StringVar(&renderingStage, "rendering-stage", "", "complete|domcontentloaded")
	cmd.Flags().StringVar(&jsFile, "js-file", "", "file containing JS to execute in browser")
	cmd.Flags().StringVar(&jsScenarioFile, "js-scenario-file", "", "JSON file with JS scenario steps (array of action objects; see scrapfly.io/docs)")
	cmd.Flags().BoolVar(&autoScroll, "auto-scroll", false, "auto-scroll page to load lazy content")

	cmd.Flags().BoolVar(&asp, "asp", false, "enable Anti-Scraping Protection bypass")
	cmd.Flags().BoolVar(&cache, "cache", false, "enable response caching")
	cmd.Flags().IntVar(&cacheTTL, "cache-ttl", 0, "cache TTL seconds")
	cmd.Flags().BoolVar(&cacheClear, "cache-clear", false, "force cache refresh")

	cmd.Flags().StringVar(&session, "session", "", "persistent session id")
	cmd.Flags().BoolVar(&sessionStickyProxy, "session-sticky-proxy", false, "stick proxy across session")

	cmd.Flags().StringVarP(&format, "format", "f", "", "response format: raw|markdown|clean_html|text")
	cmd.Flags().StringSliceVar(&formatOptions, "format-option", nil, "format options (repeatable)")
	cmd.Flags().BoolVar(&screenshotFull, "screenshot", false, "also capture a fullpage screenshot (forces --render-js; returned in result.screenshots)")
	cmd.Flags().BoolVar(&onlyMainContent, "only-main-content", false, "alias: set --format markdown if no format given (strips boilerplate)")
	cmd.Flags().BoolVar(&timing, "timing", false, "print request duration to stderr")
	cmd.Flags().StringVar(&fromFile, "from-file", "", "read URLs from file (one per line; `-` for stdin)")
	cmd.Flags().IntVar(&concurrency, "concurrency", 5, "max concurrent scrapes in batch mode")
	cmd.Flags().StringVar(&webhook, "webhook", "", "webhook name to notify on completion")
	cmd.Flags().BoolVar(&retry, "retry", true, "retry on transient failures (default true; --retry=false to disable)")
	cmd.Flags().StringVar(&dataFile, "data-file", "", "JSON file for structured request body (cannot combine with --body)")
	cmd.Flags().StringVar(&extTemplateFile, "extraction-template-file", "", "JSON file with an ephemeral extraction template (inline rules)")
	cmd.Flags().StringArrayVar(&screenshotNamed, "screenshot-named", nil, "extra screenshot name=selector (repeatable); use fullpage as the selector for a full page capture")
	cmd.Flags().StringSliceVar(&screenshotFlags, "screenshot-flag", nil, "screenshot flag: load_images|dark_mode|block_banners|print_media_format|high_quality (repeatable)")
	cmd.Flags().BoolVar(&contentOnly, "content-only", false, "print the scraped body to stdout with no JSON envelope (Scrapfly-parsed; use --proxified for the upstream raw body)")

	cmd.Flags().StringVar(&extractionPrompt, "extraction-prompt", "", "AI extraction prompt")
	cmd.Flags().StringVar(&extractionModel, "extraction-model", "", "extraction model (product, article, ...)")
	cmd.Flags().StringVar(&extractionTemplate, "extraction-template", "", "saved extraction template name")

	cmd.Flags().StringVar(&proxyPool, "proxy-pool", "", "public_datacenter_pool|public_residential_pool")
	cmd.Flags().IntVar(&timeoutMs, "request-timeout", 0, "upstream request timeout (ms)")
	cmd.Flags().IntVar(&costBudget, "cost-budget", 0, "max credit cost for ASP retries")
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "request tag (repeatable)")
	cmd.Flags().BoolVar(&debug, "debug", false, "enable Scrapfly debug mode")
	cmd.Flags().BoolVar(&ssl, "ssl", false, "capture SSL details")
	cmd.Flags().BoolVar(&dns, "dns", false, "capture DNS details")
	cmd.Flags().StringVar(&correlationID, "correlation-id", "", "correlation id for tracking")
	cmd.Flags().BoolVar(&proxified, "proxified", false, "stream raw upstream body to stdout (or -o) with no JSON envelope — use for pipes")

	// Web Scraping API sub-actions that live under `scrape` / `scraper`:
	//   scrapfly scrape batch ...
	//   scrapfly scrape classify ...
	cmd.AddCommand(newBatchCmd(flags))
	cmd.AddCommand(newClassifyCmd(flags))

	return cmd
}

// readURLsFile reads URLs one-per-line from path, or stdin if path is "-".
// Blank lines and lines starting with # are ignored.
func readURLsFile(path string) ([]string, error) {
	var (
		r   io.Reader
		err error
	)
	if path == "-" {
		r = os.Stdin
	} else {
		f, oerr := os.Open(path)
		if oerr != nil {
			return nil, oerr
		}
		defer f.Close()
		r = f
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out, nil
}

// runBatchScrape fans out via SDK ConcurrentScrape and emits one JSON envelope
// per line (ndjson) in result order.
func runBatchScrape(client *scrapfly.Client, template *scrapfly.ScrapeConfig, urls []string, concurrency int) error {
	if concurrency <= 0 {
		concurrency = 5
	}
	configs := make([]*scrapfly.ScrapeConfig, len(urls))
	for i, u := range urls {
		c := *template
		c.URL = u
		configs[i] = &c
	}
	enc := json.NewEncoder(os.Stdout)
	for item := range client.ConcurrentScrape(configs, concurrency) {
		env := out.Envelope{Product: "scrape"}
		if item.Error != nil {
			env.Success = false
			env.Error = &out.EnvelopeError{Message: item.Error.Error()}
		} else {
			env.Success = true
			env.Data = item.Result
		}
		if err := enc.Encode(env); err != nil {
			return err
		}
	}
	return nil
}

func parseKeyVals(entries []string) (map[string]string, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		idx := strings.Index(e, "=")
		if idx <= 0 {
			return nil, fmt.Errorf("expected key=value, got %q", e)
		}
		out[e[:idx]] = e[idx+1:]
	}
	return out, nil
}
