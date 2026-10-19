package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"
)

// A call naming no pool and neither toggle is sent as the releases before
// proxy_pool sent it: no pool, Unblocker off. No date changes that.
func TestResolveProxyPool(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name           string
		pool           string
		asp, unblocker *bool
		wantPool       string
		wantASP        bool
	}{
		{"nothing named is sent as before", "", nil, nil, "", false},
		{"pool alone", "unblocker", nil, nil, "unblocker", false},
		{"pool wins over unblocker false", "unblocker_ultra", nil, &no, "unblocker_ultra", false},
		{"pool wins over asp true", "basic", &yes, nil, "basic", false},
		{"auto alone", "auto", nil, nil, "auto", false},
		{"auto wins over asp false", "auto", &no, nil, "auto", false},
		{"auto wins over unblocker true", "auto", nil, &yes, "auto", false},
		{"legacy toggle alone", "", nil, &yes, "", true},
		{"legacy asp false", "", &no, nil, "", false},
		{"legacy pool keeps its toggle", "public_residential_pool", nil, &yes, "public_residential_pool", true},
		{"legacy pool alone", "public_datacenter_pool", nil, nil, "public_datacenter_pool", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool, asp := resolveProxyPool(tc.pool, tc.asp, tc.unblocker)
			if pool != tc.wantPool || asp != tc.wantASP {
				t.Errorf("resolveProxyPool = (%q, %v), want (%q, %v)", pool, asp, tc.wantPool, tc.wantASP)
			}
		})
	}
}

// mcpSession connects an in-memory client to a server holding the scrape and
// crawl_run tools.
func mcpSession(t *testing.T, flags *rootFlags) *mcpsdk.ClientSession {
	t.Helper()
	ctx := t.Context()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "scrapfly", Version: version}, nil)
	registerScrapeTool(server, flags)
	registerCrawlRunTool(server, flags)
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	t.Cleanup(func() { serverSession.Close() })
	clientSession, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { clientSession.Close() })
	return clientSession
}

// The published schemas list one word per pool, auto included, tor for the Tor
// pool, and no public_ name, nowhere in the tool, description included. asp
// and unblocker are one-sentence deprecations that leave the rest to the
// migration guide.
func TestMcpToolSchemasDeclareProxyPools(t *testing.T) {
	tools, err := mcpSession(t, &rootFlags{}).ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools.Tools) != 2 {
		t.Fatalf("published %d tools, want scrape and crawl_run", len(tools.Tools))
	}
	for _, tool := range tools.Tools {
		t.Run(tool.Name, func(t *testing.T) {
			encoded, err := json.Marshal(tool)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "public_") {
				t.Errorf("%s advertises a public_ pool name: %s", tool.Name, encoded)
			}
			if strings.Contains(strings.ToLower(string(encoded)), "legacy") {
				t.Errorf("%s describes a legacy input: %s", tool.Name, encoded)
			}
			if want := "The unblocker and asp arguments are deprecated: use proxy_pool (unblocker_ultra for the Unblocker)."; !strings.Contains(tool.Description, want) {
				t.Errorf("%s description lacks %q: %s", tool.Name, want, tool.Description)
			}
			encoded, err = json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			var schema jsonschema.Schema
			if err := json.Unmarshal(encoded, &schema); err != nil {
				t.Fatal(err)
			}
			pool, ok := schema.Properties["proxy_pool"]
			if !ok {
				t.Fatalf("%s publishes no proxy_pool", tool.Name)
			}
			// A default would reach every call that omits the pool and change
			// what it sends.
			if pool.Default != nil {
				t.Errorf("%s proxy_pool carries a default %s", tool.Name, pool.Default)
			}
			if want := []any{"basic", "unblocker", "unblocker_ultra", "auto", "tor"}; !slices.Equal(pool.Enum, want) {
				t.Errorf("%s proxy_pool enum = %v, want %v", tool.Name, pool.Enum, want)
			}
			if !strings.Contains(pool.Description, "auto") {
				t.Errorf("%s proxy_pool description lists no auto: %q", tool.Name, pool.Description)
			}
			for _, name := range []string{"asp", "unblocker"} {
				property, ok := schema.Properties[name]
				if !ok {
					t.Fatalf("%s publishes no %s", tool.Name, name)
				}
				if want := "Deprecated: use proxy_pool (unblocker_ultra for the Unblocker)."; property.Description != want {
					t.Errorf("%s %s description = %q, want %q", tool.Name, name, property.Description, want)
				}
			}
		})
	}
}

// Only the migration guide explains the API's asp and unblocker parameters:
// the help and the MCP tools describe nothing of what they do, and every
// --help that names asp points to the guide.
func TestLegacyParametersLeftToTheGuide(t *testing.T) {
	const guide = "https://scrapfly.io/new-pricing#legacy-spellings"
	flags := &rootFlags{}
	texts := map[string]string{}
	for name, cmd := range map[string]*cobra.Command{
		"scrape":      newScrapeCmd(flags),
		"batch":       newBatchCmd(flags),
		"crawl start": newCrawlStartCmd(flags),
		"crawl run":   newCrawlRunCmd(flags),
		"mcp serve":   newMcpServeCmd(flags),
	} {
		texts[name] = cmd.Long + "\n" + cmd.Flags().FlagUsages()
		if !strings.Contains(texts[name], guide) {
			t.Errorf("%s --help names asp without pointing to %s", name, guide)
		}
	}
	tools, err := mcpSession(t, flags).ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range tools.Tools {
		encoded, err := json.Marshal(tool)
		if err != nil {
			t.Fatal(err)
		}
		texts["mcp tool "+tool.Name] = string(encoded)
	}
	for name, text := range texts {
		flat := strings.ToLower(strings.Join(strings.Fields(text), " "))
		for _, claim := range []string{"legacy unblocker", "legacy toggle", "runs as unblocker ultra", "keeps working", "forever", "no target refuses", "decides the unblocker", "unblocker on in"} {
			if strings.Contains(flat, claim) {
				t.Errorf("%s says %q:\n%s", name, claim, text)
			}
		}
	}
}

// Through a live session against a fake API, so schema validation and the
// resolution both run as they do for a client.
func TestMcpScrapeToolSendsTheResolvedPool(t *testing.T) {
	var (
		mu      sync.Mutex
		queries []url.Values
	)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.Query())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"uuid":"00000000-0000-0000-0000-000000000000","config":{"url":"https://example.test"},` +
			`"context":{"cost":{"total":1,"details":[]}},` +
			`"result":{"success":true,"status":"DONE","status_code":200,"content":"<html></html>","content_type":"text/html","format":"text"}}`))
	}))
	defer api.Close()
	session := mcpSession(t, &rootFlags{apiKey: "test-key", host: api.URL})

	for _, tc := range []struct {
		args     map[string]any
		wantPool string
		wantASP  string
	}{
		{map[string]any{"url": "https://example.test"}, "", ""},
		{map[string]any{"url": "https://example.test", "proxy_pool": "unblocker", "unblocker": false}, "unblocker", ""},
		{map[string]any{"url": "https://example.test", "proxy_pool": "basic", "asp": true}, "basic", ""},
		{map[string]any{"url": "https://example.test", "proxy_pool": "unblocker_ultra"}, "unblocker_ultra", ""},
		{map[string]any{"url": "https://example.test", "proxy_pool": "auto"}, "auto", ""},
		{map[string]any{"url": "https://example.test", "proxy_pool": "auto", "asp": false}, "auto", ""},
		{map[string]any{"url": "https://example.test", "proxy_pool": "auto", "unblocker": true}, "auto", ""},
		// The legacy toggle keeps its API meaning: asp=true, no pool.
		{map[string]any{"url": "https://example.test", "unblocker": true}, "", "true"},
		{map[string]any{"url": "https://example.test", "asp": true, "unblocker": false}, "", "true"},
		{map[string]any{"url": "https://example.test", "proxy_pool": "tor"}, "tor", ""},
		{map[string]any{"url": "https://example.test", "proxy_pool": "tor", "unblocker": true}, "tor", "true"},
		// Legacy pool names are served though no schema lists them.
		{map[string]any{"url": "https://example.test", "proxy_pool": "public_residential_pool", "asp": true}, "public_residential_pool", "true"},
		{map[string]any{"url": "https://example.test", "proxy_pool": "public_datacenter_pool"}, "public_datacenter_pool", ""},
		{map[string]any{"url": "https://example.test", "proxy_pool": "public_tor_pool"}, "public_tor_pool", ""},
	} {
		mu.Lock()
		queries = nil
		mu.Unlock()
		res, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "scrape", Arguments: tc.args})
		if err != nil {
			t.Fatalf("CallTool(%v): %v", tc.args, err)
		}
		if res.IsError {
			t.Fatalf("CallTool(%v) errored: %v", tc.args, res.Content)
		}
		mu.Lock()
		got := queries
		mu.Unlock()
		if len(got) != 1 {
			t.Fatalf("%v: %d API calls, want 1", tc.args, len(got))
		}
		if pool := got[0].Get("proxy_pool"); pool != tc.wantPool {
			t.Errorf("%v: proxy_pool=%q, want %q", tc.args, pool, tc.wantPool)
		}
		if asp := got[0].Get("asp"); asp != tc.wantASP {
			t.Errorf("%v: asp=%q, want %q", tc.args, asp, tc.wantASP)
		}
	}

	// The validation the published schema promises still holds, and no error
	// lists a legacy pool name: the enum error prints the values it accepts.
	for name, args := range map[string]map[string]any{
		"an unknown proxy_pool":                  {"url": "https://example.test", "proxy_pool": "nonexistent_pool"},
		"an empty proxy_pool":                    {"url": "https://example.test", "proxy_pool": ""},
		"a pool word in capitals":                {"url": "https://example.test", "proxy_pool": "UNBLOCKER"},
		"auto in capitals":                       {"url": "https://example.test", "proxy_pool": "AUTO"},
		"a key the schema lacks":                 {"url": "https://example.test", "URL": "https://other.test"},
		"a legacy pool beside a key it lacks":    {"url": "https://example.test", "proxy_pool": "public_residential_pool", "URL": "https://other.test"},
		"a legacy pool beside a non-boolean asp": {"url": "https://example.test", "proxy_pool": "public_tor_pool", "asp": "yes"},
		"a non-boolean toggle":                   {"url": "https://example.test", "unblocker": "yes"},
		"no url":                                 {"proxy_pool": "unblocker"},
		"no url beside a legacy pool":            {"proxy_pool": "public_datacenter_pool"},
		"a proxy_pool of no string":              {"url": "https://example.test", "proxy_pool": 1},
	} {
		mu.Lock()
		queries = nil
		mu.Unlock()
		res, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "scrape", Arguments: args})
		if err == nil && !res.IsError {
			t.Errorf("%s was accepted: %v", name, args)
		}
		var text string
		if err != nil {
			text = err.Error()
		} else {
			for _, c := range res.Content {
				if tc, ok := c.(*mcpsdk.TextContent); ok {
					text += tc.Text
				}
			}
		}
		if strings.Contains(text, "public_") {
			t.Errorf("%s: the error names a legacy pool: %s", name, text)
		}
		mu.Lock()
		if len(queries) != 0 {
			t.Errorf("%s reached the API: %v", name, queries)
		}
		mu.Unlock()
	}
}

// crawl_run serves a legacy pool name the same way, against the wire.
func TestMcpCrawlRunToolSendsALegacyPool(t *testing.T) {
	api, take := newWireAPI(t)
	session := mcpSession(t, &rootFlags{apiKey: "test-key", host: api.URL})
	for _, tc := range []struct {
		args map[string]any
		want wireRequest
	}{
		{map[string]any{"url": "https://example.test", "proxy_pool": "public_tor_pool"}, wireRequest{"public_tor_pool", ""}},
		{map[string]any{"url": "https://example.test", "proxy_pool": "unblocker_ultra", "asp": false}, wireRequest{"unblocker_ultra", ""}},
		{map[string]any{"url": "https://example.test", "proxy_pool": "auto", "unblocker": false}, wireRequest{"auto", ""}},
		{map[string]any{"url": "https://example.test", "unblocker": true}, wireRequest{"", "true"}},
	} {
		res, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "crawl_run", Arguments: tc.args})
		if err != nil {
			t.Fatalf("CallTool(%v): %v", tc.args, err)
		}
		if res.IsError {
			t.Fatalf("CallTool(%v) errored: %v", tc.args, res.Content)
		}
		if sent := take(); len(sent) != 1 || sent[0] != tc.want {
			t.Errorf("%v sent %+v, want %+v", tc.args, sent, tc.want)
		}
	}
}

// go-scrapfly's ScrapeConfig has no json tags, so a JSONL line's "proxy_pool"
// never bound to ProxyPool and the line ran on the flag's pool instead.
func TestBatchJSONLProxyPool(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		flag string
		want string
	}{
		{name: "the line's pool", line: `{"url":"https://a.test","proxy_pool":"unblocker"}`, want: "unblocker"},
		{name: "the line's pool wins over the flag", line: `{"url":"https://a.test","proxy_pool":"unblocker_ultra"}`, flag: "basic", want: "unblocker_ultra"},
		{name: "the line's auto wins over the flag", line: `{"url":"https://a.test","proxy_pool":"auto"}`, flag: "basic", want: "auto"},
		{name: "no pool takes the flag's auto", line: `{"url":"https://a.test"}`, flag: "auto", want: "auto"},
		{name: "a legacy pool name", line: `{"url":"https://a.test","proxy_pool":"public_residential_pool"}`, want: "public_residential_pool"},
		{name: "wins over a ProxyPool key", line: `{"url":"https://a.test","ProxyPool":"basic","proxy_pool":"unblocker"}`, want: "unblocker"},
		{name: "a ProxyPool key alone still binds", line: `{"url":"https://a.test","ProxyPool":"basic"}`, want: "basic"},
		{name: "no pool takes the flag", line: `{"url":"https://a.test"}`, flag: "basic", want: "basic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configs, _, err := collectBatchConfigs(bufio.NewReader(strings.NewReader(tc.line+"\n")), nil, "", "", proxyPoolFlags{pool: tc.flag}, false)
			if err != nil {
				t.Fatalf("collectBatchConfigs: %v", err)
			}
			if len(configs) != 1 {
				t.Fatalf("got %d configs, want 1", len(configs))
			}
			if got := string(configs[0].ProxyPool); got != tc.want {
				t.Errorf("ProxyPool = %q, want %q", got, tc.want)
			}
		})
	}
}

// A JSONL line keeps the API meaning of its own keys; the flags fill in only
// what it leaves out, and never flip the Unblocker a line decided. The pool is
// the flags' only when the line named none and the flags' pool reached it: a
// line naming the very pool the flags send still named its own.
func TestBatchLineResolve(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name          string
		flags         proxyPoolFlags
		linePool      string
		lineASP       *bool
		wantPool      string
		wantASP       bool
		wantFromFlags bool
	}{
		{"nothing anywhere", proxyPoolFlags{}, "", nil, "", false, true},
		{"bare line takes --unblocker", proxyPoolFlags{unblocker: true}, "", nil, "unblocker", false, true},
		{"bare line takes --unblocker-ultra", proxyPoolFlags{unblockerUltra: true}, "", nil, "unblocker_ultra", false, true},
		{"bare line takes a named pool with the Unblocker", proxyPoolFlags{pool: "public_residential_pool", unblocker: true}, "", nil, "public_residential_pool", true, true},
		{"bare line takes --proxy-pool auto", proxyPoolFlags{pool: "auto", unblocker: true}, "", nil, "auto", false, true},
		{"line auto beats the flags", proxyPoolFlags{pool: "basic"}, "auto", nil, "auto", false, false},
		{"line auto goes out alone", proxyPoolFlags{}, "auto", &no, "auto", false, false},
		{"line named pool under --proxy-pool auto", proxyPoolFlags{pool: "auto"}, "my_pool", nil, "my_pool", true, false},
		{"line asp true takes the agreeing auto", proxyPoolFlags{pool: "auto"}, "", &yes, "auto", false, true},
		{"line asp false beats --proxy-pool auto", proxyPoolFlags{pool: "auto"}, "", &no, "", false, false},
		{"line pool word beats the flags", proxyPoolFlags{unblockerUltra: true}, "basic", nil, "basic", false, false},
		{"line pool word goes out alone", proxyPoolFlags{}, "unblocker", &no, "unblocker", false, false},
		{"line naming the --proxy-pool word", proxyPoolFlags{pool: "basic"}, "basic", nil, "basic", false, false},
		{"line naming the --unblocker pool", proxyPoolFlags{unblocker: true}, "unblocker", nil, "unblocker", false, false},
		{"line naming the --proxy-pool word in its own spelling", proxyPoolFlags{pool: "basic"}, " BASIC ", nil, " BASIC ", false, false},
		{"line naming the flags' named pool", proxyPoolFlags{pool: "public_residential_pool", unblocker: true}, "public_residential_pool", nil, "public_residential_pool", true, false},
		{"line named pool keeps its asp", proxyPoolFlags{unblocker: true}, "public_residential_pool", &no, "public_residential_pool", false, false},
		{"line named pool takes the flags' Unblocker", proxyPoolFlags{unblocker: true}, "public_residential_pool", nil, "public_residential_pool", true, false},
		{"line named pool under basic beating --unblocker", proxyPoolFlags{pool: "basic", unblocker: true}, "my_pool", nil, "my_pool", false, false},
		{"line named pool under --proxy-pool unblocker", proxyPoolFlags{pool: "unblocker"}, "my_pool", nil, "my_pool", true, false},
		{"line asp true alone is a legacy request", proxyPoolFlags{}, "", &yes, "", true, true},
		{"line asp false beats --unblocker", proxyPoolFlags{unblocker: true}, "", &no, "", false, false},
		{"line asp false beats --proxy-pool unblocker_ultra", proxyPoolFlags{pool: "unblocker_ultra"}, "", &no, "", false, false},
		{"line asp true takes the agreeing --unblocker-ultra", proxyPoolFlags{unblockerUltra: true}, "", &yes, "unblocker_ultra", false, true},
		{"line asp false takes the agreeing basic", proxyPoolFlags{pool: "basic"}, "", &no, "basic", false, true},
		{"line asp true beats basic", proxyPoolFlags{pool: "basic"}, "", &yes, "", true, false},
		{"line asp false on a named flag pool", proxyPoolFlags{pool: "public_residential_pool", unblocker: true}, "", &no, "public_residential_pool", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool, asp, fromFlags := tc.flags.resolveLine(tc.linePool, tc.lineASP)
			if pool != tc.wantPool || asp != tc.wantASP || fromFlags != tc.wantFromFlags {
				t.Errorf("resolveLine(%q, %v) under %+v = (%q, %v, %v), want (%q, %v, %v)",
					tc.linePool, tc.lineASP, tc.flags, pool, asp, fromFlags, tc.wantPool, tc.wantASP, tc.wantFromFlags)
			}
		})
	}
}

// A batch item refused with ERR::PROXY::UNBLOCKER_ULTRA_REQUIRED gets the
// flags' hint only when its pool came from the flags, so following the hint
// changes what the item sends. A JSONL line that named its own pool gets none,
// whatever pool it named, the flags' own included.
func TestBatchUltraRefusalHintFollowsTheFlagsPool(t *testing.T) {
	const (
		ultra      = "retry with --unblocker-ultra"
		ultraBasic = "retry with --unblocker-ultra in place of --proxy-pool basic"
	)
	urlFile := filepath.Join(t.TempDir(), "urls.txt")
	if err := os.WriteFile(urlFile, []byte("https://a.test\n# skipped\nhttps://b.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		flags   proxyPoolFlags
		urls    []string
		urlFile string
		stdin   []string
		want    map[string]string // correlation ID -> hint were it refused
	}{
		{
			name:  "--url entries",
			flags: proxyPoolFlags{unblocker: true},
			urls:  []string{"https://a.test", "https://b.test"},
			want:  map[string]string{"item-1": ultra, "item-2": ultra},
		},
		{
			name:    "--url-file entries",
			flags:   proxyPoolFlags{pool: "basic"},
			urlFile: urlFile,
			want:    map[string]string{"item-1": ultraBasic, "item-2": ultraBasic},
		},
		{
			name:  "JSONL lines under --proxy-pool basic",
			flags: proxyPoolFlags{pool: "basic"},
			stdin: []string{
				`{"url":"https://a.test"}`,
				`{"url":"https://a.test","proxy_pool":"basic"}`,
				`{"url":"https://a.test","proxy_pool":" BASIC "}`,
				`{"url":"https://a.test","ProxyPool":"basic"}`,
				`{"url":"https://a.test","proxy_pool":"unblocker"}`,
				// An empty pool is no pool: the flags fill it in.
				`{"url":"https://a.test","proxy_pool":""}`,
				// basic agrees with asp false and reaches the line.
				`{"url":"https://a.test","asp":false}`,
				// asp true sets basic aside: no pool goes out.
				`{"url":"https://a.test","asp":true}`,
				`{"url":"https://a.test","CorrelationID":"mine"}`,
				`{"url":"https://a.test","CorrelationID":"mine-too","proxy_pool":"basic"}`,
			},
			want: map[string]string{
				"item-1": ultraBasic, "item-2": "", "item-3": "", "item-4": "", "item-5": "",
				"item-6": ultraBasic, "item-7": ultraBasic, "item-8": "", "mine": ultraBasic, "mine-too": "",
			},
		},
		{
			name:  "JSONL lines under --unblocker",
			flags: proxyPoolFlags{unblocker: true},
			stdin: []string{
				`{"url":"https://a.test"}`,
				`{"url":"https://a.test","proxy_pool":"unblocker"}`,
				`{"url":"https://a.test","unblocker":true}`,
				`{"url":"https://a.test","unblocker":false}`,
			},
			want: map[string]string{"item-1": ultra, "item-2": "", "item-3": ultra, "item-4": ""},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdin io.Reader = strings.NewReader("")
			if len(tc.stdin) > 0 {
				// bufio.Reader so isStdinPiped takes the JSONL branch.
				stdin = bufio.NewReader(strings.NewReader(strings.Join(tc.stdin, "\n") + "\n"))
			}
			configs, poolFromFlags, err := collectBatchConfigs(stdin, tc.urls, tc.urlFile, "", tc.flags, false)
			if err != nil {
				t.Fatalf("collectBatchConfigs: %v", err)
			}
			if len(configs) != len(tc.want) {
				t.Fatalf("got %d configs, want %d", len(configs), len(tc.want))
			}
			hint := batchUltraRefusalHints(poolFromFlags, tc.flags)
			for _, cfg := range configs {
				want, ok := tc.want[cfg.CorrelationID]
				if !ok {
					t.Fatalf("unexpected correlation ID %q", cfg.CorrelationID)
				}
				if got := hint(cfg.CorrelationID); got != want {
					t.Errorf("%s (proxy_pool=%q asp=%v): hint = %q, want %q", cfg.CorrelationID, string(cfg.ProxyPool), cfg.ASP, got, want)
				}
			}
		})
	}
}

// wireRequest is the proxy_pool and asp one scrape, batch config or crawl
// carried to the API, "" when absent.
type wireRequest struct{ pool, asp string }

// newWireAPI fakes the endpoints scrape, scrape batch, crawl start and crawl
// run call, recording what each request carried.
func newWireAPI(t *testing.T) (*httptest.Server, func() []wireRequest) {
	t.Helper()
	var (
		mu   sync.Mutex
		sent []wireRequest
	)
	record := func(pool, asp string) {
		mu.Lock()
		sent = append(sent, wireRequest{pool, asp})
		mu.Unlock()
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/scrape":
			record(r.URL.Query().Get("proxy_pool"), r.URL.Query().Get("asp"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"uuid":"00000000-0000-0000-0000-000000000000","config":{"url":"https://example.test"},` +
				`"context":{"cost":{"total":1,"details":[]}},` +
				`"result":{"success":true,"status":"DONE","status_code":200,"content":"<html></html>","content_type":"text/html","format":"text"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/scrape/batch":
			var body struct {
				Configs []map[string]string `json:"configs"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("batch body: %v", err)
			}
			for _, c := range body.Configs {
				record(c["proxy_pool"], c["asp"])
			}
			w.Header().Set("Content-Type", "multipart/mixed; boundary=end")
			_, _ = w.Write([]byte("--end--\r\n"))
		case r.Method == http.MethodPost && r.URL.Path == "/crawl":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("crawl body: %v", err)
			}
			pool, _ := body["proxy_pool"].(string)
			asp := ""
			if v, ok := body["asp"]; ok {
				asp = fmt.Sprint(v)
			}
			record(pool, asp)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"crawler_uuid":"crawl-1","status":"PENDING"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/crawl/crawl-1/status":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"crawler_uuid":"crawl-1","status":"DONE","is_finished":true,"is_success":true,` +
				`"state":{"urls_visited":0,"urls_extracted":0,"urls_failed":0,"urls_skipped":0,"urls_to_crawl":0,"api_credit_used":0,"duration":0}}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	take := func() []wireRequest {
		mu.Lock()
		defer mu.Unlock()
		got := sent
		sent = nil
		return got
	}
	return api, take
}

// --unblocker selects the Unblocker pool, --unblocker-ultra (or its alias
// --asp) the Unblocker Ultra pool and wins over --unblocker, a --proxy-pool
// word, auto included, wins over all three and goes out without asp, and
// beside any other pool name, tor included, any of them sends asp=true.
// Driven through each command against a fake API, so the wire is what is
// checked.
func TestPoolFlagsReachTheWire(t *testing.T) {
	cases := []struct {
		args     []string
		wantPool string
		wantASP  string
	}{
		{nil, "", ""},
		{[]string{"--unblocker"}, "unblocker", ""},
		{[]string{"--unblocker-ultra"}, "unblocker_ultra", ""},
		{[]string{"--asp"}, "unblocker_ultra", ""},
		{[]string{"--unblocker", "--unblocker-ultra"}, "unblocker_ultra", ""},
		{[]string{"--unblocker", "--asp"}, "unblocker_ultra", ""},
		{[]string{"--unblocker-ultra", "--asp"}, "unblocker_ultra", ""},
		{[]string{"--unblocker-ultra=false", "--unblocker"}, "unblocker", ""},
		{[]string{"--asp=false", "--unblocker"}, "unblocker", ""},
		{[]string{"--asp=false", "--unblocker-ultra"}, "", ""},
		{[]string{"--unblocker-ultra", "--asp=false"}, "", ""},
		{[]string{"--asp", "--unblocker-ultra=false"}, "unblocker_ultra", ""},
		{[]string{"--unblocker=false"}, "", ""},
		{[]string{"--asp=false"}, "", ""},
		{[]string{"--proxy-pool", "basic"}, "basic", ""},
		{[]string{"--proxy-pool", "basic", "--unblocker"}, "basic", ""},
		{[]string{"--unblocker-ultra", "--proxy-pool", "basic"}, "basic", ""},
		{[]string{"--proxy-pool", "basic", "--asp"}, "basic", ""},
		{[]string{"--proxy-pool", "unblocker", "--unblocker-ultra"}, "unblocker", ""},
		{[]string{"--proxy-pool", "unblocker_ultra", "--asp"}, "unblocker_ultra", ""},
		{[]string{"--proxy-pool", "Unblocker_Ultra", "--unblocker"}, "Unblocker_Ultra", ""},
		{[]string{"--proxy-pool", "auto"}, "auto", ""},
		{[]string{"--proxy-pool", "auto", "--unblocker"}, "auto", ""},
		{[]string{"--unblocker-ultra", "--proxy-pool", "auto"}, "auto", ""},
		{[]string{"--asp=false", "--proxy-pool", "auto"}, "auto", ""},
		{[]string{"--proxy-pool", "Auto", "--asp"}, "Auto", ""},
		{[]string{"--proxy-pool", "tor"}, "tor", ""},
		{[]string{"--proxy-pool", "tor", "--asp"}, "tor", "true"},
		{[]string{"--proxy-pool", "public_residential_pool"}, "public_residential_pool", ""},
		{[]string{"--proxy-pool", "public_residential_pool", "--unblocker"}, "public_residential_pool", "true"},
		{[]string{"--proxy-pool", "public_datacenter_pool", "--unblocker-ultra"}, "public_datacenter_pool", "true"},
		{[]string{"--proxy-pool", "proxy_saver:Mine", "--asp"}, "proxy_saver:Mine", "true"},
	}

	api, take := newWireAPI(t)
	flags := &rootFlags{apiKey: "test-key", host: api.URL}
	const target = "https://example.test"
	commands := map[string]func(args []string) (*cobra.Command, []string){
		"scrape": func(args []string) (*cobra.Command, []string) {
			return newScrapeCmd(flags), append([]string{target}, args...)
		},
		"batch": func(args []string) (*cobra.Command, []string) {
			return newBatchCmd(flags), append([]string{"--url", target}, args...)
		},
		"crawlStart": func(args []string) (*cobra.Command, []string) {
			return newCrawlStartCmd(flags), append([]string{target}, args...)
		},
		"crawlRun": func(args []string) (*cobra.Command, []string) {
			return newCrawlRunCmd(flags), append([]string{target}, args...)
		},
	}
	for cmdName, build := range commands {
		for _, tc := range cases {
			t.Run(cmdName+"/"+strings.Join(tc.args, " "), func(t *testing.T) {
				cmd, args := build(tc.args)
				cmd.SetArgs(args)
				var err error
				// Keep the envelope on stdout out of the test log.
				_, _ = captureStdio(t, func() { err = cmd.Execute() })
				if err != nil {
					t.Fatalf("%v: %v", args, err)
				}
				sent := take()
				if len(sent) != 1 {
					t.Fatalf("%v: %d requests carried a pool, want 1: %+v", args, len(sent), sent)
				}
				if want := (wireRequest{tc.wantPool, tc.wantASP}); sent[0] != want {
					t.Errorf("%v sent proxy_pool=%q asp=%q, want proxy_pool=%q asp=%q", args, sent[0].pool, sent[0].asp, want.pool, want.asp)
				}
			})
		}
	}
}

// ultraGateAPI fakes the API's Unblocker Ultra gate on /scrape and
// /scrape/batch: basic and unblocker are refused with
// ERR::PROXY::UNBLOCKER_ULTRA_REQUIRED (HTTP 422), every other request is
// served. A batch config whose URL ends in /api-error is refused in the shape
// of an API error part rather than of a scrape result.
func ultraGateAPI(t *testing.T) *httptest.Server {
	t.Helper()
	refused := func(pool string) bool {
		switch strings.ToLower(strings.TrimSpace(pool)) {
		case proxyPoolBasic, proxyPoolUnblocker:
			return true
		}
		return false
	}
	const message = "example.test accepts proxy_pool=unblocker_ultra only"
	refusal := `{"config":{"url":"https://example.test"},"context":{},"result":{"success":false,"status":"ERROR","status_code":422,` +
		`"error":{"code":"` + errUnblockerUltraRequired + `","message":"` + message + `","http_code":422,"retryable":false}}}`
	apiErrorPart := `{"code":"` + errUnblockerUltraRequired + `","message":"` + message + `","http_code":422,"retryable":false}`
	done := `{"uuid":"00000000-0000-0000-0000-000000000000","config":{"url":"https://example.test"},` +
		`"context":{"cost":{"total":1,"details":[]}},` +
		`"result":{"success":true,"status":"DONE","status_code":200,"content":"<html></html>","content_type":"text/html","format":"text"}}`

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/scrape":
			q := r.URL.Query()
			switch {
			case !refused(q.Get("proxy_pool")):
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(done))
			case q.Get("proxified_response") == "true":
				w.Header().Set("X-Scrapfly-Reject-Code", errUnblockerUltraRequired)
				w.Header().Set("X-Scrapfly-Reject-Description", message)
				w.WriteHeader(http.StatusUnprocessableEntity)
			default:
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(refusal))
			}
		case r.Method == http.MethodPost && r.URL.Path == "/scrape/batch":
			var body struct {
				Configs []map[string]string `json:"configs"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("batch body: %v", err)
			}
			parts := multipart.NewWriter(w)
			w.Header().Set("Content-Type", "multipart/mixed; boundary="+parts.Boundary())
			for _, c := range body.Configs {
				payload, status := done, "200"
				if refused(c["proxy_pool"]) {
					payload, status = refusal, "422"
					if strings.HasSuffix(c["url"], "/api-error") {
						payload = apiErrorPart
					}
				}
				part, err := parts.CreatePart(textproto.MIMEHeader{
					"Content-Type":              {"application/json"},
					"X-Scrapfly-Correlation-Id": {c["correlation_id"]},
					"X-Scrapfly-Scrape-Status":  {status},
				})
				if err != nil {
					t.Errorf("batch part: %v", err)
					return
				}
				_, _ = part.Write([]byte(payload))
			}
			_ = parts.Close()
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	return api
}

// ndjsonEnvelope is one line of scrape or batch ndjson, with the batch data
// fields the hint test reads.
type ndjsonEnvelope struct {
	Success bool               `json:"success"`
	Error   *out.EnvelopeError `json:"error"`
	Data    struct {
		CorrelationID string `json:"correlation_id"`
		Hint          string `json:"hint"`
		Result        struct {
			Result struct {
				Error *struct {
					Code string `json:"code"`
				} `json:"error"`
			} `json:"result"`
		} `json:"result"`
	} `json:"data"`
}

// ultraRefused reports whether a batch envelope carries the Unblocker Ultra
// refusal, as an error part or as a scrape result.
func (env ndjsonEnvelope) ultraRefused() bool {
	if env.Error != nil {
		return strings.Contains(env.Error.Message, errUnblockerUltraRequired)
	}
	return env.Data.Result.Result.Error != nil && env.Data.Result.Result.Error.Code == errUnblockerUltraRequired
}

func ndjsonEnvelopes(t *testing.T, stdout string) []ndjsonEnvelope {
	t.Helper()
	var envs []ndjsonEnvelope
	dec := json.NewDecoder(strings.NewReader(stdout))
	for dec.More() {
		var env ndjsonEnvelope
		if err := dec.Decode(&env); err != nil {
			t.Fatalf("stdout is not ndjson envelopes: %v\n%s", err, stdout)
		}
		envs = append(envs, env)
	}
	return envs
}

// The API's ERR::PROXY::UNBLOCKER_ULTRA_REQUIRED reaches the error output with
// its own code and message plus a one-line hint naming the flag to retry
// with. A pool word given to --proxy-pool wins over --unblocker-ultra, so the
// hint says to drop it.
func TestUltraRefusalHint(t *testing.T) {
	api := ultraGateAPI(t)
	flags := &rootFlags{apiKey: "test-key", host: api.URL}
	const target = "https://example.test"

	t.Run("scrape", func(t *testing.T) {
		for _, tc := range []struct {
			args     []string
			wantHint string // "" when the scrape is served
		}{
			{[]string{"--unblocker"}, "retry with --unblocker-ultra"},
			{[]string{"--unblocker", "--proxified"}, "retry with --unblocker-ultra"},
			{[]string{"--proxy-pool", "unblocker"}, "retry with --unblocker-ultra in place of --proxy-pool unblocker"},
			{[]string{"--proxy-pool", "basic", "--unblocker-ultra"}, "retry with --unblocker-ultra in place of --proxy-pool basic"},
			{nil, ""},
			{[]string{"--unblocker-ultra"}, ""},
			{[]string{"--asp"}, ""},
			{[]string{"--unblocker", "--unblocker-ultra"}, ""},
			{[]string{"--proxy-pool", "auto"}, ""},
			{[]string{"--proxy-pool", "auto", "--unblocker"}, ""},
		} {
			cmd := newScrapeCmd(flags)
			cmd.SetArgs(append([]string{target}, tc.args...))
			var err error
			_, _ = captureStdio(t, func() { err = cmd.Execute() })
			if tc.wantHint == "" {
				if err != nil {
					t.Errorf("%v: %v", tc.args, err)
				}
				continue
			}
			if err == nil {
				t.Fatalf("%v: served, want the refusal", tc.args)
			}
			var buf bytes.Buffer
			if err := out.WriteError(&buf, false, "", err); err != nil {
				t.Fatal(err)
			}
			var env out.Envelope
			if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
				t.Fatalf("%v: %v", tc.args, err)
			}
			if env.Error == nil || env.Error.Code != errUnblockerUltraRequired || env.Error.HTTPStatus != http.StatusUnprocessableEntity ||
				!strings.Contains(env.Error.Message, "example.test accepts proxy_pool=unblocker_ultra only") {
				t.Errorf("%v: error = %+v, want the API's code, status and message", tc.args, env.Error)
			}
			if env.Error != nil && env.Error.Hint != tc.wantHint {
				t.Errorf("%v: hint = %q, want %q", tc.args, env.Error.Hint, tc.wantHint)
			}
		}
	})

	// What execute writes to stderr is the envelope an agent reads.
	t.Run("root error output", func(t *testing.T) {
		t.Setenv("SCRAPFLY_NO_UPDATE_CHECK", "1")
		var err error
		stdout, stderr := captureStdio(t, func() {
			err = execute([]string{"--api-key", "test-key", "--host", api.URL, "scrape", target, "--unblocker"})
		})
		if err == nil {
			t.Fatal("served, want the refusal")
		}
		if stdout != "" {
			t.Errorf("stdout = %q, want the error on stderr only", stdout)
		}
		var env out.Envelope
		if err := json.Unmarshal([]byte(stderr), &env); err != nil {
			t.Fatalf("stderr is not an envelope: %v\n%s", err, stderr)
		}
		if env.Error == nil || env.Error.Code != errUnblockerUltraRequired || env.Error.Hint != "retry with --unblocker-ultra" {
			t.Errorf("stderr envelope error = %+v", env.Error)
		}
	})

	t.Run("scrape several URLs", func(t *testing.T) {
		cmd := newScrapeCmd(flags)
		cmd.SetArgs([]string{target, target + "/2", "--unblocker"})
		var err error
		stdout, _ := captureStdio(t, func() { err = cmd.Execute() })
		if err != nil {
			t.Fatal(err)
		}
		envs := ndjsonEnvelopes(t, stdout)
		if len(envs) != 2 {
			t.Fatalf("got %d envelopes, want 2:\n%s", len(envs), stdout)
		}
		for _, env := range envs {
			if env.Success || env.Error == nil || !strings.Contains(env.Error.Message, errUnblockerUltraRequired) || env.Error.Hint != "retry with --unblocker-ultra" {
				t.Errorf("envelope = %+v, want the refusal with its hint", env)
			}
		}
	})

	t.Run("batch", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			args    []string
			stdin   string
			want    map[string]string // correlation ID -> hint, "" for none
			refused []string          // correlation IDs the gate refuses
		}{
			{
				name:    "the flags' pool",
				args:    []string{"--url", target, "--url", target + "/api-error", "--unblocker"},
				want:    map[string]string{"item-1": "retry with --unblocker-ultra", "item-2": "retry with --unblocker-ultra"},
				refused: []string{"item-1", "item-2"},
			},
			{
				name:    "a --proxy-pool word",
				args:    []string{"--url", target, "--proxy-pool", "basic", "--unblocker-ultra"},
				want:    map[string]string{"item-1": "retry with --unblocker-ultra in place of --proxy-pool basic"},
				refused: []string{"item-1"},
			},
			{
				name: "served",
				args: []string{"--url", target, "--unblocker-ultra"},
				want: map[string]string{"item-1": ""},
			},
			{
				// No flag changes a pool the line named itself.
				name:    "a line's own pool",
				args:    []string{"--proxy-pool", "basic"},
				stdin:   `{"url":"` + target + `","proxy_pool":"unblocker"}` + "\n" + `{"url":"` + target + `/2"}` + "\n",
				want:    map[string]string{"item-1": "", "item-2": "retry with --unblocker-ultra in place of --proxy-pool basic"},
				refused: []string{"item-1", "item-2"},
			},
			{
				// The line named the very pool the flags send: following the
				// hint would leave it there.
				name: "a line naming the --proxy-pool word",
				args: []string{"--proxy-pool", "basic"},
				stdin: `{"url":"` + target + `","proxy_pool":"basic"}` + "\n" +
					`{"url":"` + target + `/api-error","proxy_pool":"basic"}` + "\n" +
					`{"url":"` + target + `/api-error"}` + "\n",
				want:    map[string]string{"item-1": "", "item-2": "", "item-3": "retry with --unblocker-ultra in place of --proxy-pool basic"},
				refused: []string{"item-1", "item-2", "item-3"},
			},
			{
				// asp true agrees with --unblocker, whose pool reaches the line.
				name:    "a line naming the --unblocker pool",
				args:    []string{"--unblocker"},
				stdin:   `{"url":"` + target + `","proxy_pool":"unblocker"}` + "\n" + `{"url":"` + target + `/2","asp":true}` + "\n",
				want:    map[string]string{"item-1": "", "item-2": "retry with --unblocker-ultra"},
				refused: []string{"item-1", "item-2"},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				cmd := newBatchCmd(flags)
				cmd.SetArgs(tc.args)
				if tc.stdin != "" {
					cmd.SetIn(bufio.NewReader(strings.NewReader(tc.stdin)))
				}
				var err error
				stdout, _ := captureStdio(t, func() { err = cmd.Execute() })
				if err != nil {
					t.Fatal(err)
				}
				envs := ndjsonEnvelopes(t, stdout)
				if len(envs) != len(tc.want) {
					t.Fatalf("got %d envelopes, want %d:\n%s", len(envs), len(tc.want), stdout)
				}
				for _, env := range envs {
					hint := env.Data.Hint
					if env.Error != nil {
						hint = env.Error.Hint
					}
					want, ok := tc.want[env.Data.CorrelationID]
					if !ok {
						t.Fatalf("unexpected correlation ID %q:\n%s", env.Data.CorrelationID, stdout)
					}
					if refused := slices.Contains(tc.refused, env.Data.CorrelationID); env.ultraRefused() != refused {
						t.Errorf("%s: refused = %v, want %v:\n%s", env.Data.CorrelationID, !refused, refused, stdout)
					}
					if hint != want {
						t.Errorf("%s: hint = %q, want %q:\n%s", env.Data.CorrelationID, hint, want, stdout)
					}
				}
			})
		}
	})
}

// Only ERR::PROXY::UNBLOCKER_ULTRA_REQUIRED takes the hint. An API that knows
// no pool word answers ERR::PROXY::POOL_NOT_FOUND, which --unblocker-ultra
// would not cure.
func TestUltraRefusalHintOnlyOnThatRefusal(t *testing.T) {
	const code = "ERR::PROXY::POOL_NOT_FOUND"
	result := `{"config":{"url":"https://example.test"},"context":{},"result":{"success":false,"status":"ERROR","status_code":400,` +
		`"error":{"code":"` + code + `","message":"pool not found","http_code":400,"retryable":false}}}`
	apiErrorPart := `{"code":"` + code + `","message":"pool not found","http_code":400,"retryable":false}`
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/scrape":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(result))
		case r.Method == http.MethodPost && r.URL.Path == "/scrape/batch":
			var body struct {
				Configs []map[string]string `json:"configs"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("batch body: %v", err)
			}
			parts := multipart.NewWriter(w)
			w.Header().Set("Content-Type", "multipart/mixed; boundary="+parts.Boundary())
			for _, c := range body.Configs {
				payload := result
				if strings.HasSuffix(c["url"], "/api-error") {
					payload = apiErrorPart
				}
				part, err := parts.CreatePart(textproto.MIMEHeader{
					"Content-Type":              {"application/json"},
					"X-Scrapfly-Correlation-Id": {c["correlation_id"]},
					"X-Scrapfly-Scrape-Status":  {"400"},
				})
				if err != nil {
					t.Errorf("batch part: %v", err)
					return
				}
				_, _ = part.Write([]byte(payload))
			}
			_ = parts.Close()
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	flags := &rootFlags{apiKey: "test-key", host: api.URL}
	const target = "https://example.test"

	t.Run("scrape", func(t *testing.T) {
		cmd := newScrapeCmd(flags)
		cmd.SetArgs([]string{target, "--proxy-pool", "basic"})
		var err error
		_, _ = captureStdio(t, func() { err = cmd.Execute() })
		if err == nil {
			t.Fatal("served, want the error")
		}
		var buf bytes.Buffer
		if err := out.WriteError(&buf, false, "", err); err != nil {
			t.Fatal(err)
		}
		var env out.Envelope
		if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		if env.Error == nil || env.Error.Code != code || env.Error.Hint != "" {
			t.Errorf("error = %+v, want %s without a hint", env.Error, code)
		}
	})

	t.Run("scrape several URLs", func(t *testing.T) {
		cmd := newScrapeCmd(flags)
		cmd.SetArgs([]string{target, target + "/2", "--unblocker"})
		var err error
		stdout, _ := captureStdio(t, func() { err = cmd.Execute() })
		if err != nil {
			t.Fatal(err)
		}
		envs := ndjsonEnvelopes(t, stdout)
		if len(envs) != 2 {
			t.Fatalf("got %d envelopes, want 2:\n%s", len(envs), stdout)
		}
		for _, env := range envs {
			if env.Error == nil || !strings.Contains(env.Error.Message, code) || env.Error.Hint != "" {
				t.Errorf("envelope = %+v, want %s without a hint", env, code)
			}
		}
	})

	t.Run("batch", func(t *testing.T) {
		cmd := newBatchCmd(flags)
		cmd.SetArgs([]string{"--url", target, "--url", target + "/api-error", "--proxy-pool", "basic"})
		var err error
		stdout, _ := captureStdio(t, func() { err = cmd.Execute() })
		if err != nil {
			t.Fatal(err)
		}
		envs := ndjsonEnvelopes(t, stdout)
		if len(envs) != 2 {
			t.Fatalf("got %d envelopes, want 2:\n%s", len(envs), stdout)
		}
		for _, env := range envs {
			hint, carried := env.Data.Hint, false
			if env.Error != nil {
				hint, carried = env.Error.Hint, strings.Contains(env.Error.Message, code)
			} else if e := env.Data.Result.Result.Error; e != nil {
				carried = e.Code == code
			}
			if !carried || hint != "" {
				t.Errorf("%s: carried %s = %v, hint = %q, want it without a hint:\n%s", env.Data.CorrelationID, code, carried, hint, stdout)
			}
		}
	})
}
