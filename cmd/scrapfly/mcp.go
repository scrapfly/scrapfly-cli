package main

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	scrapfly "github.com/scrapfly/go-scrapfly"
	"github.com/spf13/cobra"
)

// mustBoolPinnedSchema infers the tool input schema and narrows the named
// properties back to a plain "boolean".
//
// The tri-state names are *bool so an absent key stays distinguishable from an
// explicit false, and jsonschema-go renders a pointer as type
// ["null","boolean"]. `asp` is frozen and was declared "boolean" before the
// rename; a widened type on a frozen property is what downstream schema
// translators trip over (this codebase already carries a workaround for one
// rejecting a generated construct), so declare what callers may actually send.
func mustBoolPinnedSchema[T any](properties ...string) *jsonschema.Schema {
	schema, err := jsonschema.For[T](nil)
	if err != nil {
		// Reflection over a literal struct type; a failure here is a
		// build-time mistake, not a runtime condition.
		panic(err)
	}
	for _, name := range properties {
		property, ok := schema.Properties[name]
		if !ok {
			panic("mcp: schema has no property " + name)
		}
		property.Types = nil
		property.Type = "boolean"
	}
	return schema
}

// mcpProxyPoolHint closes the scrape and crawl_run descriptions. It describes
// the pools and the one refusal that names a pool, and recommends none: a call
// without proxy_pool is sent as the releases before it sent it.
const mcpProxyPoolHint = "proxy_pool picks the proxy pool: basic (datacenter proxies, Unblocker off), unblocker (residential proxies with the Unblocker), unblocker_ultra (residential proxies with the Unblocker, never refused: the pool the targets and anti-bot protections priced on their own require), auto (the API picks the pool before the request, from the target's profile: the Unblocker on datacenter proxies, or the pool the target needs; accepted on every target, the ones that require unblocker_ultra included, where their price is added; priced as the pool it runs on) or tor, also named public_tor_pool as results name it (the Tor network, the only pool that reaches .onion addresses). On ERR::PROXY::UNBLOCKER_ULTRA_REQUIRED, send proxy_pool=unblocker_ultra. The unblocker and asp arguments are deprecated: use proxy_pool (unblocker_ultra for the Unblocker)."

// mcpProxyPools are the proxy_pool values the scrape and crawl_run schemas
// publish: the four pool words, and the Tor pool under both of its names, tor
// and public_tor_pool, the one results give it. mcpLegacyProxyPools, the pool
// names written before the proxy pools, are accepted from a caller that sends
// one and named nowhere, validation errors included: the API still runs them.
var (
	mcpProxyPools       = []any{proxyPoolBasic, proxyPoolUnblocker, proxyPoolUnblockerUltra, proxyPoolAuto, proxyPoolTor, proxyPoolPublicTor}
	mcpLegacyProxyPools = []string{"public_datacenter_pool", "public_residential_pool"}
)

// mustScrapingToolSchema pins asp and unblocker to "boolean" and closes
// proxy_pool on mcpProxyPools. No schema default on proxy_pool: a default
// would reach every call that omits it and change what a call naming no pool,
// or only asp or unblocker, sends.
func mustScrapingToolSchema[T any]() *jsonschema.Schema {
	schema := mustBoolPinnedSchema[T]("asp", "unblocker")
	property, ok := schema.Properties["proxy_pool"]
	if !ok {
		panic("mcp: schema has no property proxy_pool")
	}
	property.Enum = slices.Clone(mcpProxyPools)
	return schema
}

// addScrapingTool registers scrape or crawl_run. mcpsdk.AddTool validates a
// call against the schema it publishes, which refuses a legacy pool name;
// this validates against that same schema, as mcpsdk.AddTool does (it declares
// no default to apply), with a legacy pool name set aside. Not a second schema
// listing the legacy names: its enum error would print them to the caller.
func addScrapingTool[In any](server *mcpsdk.Server, tool *mcpsdk.Tool, handler mcpsdk.ToolHandlerFor[In, any]) {
	schema := mustScrapingToolSchema[In]()
	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
	if err != nil {
		// Built from a literal struct type; a failure here is a build-time
		// mistake, not a runtime condition.
		panic(err)
	}
	tool.InputSchema = schema
	server.AddTool(tool, func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		res, err := callScrapingTool(ctx, req, resolved, handler)
		if err != nil {
			// A tool error, as mcpsdk.AddTool reports one: the client reads
			// it, the session goes on.
			res = &mcpsdk.CallToolResult{}
			res.SetError(err)
		}
		if res == nil {
			res = &mcpsdk.CallToolResult{}
		}
		return res, nil
	})
}

func callScrapingTool[In any](ctx context.Context, req *mcpsdk.CallToolRequest, published *jsonschema.Resolved, handler mcpsdk.ToolHandlerFor[In, any]) (*mcpsdk.CallToolResult, error) {
	args := map[string]any{}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return nil, fmt.Errorf("unmarshaling arguments: %w", err)
		}
	}
	checked := args
	if pool, ok := args["proxy_pool"].(string); ok && slices.Contains(mcpLegacyProxyPools, pool) {
		checked = maps.Clone(args)
		delete(checked, "proxy_pool")
	}
	if err := published.Validate(&checked); err != nil {
		return nil, fmt.Errorf("validating \"arguments\": %w", err)
	}
	// Decoded from the validated object: the closed schema has already
	// refused every key that is not one of In's exact names.
	validated, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	var in In
	if err := json.Unmarshal(validated, &in); err != nil {
		return nil, err
	}
	res, _, err := handler(ctx, req, in)
	return res, err
}

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
here). One MCP tool per Scrapfly verb. Tool inputs follow the API, not the
CLI flags: proxy_pool picks basic, unblocker, unblocker_ultra, auto, tor or
public_tor_pool.
The unblocker and asp inputs are not the CLI's --unblocker and --asp flags:
they are deprecated, use proxy_pool (unblocker_ultra for the Unblocker). The
migration guide covers requests written before the proxy pools:
https://scrapfly.io/new-pricing#legacy-spellings`,
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

// Tool input schemas are inferred from these structs and are closed
// (additionalProperties: false), so "asp" and "unblocker" both stay declared
// permanently: dropping either one turns a pinned older client's call into a
// hard schema rejection. Both are pointers so an absent key is distinguishable
// from an explicit false; see resolveUnblocker for the precedence, and
// resolveProxyPool for how they combine with proxy_pool.
type mcpScrapeArgs struct {
	URL              string `json:"url"                          jsonschema:"URL to scrape (http/https)"`
	RenderJS         bool   `json:"render_js,omitempty"          jsonschema:"render the page with a headless browser"`
	ProxyPool        string `json:"proxy_pool,omitempty"         jsonschema:"proxy pool: basic (datacenter proxies, Unblocker off), unblocker (residential proxies with the Unblocker), unblocker_ultra (residential proxies with the Unblocker, never refused: send it on ERR::PROXY::UNBLOCKER_ULTRA_REQUIRED), auto (the API picks the pool from the target's profile, never refused) or tor / public_tor_pool (the Tor network, for .onion addresses)"`
	Unblocker        *bool  `json:"unblocker,omitempty"          jsonschema:"Deprecated: use proxy_pool (unblocker_ultra for the Unblocker)."`
	ASP              *bool  `json:"asp,omitempty"                jsonschema:"Deprecated: use proxy_pool (unblocker_ultra for the Unblocker)."`
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
	ProxyPool     string   `json:"proxy_pool,omitempty"        jsonschema:"proxy pool for crawled pages: basic, unblocker, unblocker_ultra, auto, tor or public_tor_pool"`
	Unblocker     *bool    `json:"unblocker,omitempty"         jsonschema:"Deprecated: use proxy_pool (unblocker_ultra for the Unblocker)."`
	ASP           *bool    `json:"asp,omitempty"               jsonschema:"Deprecated: use proxy_pool (unblocker_ultra for the Unblocker)."`
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
	addScrapingTool(server, &mcpsdk.Tool{
		Name:        "scrape",
		Description: "Scrape a URL through the Scrapfly Web Scraping API. Returns the scrape envelope (status, content, cost, etc). " + mcpProxyPoolHint,
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, a mcpScrapeArgs) (*mcpsdk.CallToolResult, any, error) {
		client, err := buildClient(flags)
		if err != nil {
			return nil, nil, err
		}
		pool, asp := resolveProxyPool(a.ProxyPool, a.ASP, a.Unblocker)
		cfg := &scrapfly.ScrapeConfig{
			URL:              a.URL,
			RenderJS:         a.RenderJS,
			ProxyPool:        scrapfly.ProxyPool(pool),
			ASP:              asp, // wire key stays "asp"
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
	addScrapingTool(server, &mcpsdk.Tool{
		Name:        "crawl_run",
		Description: "Start a crawl and block until it terminates. Returns the final status + discovered URL count. " + mcpProxyPoolHint,
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, a mcpCrawlArgs) (*mcpsdk.CallToolResult, any, error) {
		client, err := buildClient(flags)
		if err != nil {
			return nil, nil, err
		}
		pool, asp := resolveProxyPool(a.ProxyPool, a.ASP, a.Unblocker)
		cfg := &scrapfly.CrawlerConfig{
			URL:       a.URL,
			PageLimit: a.MaxPages,
			MaxDepth:  a.MaxDepth,
			ProxyPool: pool,
			ASP:       asp, // wire key stays "asp"
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
