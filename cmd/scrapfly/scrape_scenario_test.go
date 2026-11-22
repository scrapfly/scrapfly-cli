package main

import (
	"bufio"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScrapeScenarioFileRequiresRendering(t *testing.T) {
	requests := make(chan url.Values, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests <- req.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"result":{"success":true,"status":"DONE","status_code":200}}`)
	}))
	t.Cleanup(server.Close)
	flags := &rootFlags{apiKey: "test-key", host: server.URL}

	for _, tc := range []struct {
		name, steps, render       string
		wantRefusal, wantScenario bool
	}{
		{name: "omitted", steps: `[{"wait":50}]`, wantRefusal: true},
		{name: "disabled", steps: `[{"wait":50}]`, render: "--render-js=false", wantRefusal: true},
		{name: "enabled", steps: `[{"wait":50}]`, render: "--render-js", wantScenario: true},
		{name: "empty", steps: "[]"},
		{name: "null", steps: "null"},
		{name: "no file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newScrapeCmd(flags)
			args := []string{wireTarget}
			if tc.steps != "" {
				file := filepath.Join(t.TempDir(), "scenario.json")
				if err := os.WriteFile(file, []byte(tc.steps), 0o600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--js-scenario-file", file)
			}
			if tc.render != "" {
				args = append(args, tc.render)
			}
			cmd.SetArgs(args)
			var err error
			_, _ = captureStdio(t, func() { err = cmd.Execute() })
			if tc.wantRefusal {
				if err == nil || !strings.Contains(err.Error(), "--js-scenario-file requires --render-js") || len(requests) != 0 {
					t.Fatalf("expected refusal before the API call: error=%v requests=%d", err, len(requests))
				}
				return
			}
			if err != nil || len(requests) != 1 {
				t.Fatalf("expected API call: error=%v requests=%d", err, len(requests))
			}
			query := <-requests
			wantScenario := ""
			if tc.wantScenario {
				wantScenario = base64.RawURLEncoding.EncodeToString([]byte(tc.steps))
			}
			if query.Get("js_scenario") != wantScenario || (query.Get("render_js") == "true") != tc.wantScenario {
				t.Fatalf("render_js=%q js_scenario=%q, want scenario %q", query.Get("render_js"), query.Get("js_scenario"), wantScenario)
			}
		})
	}
}

func TestBatchScenarioRequiresRendering(t *testing.T) {
	server, take := batchWireAPI(t)
	flags := &rootFlags{apiKey: "test-key", host: server.URL}
	for _, tc := range []struct {
		name, fields             string
		renderFlag, wantScenario bool
		wantRefusal              bool
	}{
		{name: "disabled", fields: `,"JSScenario":[{"wait":50}],"RenderJS":false`, wantRefusal: true},
		{name: "enabled in config", fields: `,"JSScenario":[{"wait":50}],"RenderJS":true`, wantScenario: true},
		{name: "enabled by flag", fields: `,"JSScenario":[{"wait":50}]`, renderFlag: true, wantScenario: true},
		{name: "empty", fields: `,"JSScenario":[]`},
		{name: "null", fields: `,"JSScenario":null`},
		{name: "omitted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := `{"URL":"` + wireTarget + `"` + tc.fields + `}`
			cmd := newBatchCmd(flags)
			cmd.SetIn(bufio.NewReader(strings.NewReader(line)))
			args := []string{}
			if tc.renderFlag {
				args = append(args, "--render-js")
			}
			cmd.SetArgs(args)
			var err error
			_, _ = captureStdio(t, func() { err = cmd.Execute() })
			sent := take()
			if tc.wantRefusal {
				if err == nil || !strings.Contains(err.Error(), "JSONL line 1: JSScenario requires RenderJS or --render-js") || len(sent) != 0 {
					t.Fatalf("expected JSONL validation error without requests: error=%v configs=%d", err, len(sent))
				}
				return
			}
			if err != nil || len(sent) != 1 {
				t.Fatalf("expected one config in request: error=%v configs=%d", err, len(sent))
			}
			wantScenario := ""
			if tc.wantScenario {
				wantScenario = base64.RawURLEncoding.EncodeToString([]byte(`[{"wait":50}]`))
			}
			if sent[0]["js_scenario"] != wantScenario || (sent[0]["render_js"] == "true") != tc.wantScenario {
				t.Fatalf("render_js=%q js_scenario=%q, want scenario %q", sent[0]["render_js"], sent[0]["js_scenario"], wantScenario)
			}
		})
	}
}
