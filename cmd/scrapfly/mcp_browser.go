package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/scrapfly/scrapfly-cli/internal/cdp"
)

// mcpBrowserState holds a lazy-initialized CDP session for the lifetime of
// the MCP server process. The first browser_* tool call dials the CDP URL
// and attaches; subsequent calls reuse it. Cleaned up when the MCP server
// exits.
type mcpBrowserState struct {
	mu      sync.Mutex
	flags   *rootFlags
	client  *cdp.Client
	session *cdp.Session
}

func (s *mcpBrowserState) ensure(ctx context.Context) (*cdp.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != nil {
		return s.session, nil
	}
	sfClient, err := buildClient(s.flags)
	if err != nil {
		return nil, err
	}
	wsURL := sfClient.CloudBrowser(nil)
	c, err := cdp.Dial(ctx, wsURL)
	if err != nil {
		return nil, fmt.Errorf("cdp dial: %w", err)
	}
	sess, err := cdp.Attach(ctx, c)
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("cdp attach: %w", err)
	}
	s.client = c
	s.session = sess
	return sess, nil
}

func (s *mcpBrowserState) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != nil {
		_ = s.session.Detach(context.Background())
	}
	if s.client != nil {
		_ = s.client.Close()
	}
	s.session = nil
	s.client = nil
}

// registerBrowserTools adds the browser action tools to the MCP server.
// The session is lazy-started on first call and shared across all tools.
func registerBrowserTools(server *mcpsdk.Server, flags *rootFlags) *mcpBrowserState {
	state := &mcpBrowserState{flags: flags}

	// browser_navigate
	type navArgs struct {
		URL string `json:"url" jsonschema:"URL to navigate to"`
	}
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "browser_navigate",
		Description: "Navigate the browser to a URL and wait for load. Auto-starts a browser session if none is active.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, a navArgs) (*mcpsdk.CallToolResult, any, error) {
		sess, err := state.ensure(ctx)
		if err != nil {
			return nil, nil, err
		}
		res, err := sess.Open(ctx, a.URL)
		if err != nil {
			return nil, nil, err
		}
		return textResult(res), nil, nil
	})

	// browser_snapshot
	type snapArgs struct{}
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "browser_snapshot",
		Description: "Capture the accessibility tree of the current page. Returns flat list of {ref, role, name, value, children}. Use refs in click/fill.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, a snapArgs) (*mcpsdk.CallToolResult, any, error) {
		sess, err := state.ensure(ctx)
		if err != nil {
			return nil, nil, err
		}
		nodes, err := sess.Snapshot(ctx)
		if err != nil {
			return nil, nil, err
		}
		return textResult(map[string]any{"nodes": nodes}), nil, nil
	})

	// browser_click
	type clickArgs struct {
		Locator string `json:"locator" jsonschema:"AXTree ref (e3) or CSS selector (button[type=submit])"`
	}
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "browser_click",
		Description: "Click an element by ref or CSS selector.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, a clickArgs) (*mcpsdk.CallToolResult, any, error) {
		sess, err := state.ensure(ctx)
		if err != nil {
			return nil, nil, err
		}
		res, err := sess.Click(ctx, a.Locator)
		if err != nil {
			return nil, nil, err
		}
		return textResult(res), nil, nil
	})

	// browser_fill
	type fillArgs struct {
		Locator string `json:"locator" jsonschema:"AXTree ref (e3) or CSS selector (input[name=q])"`
		Value   string `json:"value"   jsonschema:"text to type into the element"`
	}
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "browser_fill",
		Description: "Focus an element and type text. Uses Antibot.fill for human-like timing on CSS selectors.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, a fillArgs) (*mcpsdk.CallToolResult, any, error) {
		sess, err := state.ensure(ctx)
		if err != nil {
			return nil, nil, err
		}
		res, err := sess.Fill(ctx, a.Locator, a.Value)
		if err != nil {
			return nil, nil, err
		}
		return textResult(res), nil, nil
	})

	// browser_content
	type contentArgs struct {
		Raw bool `json:"raw,omitempty" jsonschema:"if true, return the full HTML body instead of metadata-only summary"`
	}
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "browser_content",
		Description: "Get the current page content. Default: metadata only (URL, status, headers, size). Set raw=true for the full HTML body.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, a contentArgs) (*mcpsdk.CallToolResult, any, error) {
		sess, err := state.ensure(ctx)
		if err != nil {
			return nil, nil, err
		}
		content, contentType, err := sess.RenderedContent(ctx, true)
		if err != nil {
			return nil, nil, err
		}
		if a.Raw {
			return textResult(map[string]any{"content": content, "type": contentType}), nil, nil
		}
		// Metadata-only: nav response + content size.
		var meta map[string]any
		if entries, err := sess.NavigationResponse(ctx); err == nil && len(entries) > 0 {
			last := entries[len(entries)-1]
			meta = map[string]any{
				"url":     last["url"],
				"status":  last["statusCode"],
				"headers": last["headers"],
				"bytes":   len(content),
				"type":    contentType,
			}
		} else {
			meta = map[string]any{"bytes": len(content), "type": contentType}
		}
		return textResult(meta), nil, nil
	})

	// browser_eval
	type evalArgs struct {
		JS string `json:"js" jsonschema:"JavaScript expression to evaluate (awaitPromise supported)"`
	}
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "browser_eval",
		Description: "Evaluate a JS expression in the page and return the result.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, a evalArgs) (*mcpsdk.CallToolResult, any, error) {
		sess, err := state.ensure(ctx)
		if err != nil {
			return nil, nil, err
		}
		v, err := sess.Eval(ctx, a.JS)
		if err != nil {
			return nil, nil, err
		}
		return textResult(map[string]any{"value": v}), nil, nil
	})

	// browser_screenshot
	type shotArgs struct {
		FullPage bool `json:"fullpage,omitempty" jsonschema:"capture beyond viewport"`
	}
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "browser_screenshot",
		Description: "Take a PNG screenshot of the current viewport (or fullpage). Returns an inline image.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, a shotArgs) (*mcpsdk.CallToolResult, any, error) {
		sess, err := state.ensure(ctx)
		if err != nil {
			return nil, nil, err
		}
		png, err := sess.Screenshot(ctx, a.FullPage)
		if err != nil {
			return nil, nil, err
		}
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.ImageContent{Data: png, MIMEType: "image/png"}},
		}, nil, nil
	})

	// browser_close
	type closeArgs struct{}
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "browser_close",
		Description: "Close the browser session and release resources.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, a closeArgs) (*mcpsdk.CallToolResult, any, error) {
		state.close()
		return textResult(map[string]string{"status": "closed"}), nil, nil
	})

	return state
}

// textResultBrowser is the same as textResult but avoids a redeclaration.
func textResultBrowser(v any) *mcpsdk.CallToolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		b = []byte(fmt.Sprintf("%v", v))
	}
	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: string(b)}},
	}
}
