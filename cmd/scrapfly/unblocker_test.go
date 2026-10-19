package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

// commandsWithUnblocker is every command that registers the pool flags. Each
// entry must behave identically, so they are driven by one table.
func commandsWithUnblocker() map[string]func() *cobra.Command {
	flags := &rootFlags{}
	return map[string]func() *cobra.Command{
		"scrape":     func() *cobra.Command { return newScrapeCmd(flags) },
		"batch":      func() *cobra.Command { return newBatchCmd(flags) },
		"crawlStart": func() *cobra.Command { return newCrawlStartCmd(flags) },
		"crawlRun":   func() *cobra.Command { return newCrawlRunCmd(flags) },
	}
}

// TestUnblockerUltraAndASPShareOneDestination pins the precedence contract:
// --asp and --unblocker-ultra write the same bool, an explicit false on either
// name switches it off, and a supplied --asp wins whatever the order on the
// command line. Two variables OR-ed together would make every explicit-false
// case come out true; one shared BoolVar destination would make the last case
// true. --unblocker is another pool, which neither name touches.
func TestUnblockerUltraAndASPShareOneDestination(t *testing.T) {
	cases := []struct {
		name          string
		args          []string
		want          string
		wantUnblocker string
	}{
		{"neither", nil, "false", "false"},
		{"unblocker-ultra only", []string{"--unblocker-ultra"}, "true", "false"},
		{"asp only", []string{"--asp"}, "true", "false"},
		{"asp explicit false", []string{"--asp=false"}, "false", "false"},
		{"unblocker-ultra explicit false", []string{"--unblocker-ultra=false"}, "false", "false"},
		{"unblocker only", []string{"--unblocker"}, "false", "true"},
		{"asp beside unblocker", []string{"--asp", "--unblocker"}, "true", "true"},
		{"asp=false beside unblocker", []string{"--asp=false", "--unblocker"}, "false", "true"},
		// Order does not decide it: a supplied --asp wins either way.
		{"asp then unblocker-ultra=false", []string{"--asp", "--unblocker-ultra=false"}, "true", "false"},
		{"unblocker-ultra then asp=false", []string{"--unblocker-ultra", "--asp=false"}, "false", "false"},
		{"asp=false then unblocker-ultra", []string{"--asp=false", "--unblocker-ultra"}, "false", "false"},
		{"unblocker-ultra=false then asp", []string{"--unblocker-ultra=false", "--asp"}, "true", "false"},
	}

	for cmdName, build := range commandsWithUnblocker() {
		for _, tc := range cases {
			t.Run(cmdName+"/"+tc.name, func(t *testing.T) {
				cmd := build()
				stdout, stderr := captureStdio(t, func() {
					if err := cmd.ParseFlags(tc.args); err != nil {
						t.Errorf("ParseFlags(%v): %v", tc.args, err)
					}
				})
				if stdout != "" || stderr != "" {
					t.Errorf("%v printed something: stdout=%q stderr=%q", tc.args, stdout, stderr)
				}

				ultra := cmd.Flags().Lookup("unblocker-ultra")
				if ultra == nil {
					t.Fatalf("%s has no --unblocker-ultra flag", cmdName)
				}
				asp := cmd.Flags().Lookup("asp")
				if asp == nil {
					t.Fatalf("%s dropped --asp; the name must keep working forever", cmdName)
				}
				if got := ultra.Value.String(); got != tc.want {
					t.Errorf("--unblocker-ultra = %s, want %s", got, tc.want)
				}
				if got := asp.Value.String(); got != tc.want {
					t.Errorf("--asp = %s, want %s (both names must read one destination)", got, tc.want)
				}
				if got := cmd.Flags().Lookup("unblocker").Value.String(); got != tc.wantUnblocker {
					t.Errorf("--unblocker = %s, want %s", got, tc.wantUnblocker)
				}
			})
		}
	}
}

// TestPoolFlagsInHelp pins what --help shows. Every pool flag is documented:
// none may carry pflag's deprecation, which hides a flag and prints
// "deprecated" on every use, and none may be hidden, which also drops it from
// shell completions (cobra's nonCompletableFlag reads Hidden||Deprecated).
// The help says plainly which flag to use when a site blocks, what it costs,
// what --unblocker answers there, and lists auto among the --proxy-pool words.
// Only the API's asp and unblocker parameters are deprecated, which the help
// leaves to the migration guide.
func TestPoolFlagsInHelp(t *testing.T) {
	for cmdName, build := range commandsWithUnblocker() {
		t.Run(cmdName, func(t *testing.T) {
			cmd := build()
			for _, name := range []string{"unblocker", "unblocker-ultra", "asp", "proxy-pool"} {
				flag := cmd.Flags().Lookup(name)
				if flag == nil {
					t.Fatalf("no --%s flag", name)
				}
				if flag.Hidden || flag.Deprecated != "" {
					t.Errorf("--%s hidden=%v deprecated=%q, want a documented flag", name, flag.Hidden, flag.Deprecated)
				}
			}

			usage := cmd.Flags().FlagUsages()
			for _, want := range []string{
				"--unblocker ", "--unblocker-ultra ", "--asp ",
				"proxy_pool=unblocker,", "proxy_pool=unblocker_ultra,",
				"the flag to use when a site blocks. Never refused",
				"refuses it with ERR::PROXY::UNBLOCKER_ULTRA_REQUIRED (HTTP 422, not billed): use --unblocker-ultra there",
				"alias of --unblocker-ultra",
				"basic (datacenter proxies, Unblocker off), unblocker, unblocker_ultra or auto (the API picks the pool from the target's profile)",
				"with asp=true if one of those flags is set; the migration guide covers asp: https://scrapfly.io/new-pricing#legacy-spellings",
			} {
				if !strings.Contains(usage, want) {
					t.Errorf("flag usage lacks %q:\n%s", want, usage)
				}
			}

			long := strings.Join(strings.Fields(cmd.Long), " ")
			for _, want := range []string{
				"when a site blocks, --unblocker-ultra is the flag to use",
				"is never refused, and costs the same as --unblocker except on a target or anti-bot protection with a price of its own, which it adds",
				"is refused there with ERR::PROXY::UNBLOCKER_ULTRA_REQUIRED (HTTP 422, not billed)",
				"--asp is an alias of --unblocker-ultra, and --proxy-pool basic|unblocker|unblocker_ultra|auto wins over all three.",
				"--proxy-pool auto lets the API pick the pool before the request, from the target's profile",
				"accepted on every target, the ones that require unblocker_ultra included, where their price is added, and is priced as the pool it runs on",
			} {
				if !strings.Contains(long, want) {
					t.Errorf("--help lacks %q:\n%s", want, cmd.Long)
				}
			}
			if strings.Contains(strings.ToLower(usage), "deprecat") {
				t.Errorf("a flag usage calls something deprecated:\n%s", usage)
			}
			for _, sentence := range strings.Split(long, ". ") {
				if s := strings.ToLower(sentence); strings.Contains(s, "deprecat") && strings.Contains(s, "--") {
					t.Errorf("--help calls a flag deprecated: %q", sentence)
				}
			}
		})
	}
}

// Every pool flag parses silently: stdout carries the JSON envelope, and none
// of them is deprecated.
func TestPoolFlagsPrintNoNotice(t *testing.T) {
	for _, args := range [][]string{{"--unblocker"}, {"--unblocker-ultra"}, {"--asp"}, {"--asp=false"}, {"--proxy-pool", "unblocker_ultra"}, {"--proxy-pool", "auto"}} {
		t.Run(args[0], func(t *testing.T) {
			cmd := newScrapeCmd(&rootFlags{})
			stdout, stderr := captureStdio(t, func() {
				if err := cmd.ParseFlags(args); err != nil {
					t.Errorf("ParseFlags: %v", err)
				}
			})
			if stdout != "" || stderr != "" {
				t.Errorf("%v printed something: stdout=%q stderr=%q", args, stdout, stderr)
			}
		})
	}
}

// TestBatchJSONLUnblocker covers the per-line batch format. A line naming
// either key decides the Unblocker for itself; the flag is a default for the
// lines that name neither, and sends its pool.
func TestBatchJSONLUnblocker(t *testing.T) {
	cases := []struct {
		name     string
		line     string
		flag     bool
		want     bool
		wantPool string
	}{
		{name: "unblocker true", line: `{"url":"https://a.test","unblocker":true}`, want: true},
		{name: "unblocker false", line: `{"url":"https://a.test","unblocker":false}`, flag: true, want: false},
		{name: "legacy asp true", line: `{"url":"https://a.test","asp":true}`, want: true},
		{name: "legacy asp false", line: `{"url":"https://a.test","asp":false}`, flag: true, want: false},
		{name: "asp wins over unblocker", line: `{"url":"https://a.test","asp":false,"unblocker":true}`, want: false},
		{name: "asp true wins", line: `{"url":"https://a.test","asp":true,"unblocker":false}`, want: true},
		{name: "neither, flag off", line: `{"url":"https://a.test"}`, want: false},
		{name: "neither, flag on", line: `{"url":"https://a.test"}`, flag: true, wantPool: "unblocker"},
		{name: "explicit null is absent", line: `{"url":"https://a.test","unblocker":null}`, flag: true, wantPool: "unblocker"},
		{name: "line true agrees with the flag", line: `{"url":"https://a.test","asp":true}`, flag: true, wantPool: "unblocker"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// bufio.Reader so isStdinPiped takes the JSONL branch.
			configs, _, err := collectBatchConfigs(bufio.NewReader(strings.NewReader(tc.line+"\n")), nil, "", "", proxyPoolFlags{unblocker: tc.flag}, false)
			if err != nil {
				t.Fatalf("collectBatchConfigs: %v", err)
			}
			if len(configs) != 1 {
				t.Fatalf("got %d configs, want 1", len(configs))
			}
			cfg := configs[0]
			// ASP is the only field the CLI writes: it is the one the SDK
			// serializes under the frozen "asp" wire key.
			if cfg.ASP != tc.want {
				t.Errorf("cfg.ASP = %v, want %v", cfg.ASP, tc.want)
			}
			if got := string(cfg.ProxyPool); got != tc.wantPool {
				t.Errorf("cfg.ProxyPool = %q, want %q", got, tc.wantPool)
			}
			if !unblockerFieldIsUnset(cfg) {
				t.Error("cfg.Unblocker survived decoding; the SDK's own fallback could then override the resolved value")
			}
			if cfg.URL != "https://a.test" {
				t.Errorf("cfg.URL = %q, want the line's URL (stripping a key must not drop the rest)", cfg.URL)
			}
			if cfg.CorrelationID != "item-1" {
				t.Errorf("cfg.CorrelationID = %q, want item-1", cfg.CorrelationID)
			}
		})
	}
}

func TestBatchJSONLBypassKeysKeepDocumentOrder(t *testing.T) {
	cases := []struct {
		name     string
		keys     string
		flag     bool
		want     bool
		wantPool string
	}{
		{name: "lowercase asp opt-out", keys: `"ASP":true,"asp":false`},
		{name: "uppercase asp opt-in", keys: `"asp":false,"ASP":true`, want: true},
		{name: "lowercase unblocker opt-out", keys: `"Unblocker":true,"unblocker":false`},
		{name: "uppercase unblocker opt-in", keys: `"unblocker":false,"Unblocker":true`, want: true},
		{name: "legacy decision beats alias", keys: `"ASP":true,"asp":false,"Unblocker":true`},
		{name: "null legacy falls back to alias", keys: `"ASP":false,"asp":null,"unblocker":true`, want: true},
		{name: "null alias falls back to flag", keys: `"Unblocker":false,"unblocker":null`, flag: true, wantPool: "unblocker"},
		{name: "duplicate key opt-out", keys: `"asp":true,"asp":false`},
		{name: "duplicate key opt-out beats the flag", keys: `"asp":true,"asp":false`, flag: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := `{"URL":"https://a.test",` + tc.keys + `}`
			// Repeated identical input must make the same billing decision.
			for range 256 {
				configs, _, err := collectBatchConfigs(bufio.NewReader(strings.NewReader(line)), nil, "", "", proxyPoolFlags{unblocker: tc.flag}, false)
				if err != nil {
					t.Fatal(err)
				}
				if len(configs) != 1 || configs[0].ASP != tc.want || string(configs[0].ProxyPool) != tc.wantPool || !unblockerFieldIsUnset(configs[0]) {
					t.Fatalf("JSONL %s: want bypass=%v pool=%q with no remaining alias, got %+v", line, tc.want, tc.wantPool, configs)
				}
			}
		})
	}
}

func TestBatchJSONLRejectsNonBooleanUnblocker(t *testing.T) {
	line := bufio.NewReader(strings.NewReader(`{"url":"https://a.test","unblocker":"yes"}` + "\n"))
	_, _, err := collectBatchConfigs(line, nil, "", "", proxyPoolFlags{}, false)
	if err == nil {
		t.Fatal("want an error for a non-boolean unblocker value")
	}
	if !strings.Contains(err.Error(), "unblocker") {
		t.Errorf("error = %v, want it to name the offending key", err)
	}
}

// TestResolveUnblockerPrecedence covers the shape used by the MCP tool
// arguments, where both names arrive as independent tri-states.
func TestResolveUnblockerPrecedence(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name           string
		asp, unblocker *bool
		want           bool
	}{
		{"neither", nil, nil, false},
		{"unblocker true", nil, &yes, true},
		{"unblocker false", nil, &no, false},
		{"asp true", &yes, nil, true},
		{"asp false", &no, nil, false},
		{"asp false beats unblocker true", &no, &yes, false},
		{"asp true beats unblocker false", &yes, &no, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveUnblocker(tc.asp, tc.unblocker); got != tc.want {
				t.Errorf("resolveUnblocker = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMcpArgsDeclareBothNames pins the closed-schema requirement: the MCP tool
// schemas are inferred from these structs, so a client pinned to an older
// version that still sends "asp" must keep validating.
func TestMcpArgsDeclareBothNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  reflect.Type
	}{
		{"mcpScrapeArgs", reflect.TypeOf(mcpScrapeArgs{})},
		{"mcpCrawlArgs", reflect.TypeOf(mcpCrawlArgs{})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, want := range []string{"asp", "unblocker"} {
				f, ok := fieldByJSONName(tc.typ, want)
				if !ok {
					t.Fatalf("%s declares no %q property", tc.name, want)
				}
				// A plain bool cannot express "absent", which is what the
				// precedence rule keys on.
				if f.Type.Kind() != reflect.Pointer || f.Type.Elem().Kind() != reflect.Bool {
					t.Errorf("%s.%s is %s, want *bool", tc.name, f.Name, f.Type)
				}
			}
		})
	}
}

func fieldByJSONName(t reflect.Type, name string) (reflect.StructField, bool) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag == name {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

// unblockerFieldIsUnset reports whether the SDK's tri-state Unblocker field is
// nil. Reflection because the CLI must keep building against published
// go-scrapfly versions that have no such field: production code deliberately
// never names it, so a compile-time reference here would defeat that.
func unblockerFieldIsUnset(cfg any) bool {
	v := reflect.ValueOf(cfg)
	for v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	f := v.FieldByName("Unblocker")
	if !f.IsValid() {
		return true
	}
	return f.Kind() != reflect.Pointer || f.IsNil()
}

// captureStdio swaps the process stdout/stderr for pipes around fn. cobra
// resolves os.Stderr at print time, so this is what actually proves that
// parsing a flag prints nothing.
func captureStdio(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()

	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = wOut, wErr
	defer func() { os.Stdout, os.Stderr = origOut, origErr }()

	outCh, errCh := make(chan string, 1), make(chan string, 1)
	go func() { b, _ := io.ReadAll(rOut); outCh <- string(b) }()
	go func() { b, _ := io.ReadAll(rErr); errCh <- string(b) }()

	fn()

	wOut.Close()
	wErr.Close()
	return <-outCh, <-errCh
}

// The tri-state names are *bool, which jsonschema-go renders as
// type ["null","boolean"]. `asp` is frozen and was declared "boolean" before
// the rename, so what the server publishes must still say "boolean" — the Go
// pointer is an implementation detail of the precedence rule, not something
// callers are being asked to send. Read back over a live session so a
// registration that forgets to pin the schema is caught too.
func TestMcpToolSchemasDeclareBooleanNames(t *testing.T) {
	ctx := t.Context()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "scrapfly", Version: version}, nil)
	registerScrapeTool(server, &rootFlags{})
	registerCrawlRunTool(server, &rootFlags{})

	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	defer serverSession.Close()
	clientSession, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer clientSession.Close()

	tools, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools.Tools) != 2 {
		t.Fatalf("published %d tools, want scrape and crawl_run", len(tools.Tools))
	}
	for _, tool := range tools.Tools {
		t.Run(tool.Name, func(t *testing.T) {
			// The published schema crosses the session as raw JSON.
			encoded, err := json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatalf("marshaling %s input schema: %v", tool.Name, err)
			}
			var schema jsonschema.Schema
			if err := json.Unmarshal(encoded, &schema); err != nil {
				t.Fatalf("decoding %s input schema: %v", tool.Name, err)
			}
			for _, name := range []string{"asp", "unblocker"} {
				property, ok := schema.Properties[name]
				if !ok {
					t.Fatalf("%s publishes no %q property", tool.Name, name)
				}
				if property.Type != "boolean" || len(property.Types) != 0 {
					t.Errorf("%s.%s is type %q%v, want boolean", tool.Name, name, property.Type, property.Types)
				}
			}
			// Closed schemas: an undeclared name is a hard call rejection, so
			// dropping either one strands pinned clients.
			if schema.AdditionalProperties == nil {
				t.Errorf("%s input schema is no longer closed; the freeze on \"asp\" assumes it is", tool.Name)
			}
		})
	}
}
