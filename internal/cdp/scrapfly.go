package cdp

import (
	"context"
	"encoding/json"
	"fmt"
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
