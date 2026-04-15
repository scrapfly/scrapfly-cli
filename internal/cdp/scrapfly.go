package cdp

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// This file wires the Scrapfly-specific Chromium domains (Antibot + Page
// extensions) into the session. These calls only succeed against a Scrapfly
// Browser — on stock Chromium they'll return method-not-found.
//
// Domains:
//   - Antibot: human-like input (fill / clickOn / clickAndSlide / typeText /
//     scroll / pressKey / selectOption / hover), element waiting
//     (waitForElement), and AX locators.
//   - Page.getRenderedContent: full rendered HTML with iframes inlined —
//     the correct "get page content" primitive for agents.

// Selector is Antibot's element locator shape: {type, query}.
type Selector struct {
	Type  string `json:"type"` // "css" | "xpath" | "axNodeId" | "coord"
	Query string `json:"query"`
}

// CSSSelector returns a Selector for a CSS query.
func CSSSelector(q string) Selector { return Selector{Type: "css", Query: q} }

// XPathSelector returns a Selector for an XPath query.
func XPathSelector(q string) Selector { return Selector{Type: "xpath", Query: q} }

// FillAntibot types into the matched element with human-like WPM timing via
// the Scrapfly Antibot domain. Clears first when clear=true.
func (s *Session) FillAntibot(ctx context.Context, sel Selector, text string, clear bool, wpm float64) (map[string]any, error) {
	params := map[string]any{
		"selector": sel,
		"text":     text,
	}
	if clear {
		params["clear"] = true
	}
	if wpm > 0 {
		params["wpm"] = wpm
	}
	res, err := s.Call(ctx, "Antibot.fill", params)
	if err != nil {
		return nil, err
	}
	return parseAntibotResult(res, sel)
}

// ClickAntibot performs a human-like click on the matched element.
func (s *Session) ClickAntibot(ctx context.Context, sel Selector, button string, clickCount int) (map[string]any, error) {
	params := map[string]any{"selector": sel}
	if button != "" {
		params["button"] = button
	}
	if clickCount > 0 {
		params["clickCount"] = clickCount
	}
	res, err := s.Call(ctx, "Antibot.clickOn", params)
	if err != nil {
		return nil, err
	}
	return parseAntibotResult(res, sel)
}

// ClickAndSlide runs the "slider captcha" primitive: press-and-hold on the
// source element, slide to target, release. One of {Distance, Target} must
// be set.
type SlideOptions struct {
	Target         *Selector
	Distance       float64 // px, used if Target is nil
	VerticalOffset float64
	Button         string
	Overshoot      bool
}

func (s *Session) ClickAndSlide(ctx context.Context, source Selector, opts SlideOptions) (map[string]any, error) {
	params := map[string]any{"selector": source}
	if opts.Target != nil {
		params["target"] = *opts.Target
	}
	if opts.Distance != 0 {
		params["distance"] = opts.Distance
	}
	if opts.VerticalOffset != 0 {
		params["verticalOffset"] = opts.VerticalOffset
	}
	if opts.Button != "" {
		params["button"] = opts.Button
	}
	if opts.Overshoot {
		params["overshoot"] = true
	}
	res, err := s.Call(ctx, "Antibot.clickAndSlide", params)
	if err != nil {
		return nil, err
	}
	return parseAntibotResult(res, source)
}

// WaitForElement polls natively (no JS) until the selector resolves.
// timeout defaults to 10s server-side. visible adds display/opacity check.
func (s *Session) WaitForElement(ctx context.Context, sel Selector, timeoutMs int, visible bool) (map[string]any, error) {
	params := map[string]any{"selector": sel}
	if timeoutMs > 0 {
		params["timeout"] = timeoutMs
	}
	if visible {
		params["visible"] = true
	}
	res, err := s.Call(ctx, "Antibot.waitForElement", params)
	if err != nil {
		return nil, err
	}
	return parseAntibotResult(res, sel)
}

// RenderedContent returns the fully rendered page content — HTML with iframes
// inlined for text types; base64 bytes for binaries. Default is renderIframe=true.
func (s *Session) RenderedContent(ctx context.Context, renderIframes bool) (content, contentType string, err error) {
	params := map[string]any{"renderIframe": renderIframes}
	res, err := s.Call(ctx, "Page.getRenderedContent", params)
	if err != nil {
		return "", "", err
	}
	var r struct {
		Content string `json:"content"`
		Type    string `json:"type"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return "", "", err
	}
	return r.Content, r.Type, nil
}

// NavigationResponse returns the redirect chain + final response for the
// current page via Page.getNavigationResponse. Each entry has url, statusCode,
// headers. Only works on Scrapfly's custom browser.
func (s *Session) NavigationResponse(ctx context.Context) ([]map[string]any, error) {
	res, err := s.Call(ctx, "Page.getNavigationResponse", map[string]any{})
	if err != nil {
		return nil, err
	}
	var r struct {
		Entries []map[string]any `json:"entries"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return nil, fmt.Errorf("parse NavigationResponse: %w", err)
	}
	return r.Entries, nil
}

// MatchedRequests walks the buffered Network.responseReceived events, matches
// each request URL against a glob pattern (supports * and ?), and optionally
// fetches the response body via Network.getResponseBody. Returns a list of
// matched request/response pairs.
func (s *Session) MatchedRequests(ctx context.Context, pattern string, includeBody bool) ([]map[string]any, error) {
	re, err := globToRegex(pattern)
	if err != nil {
		return nil, fmt.Errorf("bad pattern %q: %w", pattern, err)
	}

	s.Client.eventsMu.Lock()
	events := make([]*Message, len(s.Client.events))
	copy(events, s.Client.events)
	s.Client.eventsMu.Unlock()

	var results []map[string]any
	seen := map[string]bool{}
	for _, ev := range events {
		if ev.Method != "Network.responseReceived" {
			continue
		}
		var p struct {
			RequestID string `json:"requestId"`
			Response  struct {
				URL        string         `json:"url"`
				Status     int            `json:"status"`
				StatusText string         `json:"statusText"`
				Headers    map[string]any `json:"headers"`
				MIMEType   string         `json:"mimeType"`
			} `json:"response"`
			Type string `json:"type"`
		}
		if err := json.Unmarshal(ev.Params, &p); err != nil {
			continue
		}
		if seen[p.RequestID] || !re.MatchString(p.Response.URL) {
			continue
		}
		seen[p.RequestID] = true

		entry := map[string]any{
			"url":         p.Response.URL,
			"status":      p.Response.Status,
			"status_text": p.Response.StatusText,
			"mime_type":   p.Response.MIMEType,
			"type":        p.Type,
		}
		if includeBody {
			bodyRes, err := s.Call(ctx, "Network.getResponseBody", map[string]any{"requestId": p.RequestID})
			if err == nil {
				var b struct {
					Body          string `json:"body"`
					Base64Encoded bool   `json:"base64Encoded"`
				}
				_ = json.Unmarshal(bodyRes, &b)
				entry["body"] = b.Body
				entry["base64"] = b.Base64Encoded
			}
		}
		results = append(results, entry)
	}
	return results, nil
}

// globToRegex converts a URL glob pattern (with * and ?) to a compiled
// regular expression. `*` matches any run of non-empty characters; `**` is
// treated the same as `*` since we're matching flat URL strings, not paths.
func globToRegex(pattern string) (*regexp.Regexp, error) {
	var buf strings.Builder
	buf.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			buf.WriteString(".*")
		case '?':
			buf.WriteString(".")
		case '.', '+', '(', ')', '[', ']', '{', '}', '^', '$', '|', '\\':
			buf.WriteByte('\\')
			buf.WriteByte(pattern[i])
		default:
			buf.WriteByte(pattern[i])
		}
	}
	buf.WriteString("$")
	return regexp.Compile(buf.String())
}

// ── ScrapiumBrowser download domain ─────────────────────────────────

// HasDownloads checks if any files have been downloaded in this session.
func (s *Session) HasDownloads(ctx context.Context) (bool, error) {
	res, err := s.Call(ctx, "ScrapiumBrowser.hasDownloads", map[string]any{})
	if err != nil {
		return false, err
	}
	var r struct {
		Result bool `json:"result"`
	}
	_ = json.Unmarshal(res, &r)
	return r.Result, nil
}

// GetDownloadsMetadata returns {filename: sizeBytes} for all downloaded files.
func (s *Session) GetDownloadsMetadata(ctx context.Context) (map[string]int64, error) {
	res, err := s.Call(ctx, "ScrapiumBrowser.getDownloadsMetadatas", map[string]any{})
	if err != nil {
		return nil, err
	}
	var r struct {
		Metadata map[string]int64 `json:"metadata"`
	}
	_ = json.Unmarshal(res, &r)
	return r.Metadata, nil
}

// GetDownload fetches a single file by name as base64.
func (s *Session) GetDownload(ctx context.Context, filename string) (string, error) {
	res, err := s.Call(ctx, "ScrapiumBrowser.getDownload", map[string]any{"filename": filename})
	if err != nil {
		return "", err
	}
	var r struct {
		Data string `json:"data"`
	}
	_ = json.Unmarshal(res, &r)
	return r.Data, nil
}

// GetDownloads fetches all downloaded files as {filename: base64Content}.
// If delete is true, files are removed from disk after reading.
func (s *Session) GetDownloads(ctx context.Context, deleteAfter bool) (map[string]string, error) {
	res, err := s.Call(ctx, "ScrapiumBrowser.getDownloads", map[string]any{"delete": deleteAfter})
	if err != nil {
		return nil, err
	}
	var r struct {
		Files map[string]string `json:"files"`
	}
	_ = json.Unmarshal(res, &r)
	return r.Files, nil
}

func parseAntibotResult(raw json.RawMessage, sel Selector) (map[string]any, error) {
	var r struct {
		Success      bool     `json:"success"`
		ErrorMessage string   `json:"errorMessage"`
		PointX       *float64 `json:"pointX,omitempty"`
		PointY       *float64 `json:"pointY,omitempty"`
		BoundsX      *float64 `json:"boundsX,omitempty"`
		BoundsY      *float64 `json:"boundsY,omitempty"`
		BoundsWidth  *float64 `json:"boundsWidth,omitempty"`
		BoundsHeight *float64 `json:"boundsHeight,omitempty"`
		FrameID      string   `json:"frameId,omitempty"`
		AtBottom     *bool    `json:"atBottom,omitempty"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	if !r.Success {
		msg := r.ErrorMessage
		if msg == "" {
			msg = "antibot action failed"
		}
		return nil, fmt.Errorf("%s (%s: %s)", msg, sel.Type, sel.Query)
	}
	out := map[string]any{
		"selector": map[string]string{"type": sel.Type, "query": sel.Query},
	}
	if r.PointX != nil {
		out["x"] = *r.PointX
	}
	if r.PointY != nil {
		out["y"] = *r.PointY
	}
	if r.FrameID != "" {
		out["frame_id"] = r.FrameID
	}
	if r.AtBottom != nil {
		out["at_bottom"] = *r.AtBottom
	}
	return out, nil
}
