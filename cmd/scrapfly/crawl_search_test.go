package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	scrapfly "github.com/scrapfly/go-scrapfly"
)

func TestParseSearchFilters(t *testing.T) {
	cases := []struct {
		name    string
		pairs   []string
		want    map[string]interface{}
		wantErr bool
	}{
		{"nil stays nil", nil, nil, false},
		{
			"plain string value",
			[]string{"url_prefix=https://example.com/docs/"},
			map[string]interface{}{"url_prefix": "https://example.com/docs/"},
			false,
		},
		{
			// A range filter has to reach the API as a JSON array, not a string.
			"json array value keeps its type",
			[]string{"http_status=[200,299]"},
			map[string]interface{}{"http_status": []interface{}{float64(200), float64(299)}},
			false,
		},
		{
			"json string list keeps its type",
			[]string{`host=["a.com","b.com"]`},
			map[string]interface{}{"host": []interface{}{"a.com", "b.com"}},
			false,
		},
		{
			"value containing = is not split twice",
			[]string{"url_prefix=https://example.com/?a=b"},
			map[string]interface{}{"url_prefix": "https://example.com/?a=b"},
			false,
		},
		{"missing = is rejected", []string{"url_prefix"}, nil, true},
		{"empty key is rejected", []string{"=value"}, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseSearchFilters(c.pairs)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %#v, want %#v", got, c.want)
			}
		})
	}
}

func TestBuildCrawlerConfig_SearchFlag(t *testing.T) {
	cfg, err := buildCrawlerConfig("https://example.com", &crawlStartFlags{search: true})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Search {
		t.Fatal("--search did not reach the SDK config")
	}

	// Unset means server default: never emit a field to send its default.
	off, err := buildCrawlerConfig("https://example.com", &crawlStartFlags{})
	if err != nil {
		t.Fatal(err)
	}
	if off.Search {
		t.Error("Search must stay false when the flag is absent")
	}
}

func TestBuildCrawlerConfig_SearchWebhookEvents(t *testing.T) {
	cfg, err := buildCrawlerConfig("https://example.com", &crawlStartFlags{
		search:        true,
		webhookName:   "hook",
		webhookEvents: []string{"crawler_search_ready", "crawler_search_failed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []scrapfly.CrawlerWebhookEvent{
		scrapfly.WebhookCrawlerSearchReady,
		scrapfly.WebhookCrawlerSearchFailed,
	}
	if !reflect.DeepEqual(cfg.WebhookEvents, want) {
		t.Fatalf("got %v, want %v", cfg.WebhookEvents, want)
	}
	// The CLI casts raw strings into the enum with no pre-validation, so the
	// SDK's serialize-time check is the only thing standing between a typo and
	// a silently-unsubscribed webhook.
	for _, event := range cfg.WebhookEvents {
		if !event.IsValid() {
			t.Errorf("%q rejected by the SDK enum", event)
		}
	}
}

func TestCrawlSearchCmd_RequiresQuery(t *testing.T) {
	flags := &rootFlags{}
	cmd := newCrawlSearchCmd(flags)
	cmd.SetArgs([]string{"01HX7K4"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error when --query is missing")
	}
}

func TestCrawlPromptCmd_RequiresPrompt(t *testing.T) {
	flags := &rootFlags{}
	cmd := newCrawlPromptCmd(flags)
	cmd.SetArgs([]string{"01HX7K4"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error when --prompt is missing")
	}
}

func TestCrawlCmd_RegistersSearchAndPrompt(t *testing.T) {
	flags := &rootFlags{}
	cmd := newCrawlCmd(flags)
	found := map[string]bool{}
	for _, sub := range cmd.Commands() {
		found[sub.Name()] = true
	}
	for _, name := range []string{"search", "prompt"} {
		if !found[name] {
			t.Errorf("crawl %s is not registered", name)
		}
	}
}

func TestSanitizeTerminal(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain text is untouched", "pricing plans", "pricing plans"},
		{"ansi colour escape is stripped", "\x1b[31mred\x1b[0m", "[31mred[0m"},
		{"osc title escape is stripped", "a\x1b]0;pwned\x07b", "a]0;pwnedb"},
		{"carriage return cannot overwrite the line", "safe\rmalicious", "safemalicious"},
		{"bidi override cannot reorder a url", "example.com/‮gnp.exe", "example.com/gnp.exe"},
		{"tab and newline survive", "a\tb\nc", "a\tb\nc"},
		{"non-ascii content survives", "café 日本語", "café 日本語"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeTerminal(tc.in); got != tc.want {
				t.Errorf("sanitizeTerminal(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// runCrawlPrompt drives `crawl prompt` against an SSE stub and returns what the
// command wrote to stdout. The command prints with fmt.Print and out.Pretty,
// which resolve os.Stdout at call time, so the pipe has to replace the real
// descriptor rather than a cobra writer.
func runCrawlPrompt(t *testing.T, doneFrame string, pretty bool) string {
	t.Helper()
	output, err := executeCrawlPrompt(t, doneFrame, pretty)
	if err != nil {
		t.Fatalf("crawl prompt: %v", err)
	}
	return output
}

func executeCrawlPrompt(t *testing.T, doneFrame string, pretty bool) (string, error) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: token\ndata: \"hi\"\n\n" + doneFrame))
	}))
	defer server.Close()

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	realStdout := os.Stdout
	os.Stdout = write
	defer func() { os.Stdout = realStdout }()

	cmd := newCrawlPromptCmd(&rootFlags{apiKey: "test-key", host: server.URL, pretty: pretty})
	cmd.SetArgs([]string{"01HX7K4", "--prompt", "anything"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	execErr := cmd.Execute()

	_ = write.Close()
	captured, _ := io.ReadAll(read)
	os.Stdout = realStdout

	return string(captured), execErr
}

func TestCrawlPromptRejectsIncompleteAnswer(t *testing.T) {
	for _, pretty := range []bool{false, true} {
		output, err := executeCrawlPrompt(t, "", pretty)
		if !errors.Is(err, scrapfly.ErrCrawlerFailed) {
			t.Errorf("pretty=%v: expected crawler failure, got %v", pretty, err)
		}
		if !pretty && output != "" {
			t.Errorf("incomplete answer must not produce a success envelope: %s", output)
		}
		if pretty && !strings.Contains(output, "hi") {
			t.Errorf("pretty mode should keep the partial answer: %q", output)
		}
	}
}

// The done frame's api_credit is what the run was actually charged, and it is
// the only cost fact the API publishes for a prompt. Dropping it leaves the
// caller unable to reconcile a bill.
func TestCrawlPromptReportsAPICredit(t *testing.T) {
	done := "event: done\ndata: {\"sources_used\":[1],\"sources_dropped\":0,\"truncated\":false,\"api_credit\":3}\n\n"

	if got := runCrawlPrompt(t, done, true); !strings.Contains(got, "api_credit=3") {
		t.Errorf("pretty output does not report the charge: %q", got)
	}

	var envelope struct {
		Data struct {
			APICredit *int `json:"api_credit"`
		} `json:"data"`
	}
	raw := runCrawlPrompt(t, done, false)
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("envelope is not JSON (%v): %q", err, raw)
	}
	if envelope.Data.APICredit == nil || *envelope.Data.APICredit != 3 {
		t.Errorf("envelope api_credit: %v", envelope.Data.APICredit)
	}
}

// An engine too old to report the charge sends no key. Printing 0 there would
// state a free run that was in fact billed, so the absence is reported as such.
func TestCrawlPromptUnknownAPICreditIsNotZero(t *testing.T) {
	done := "event: done\ndata: {\"sources_used\":[],\"sources_dropped\":0,\"truncated\":false}\n\n"

	got := runCrawlPrompt(t, done, true)
	if !strings.Contains(got, "api_credit=unknown") {
		t.Errorf("pretty output should say unknown, got: %q", got)
	}
	if strings.Contains(got, "api_credit=0") {
		t.Errorf("an unreported charge must not print as zero: %q", got)
	}
}
