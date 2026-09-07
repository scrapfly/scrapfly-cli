package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	scrapfly "github.com/scrapfly/go-scrapfly"
	"github.com/spf13/cobra"
)

func TestBuildCrawlerConfig_RefreshFlags(t *testing.T) {
	cfg, err := buildCrawlerConfig("https://example.com", &crawlStartFlags{
		refresh:         true,
		refreshInterval: 86400,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Refresh || cfg.RefreshInterval != 86400 {
		t.Fatalf("--refresh/--refresh-interval did not reach the SDK config: %+v", cfg)
	}

	// Unset means server default: never emit a field to send its default. The
	// SDK drops both zero values from the wire body.
	off, err := buildCrawlerConfig("https://example.com", &crawlStartFlags{})
	if err != nil {
		t.Fatal(err)
	}
	if off.Refresh || off.RefreshInterval != 0 {
		t.Errorf("refresh must stay unset when the flags are absent: %+v", off)
	}
}

func TestCrawlStartAndRun_ExposeRefreshFlags(t *testing.T) {
	// Two commands bind crawlStartFlags; a flag added to only one of them is
	// silently missing from the other.
	for name, cmd := range map[string]*cobra.Command{
		"start": newCrawlStartCmd(&rootFlags{}),
		"run":   newCrawlRunCmd(&rootFlags{}),
	} {
		for _, flag := range []string{"refresh", "refresh-interval"} {
			if cmd.Flags().Lookup(flag) == nil {
				t.Errorf("crawl %s is missing --%s", name, flag)
			}
		}
	}
}

func TestCrawlRefreshCmd_EnableAndDisableAreExclusive(t *testing.T) {
	cmd := newCrawlRefreshCmd(&rootFlags{})
	cmd.SetArgs([]string{"01HX7K4", "--enable", "--disable"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error when --enable and --disable are both set")
	}
}

func TestCrawlCmd_RegistersRefreshCommands(t *testing.T) {
	cmd := newCrawlCmd(&rootFlags{})
	found := map[string]bool{}
	for _, sub := range cmd.Commands() {
		found[sub.Name()] = true
	}
	for _, name := range []string{"refresh", "refresh-history"} {
		if !found[name] {
			t.Errorf("crawl %s is not registered", name)
		}
	}
}

func TestCrawlRefreshCmd_DispatchesExplicitFlags(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		wantMethod string
		wantBody   map[string]any
		wantError  bool
	}{
		{"immediate", nil, http.MethodPost, nil, false},
		{"enable", []string{"--enable"}, http.MethodPatch, map[string]any{"refresh": true}, false},
		{"disable", []string{"--disable"}, http.MethodPatch, map[string]any{"refresh": false}, false},
		{"enable false", []string{"--enable=false"}, http.MethodPatch, map[string]any{"refresh": false}, false},
		{"disable false", []string{"--disable=false"}, http.MethodPatch, map[string]any{"refresh": true}, false},
		{"interval only", []string{"--interval=86400"}, http.MethodPatch, map[string]any{"refresh_interval": float64(86400)}, false},
		{"disable with interval", []string{"--enable=false", "--interval=86400"}, http.MethodPatch, map[string]any{"refresh": false, "refresh_interval": float64(86400)}, false},
		{"conflicting true flags", []string{"--enable", "--disable"}, "", nil, true},
		{"conflicting false flags", []string{"--enable=false", "--disable=false"}, "", nil, true},
		{"conflicting mixed flags", []string{"--enable=false", "--disable"}, "", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != tc.wantMethod {
					t.Errorf("method = %s, want %s", r.Method, tc.wantMethod)
				}
				if r.URL.Path != "/crawl/crawl-test/refresh" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
					t.Errorf("decode request: %v", err)
				}
				if !reflect.DeepEqual(body, tc.wantBody) {
					t.Errorf("body = %v, want %v", body, tc.wantBody)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"enabled":false,"status":"SCHEDULED","generation":1}`)
			}))
			defer server.Close()

			cmd := newCrawlRefreshCmd(&rootFlags{apiKey: "test-key", host: server.URL})
			cmd.SetArgs(append([]string{"crawl-test"}, tc.args...))
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err := cmd.Execute()
			if tc.wantError {
				if !errors.Is(err, scrapfly.ErrCrawlerConfig) {
					t.Errorf("expected configuration error, got %v", err)
				}
				if requests != 0 {
					t.Errorf("invalid flags issued %d requests", requests)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if requests != 1 {
					t.Errorf("issued %d requests, want 1", requests)
				}
			}
		})
	}
}
