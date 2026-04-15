package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	scrapfly "github.com/scrapfly/go-scrapfly"
	"github.com/spf13/cobra"
)

// newMcpCmd wires every core Scrapfly verb up as an MCP tool. Stdio-only for
// now (the most common integration path for Cursor / Claude Desktop /
// Claude Code).
func newMcpCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Expose Scrapfly as tools over the Model Context Protocol",
	}
	cmd.AddCommand(newMcpServeCmd(flags))
	return cmd
}

func newMcpServeCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run an MCP stdio server exposing Scrapfly tools (scrape, screenshot, extract, crawl, selector)",
		Long: `Exposes the Scrapfly product surface as MCP tools. Add this binary to
your MCP client (Claude Desktop, Cursor, Claude Code, etc.) as:

  command: scrapfly
  args:    ["mcp", "serve"]
  env:
    SCRAPFLY_API_KEY: scp-live-...

All existing flags still apply (e.g. --host for dev stacks, --pretty ignored
here). One MCP tool per Scrapfly verb; inputs mirror the CLI flags.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			server := mcpsdk.NewServer(&mcpsdk.Implementation{
				Name:    "scrapfly",
				Version: version,
			}, nil)

			registerScrapeTool(server, flags)
			registerScreenshotTool(server, flags)
			registerExtractTool(server, flags)
			registerCrawlRunTool(server, flags)
			registerSelectorTool(server, flags)

			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			return server.Run(ctx, &mcpsdk.StdioTransport{})
		},
	}
}

// ── Tool argument schemas ─────────────────────────────────────────────

type mcpScrapeArgs struct {
	URL              string `json:"url"                          jsonschema:"URL to scrape (http/https)"`
	RenderJS         bool   `json:"render_js,omitempty"          jsonschema:"render the page with a headless browser"`
	ASP              bool   `json:"asp,omitempty"                jsonschema:"enable anti-bot bypass"`
	Country          string `json:"country,omitempty"            jsonschema:"ISO country code for the proxy (e.g. us, fr)"`
	Format           string `json:"format,omitempty"             jsonschema:"response format: raw|markdown|clean_html|text"`
	ExtractionPrompt string `json:"extraction_prompt,omitempty"  jsonschema:"AI extraction prompt applied to the scraped content"`
	ExtractionModel  string `json:"extraction_model,omitempty"   jsonschema:"named extraction model (product, article, job_posting, ...)"`
	WaitForSelector  string `json:"wait_for_selector,omitempty"  jsonschema:"CSS selector to wait for before capturing (requires render_js)"`
}

type mcpScreenshotArgs struct {
	URL        string `json:"url"                   jsonschema:"URL to capture"`
	Format     string `json:"format,omitempty"      jsonschema:"png|jpg|webp|gif"`
	Capture    string `json:"capture,omitempty"     jsonschema:"fullpage or a CSS selector"`
	Resolution string `json:"resolution,omitempty"  jsonschema:"viewport, e.g. 1920x1080"`
	Country    string `json:"country,omitempty"     jsonschema:"proxy country ISO code"`
}

type mcpExtractArgs struct {
	URL         string `json:"url,omitempty"           jsonschema:"source URL (context only, not fetched)"`
	ContentType string `json:"content_type"            jsonschema:"MIME, e.g. text/html"`
	Body        string `json:"body"                    jsonschema:"the document to extract from (pass raw text/html)"`
	Prompt      string `json:"prompt,omitempty"        jsonschema:"AI extraction prompt"`
	Model       string `json:"model,omitempty"         jsonschema:"named extraction model"`
	Template    string `json:"template,omitempty"      jsonschema:"saved extraction template"`
}

type mcpCrawlArgs struct {
	URL           string   `json:"url"                         jsonschema:"seed URL"`
	MaxPages      int      `json:"max_pages,omitempty"         jsonschema:"page limit (0 = server default)"`
	MaxDepth      int      `json:"max_depth,omitempty"         jsonschema:"link-depth limit"`
	ContentFormat []string `json:"content_formats,omitempty"   jsonschema:"content formats: markdown|html|clean_html|text|json|extracted_data|page_metadata"`
	ASP           bool     `json:"asp,omitempty"               jsonschema:"enable anti-bot bypass on crawled pages"`
	Country       string   `json:"country,omitempty"           jsonschema:"ISO country code"`
	MaxWaitS      int      `json:"max_wait_seconds,omitempty"  jsonschema:"max seconds to poll for completion (default 900)"`
}

type mcpSelectorArgs struct {
	Description string `json:"description"           jsonschema:"natural-language description of the target element"`
	HTML        string `json:"html,omitempty"        jsonschema:"HTML to search — one of html or url is required"`
	URL         string `json:"url,omitempty"         jsonschema:"URL to scrape + search"`
	WantText    string `json:"want_text,omitempty"   jsonschema:"optional substring that must be in the match's text"`
	Attempts    int    `json:"attempts,omitempty"    jsonschema:"max LLM retries (default 4)"`
}

// ── Tool registrations ─────────────────────────────────────────────────

func registerScrapeTool(server *mcpsdk.Server, flags *rootFlags) {
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "scrape",
		Description: "Scrape a URL through the Scrapfly Web Scraping API. Returns the scrape envelope (status, content, cost, etc).",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, a mcpScrapeArgs) (*mcpsdk.CallToolResult, any, error) {
		client, err := buildClient(flags)
		if err != nil {
			return nil, nil, err
		}
		cfg := &scrapfly.ScrapeConfig{
			URL:              a.URL,
			RenderJS:         a.RenderJS,
			ASP:              a.ASP,
			Country:          a.Country,
			ExtractionPrompt: a.ExtractionPrompt,
			ExtractionModel:  scrapfly.ExtractionModel(a.ExtractionModel),
			WaitForSelector:  a.WaitForSelector,
			Retry:            true,
		}
		if a.Format != "" {
			cfg.Format = scrapfly.Format(a.Format)
		}
		res, err := client.Scrape(cfg)
		if err != nil {
			return nil, nil, err
		}
		return textResult(res), nil, nil
	})
}

func registerScreenshotTool(server *mcpsdk.Server, flags *rootFlags) {
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "screenshot",
		Description: "Capture a screenshot via Scrapfly Screenshot API. Returns the PNG as inline image content.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, a mcpScreenshotArgs) (*mcpsdk.CallToolResult, any, error) {
		client, err := buildClient(flags)
		if err != nil {
			return nil, nil, err
		}
		cfg := &scrapfly.ScreenshotConfig{
			URL:        a.URL,
			Format:     scrapfly.ScreenshotFormat(a.Format),
			Capture:    a.Capture,
			Resolution: a.Resolution,
			Country:    a.Country,
		}
		res, err := client.Screenshot(cfg)
		if err != nil {
			return nil, nil, err
		}
		mime := "image/png"
		switch res.Metadata.ExtensionName {
		case "jpg", "jpeg":
			mime = "image/jpeg"
		case "webp":
			mime = "image/webp"
		case "gif":
			mime = "image/gif"
		}
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{
				&mcpsdk.ImageContent{Data: res.Image, MIMEType: mime},
			},
		}, nil, nil
	})
}

func registerExtractTool(server *mcpsdk.Server, flags *rootFlags) {
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "extract",
		Description: "Run AI/template extraction on a document the caller already has. Returns the extracted structured data.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, a mcpExtractArgs) (*mcpsdk.CallToolResult, any, error) {
		client, err := buildClient(flags)
		if err != nil {
			return nil, nil, err
		}
		cfg := &scrapfly.ExtractionConfig{
			Body:               []byte(a.Body),
			ContentType:        a.ContentType,
			URL:                a.URL,
			ExtractionPrompt:   a.Prompt,
			ExtractionModel:    scrapfly.ExtractionModel(a.Model),
			ExtractionTemplate: a.Template,
		}
		res, err := client.Extract(cfg)
		if err != nil {
			return nil, nil, err
		}
		return textResult(res), nil, nil
	})
}

func registerCrawlRunTool(server *mcpsdk.Server, flags *rootFlags) {
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "crawl_run",
		Description: "Start a crawl and block until it terminates. Returns the final status + discovered URL count.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, a mcpCrawlArgs) (*mcpsdk.CallToolResult, any, error) {
		client, err := buildClient(flags)
		if err != nil {
			return nil, nil, err
		}
		cfg := &scrapfly.CrawlerConfig{
			URL:       a.URL,
			PageLimit: a.MaxPages,
			MaxDepth:  a.MaxDepth,
			ASP:       a.ASP,
			Country:   a.Country,
		}
		for _, f := range a.ContentFormat {
			cfg.ContentFormats = append(cfg.ContentFormats, scrapfly.CrawlerContentFormat(f))
		}
		start, err := client.StartCrawl(cfg)
		if err != nil {
			return nil, nil, err
		}
		wait := time.Duration(a.MaxWaitS) * time.Second
		if wait == 0 {
			wait = 15 * time.Minute
		}
		deadline := time.Now().Add(wait)
		var last *scrapfly.CrawlerStatus
		for {
			st, err := client.CrawlStatus(start.CrawlerUUID)
			if err != nil {
				return nil, nil, err
			}
			last = st
			if st.IsComplete() || st.IsFailed() || st.IsCancelled() {
				break
			}
			if time.Now().After(deadline) {
				return nil, nil, fmt.Errorf("crawl %s did not finish within %s", start.CrawlerUUID, wait)
			}
			time.Sleep(5 * time.Second)
		}
		return textResult(map[string]any{"uuid": start.CrawlerUUID, "status": last}), nil, nil
	})
}

func registerSelectorTool(server *mcpsdk.Server, flags *rootFlags) {
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "selector",
		Description: "Find a robust CSS selector for a described element in HTML. Either pass the HTML inline or a URL to scrape first.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, a mcpSelectorArgs) (*mcpsdk.CallToolResult, any, error) {
		if a.HTML == "" && a.URL == "" {
			return nil, nil, fmt.Errorf("need html or url")
		}
		html := a.HTML
		if html == "" {
			client, err := buildClient(flags)
			if err != nil {
				return nil, nil, err
			}
			res, err := client.Scrape(&scrapfly.ScrapeConfig{URL: a.URL, RenderJS: true, Retry: true})
			if err != nil {
				return nil, nil, err
			}
			html = res.Result.Content
		}
		attempts := a.Attempts
		if attempts == 0 {
			attempts = 4
		}
		result, err := runSelectorFinder(ctx, flags, a.Description, html, a.WantText, attempts, "", "")
		if err != nil {
			return nil, nil, err
		}
		return textResult(result), nil, nil
	})
}

// ── Helpers ────────────────────────────────────────────────────────────

func textResult(v any) *mcpsdk.CallToolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		b = []byte(fmt.Sprintf("%v", v))
	}
	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: string(b)}},
	}
}
