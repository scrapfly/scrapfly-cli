package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

// wireTarget is the URL the wire tests scrape; the API they call is a fake.
const wireTarget = "https://web-scraping.dev/products"

// unacceptedPools are pool names the API refuses with ERR::PROXY::POOL_NOT_FOUND.
var unacceptedPools = []string{"basic", "unblocker", "unblocker_ultra", "auto"}

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
			_, _ = w.Write([]byte(`{"uuid":"00000000-0000-0000-0000-000000000000","config":{"url":"` + wireTarget + `"},` +
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

// poolFlagValue returns the value given to --proxy-pool in args, "" when none.
func poolFlagValue(args []string) string {
	for i, arg := range args {
		if v, ok := strings.CutPrefix(arg, "--proxy-pool="); ok {
			return v
		}
		if arg == "--proxy-pool" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// --unblocker and --asp send asp=true and no proxy_pool, --proxy-pool is sent
// as given beside them, and no command sends a proxy_pool that --proxy-pool did
// not name. Driven through each command against a fake API.
func TestPoolFlagsReachTheWire(t *testing.T) {
	cases := []struct {
		args     []string
		wantPool string
		wantASP  string
	}{
		{nil, "", ""},
		{[]string{"--unblocker"}, "", "true"},
		{[]string{"--asp"}, "", "true"},
		{[]string{"--unblocker", "--asp"}, "", "true"},
		{[]string{"--unblocker=false"}, "", ""},
		{[]string{"--asp=false"}, "", ""},
		// A supplied --asp wins over --unblocker whatever the order.
		{[]string{"--asp=false", "--unblocker"}, "", ""},
		{[]string{"--unblocker", "--asp=false"}, "", ""},
		{[]string{"--asp", "--unblocker=false"}, "", "true"},
		{[]string{"--country", "us", "--unblocker"}, "", "true"},
		{[]string{"--proxy-pool", "public_datacenter_pool"}, "public_datacenter_pool", ""},
		{[]string{"--proxy-pool", "public_residential_pool"}, "public_residential_pool", ""},
		{[]string{"--proxy-pool", "public_residential_pool", "--unblocker"}, "public_residential_pool", "true"},
		{[]string{"--asp", "--proxy-pool=public_residential_pool"}, "public_residential_pool", "true"},
		{[]string{"--proxy-pool", "public_tor_pool"}, "public_tor_pool", ""},
		{[]string{"--proxy-pool", "my_own_pool", "--unblocker"}, "my_own_pool", "true"},
		// A name the user typed goes out as typed, whatever it is.
		{[]string{"--proxy-pool", "unblocker_ultra"}, "unblocker_ultra", ""},
		{[]string{"--proxy-pool", "auto", "--unblocker"}, "auto", "true"},
	}

	api, take := newWireAPI(t)
	flags := &rootFlags{apiKey: "test-key", host: api.URL}
	commands := map[string]func(args []string) (*cobra.Command, []string){
		"scrape": func(args []string) (*cobra.Command, []string) {
			return newScrapeCmd(flags), append([]string{wireTarget}, args...)
		},
		"batch": func(args []string) (*cobra.Command, []string) {
			return newBatchCmd(flags), append([]string{"--url", wireTarget}, args...)
		},
		"crawlStart": func(args []string) (*cobra.Command, []string) {
			return newCrawlStartCmd(flags), append([]string{wireTarget}, args...)
		},
		"crawlRun": func(args []string) (*cobra.Command, []string) {
			return newCrawlRunCmd(flags), append([]string{wireTarget}, args...)
		},
	}
	for cmdName, build := range commands {
		for _, tc := range cases {
			t.Run(cmdName+"/"+strings.Join(tc.args, " "), func(t *testing.T) {
				cmd, args := build(tc.args)
				cmd.SetArgs(args)
				var err error
				// Keeps the envelope and the --asp notice out of the test log.
				_, _ = captureStdio(t, func() { err = cmd.Execute() })
				if err != nil {
					t.Fatalf("%v: %v", args, err)
				}
				sent := take()
				if len(sent) != 1 {
					t.Fatalf("%v: %d requests, want 1: %+v", args, len(sent), sent)
				}
				if want := (wireRequest{tc.wantPool, tc.wantASP}); sent[0] != want {
					t.Errorf("%v sent proxy_pool=%q asp=%q, want proxy_pool=%q asp=%q", args, sent[0].pool, sent[0].asp, want.pool, want.asp)
				}
				if typed := poolFlagValue(tc.args); sent[0].pool != typed {
					t.Errorf("%v sent proxy_pool=%q, want only what --proxy-pool named (%q)", args, sent[0].pool, typed)
				}
			})
		}
	}
}

// --unblocker-ultra was never released: every pool command refuses it.
func TestUnblockerUltraFlagIsGone(t *testing.T) {
	for cmdName, build := range commandsWithUnblocker() {
		t.Run(cmdName, func(t *testing.T) {
			cmd := build()
			if cmd.Flags().Lookup("unblocker-ultra") != nil {
				t.Fatal("--unblocker-ultra is registered")
			}
			if err := cmd.ParseFlags([]string{"--unblocker-ultra"}); err == nil || !strings.Contains(err.Error(), "unknown flag") {
				t.Errorf("ParseFlags(--unblocker-ultra) = %v, want unknown flag", err)
			}
		})
	}
}

// The help of the pool commands and the MCP tools name only the pools the API
// accepts, and link no pricing migration page.
func TestHelpNamesOnlyAcceptedPools(t *testing.T) {
	t.Setenv("SCRAPFLY_NO_UPDATE_CHECK", "1")
	rootHelp, _ := captureStdio(t, func() {
		if err := execute([]string{"--help"}); err != nil {
			t.Errorf("--help: %v", err)
		}
	})
	if !strings.Contains(rootHelp, "scrapfly scrape") {
		t.Fatalf("root --help printed no examples: %q", rootHelp)
	}
	flags := &rootFlags{}
	texts := map[string]string{"root": rootHelp}
	for name, cmd := range map[string]*cobra.Command{
		"scrape":      newScrapeCmd(flags),
		"batch":       newBatchCmd(flags),
		"crawl start": newCrawlStartCmd(flags),
		"crawl run":   newCrawlRunCmd(flags),
		"mcp serve":   newMcpServeCmd(flags),
	} {
		texts[name] = cmd.Long + "\n" + cmd.Example + "\n" + cmd.Flags().FlagUsages()
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

	// A pool word on its own, not part of a flag name such as --auto-scroll.
	poolWord := regexp.MustCompile("(?:^|[\\s,(|=`'\"])(basic|auto|unblocker_ultra)(?:$|[\\s,.;:)|`'\"])")
	for name, text := range texts {
		flat := strings.ToLower(text)
		for _, banned := range []string{
			"unblocker-ultra", "unblocker_ultra", "unblocker ultra", "unblocker pool",
			"proxy_pool=unblocker", "proxy-pool unblocker",
			"new-pricing", "unblocker_ultra_required", "deprecated: use proxy_pool",
		} {
			if strings.Contains(flat, banned) {
				t.Errorf("%s says %q:\n%s", name, banned, text)
			}
		}
		if m := poolWord.FindStringSubmatch(text); m != nil {
			t.Errorf("%s names the pool %q:\n%s", name, m[1], text)
		}
	}
	for _, name := range []string{"scrape", "batch", "crawl start", "crawl run"} {
		for _, want := range []string{"public_datacenter_pool", "public_residential_pool", "public_tor_pool", "--unblocker", "asp=true"} {
			if !strings.Contains(texts[name], want) {
				t.Errorf("%s --help lacks %q:\n%s", name, want, texts[name])
			}
		}
	}
}

// The MCP proxy_pool input is a free string with no default, described by the
// pools the API accepts.
func TestMcpToolSchemasDeclareProxyPool(t *testing.T) {
	tools, err := mcpSession(t, &rootFlags{}).ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools.Tools) != 2 {
		t.Fatalf("published %d tools, want scrape and crawl_run", len(tools.Tools))
	}
	for _, tool := range tools.Tools {
		t.Run(tool.Name, func(t *testing.T) {
			encoded, err := json.Marshal(tool.InputSchema)
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
			if pool.Type != "string" || pool.Enum != nil || pool.Default != nil {
				t.Errorf("%s proxy_pool = type %q enum %v default %s, want a free string", tool.Name, pool.Type, pool.Enum, pool.Default)
			}
			for _, want := range []string{"public_datacenter_pool", "public_residential_pool", "public_tor_pool"} {
				if !strings.Contains(pool.Description, want) {
					t.Errorf("%s proxy_pool description lacks %s: %q", tool.Name, want, pool.Description)
				}
			}
		})
	}
}

// mcpScrapeAPI fakes GET /scrape for the MCP scrape tool and records the
// query of every call.
func mcpScrapeAPI(t *testing.T) (*httptest.Server, func() []wireRequest) {
	t.Helper()
	var (
		mu   sync.Mutex
		sent []wireRequest
	)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sent = append(sent, wireRequest{r.URL.Query().Get("proxy_pool"), r.URL.Query().Get("asp")})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"uuid":"00000000-0000-0000-0000-000000000000","config":{"url":"` + wireTarget + `"},` +
			`"context":{"cost":{"total":1,"details":[]}},` +
			`"result":{"success":true,"status":"DONE","status_code":200,"content":"<html></html>","content_type":"text/html","format":"text"}}`))
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

// The MCP scrape tool sends proxy_pool as given and unblocker/asp as asp,
// through a live session so schema validation runs as it does for a client.
func TestMcpScrapeToolSendsThePoolAsGiven(t *testing.T) {
	api, take := mcpScrapeAPI(t)
	session := mcpSession(t, &rootFlags{apiKey: "test-key", host: api.URL})

	for _, tc := range []struct {
		args map[string]any
		want wireRequest
	}{
		{map[string]any{"url": wireTarget}, wireRequest{"", ""}},
		{map[string]any{"url": wireTarget, "unblocker": true}, wireRequest{"", "true"}},
		{map[string]any{"url": wireTarget, "asp": true}, wireRequest{"", "true"}},
		{map[string]any{"url": wireTarget, "asp": true, "unblocker": false}, wireRequest{"", "true"}},
		{map[string]any{"url": wireTarget, "asp": false, "unblocker": true}, wireRequest{"", ""}},
		{map[string]any{"url": wireTarget, "proxy_pool": "public_residential_pool"}, wireRequest{"public_residential_pool", ""}},
		{map[string]any{"url": wireTarget, "proxy_pool": "public_residential_pool", "unblocker": true}, wireRequest{"public_residential_pool", "true"}},
		{map[string]any{"url": wireTarget, "proxy_pool": "public_datacenter_pool", "asp": true}, wireRequest{"public_datacenter_pool", "true"}},
		{map[string]any{"url": wireTarget, "proxy_pool": "public_tor_pool"}, wireRequest{"public_tor_pool", ""}},
		{map[string]any{"url": wireTarget, "proxy_pool": "my_own_pool", "unblocker": true}, wireRequest{"my_own_pool", "true"}},
	} {
		res, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "scrape", Arguments: tc.args})
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

	for name, args := range map[string]map[string]any{
		"a key the schema lacks":    {"url": wireTarget, "URL": "https://httpbin.dev/get"},
		"a non-boolean toggle":      {"url": wireTarget, "unblocker": "yes"},
		"a proxy_pool of no string": {"url": wireTarget, "proxy_pool": 1},
		"no url":                    {"proxy_pool": "public_residential_pool"},
	} {
		res, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "scrape", Arguments: args})
		if err == nil && !res.IsError {
			t.Errorf("%s was accepted: %v", name, args)
		}
		if sent := take(); len(sent) != 0 {
			t.Errorf("%s reached the API: %v", name, sent)
		}
	}
}

// crawl_run sends proxy_pool and asp the same way, against the wire.
func TestMcpCrawlRunToolSendsThePoolAsGiven(t *testing.T) {
	api, take := newWireAPI(t)
	session := mcpSession(t, &rootFlags{apiKey: "test-key", host: api.URL})
	for _, tc := range []struct {
		args map[string]any
		want wireRequest
	}{
		{map[string]any{"url": wireTarget}, wireRequest{"", ""}},
		{map[string]any{"url": wireTarget, "unblocker": true}, wireRequest{"", "true"}},
		{map[string]any{"url": wireTarget, "asp": false, "unblocker": true}, wireRequest{"", ""}},
		{map[string]any{"url": wireTarget, "proxy_pool": "public_tor_pool"}, wireRequest{"public_tor_pool", ""}},
		{map[string]any{"url": wireTarget, "proxy_pool": "public_residential_pool", "asp": true}, wireRequest{"public_residential_pool", "true"}},
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

// A JSONL line's "proxy_pool" binds to ProxyPool and wins over --proxy-pool.
func TestBatchJSONLProxyPool(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		flag string
		want string
	}{
		{name: "the line's pool", line: `{"url":"https://web-scraping.dev/product/1","proxy_pool":"public_residential_pool"}`, want: "public_residential_pool"},
		{name: "the line's pool wins over the flag", line: `{"url":"https://web-scraping.dev/product/1","proxy_pool":"public_tor_pool"}`, flag: "public_residential_pool", want: "public_tor_pool"},
		{name: "wins over a ProxyPool key", line: `{"url":"https://web-scraping.dev/product/1","ProxyPool":"public_datacenter_pool","proxy_pool":"public_residential_pool"}`, want: "public_residential_pool"},
		{name: "a ProxyPool key alone still binds", line: `{"url":"https://web-scraping.dev/product/1","ProxyPool":"public_residential_pool"}`, want: "public_residential_pool"},
		{name: "an empty pool takes the flag", line: `{"url":"https://web-scraping.dev/product/1","proxy_pool":""}`, flag: "public_residential_pool", want: "public_residential_pool"},
		{name: "no pool takes the flag", line: `{"url":"https://web-scraping.dev/product/1"}`, flag: "public_residential_pool", want: "public_residential_pool"},
		{name: "no pool anywhere", line: `{"url":"https://web-scraping.dev/product/1","asp":true}`, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configs, err := collectBatchConfigs(bufio.NewReader(strings.NewReader(tc.line+"\n")), nil, "", "", proxyPoolFlags{pool: tc.flag}, false)
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

// A JSONL line's own proxy_pool and asp each win over the flags, which fill in
// only what the line leaves out.
func TestBatchLineResolve(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name     string
		flags    proxyPoolFlags
		linePool string
		lineASP  *bool
		wantPool string
		wantASP  bool
	}{
		{"nothing anywhere", proxyPoolFlags{}, "", nil, "", false},
		{"bare line takes --unblocker", proxyPoolFlags{unblocker: true}, "", nil, "", true},
		{"bare line takes --proxy-pool", proxyPoolFlags{pool: "public_residential_pool"}, "", nil, "public_residential_pool", false},
		{"bare line takes both", proxyPoolFlags{pool: "public_residential_pool", unblocker: true}, "", nil, "public_residential_pool", true},
		{"line pool beats --proxy-pool", proxyPoolFlags{pool: "public_residential_pool", unblocker: true}, "public_tor_pool", nil, "public_tor_pool", true},
		{"line asp false beats --unblocker", proxyPoolFlags{unblocker: true}, "", &no, "", false},
		{"line asp true without the flag", proxyPoolFlags{}, "", &yes, "", true},
		{"line pool and asp beat both flags", proxyPoolFlags{pool: "public_residential_pool", unblocker: true}, "my_own_pool", &no, "my_own_pool", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool, asp := tc.flags.resolveLine(tc.linePool, tc.lineASP)
			if pool != tc.wantPool || asp != tc.wantASP {
				t.Errorf("resolveLine(%q, %v) under %+v = (%q, %v), want (%q, %v)",
					tc.linePool, tc.lineASP, tc.flags, pool, asp, tc.wantPool, tc.wantASP)
			}
		})
	}
}

// Every way of building a batch, the flags' and a line's own keys, leaves the
// pools the API refuses out unless a line or --proxy-pool named one.
func TestBatchSendsNoUnacceptedPool(t *testing.T) {
	for _, flags := range []proxyPoolFlags{{}, {unblocker: true}, {pool: "public_residential_pool", unblocker: true}} {
		configs, err := collectBatchConfigs(bufio.NewReader(strings.NewReader(strings.Join([]string{
			`{"url":"https://web-scraping.dev/product/1"}`,
			`{"url":"https://web-scraping.dev/product/1","asp":true}`,
			`{"url":"https://web-scraping.dev/product/1","unblocker":false}`,
			`{"url":"https://web-scraping.dev/product/1","proxy_pool":"public_tor_pool","asp":true}`,
		}, "\n")+"\n")), nil, "", "", flags, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, cfg := range configs {
			if slices.Contains(unacceptedPools, string(cfg.ProxyPool)) {
				t.Errorf("flags %+v: %s sent proxy_pool=%q", flags, cfg.CorrelationID, cfg.ProxyPool)
			}
		}
	}
}
