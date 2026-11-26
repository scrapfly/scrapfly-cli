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
// one a line names.
func TestBatchCorrelationIDs(t *testing.T) {
	type entry struct{ url, id string }
	for _, tc := range []struct {
		name  string
		urls  []string
		file  string
		stdin []string
		want  []entry
	}{
		{
			name: "--url",
			urls: []string{"https://web-scraping.dev/product/1", "https://web-scraping.dev/product/2"},
			want: []entry{{"https://web-scraping.dev/product/1", "item-1"}, {"https://web-scraping.dev/product/2", "item-2"}},
		},
		{
			name: "--url skips a blank entry",
			urls: []string{"https://web-scraping.dev/product/1", "", "  ", "https://web-scraping.dev/product/2"},
			want: []entry{{"https://web-scraping.dev/product/1", "item-1"}, {"https://web-scraping.dev/product/2", "item-2"}},
		},
		{
			name: "--url-file skips blank and comment lines",
			file: "https://web-scraping.dev/product/1\n\n# note\nhttps://web-scraping.dev/product/2\n",
			want: []entry{{"https://web-scraping.dev/product/1", "item-1"}, {"https://web-scraping.dev/product/2", "item-2"}},
		},
		{
			// b and c both took item-3.
			name: "--url-file after a blank --url",
			urls: []string{"https://web-scraping.dev/product/1", "", "https://web-scraping.dev/product/2"},
			file: "https://web-scraping.dev/product/3\nhttps://web-scraping.dev/product/4\n",
			want: []entry{
				{"https://web-scraping.dev/product/1", "item-1"}, {"https://web-scraping.dev/product/2", "item-2"},
				{"https://web-scraping.dev/product/3", "item-3"}, {"https://web-scraping.dev/product/4", "item-4"},
			},
		},
		{
			name:  "--url leaves piped stdin unread",
			urls:  []string{"https://web-scraping.dev/product/1"},
			stdin: []string{`{"url":"https://web-scraping.dev/product/5","correlation_id":"item-1"}`},
			want:  []entry{{"https://web-scraping.dev/product/1", "item-1"}},
		},
		{
			name:  "--url-file leaves piped stdin unread",
			file:  "https://web-scraping.dev/product/1\n",
			stdin: []string{`{"url":"https://web-scraping.dev/product/5","correlation_id":"item-1"}`},
			want:  []entry{{"https://web-scraping.dev/product/1", "item-1"}},
		},
		{
			// The unread line claims no ID.
			name:  "--url and --url-file leave piped stdin unread",
			urls:  []string{"https://web-scraping.dev/product/1", ""},
			file:  "https://web-scraping.dev/product/2\n",
			stdin: []string{`{"url":"https://web-scraping.dev/product/5","correlation_id":"item-2"}`},
			want:  []entry{{"https://web-scraping.dev/product/1", "item-1"}, {"https://web-scraping.dev/product/2", "item-2"}},
		},
		{
			name:  "blank --url entries fall through to stdin",
			urls:  []string{"", " "},
			stdin: []string{`{"url":"https://web-scraping.dev/product/1"}`},
			want:  []entry{{"https://web-scraping.dev/product/1", "item-1"}},
		},
		{
			name:  "JSONL numbers lines by position",
			stdin: []string{`{"url":"https://web-scraping.dev/product/1"}`, ``, `{"url":"https://web-scraping.dev/product/2"}`},
			want:  []entry{{"https://web-scraping.dev/product/1", "item-1"}, {"https://web-scraping.dev/product/2", "item-2"}},
		},
		{
			// correlation_id was dropped: only CorrelationID bound.
			name:  "a line's correlation_id",
			stdin: []string{`{"url":"https://web-scraping.dev/product/1","correlation_id":"mine"}`, `{"url":"https://web-scraping.dev/product/2"}`},
			want:  []entry{{"https://web-scraping.dev/product/1", "mine"}, {"https://web-scraping.dev/product/2", "item-2"}},
		},
		{
			name:  "a line's CorrelationID",
			stdin: []string{`{"url":"https://web-scraping.dev/product/1","CorrelationID":"mine"}`},
			want:  []entry{{"https://web-scraping.dev/product/1", "mine"}},
		},
		{
			name: "correlation_id wins over CorrelationID in either order",
			stdin: []string{
				`{"url":"https://web-scraping.dev/product/1","CorrelationID":"field","correlation_id":"wire"}`,
				`{"url":"https://web-scraping.dev/product/2","correlation_id":"wire-2","CorrelationID":"field-2"}`,
			},
			want: []entry{{"https://web-scraping.dev/product/1", "wire"}, {"https://web-scraping.dev/product/2", "wire-2"}},
		},
		{
			name: "an empty or null correlation_id is none",
			stdin: []string{
				`{"url":"https://web-scraping.dev/product/1","correlation_id":""}`,
				`{"url":"https://web-scraping.dev/product/2","correlation_id":null}`,
				`{"url":"https://web-scraping.dev/product/3","CorrelationID":"field","correlation_id":""}`,
			},
			want: []entry{{"https://web-scraping.dev/product/1", "item-1"}, {"https://web-scraping.dev/product/2", "item-2"}, {"https://web-scraping.dev/product/3", "item-3"}},
		},
		{
			name: "a line naming a later line's auto ID",
			stdin: []string{
				`{"url":"https://web-scraping.dev/product/1"}`,
				`{"url":"https://web-scraping.dev/product/2","proxy_pool":"public_residential_pool","correlation_id":"item-1"}`,
			},
			want: []entry{{"https://web-scraping.dev/product/1", "item-3"}, {"https://web-scraping.dev/product/2", "item-1"}},
		},
		{
			name: "a line naming an earlier line's auto ID",
			stdin: []string{
				`{"url":"https://web-scraping.dev/product/1","proxy_pool":"public_residential_pool","correlation_id":"item-2"}`,
				`{"url":"https://web-scraping.dev/product/2"}`,
			},
			want: []entry{{"https://web-scraping.dev/product/1", "item-2"}, {"https://web-scraping.dev/product/2", "item-3"}},
		},
		{
			name: "the fallback skips IDs lines name",
			stdin: []string{
				`{"url":"https://web-scraping.dev/product/1","correlation_id":"item-3"}`,
				`{"url":"https://web-scraping.dev/product/2","correlation_id":"item-4"}`,
				`{"url":"https://web-scraping.dev/product/3"}`,
			},
			want: []entry{{"https://web-scraping.dev/product/1", "item-3"}, {"https://web-scraping.dev/product/2", "item-4"}, {"https://web-scraping.dev/product/3", "item-5"}},
		},
		{
			name: "fallbacks in order",
			stdin: []string{
				`{"url":"https://web-scraping.dev/product/1"}`,
				`{"url":"https://web-scraping.dev/product/2"}`,
				`{"url":"https://web-scraping.dev/product/3","proxy_pool":"public_residential_pool","correlation_id":"item-1"}`,
				`{"url":"https://web-scraping.dev/product/4","correlation_id":"item-2"}`,
			},
			want: []entry{
				{"https://web-scraping.dev/product/1", "item-5"}, {"https://web-scraping.dev/product/2", "item-6"},
				{"https://web-scraping.dev/product/3", "item-1"}, {"https://web-scraping.dev/product/4", "item-2"},
			},
		},
		{
			// The API compares IDs byte for byte.
			name: "IDs differing in case are distinct",
			stdin: []string{
				`{"url":"https://web-scraping.dev/product/1"}`,
				`{"url":"https://web-scraping.dev/product/2","correlation_id":"ITEM-1"}`,
				`{"url":"https://web-scraping.dev/product/3","correlation_id":"Mine"}`,
				`{"url":"https://web-scraping.dev/product/4","correlation_id":"mine"}`,
			},
			want: []entry{
				{"https://web-scraping.dev/product/1", "item-1"}, {"https://web-scraping.dev/product/2", "ITEM-1"},
				{"https://web-scraping.dev/product/3", "Mine"}, {"https://web-scraping.dev/product/4", "mine"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configs, err := collectBatchConfigs(batchStdin(tc.stdin), tc.urls, batchURLFile(t, tc.file), "", proxyPoolFlags{pool: "public_datacenter_pool"}, false)
			if err != nil {
				t.Fatalf("collectBatchConfigs: %v", err)
			}
			got := make([]entry, len(configs))
			for i, cfg := range configs {
				got[i] = entry{cfg.URL, cfg.CorrelationID}
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("configs = %+v, want %+v", got, tc.want)
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
			stdin: []string{`{"url":"https://web-scraping.dev/product/1","correlation_id":"x"}`, ``, `{"url":"https://web-scraping.dev/product/2","correlation_id":"x"}`},
			want:  `correlation_id "x" reused by JSONL lines 1 and 3` + tail,
		},
		{
			name: "three lines",
			stdin: []string{
				`{"url":"https://web-scraping.dev/product/1","correlation_id":"x"}`,
				`{"url":"https://web-scraping.dev/product/2","correlation_id":"x"}`,
				`{"url":"https://web-scraping.dev/product/3"}`,
				`{"url":"https://web-scraping.dev/product/4","correlation_id":"x"}`,
			},
			want: `correlation_id "x" reused by JSONL lines 1, 2 and 4` + tail,
		},
		{
			name: "every reused ID, by first line",
			stdin: []string{
				`{"url":"https://web-scraping.dev/product/1","correlation_id":"b"}`,
				`{"url":"https://web-scraping.dev/product/2","correlation_id":"a"}`,
				`{"url":"https://web-scraping.dev/product/3","correlation_id":"b"}`,
				`{"url":"https://web-scraping.dev/product/4","correlation_id":"a"}`,
				`{"url":"https://web-scraping.dev/product/5","correlation_id":"c"}`,
			},
			want: `correlation_id "b" reused by JSONL lines 1 and 3; correlation_id "a" reused by JSONL lines 2 and 4` + tail,
		},
		{
			name:  "either key spelling",
			stdin: []string{`{"url":"https://web-scraping.dev/product/1","correlation_id":"x"}`, `{"url":"https://web-scraping.dev/product/2","CorrelationID":"x"}`},
			want:  `correlation_id "x" reused by JSONL lines 1 and 2` + tail,
		},
		{
			// The auto line names no ID: only the two lines are reported.
			name: "lines naming an auto ID",
			stdin: []string{
				`{"url":"https://web-scraping.dev/product/1"}`,
				`{"url":"https://web-scraping.dev/product/2","correlation_id":"item-1"}`,
				`{"url":"https://web-scraping.dev/product/3","correlation_id":"item-1"}`,
			},
			want: `correlation_id "item-1" reused by JSONL lines 2 and 3` + tail,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configs, err := collectBatchConfigs(batchStdin(tc.stdin), nil, "", "", proxyPoolFlags{}, false)
			if err == nil {
				t.Fatalf("got %d configs, want the reuse refused", len(configs))
			}
			if err.Error() != tc.want {
				t.Errorf("error = %q, want %q", err, tc.want)
			}
			if configs != nil {
				t.Errorf("returned configs %v beside the error", configs)
			}
		})
	}
}

// batchWireAPI fakes POST /scrape/batch and records the configs each batch
// carried; each part carries the config's correlation ID and its URL.
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
					"success": true, "status": "DONE", "status_code": 200,
					"content": "<html></html>", "content_type": "text/html", "format": "text",
				},
			})
			if err != nil {
				t.Errorf("batch part: %v", err)
				return
			}
			part, err := parts.CreatePart(textproto.MIMEHeader{
				"Content-Type":              {"application/json"},
				"X-Scrapfly-Correlation-Id": {c["correlation_id"]},
				"X-Scrapfly-Scrape-Status":  {"200"},
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

// Each config reaches the API with its own correlation ID and pool, each
// envelope carries its own config's result, and a reused ID stops the batch
// before anything is sent.
func TestBatchCorrelationIDsOnTheWire(t *testing.T) {
	api, take := batchWireAPI(t)
	flags := &rootFlags{apiKey: "test-key", host: api.URL}
	const pool = "public_datacenter_pool"
	type item struct{ url, pool string }

	for _, tc := range []struct {
		name  string
		args  []string
		stdin []string
		want  map[string]item // correlation ID -> its config's URL and proxy_pool
	}{
		{
			name: "--url-file after a blank --url",
			args: []string{"--url", "https://web-scraping.dev/product/1", "--url", "", "--url", "https://web-scraping.dev/product/2", "--url-file", batchURLFile(t, "https://web-scraping.dev/product/3\n")},
			want: map[string]item{"item-1": {"https://web-scraping.dev/product/1", pool}, "item-2": {"https://web-scraping.dev/product/2", pool}, "item-3": {"https://web-scraping.dev/product/3", pool}},
		},
		{
			// b keeps the pool it names.
			name: "a line naming another line's auto ID",
			stdin: []string{
				`{"url":"https://web-scraping.dev/product/1"}`,
				`{"url":"https://web-scraping.dev/product/2","proxy_pool":"public_residential_pool","correlation_id":"item-1"}`,
			},
			want: map[string]item{"item-3": {"https://web-scraping.dev/product/1", pool}, "item-1": {"https://web-scraping.dev/product/2", "public_residential_pool"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newBatchCmd(flags)
			cmd.SetArgs(append(tc.args, "--proxy-pool", pool))
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
				if want, ok := tc.want[c["correlation_id"]]; !ok || (item{c["url"], c["proxy_pool"]}) != want {
					t.Errorf("the API got %s on %q as %q, want %+v", c["url"], c["proxy_pool"], c["correlation_id"], tc.want)
				}
			}

			type envelope struct {
				Data struct {
					CorrelationID string `json:"correlation_id"`
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
				if got := env.Data.Result.Config.URL; got != want.url {
					t.Errorf("%s holds the result of %s, want %s", id, got, want.url)
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
			`{"url":"https://web-scraping.dev/product/1","correlation_id":"x"}`,
			`{"url":"https://web-scraping.dev/product/2","correlation_id":"x"}`,
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
