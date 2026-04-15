package cdp

import (
	"context"
	"encoding/json"
	"fmt"
)

// Session is a CDP page-level context: browser connection + attached target
// (tab) session id + a rolling AXTree ref table for action references.
type Session struct {
	Client    *Client
	TargetID  string
	SessionID string

	// RefTable maps model-facing refs ("e1", "e2", ...) to the backend DOM node
	// IDs captured by the most recent Snapshot. Refs are stable only until the
	// next Snapshot call.
	RefTable map[string]int64
}

// Attach opens a fresh page target and attaches a flat session to it, ready
// for Page/DOM/Input/Accessibility calls. The caller must hold onto the
// returned Session for subsequent actions.
func Attach(ctx context.Context, c *Client) (*Session, error) {
	createRes, err := c.Call(ctx, "Target.createTarget", map[string]any{"url": "about:blank"}, "")
	if err != nil {
		return nil, fmt.Errorf("Target.createTarget: %w", err)
	}
	var cr struct {
		TargetID string `json:"targetId"`
	}
	if err := json.Unmarshal(createRes, &cr); err != nil {
		return nil, fmt.Errorf("parse createTarget: %w", err)
	}
	attachRes, err := c.Call(ctx, "Target.attachToTarget", map[string]any{
		"targetId": cr.TargetID,
		"flatten":  true,
	}, "")
	if err != nil {
		return nil, fmt.Errorf("Target.attachToTarget: %w", err)
	}
	var ar struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(attachRes, &ar); err != nil {
		return nil, fmt.Errorf("parse attachToTarget: %w", err)
	}

	s := &Session{
		Client:    c,
		TargetID:  cr.TargetID,
		SessionID: ar.SessionID,
		RefTable:  map[string]int64{},
	}

	// Enable the domains we use.
	for _, domain := range []string{"Page", "DOM", "Runtime", "Accessibility", "Network"} {
		if _, err := s.Call(ctx, domain+".enable", nil); err != nil {
			return nil, fmt.Errorf("enable %s: %w", domain, err)
		}
	}

	return s, nil
}

// Call proxies a CDP call scoped to this session.
func (s *Session) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return s.Client.Call(ctx, method, params, s.SessionID)
}

// Detach closes the tab. The browser session itself stays alive (stop it with
// `scrapfly browser close <session-id>`).
func (s *Session) Detach(ctx context.Context) error {
	_, err := s.Client.Call(ctx, "Target.closeTarget", map[string]any{"targetId": s.TargetID}, "")
	return err
}
