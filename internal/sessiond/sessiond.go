// Package sessiond runs a persistent Scrapfly Browser session in the
// foreground and serves actions to local CLI invocations over a Unix
// domain socket. The daemon holds:
//
//   - the CDP WebSocket connection to the Scrapfly Browser,
//   - the attached cdp.Session (with enabled domains + RefTable),
//
// so that multiple `scrapfly browser <action>` calls can share refs and
// cookies without re-attaching a fresh tab each time.
//
// Wire format on the socket is newline-delimited JSON. One request per
// connection; one response per request; then close.
package sessiond

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/scrapfly/scrapfly-cli/internal/cdp"
)

// Request is one action sent from a CLI process to the daemon.
type Request struct {
	Action    string  `json:"action"`
	URL       string  `json:"url,omitempty"`
	Ref       string  `json:"ref,omitempty"`
	Text      string  `json:"text,omitempty"`
	Direction string  `json:"direction,omitempty"`
	Amount    float64 `json:"amount,omitempty"`
	FullPage  bool    `json:"fullpage,omitempty"`
	JS        string  `json:"js,omitempty"`
	// Antibot / Scrapfly extras.
	Selector       string  `json:"selector,omitempty"`      // CSS selector (default) or XPath when SelectorType is "xpath"
	SelectorType   string  `json:"selector_type,omitempty"` // "css" | "xpath" (default "css")
	TimeoutMs      int     `json:"timeout_ms,omitempty"`
	Visible        bool    `json:"visible,omitempty"`
	RenderIframes  *bool   `json:"render_iframes,omitempty"` // default true on content
	Clear          bool    `json:"clear,omitempty"`
	WPM            float64 `json:"wpm,omitempty"`
	TargetSelector string  `json:"target_selector,omitempty"`
	Distance       float64 `json:"distance,omitempty"`
	// Raw lets callers send arbitrary action JSON for future actions
	// without adding a field here. Unused today.
	Raw json.RawMessage `json:"raw,omitempty"`
}

// Response is the envelope written back per request.
type Response struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

// Meta is persisted alongside the socket to record daemon info for status
// queries.
type Meta struct {
	SessionID string    `json:"session_id"`
	WSURL     string    `json:"ws_url"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

// SocketDir returns (and creates) the per-user directory that holds session
// sockets + metadata. Files live at <dir>/<session>.sock and <session>.json.
func SocketDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".scrapfly", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// PathsFor returns the socket + metadata paths for a session id.
func PathsFor(sessionID string) (sock, meta string, err error) {
	dir, err := SocketDir()
	if err != nil {
		return "", "", err
	}
	return filepath.Join(dir, sessionID+".sock"), filepath.Join(dir, sessionID+".json"), nil
}

// Serve runs the daemon until ctx is cancelled or a SIGINT/SIGTERM is
// received. It attaches to wsURL, writes <sessionID>.json + .sock under
// ~/.scrapfly/sessions/, serves requests, and cleans up on exit.
//
// The daemon is foreground: the caller is responsible for backgrounding
// (`&`, systemd, tmux). `onReady` is invoked once the socket is listening.
func Serve(ctx context.Context, sessionID, wsURL string, onReady func(sock string)) error {
	sockPath, metaPath, err := PathsFor(sessionID)
	if err != nil {
		return err
	}
	// Refuse to clobber a live socket.
	if _, err := os.Stat(sockPath); err == nil {
		if pingSocket(sockPath) {
			return fmt.Errorf("session %q is already running (socket %s)", sessionID, sockPath)
		}
		_ = os.Remove(sockPath)
	}

	dialCtx, cancelDial := context.WithTimeout(ctx, 30*time.Second)
	defer cancelDial()
	client, err := cdp.Dial(dialCtx, wsURL)
	if err != nil {
		return fmt.Errorf("cdp dial: %w", err)
	}
	sess, err := cdp.Attach(dialCtx, client)
	if err != nil {
		_ = client.Close()
		return fmt.Errorf("cdp attach: %w", err)
	}

	meta := Meta{
		SessionID: sessionID,
		WSURL:     wsURL,
		PID:       os.Getpid(),
		StartedAt: time.Now().UTC(),
	}
	mb, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(metaPath, mb, 0o600); err != nil {
		_ = client.Close()
		return fmt.Errorf("write metadata: %w", err)
	}
	defer os.Remove(metaPath)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		_ = client.Close()
		return fmt.Errorf("listen %s: %w", sockPath, err)
	}
	_ = os.Chmod(sockPath, 0o600)
	// Defers run LIFO at return. Required order: Detach (needs live CDP) →
	// client.Close (tears down WS) → ln.Close → rm sock. Register them in
	// the reverse of that.
	defer os.Remove(sockPath)
	defer ln.Close()
	defer client.Close()
	defer sess.Detach(context.Background())

	if onReady != nil {
		onReady(sockPath)
	}

	// Single shutdown channel — closed on first of: parent ctx cancelled,
	// SIGINT/SIGTERM, or "shutdown" action dispatched from a client.
	shutdown := make(chan struct{})
	var shutdownOnce sync.Once
	triggerShutdown := func() {
		shutdownOnce.Do(func() { close(shutdown) })
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sigCh:
		case <-ctx.Done():
		}
		triggerShutdown()
	}()
	go func() {
		<-shutdown
		_ = ln.Close()
	}()

	// CDP is a sequential per-session protocol — concurrent snapshot + click
	// would trash the RefTable and CDP response routing. Serialize dispatch
	// with a mutex; connections accept concurrently but execute one at a
	// time. Clients queue naturally.
	var dispatchMu sync.Mutex
	var wg sync.WaitGroup
	for {
		conn, err := ln.Accept()
		if err != nil {
			// Graceful shutdown path.
			break
		}
		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			handle(ctx, sess, &dispatchMu, c, triggerShutdown)
		}(conn)
	}
	wg.Wait()
	return nil
}

// pingSocket dials the socket and checks for a 100ms response; used by
// Serve to detect stale socket files.
func pingSocket(path string) bool {
	c, err := net.DialTimeout("unix", path, 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func handle(ctx context.Context, s *cdp.Session, mu *sync.Mutex, c net.Conn, shutdown func()) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Minute))

	br := bufio.NewReader(c)
	line, err := br.ReadBytes('\n')
	if err != nil && err != io.EOF {
		writeError(c, fmt.Sprintf("read: %v", err))
		return
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		writeError(c, fmt.Sprintf("invalid request JSON: %v", err))
		return
	}
	// Serialize all session-mutating calls. Ping/refs/shutdown are cheap so
	// we hold the mutex for them too for simplicity.
	mu.Lock()
	data, err := dispatch(ctx, s, &req, shutdown)
	mu.Unlock()
	if err != nil {
		writeError(c, err.Error())
		return
	}
	resp := Response{OK: true, Data: data}
	_ = json.NewEncoder(c).Encode(resp)
}

func writeError(c net.Conn, msg string) {
	_ = json.NewEncoder(c).Encode(Response{OK: false, Error: msg})
}

func dispatch(ctx context.Context, s *cdp.Session, r *Request, shutdown func()) (json.RawMessage, error) {
	marshal := func(v any) (json.RawMessage, error) {
		b, err := json.Marshal(v)
		return b, err
	}
	switch r.Action {
	case "navigate", "open":
		res, err := s.Open(ctx, r.URL)
		if err != nil {
			return nil, err
		}
		return marshal(res)
	case "snapshot":
		nodes, err := s.Snapshot(ctx)
		if err != nil {
			return nil, err
		}
		return marshal(map[string]any{"nodes": nodes})
	case "click":
		res, err := s.Click(ctx, r.Ref)
		if err != nil {
			return nil, err
		}
		return marshal(res)
	case "fill", "type":
		res, err := s.Fill(ctx, r.Ref, r.Text)
		if err != nil {
			return nil, err
		}
		return marshal(res)
	case "click_selector":
		res, err := s.ClickAntibot(ctx, makeSelector(r), "", 0)
		if err != nil {
			return nil, err
		}
		return marshal(res)
	case "fill_selector":
		res, err := s.FillAntibot(ctx, makeSelector(r), r.Text, r.Clear, r.WPM)
		if err != nil {
			return nil, err
		}
		return marshal(res)
	case "scroll":
		amt := r.Amount
		if amt == 0 {
			amt = 500
		}
		res, err := s.Scroll(ctx, r.Direction, amt, r.Ref)
		if err != nil {
			return nil, err
		}
		return marshal(res)
	case "screenshot":
		png, err := s.Screenshot(ctx, r.FullPage)
		if err != nil {
			return nil, err
		}
		return marshal(map[string]any{"png": png, "bytes": len(png)})
	case "eval":
		v, err := s.Eval(ctx, r.JS)
		if err != nil {
			return nil, err
		}
		return marshal(map[string]any{"value": v})
	case "content":
		rendered := true
		if r.RenderIframes != nil {
			rendered = *r.RenderIframes
		}
		content, typ, err := s.RenderedContent(ctx, rendered)
		if err != nil {
			return nil, err
		}
		return marshal(map[string]any{"content": content, "type": typ})
	case "wait":
		sel := makeSelector(r)
		res, err := s.WaitForElement(ctx, sel, r.TimeoutMs, r.Visible)
		if err != nil {
			return nil, err
		}
		return marshal(res)
	case "slide":
		src := makeSelector(r)
		opts := cdp.SlideOptions{Distance: r.Distance}
		if r.TargetSelector != "" {
			t := cdp.CSSSelector(r.TargetSelector)
			opts.Target = &t
		}
		res, err := s.ClickAndSlide(ctx, src, opts)
		if err != nil {
			return nil, err
		}
		return marshal(res)
	case "refs":
		// Diagnostic: return the current ref table.
		return marshal(s.RefTable)
	case "ping":
		return marshal(map[string]string{"status": "alive"})
	case "shutdown":
		// The client wants the daemon to exit. We succeed the request, then
		// signal the accept loop via the shutdown callback (portable across
		// Unix and Windows — no raw signal dispatch).
		if shutdown != nil {
			shutdown()
		}
		return marshal(map[string]string{"status": "shutting down"})
	}
	return nil, fmt.Errorf("unknown action %q", r.Action)
}

func makeSelector(r *Request) cdp.Selector {
	typ := r.SelectorType
	if typ == "" {
		typ = "css"
	}
	query := r.Selector
	if query == "" {
		query = r.Ref // convenience: callers may pass selector via Ref
	}
	return cdp.Selector{Type: typ, Query: query}
}

// Send opens the session's socket, writes one request, reads one response.
// Returned err covers transport + decoded Response.Error.
func Send(sessionID string, req Request) (*Response, error) {
	sockPath, _, err := PathsFor(sessionID)
	if err != nil {
		return nil, err
	}
	c, err := net.DialTimeout("unix", sockPath, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("no running session %q (start it first): %w", sessionID, err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Minute))
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := c.Write(append(body, '\n')); err != nil {
		return nil, err
	}
	var resp Response
	if err := json.NewDecoder(c).Decode(&resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return &resp, fmt.Errorf("%s", resp.Error)
	}
	return &resp, nil
}

// currentFile is the per-user file that records the "active" session id.
// `browser start` writes it; action subcommands read it when --session is
// omitted; `browser stop` clears it.
func currentFile() (string, error) {
	dir, err := SocketDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ".current"), nil
}

// SetCurrent records sessionID as the active session for later invocations.
func SetCurrent(sessionID string) error {
	p, err := currentFile()
	if err != nil {
		return err
	}
	return os.WriteFile(p, []byte(sessionID), 0o600)
}

// ClearCurrent removes the active-session marker. Called by stop.
func ClearCurrent(sessionID string) error {
	p, err := currentFile()
	if err != nil {
		return err
	}
	existing, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	// Only clear if this is the session we recorded. Avoid clobbering
	// someone else's concurrent session.
	if string(existing) != sessionID {
		return nil
	}
	return os.Remove(p)
}

// ReadCurrent returns the recorded active session id, or "" if none.
func ReadCurrent() string {
	p, err := currentFile()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

// Resolve picks a session id in priority order:
//  1. the explicit flag value (if non-empty)
//  2. the SCRAPFLY_SESSION env var
//  3. the ~/.scrapfly/sessions/.current file
//
// Returns ("", false) when nothing is available.
func Resolve(explicit string) (string, bool) {
	if explicit != "" {
		return explicit, true
	}
	if env := os.Getenv("SCRAPFLY_SESSION"); env != "" {
		return env, true
	}
	if cur := ReadCurrent(); cur != "" {
		return cur, true
	}
	return "", false
}

// LoadMeta reads the persisted metadata for a session, or nil if absent.
func LoadMeta(sessionID string) (*Meta, error) {
	_, metaPath, err := PathsFor(sessionID)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}
