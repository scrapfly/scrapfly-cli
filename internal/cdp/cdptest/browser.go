// Package cdptest is a fake CDP browser for tests.
package cdptest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
)

// Browser answers the calls Attach and Open make: one blank page, a load event after each navigation.
type Browser struct {
	mu    sync.Mutex
	calls []string
}

// Serve starts the browser and returns its WebSocket URL.
func (f *Browser) Serve(t testing.TB) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := context.Background()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var m struct {
				ID        int             `json:"id"`
				Method    string          `json:"method"`
				Params    json.RawMessage `json:"params"`
				SessionID string          `json:"sessionId"`
			}
			_ = json.Unmarshal(data, &m)
			call := m.Method
			result := `{}`
			switch m.Method {
			case "Target.createTarget":
				result = `{"targetId":"T1"}`
			case "Target.attachToTarget":
				result = `{"sessionId":"S1"}`
			case "Page.navigate":
				var p struct {
					URL string `json:"url"`
				}
				_ = json.Unmarshal(m.Params, &p)
				call += " " + p.URL + " on " + m.SessionID
				result = `{"frameId":"F1","loaderId":"L1"}`
			}
			f.mu.Lock()
			f.calls = append(f.calls, call)
			f.mu.Unlock()
			reply, _ := json.Marshal(map[string]any{"id": m.ID, "result": json.RawMessage(result), "sessionId": m.SessionID})
			_ = conn.Write(ctx, websocket.MessageText, reply)
			if m.Method == "Page.navigate" {
				_ = conn.Write(ctx, websocket.MessageText, []byte(`{"method":"Page.loadEventFired","params":{"timestamp":1},"sessionId":"S1"}`))
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// Navigations are the Page.navigate calls received, each with its URL and session.
func (f *Browser) Navigations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "Page.navigate") {
			out = append(out, c)
		}
	}
	return out
}
