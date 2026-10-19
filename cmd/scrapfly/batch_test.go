package main

import (
	"bufio"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// batchStdin is stdin carrying lines, as a bufio.Reader so isStdinPiped takes
// the JSONL branch; no lines is a stdin nothing is piped into.
func batchStdin(lines []string) io.Reader {
	if len(lines) == 0 {
		return strings.NewReader("")
	}
	return bufio.NewReader(strings.NewReader(strings.Join(lines, "\n") + "\n"))
}

// batchURLFile writes content to a --url-file, "" when content is.
func batchURLFile(t *testing.T, content string) string {
	t.Helper()
	if content == "" {
		return ""
	}
	path := filepath.Join(t.TempDir(), "urls.txt")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Every config of a batch leaves with a correlation ID of its own, whatever
// mix of sources built it: "item-N" numbers configs by position across --url
// and --url-file, a JSONL line keeps its own ID, and an auto ID never takes
// one a line names. poolFromFlags follows each config to its ID.
func TestBatchCorrelationIDs(t *testing.T) {
	type entry struct {
		url, id   string
		fromFlags bool
	}
	for _, tc := range []struct {
		name  string
		urls  []string
		file  string
		stdin []string
		want  []entry
	}{
		{
			name: "--url",
			urls: []string{"https://a.test", "https://b.test"},
			want: []entry{{"https://a.test", "item-1", true}, {"https://b.test", "item-2", true}},
		},
		{
			name: "--url skips a blank entry",
			urls: []string{"https://a.test", "", "  ", "https://b.test"},
			want: []entry{{"https://a.test", "item-1", true}, {"https://b.test", "item-2", true}},
		},
		{
			name: "--url-file skips blank and comment lines",
			file: "https://a.test\n\n# note\nhttps://b.test\n",
			want: []entry{{"https://a.test", "item-1", true}, {"https://b.test", "item-2", true}},
		},
		{
			// b and c both took item-3.
			name: "--url-file after a blank --url",
			urls: []string{"https://a.test", "", "https://b.test"},
			file: "https://c.test\nhttps://d.test\n",
			want: []entry{
				{"https://a.test", "item-1", true}, {"https://b.test", "item-2", true},
				{"https://c.test", "item-3", true}, {"https://d.test", "item-4", true},
			},
		},
		{
			name:  "--url leaves piped stdin unread",
			urls:  []string{"https://a.test"},
			stdin: []string{`{"url":"https://x.test","correlation_id":"item-1"}`},
			want:  []entry{{"https://a.test", "item-1", true}},
		},
		{
			name:  "--url-file leaves piped stdin unread",
			file:  "https://a.test\n",
			stdin: []string{`{"url":"https://x.test","correlation_id":"item-1"}`},
			want:  []entry{{"https://a.test", "item-1", true}},
		},
		{
			// The unread line claims no ID.
			name:  "--url and --url-file leave piped stdin unread",
			urls:  []string{"https://a.test", ""},
			file:  "https://b.test\n",
			stdin: []string{`{"url":"https://x.test","correlation_id":"item-2"}`},
			want:  []entry{{"https://a.test", "item-1", true}, {"https://b.test", "item-2", true}},
		},
		{
			name:  "blank --url entries fall through to stdin",
			urls:  []string{"", " "},
			stdin: []string{`{"url":"https://a.test"}`},
			want:  []entry{{"https://a.test", "item-1", true}},
		},
		{
			name:  "JSONL numbers lines by position",
			stdin: []string{`{"url":"https://a.test"}`, ``, `{"url":"https://b.test"}`},
			want:  []entry{{"https://a.test", "item-1", true}, {"https://b.test", "item-2", true}},
		},
		{
			// correlation_id was dropped: only CorrelationID bound.
			name:  "a line's correlation_id",
			stdin: []string{`{"url":"https://a.test","correlation_id":"mine"}`, `{"url":"https://b.test"}`},
			want:  []entry{{"https://a.test", "mine", true}, {"https://b.test", "item-2", true}},
		},
		{
			name:  "a line's CorrelationID",
			stdin: []string{`{"url":"https://a.test","CorrelationID":"mine"}`},
			want:  []entry{{"https://a.test", "mine", true}},
		},
		{
			name: "correlation_id wins over CorrelationID in either order",
			stdin: []string{
				`{"url":"https://a.test","CorrelationID":"field","correlation_id":"wire"}`,
				`{"url":"https://b.test","correlation_id":"wire-2","CorrelationID":"field-2"}`,
			},
			want: []entry{{"https://a.test", "wire", true}, {"https://b.test", "wire-2", true}},
		},
		{
			name: "an empty or null correlation_id is none",
			stdin: []string{
				`{"url":"https://a.test","correlation_id":""}`,
				`{"url":"https://b.test","correlation_id":null}`,
				`{"url":"https://c.test","CorrelationID":"field","correlation_id":""}`,
			},
			want: []entry{{"https://a.test", "item-1", true}, {"https://b.test", "item-2", true}, {"https://c.test", "item-3", true}},
		},
		{
			name: "a line naming a later line's auto ID",
			stdin: []string{
				`{"url":"https://a.test"}`,
				`{"url":"https://b.test","proxy_pool":"unblocker","correlation_id":"item-1"}`,
			},
			want: []entry{{"https://a.test", "item-3", true}, {"https://b.test", "item-1", false}},
		},
		{
			name: "a line naming an earlier line's auto ID",
			stdin: []string{
				`{"url":"https://a.test","proxy_pool":"unblocker","correlation_id":"item-2"}`,
				`{"url":"https://b.test"}`,
			},
			want: []entry{{"https://a.test", "item-2", false}, {"https://b.test", "item-3", true}},
		},
		{
			name: "the fallback skips IDs lines name",
			stdin: []string{
				`{"url":"https://a.test","correlation_id":"item-3"}`,
				`{"url":"https://b.test","correlation_id":"item-4"}`,
				`{"url":"https://c.test"}`,
			},
			want: []entry{{"https://a.test", "item-3", true}, {"https://b.test", "item-4", true}, {"https://c.test", "item-5", true}},
		},
		{
			name: "fallbacks in order",
			stdin: []string{
				`{"url":"https://a.test"}`,
				`{"url":"https://b.test"}`,
				`{"url":"https://c.test","proxy_pool":"unblocker","correlation_id":"item-1"}`,
				`{"url":"https://d.test","correlation_id":"item-2"}`,
			},
			want: []entry{
				{"https://a.test", "item-5", true}, {"https://b.test", "item-6", true},
				{"https://c.test", "item-1", false}, {"https://d.test", "item-2", true},
			},
		},
		{
			// The API compares IDs byte for byte.
			name: "IDs differing in case are distinct",
			stdin: []string{
				`{"url":"https://a.test"}`,
				`{"url":"https://b.test","correlation_id":"ITEM-1"}`,
				`{"url":"https://c.test","correlation_id":"Mine"}`,
				`{"url":"https://d.test","correlation_id":"mine"}`,
			},
			want: []entry{
				{"https://a.test", "item-1", true}, {"https://b.test", "ITEM-1", true},
				{"https://c.test", "Mine", true}, {"https://d.test", "mine", true},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configs, poolFromFlags, err := collectBatchConfigs(batchStdin(tc.stdin), tc.urls, batchURLFile(t, tc.file), "", proxyPoolFlags{pool: "basic"}, false)
			if err != nil {
				t.Fatalf("collectBatchConfigs: %v", err)
			}
			got := make([]entry, len(configs))
			for i, cfg := range configs {
				fromFlags, ok := poolFromFlags[cfg.CorrelationID]
				if !ok {
					t.Errorf("poolFromFlags has no %q", cfg.CorrelationID)
				}
				got[i] = entry{cfg.URL, cfg.CorrelationID, fromFlags}
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("configs = %+v, want %+v", got, tc.want)
			}
			// One key per config: no two configs share an ID.
			if len(poolFromFlags) != len(configs) {
				t.Errorf("poolFromFlags holds %d IDs for %d configs: %v", len(poolFromFlags), len(configs), poolFromFlags)
			}
		})
	}
}

// Two JSONL lines naming one correlation_id are refused, as the SDK and the
// API refuse them, with the ID and its stdin lines rather than indexes into
// the batch.
func TestBatchReusedCorrelationIDs(t *testing.T) {
	const tail = ": every config of a batch needs a correlation_id of its own"
	for _, tc := range []struct {
		name  string
		stdin []string
		want  string
	}{
		{
			name:  "blank lines are counted",
			stdin: []string{`{"url":"https://a.test","correlation_id":"x"}`, ``, `{"url":"https://b.test","correlation_id":"x"}`},
			want:  `correlation_id "x" reused by JSONL lines 1 and 3` + tail,
		},
		{
			name: "three lines",
			stdin: []string{
				`{"url":"https://a.test","correlation_id":"x"}`,
				`{"url":"https://b.test","correlation_id":"x"}`,
				`{"url":"https://c.test"}`,
				`{"url":"https://d.test","correlation_id":"x"}`,
			},
			want: `correlation_id "x" reused by JSONL lines 1, 2 and 4` + tail,
		},
		{
			name: "every reused ID, by first line",
			stdin: []string{
				`{"url":"https://a.test","correlation_id":"b"}`,
				`{"url":"https://b.test","correlation_id":"a"}`,
				`{"url":"https://c.test","correlation_id":"b"}`,
				`{"url":"https://d.test","correlation_id":"a"}`,
				`{"url":"https://e.test","correlation_id":"c"}`,
			},
			want: `correlation_id "b" reused by JSONL lines 1 and 3; correlation_id "a" reused by JSONL lines 2 and 4` + tail,
		},
		{
			name:  "either key spelling",
			stdin: []string{`{"url":"https://a.test","correlation_id":"x"}`, `{"url":"https://b.test","CorrelationID":"x"}`},
			want:  `correlation_id "x" reused by JSONL lines 1 and 2` + tail,
		},
		{
			// The auto line names no ID: only the two lines are reported.
			name: "lines naming an auto ID",
			stdin: []string{
				`{"url":"https://a.test"}`,
				`{"url":"https://b.test","correlation_id":"item-1"}`,
				`{"url":"https://c.test","correlation_id":"item-1"}`,
			},
			want: `correlation_id "item-1" reused by JSONL lines 2 and 3` + tail,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configs, poolFromFlags, err := collectBatchConfigs(batchStdin(tc.stdin), nil, "", "", proxyPoolFlags{}, false)
			if err == nil {
				t.Fatalf("got %d configs, want the reuse refused", len(configs))
			}
			if err.Error() != tc.want {
				t.Errorf("error = %q, want %q", err, tc.want)
			}
			if configs != nil || poolFromFlags != nil {
				t.Errorf("returned configs %v and poolFromFlags %v beside the error", configs, poolFromFlags)
			}
		})
	}
}

// batchWireAPI fakes POST /scrape/batch, refusing every config as a scrape
// result carrying ERR::PROXY::UNBLOCKER_ULTRA_REQUIRED, the refusal that takes
// a hint. Each part carries the config's correlation ID, and the result its
// URL, so an envelope shows whose result it holds. It records the configs
// each batch carried.
func batchWireAPI(t *testing.T) (*httptest.Server, func() []map[string]string) {
	t.Helper()
	var (
		mu   sync.Mutex
		sent []map[string]string
	)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/scrape/batch" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var body struct {
			Configs []map[string]string `json:"configs"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("batch body: %v", err)
		}
		mu.Lock()
		sent = append(sent, body.Configs...)
		mu.Unlock()
		parts := multipart.NewWriter(w)
		w.Header().Set("Content-Type", "multipart/mixed; boundary="+parts.Boundary())
		for _, c := range body.Configs {
			payload, err := json.Marshal(map[string]any{
				"config":  map[string]any{"url": c["url"]},
				"context": map[string]any{},
				"result": map[string]any{
					"success": false, "status": "ERROR", "status_code": 422,
					"error": map[string]any{"code": errUnblockerUltraRequired, "message": "refused", "http_code": 422, "retryable": false},
				},
			})
			if err != nil {
				t.Errorf("batch part: %v", err)
				return
			}
			part, err := parts.CreatePart(textproto.MIMEHeader{
				"Content-Type":              {"application/json"},
				"X-Scrapfly-Correlation-Id": {c["correlation_id"]},
				"X-Scrapfly-Scrape-Status":  {"422"},
			})
			if err != nil {
				t.Errorf("batch part: %v", err)
				return
			}
			_, _ = part.Write(payload)
		}
		_ = parts.Close()
	}))
	t.Cleanup(api.Close)
	take := func() []map[string]string {
		mu.Lock()
		defer mu.Unlock()
		got := sent
		sent = nil
		return got
	}
	return api, take
}

// Driven through the command: each config reaches the API with its own
// correlation ID, each envelope carries its own config's result and the hint
// of its own pool, and a reused ID stops the batch before anything is sent.
func TestBatchCorrelationIDsOnTheWire(t *testing.T) {
	api, take := batchWireAPI(t)
	flags := &rootFlags{apiKey: "test-key", host: api.URL}
	hint := proxyPoolFlags{pool: "basic"}.ultraRefusalHint()
	if hint == "" {
		t.Fatal("--proxy-pool basic has no refusal hint to attribute")
	}
	type item struct{ url, hint string }

	for _, tc := range []struct {
		name  string
		args  []string
		stdin []string
		want  map[string]item // correlation ID -> its config's URL and hint
	}{
		{
			name: "--url-file after a blank --url",
			args: []string{"--url", "https://a.test", "--url", "", "--url", "https://b.test", "--url-file", batchURLFile(t, "https://c.test\n")},
			want: map[string]item{"item-1": {"https://a.test", hint}, "item-2": {"https://b.test", hint}, "item-3": {"https://c.test", hint}},
		},
		{
			// b names a pool of its own: no flag changes it, so no hint.
			name: "a line naming another line's auto ID",
			stdin: []string{
				`{"url":"https://a.test"}`,
				`{"url":"https://b.test","proxy_pool":"basic","correlation_id":"item-1"}`,
			},
			want: map[string]item{"item-3": {"https://a.test", hint}, "item-1": {"https://b.test", ""}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newBatchCmd(flags)
			cmd.SetArgs(append(tc.args, "--proxy-pool", "basic"))
			cmd.SetIn(batchStdin(tc.stdin))
			var err error
			stdout, _ := captureStdio(t, func() { err = cmd.Execute() })
			if err != nil {
				t.Fatal(err)
			}

			sent := take()
			if len(sent) != len(tc.want) {
				t.Fatalf("the API got %d configs, want %d: %v", len(sent), len(tc.want), sent)
			}
			for _, c := range sent {
				if want, ok := tc.want[c["correlation_id"]]; !ok || c["url"] != want.url {
					t.Errorf("the API got %s as %q, want %+v", c["url"], c["correlation_id"], tc.want)
				}
			}

			type envelope struct {
				Data struct {
					CorrelationID string `json:"correlation_id"`
					Hint          string `json:"hint"`
					Result        struct {
						Config struct {
							URL string `json:"url"`
						} `json:"config"`
					} `json:"result"`
				} `json:"data"`
			}
			seen := make(map[string]bool)
			dec := json.NewDecoder(strings.NewReader(stdout))
			for dec.More() {
				var env envelope
				if err := dec.Decode(&env); err != nil {
					t.Fatalf("stdout is not ndjson envelopes: %v\n%s", err, stdout)
				}
				id := env.Data.CorrelationID
				want, ok := tc.want[id]
				if !ok || seen[id] {
					t.Fatalf("unexpected or repeated correlation ID %q:\n%s", id, stdout)
				}
				seen[id] = true
				if got := (item{env.Data.Result.Config.URL, env.Data.Hint}); got != want {
					t.Errorf("%s holds %+v, want %+v", id, got, want)
				}
			}
			if len(seen) != len(tc.want) {
				t.Errorf("got %d envelopes, want %d:\n%s", len(seen), len(tc.want), stdout)
			}
		})
	}

	t.Run("a reused ID is refused before anything is sent", func(t *testing.T) {
		cmd := newBatchCmd(flags)
		// Not nil: cobra would read os.Args.
		cmd.SetArgs([]string{})
		cmd.SetIn(batchStdin([]string{
			`{"url":"https://a.test","correlation_id":"x"}`,
			`{"url":"https://b.test","correlation_id":"x"}`,
		}))
		var err error
		_, _ = captureStdio(t, func() { err = cmd.Execute() })
		if err == nil || !strings.Contains(err.Error(), `correlation_id "x" reused by JSONL lines 1 and 2`) {
			t.Errorf("err = %v, want the reuse refused with its lines", err)
		}
		if sent := take(); len(sent) != 0 {
			t.Errorf("the API got %v, want nothing", sent)
		}
	})
}
