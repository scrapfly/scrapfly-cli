package cdp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Open navigates the attached page to url and waits for the load event.
func (s *Session) Open(ctx context.Context, url string) (map[string]any, error) {
	res, err := s.Call(ctx, "Page.navigate", map[string]any{"url": url})
	if err != nil {
		return nil, err
	}
	var nav struct {
		FrameID   string `json:"frameId"`
		LoaderID  string `json:"loaderId"`
		ErrorText string `json:"errorText,omitempty"`
	}
	_ = json.Unmarshal(res, &nav)
	if nav.ErrorText != "" {
		return nil, fmt.Errorf("navigate: %s", nav.ErrorText)
	}

	// Best-effort wait for load — ignore timeout so actions can still proceed
	// on slow pages.
	loadCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, _ = s.Client.WaitEvent(loadCtx, "Page.loadEventFired", nil)

	return map[string]any{"url": url, "frame_id": nav.FrameID, "loader_id": nav.LoaderID}, nil
}

// isRefLocator is true for locators shaped like "e1", "e42" — our AXTree refs.
func isRefLocator(s string) bool {
	if len(s) < 2 || s[0] != 'e' {
		return false
	}
	for _, c := range s[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// boxFor resolves a locator (AXTree ref "e3" or CSS selector) to an on-screen
// center point. CSS selectors are evaluated via Runtime.evaluate on
// document.querySelector.
func (s *Session) boxFor(ctx context.Context, locator string) (cx, cy float64, err error) {
	if isRefLocator(locator) {
		return s.boxForRef(ctx, locator)
	}
	// Selector path: evaluate getBoundingClientRect on the first match.
	res, err := s.Call(ctx, "Runtime.evaluate", map[string]any{
		"expression": fmt.Sprintf(
			`(()=>{const el=document.querySelector(%s);if(!el)throw new Error('no element matches: '+%s);const r=el.getBoundingClientRect();if(r.width===0&&r.height===0)throw new Error('zero-sized element: '+%s);return {x:r.x+r.width/2,y:r.y+r.height/2};})()`,
			jsString(locator), jsString(locator), jsString(locator),
		),
		"returnByValue": true,
		"awaitPromise":  true,
	})
	if err != nil {
		return 0, 0, err
	}
	var r struct {
		Result struct {
			Value struct {
				X, Y float64
			} `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
			Text string `json:"text"`
		} `json:"exceptionDetails,omitempty"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return 0, 0, err
	}
	if r.ExceptionDetails != nil {
		msg := r.ExceptionDetails.Text
		if d := r.ExceptionDetails.Exception.Description; d != "" {
			msg = d
		}
		return 0, 0, fmt.Errorf("selector %q: %s", locator, msg)
	}
	return r.Result.Value.X, r.Result.Value.Y, nil
}

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// boxForRef resolves a snapshot ref to an on-screen center point via
// DOM.resolveNode → Runtime.callFunctionOn(getBoundingClientRect).
func (s *Session) boxForRef(ctx context.Context, ref string) (cx, cy float64, err error) {
	id, ok := s.BackendIDForRef(ref)
	if !ok {
		return 0, 0, fmt.Errorf("unknown ref %q (take a snapshot first)", ref)
	}
	res, err := s.Call(ctx, "DOM.resolveNode", map[string]any{"backendNodeId": id})
	if err != nil {
		return 0, 0, fmt.Errorf("resolveNode: %w", err)
	}
	var rn struct {
		Object struct {
			ObjectID string `json:"objectId"`
		} `json:"object"`
	}
	if err := json.Unmarshal(res, &rn); err != nil {
		return 0, 0, err
	}
	if rn.Object.ObjectID == "" {
		return 0, 0, fmt.Errorf("no objectId for ref %s", ref)
	}
	defer func() {
		_, _ = s.Call(ctx, "Runtime.releaseObject", map[string]any{"objectId": rn.Object.ObjectID})
	}()

	callRes, err := s.Call(ctx, "Runtime.callFunctionOn", map[string]any{
		"objectId":            rn.Object.ObjectID,
		"functionDeclaration": `function(){const r=this.getBoundingClientRect();return {x:r.x,y:r.y,w:r.width,h:r.height};}`,
		"returnByValue":       true,
	})
	if err != nil {
		return 0, 0, fmt.Errorf("getBoundingClientRect: %w", err)
	}
	var cr struct {
		Result struct {
			Value struct {
				X, Y, W, H float64
			} `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(callRes, &cr); err != nil {
		return 0, 0, err
	}
	if cr.Result.Value.W == 0 && cr.Result.Value.H == 0 {
		return 0, 0, fmt.Errorf("ref %s has zero-sized box (not visible?)", ref)
	}
	return cr.Result.Value.X + cr.Result.Value.W/2, cr.Result.Value.Y + cr.Result.Value.H/2, nil
}

// Click performs a left-button press+release at the locator's center. Locator
// is either an AXTree ref ("e3") or a CSS selector (e.g. "button[type=submit]").
//
// CSS selectors go through Antibot.clickOn for human-like timing; refs use
// the raw Input.dispatchMouseEvent path (no Antibot axNodeId translation yet
// since our RefTable keys are DOM backend ids, not AX ids).
func (s *Session) Click(ctx context.Context, locator string) (map[string]any, error) {
	if !isRefLocator(locator) {
		res, err := s.ClickAntibot(ctx, CSSSelector(locator), "", 0)
		if err == nil {
			return res, nil
		}
		// Fall through to the plain Input path if Antibot isn't available
		// (e.g. non-Scrapfly Chromium). Swallow the error; if the fallback
		// also fails, its error surfaces.
	}
	cx, cy, err := s.boxFor(ctx, locator)
	if err != nil {
		return nil, err
	}
	for _, t := range []string{"mousePressed", "mouseReleased"} {
		if _, err := s.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
			"type":       t,
			"x":          cx,
			"y":          cy,
			"button":     "left",
			"clickCount": 1,
		}); err != nil {
			return nil, err
		}
	}
	return map[string]any{"locator": locator, "x": cx, "y": cy}, nil
}

// Fill focuses the locator and inserts text. Locator is an AXTree ref ("e3")
// or a CSS selector.
//
// CSS selectors use Antibot.fill (click + clear + char-by-char typing with
// human WPM). Refs fall back to Input.insertText.
func (s *Session) Fill(ctx context.Context, locator, text string) (map[string]any, error) {
	if !isRefLocator(locator) {
		res, err := s.FillAntibot(ctx, CSSSelector(locator), text, true, 0)
		if err == nil {
			return res, nil
		}
		// Antibot unavailable — fall back to focus + Input.insertText.
	}
	if _, err := s.Click(ctx, locator); err != nil {
		return nil, err
	}
	if _, err := s.Call(ctx, "Input.insertText", map[string]any{"text": text}); err != nil {
		return nil, err
	}
	return map[string]any{"locator": locator, "text": text}, nil
}

// Scroll dispatches a wheel event; direction "down"/"up"/"left"/"right", amount
// in CSS pixels. If ref is non-empty, scrolls with that element as anchor;
// otherwise uses the viewport center.
func (s *Session) Scroll(ctx context.Context, direction string, amount float64, locator string) (map[string]any, error) {
	cx, cy := 500.0, 400.0
	if locator != "" {
		x, y, err := s.boxFor(ctx, locator)
		if err != nil {
			return nil, err
		}
		cx, cy = x, y
	}
	var dx, dy float64
	switch direction {
	case "down":
		dy = amount
	case "up":
		dy = -amount
	case "right":
		dx = amount
	case "left":
		dx = -amount
	default:
		return nil, fmt.Errorf("direction must be up|down|left|right, got %q", direction)
	}
	if _, err := s.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type":   "mouseWheel",
		"x":      cx,
		"y":      cy,
		"deltaX": dx,
		"deltaY": dy,
	}); err != nil {
		return nil, err
	}
	return map[string]any{"direction": direction, "amount": amount}, nil
}

// Screenshot returns the PNG bytes of the viewport.
func (s *Session) Screenshot(ctx context.Context, fullPage bool) ([]byte, error) {
	res, err := s.Call(ctx, "Page.captureScreenshot", map[string]any{
		"format":                "png",
		"captureBeyondViewport": fullPage,
	})
	if err != nil {
		return nil, err
	}
	var r struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return nil, err
	}
	return decodeBase64(r.Data)
}

// Eval runs Runtime.evaluate and returns the result (string representation).
func (s *Session) Eval(ctx context.Context, js string) (any, error) {
	res, err := s.Call(ctx, "Runtime.evaluate", map[string]any{
		"expression":    js,
		"returnByValue": true,
		"awaitPromise":  true,
	})
	if err != nil {
		return nil, err
	}
	var r struct {
		Result struct {
			Value       json.RawMessage `json:"value"`
			Description string          `json:"description,omitempty"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails,omitempty"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return nil, err
	}
	if r.ExceptionDetails != nil {
		return nil, fmt.Errorf("eval exception: %s", r.ExceptionDetails.Text)
	}
	if len(r.Result.Value) == 0 {
		return r.Result.Description, nil
	}
	var v any
	if err := json.Unmarshal(r.Result.Value, &v); err != nil {
		return string(r.Result.Value), nil
	}
	return v, nil
}

func decodeBase64(s string) ([]byte, error) {
	// Use encoding/base64 without importing at top for minimal surface; inline:
	return base64Decode(s)
}
