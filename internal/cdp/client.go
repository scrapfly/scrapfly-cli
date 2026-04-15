// Package cdp is a minimal Chrome DevTools Protocol client.
//
// It speaks raw JSON over a single WebSocket (no type generation, no event
// registry) — just enough to drive a Scrapfly-hosted Chromium through a small
// action vocabulary (open / snapshot / click / type / scroll / screenshot /
// eval). For heavier workloads, prefer chromedp + cdproto.
//
// The client is goroutine-safe for Call. A single reader pump dispatches
// responses back to the caller via id-keyed channels; events are buffered and
// delivered through WaitEvent.
package cdp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// Message is the on-wire envelope for both calls and events.
// Responses carry ID + Result (or Error). Events carry Method + Params (no ID).
type Message struct {
	ID        int             `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *RPCError       `json:"error,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
}

// RPCError mirrors the CDP error envelope.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e.Data != "" {
		return fmt.Sprintf("cdp error %d: %s (%s)", e.Code, e.Message, e.Data)
	}
	return fmt.Sprintf("cdp error %d: %s", e.Code, e.Message)
}

// Client is a CDP session over a single WebSocket.
type Client struct {
	conn *websocket.Conn

	nextID atomic.Int64

	mu       sync.Mutex
	pending  map[int]chan *Message
	eventsMu sync.Mutex
	events   []*Message // capped ring

	doneCh chan struct{}
	err    error
}

// Dial connects to a CDP WebSocket URL (typically wss://browser.scrapfly.io/
// ?api_key=...). Pass a derived ctx for dial cancellation.
func Dial(ctx context.Context, wsURL string) (*Client, error) {
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("cdp dial: %w", err)
	}
	// CDP messages can be large (AXTree, screenshots). Disable the 32 KiB cap.
	conn.SetReadLimit(64 * 1024 * 1024)
	c := &Client{
		conn:    conn,
		pending: map[int]chan *Message{},
		doneCh:  make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

// Close closes the underlying WebSocket. Any pending Call returns the close
// error.
func (c *Client) Close() error {
	err := c.conn.Close(websocket.StatusNormalClosure, "")
	<-c.doneCh
	return err
}

// Call sends method+params and waits for the matching response.
func (c *Client) Call(ctx context.Context, method string, params any, sessionID string) (json.RawMessage, error) {
	id := int(c.nextID.Add(1))
	ch := make(chan *Message, 1)

	c.mu.Lock()
	if c.pending == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("cdp: client closed")
	}
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal params: %w", err)
		}
		rawParams = b
	}
	msg := Message{ID: id, Method: method, Params: rawParams, SessionID: sessionID}
	payload, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal message: %w", err)
	}
	if err := c.conn.Write(ctx, websocket.MessageText, payload); err != nil {
		return nil, fmt.Errorf("cdp write: %w", err)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.doneCh:
		if c.err != nil {
			return nil, c.err
		}
		return nil, fmt.Errorf("cdp connection closed")
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

// WaitEvent blocks until an event with the given method (and optional
// predicate match) is observed. Buffered events are drained first.
func (c *Client) WaitEvent(ctx context.Context, method string, match func(json.RawMessage) bool) (json.RawMessage, error) {
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	}
	for time.Now().Before(deadline) {
		c.eventsMu.Lock()
		for i, e := range c.events {
			if e.Method == method && (match == nil || match(e.Params)) {
				c.events = append(c.events[:i], c.events[i+1:]...)
				c.eventsMu.Unlock()
				return e.Params, nil
			}
		}
		c.eventsMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.doneCh:
			return nil, fmt.Errorf("cdp closed while waiting for %s", method)
		case <-time.After(50 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("timeout waiting for %s", method)
}

func (c *Client) readLoop() {
	defer close(c.doneCh)
	ctx := context.Background()
	for {
		_, data, err := c.conn.Read(ctx)
		if err != nil {
			c.err = err
			// Signal all pending callers.
			c.mu.Lock()
			for _, ch := range c.pending {
				close(ch)
			}
			c.pending = nil
			c.mu.Unlock()
			return
		}
		var m Message
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		if m.ID != 0 {
			c.mu.Lock()
			ch, ok := c.pending[m.ID]
			c.mu.Unlock()
			if ok {
				ch <- &m
			}
			continue
		}
		// Event — buffer with a cap.
		c.eventsMu.Lock()
		c.events = append(c.events, &m)
		if len(c.events) > 256 {
			c.events = c.events[len(c.events)-256:]
		}
		c.eventsMu.Unlock()
	}
}
