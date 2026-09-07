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

// commandsWithUnblocker is every command that registers the anti-bot toggle.
// Each entry must behave identically, so they are driven by one table.
func commandsWithUnblocker() map[string]func() *cobra.Command {
	flags := &rootFlags{}
	return map[string]func() *cobra.Command{
		"scrape":     func() *cobra.Command { return newScrapeCmd(flags) },
		"batch":      func() *cobra.Command { return newBatchCmd(flags) },
		"crawlStart": func() *cobra.Command { return newCrawlStartCmd(flags) },
		"crawlRun":   func() *cobra.Command { return newCrawlRunCmd(flags) },
	}
}

// TestUnblockerAndASPShareOneDestination pins the precedence contract: both
// names write the same bool, an explicit false on either name switches the
// feature off, and a supplied --asp wins whatever the order on the command
// line. Two variables OR-ed together would make every explicit-false case come
// out true; one shared BoolVar destination would make the last case true.
func TestUnblockerAndASPShareOneDestination(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"neither", nil, "false"},
		{"unblocker only", []string{"--unblocker"}, "true"},
		{"legacy asp only", []string{"--asp"}, "true"},
		{"legacy asp explicit false", []string{"--asp=false"}, "false"},
		{"unblocker explicit false", []string{"--unblocker=false"}, "false"},
		// Order does not decide it: a supplied --asp wins either way.
		{"asp then unblocker=false", []string{"--asp", "--unblocker=false"}, "true"},
		{"unblocker then asp=false", []string{"--unblocker", "--asp=false"}, "false"},
		{"asp=false then unblocker", []string{"--asp=false", "--unblocker"}, "false"},
		{"unblocker=false then asp", []string{"--unblocker=false", "--asp"}, "true"},
	}

	for cmdName, build := range commandsWithUnblocker() {
		for _, tc := range cases {
			t.Run(cmdName+"/"+tc.name, func(t *testing.T) {
				cmd := build()
				// ParseFlags writes the deprecation notice to os.Stderr; keep
				// it out of the test log.
				_, _ = captureStdio(t, func() {
					if err := cmd.ParseFlags(tc.args); err != nil {
						t.Errorf("ParseFlags(%v): %v", tc.args, err)
					}
				})

				unblocker := cmd.Flags().Lookup("unblocker")
				if unblocker == nil {
					t.Fatalf("%s has no --unblocker flag", cmdName)
				}
				asp := cmd.Flags().Lookup("asp")
				if asp == nil {
					t.Fatalf("%s dropped --asp; the legacy name must keep working forever", cmdName)
				}
				if got := unblocker.Value.String(); got != tc.want {
					t.Errorf("--unblocker = %s, want %s", got, tc.want)
				}
				if got := asp.Value.String(); got != tc.want {
					t.Errorf("--asp = %s, want %s (both names must read one destination)", got, tc.want)
				}
			})
		}
	}
}

// TestASPFlagHiddenFromHelpAndCompletions asserts the two properties cobra
// derives from MarkDeprecated: Hidden keeps the flag out of --help, and
// Hidden||Deprecated is exactly the predicate cobra's completion code
// (nonCompletableFlag) uses to drop a flag from generated and dynamic shell
// completions.
func TestASPFlagHiddenFromHelpAndCompletions(t *testing.T) {
	for cmdName, build := range commandsWithUnblocker() {
		t.Run(cmdName, func(t *testing.T) {
			cmd := build()
			asp := cmd.Flags().Lookup("asp")
			if asp == nil {
				t.Fatal("no --asp flag")
			}
			if !asp.Hidden {
				t.Error("--asp is visible; it must not appear in --help or completions")
			}
			if asp.Deprecated == "" {
				t.Error("--asp carries no deprecation notice")
			}
			if unblocker := cmd.Flags().Lookup("unblocker"); unblocker == nil || unblocker.Hidden {
				t.Error("--unblocker must be present and visible")
			}

			usage := cmd.Flags().FlagUsages()
			if !strings.Contains(usage, "--unblocker") {
				t.Errorf("--unblocker missing from flag usage:\n%s", usage)
			}
			if strings.Contains(usage, "--asp") {
				t.Errorf("--asp still listed in flag usage:\n%s", usage)
			}
		})
	}
}

// TestASPDeprecationNoticeGoesToStderr guards the JSON envelope: stdout is
// machine-read, so a notice printed there would corrupt every piped
// `scrapfly ... | jq` invocation.
func TestASPDeprecationNoticeGoesToStderr(t *testing.T) {
	cmd := newScrapeCmd(&rootFlags{})
	stdout, stderr := captureStdio(t, func() {
		if err := cmd.ParseFlags([]string{"--asp"}); err != nil {
			t.Errorf("ParseFlags: %v", err)
		}
	})

	if stdout != "" {
		t.Errorf("deprecation notice leaked to stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "--asp has been deprecated") {
		t.Errorf("stderr = %q, want the --asp deprecation notice", stderr)
	}
	if !strings.Contains(stderr, "--unblocker") {
		t.Errorf("stderr = %q, want it to name the replacement flag", stderr)
	}
	if n := strings.Count(strings.TrimRight(stderr, "\n"), "\n"); n != 0 {
		t.Errorf("notice spans %d extra lines, want one line: %q", n+1, stderr)
	}
}

func TestUnblockerNoticeSilentWhenUnused(t *testing.T) {
	cmd := newScrapeCmd(&rootFlags{})
	stdout, stderr := captureStdio(t, func() {
		if err := cmd.ParseFlags([]string{"--unblocker"}); err != nil {
			t.Errorf("ParseFlags: %v", err)
		}
	})
	if stdout != "" || stderr != "" {
		t.Errorf("--unblocker printed something: stdout=%q stderr=%q", stdout, stderr)
	}
}

// TestBatchJSONLUnblocker covers the per-line batch format. The flag supplies
// a default only for lines that name neither key.
func TestBatchJSONLUnblocker(t *testing.T) {
	cases := []struct {
		name string
		line string
		flag bool
		want bool
	}{
		{name: "unblocker true", line: `{"url":"https://a.test","unblocker":true}`, want: true},
		{name: "unblocker false", line: `{"url":"https://a.test","unblocker":false}`, flag: true, want: false},
		{name: "legacy asp true", line: `{"url":"https://a.test","asp":true}`, want: true},
		{name: "legacy asp false", line: `{"url":"https://a.test","asp":false}`, flag: true, want: false},
		{name: "asp wins over unblocker", line: `{"url":"https://a.test","asp":false,"unblocker":true}`, want: false},
		{name: "asp true wins", line: `{"url":"https://a.test","asp":true,"unblocker":false}`, want: true},
		{name: "neither, flag off", line: `{"url":"https://a.test"}`, want: false},
		{name: "neither, flag on", line: `{"url":"https://a.test"}`, flag: true, want: true},
		{name: "explicit null is absent", line: `{"url":"https://a.test","unblocker":null}`, flag: true, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// bufio.Reader so isStdinPiped takes the JSONL branch.
			configs, err := collectBatchConfigs(bufio.NewReader(strings.NewReader(tc.line+"\n")), nil, "", "", "", false, tc.flag)
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
		name string
		keys string
		flag bool
		want bool
	}{
		{name: "lowercase asp opt-out", keys: `"ASP":true,"asp":false`},
		{name: "uppercase asp opt-in", keys: `"asp":false,"ASP":true`, want: true},
		{name: "lowercase unblocker opt-out", keys: `"Unblocker":true,"unblocker":false`},
		{name: "uppercase unblocker opt-in", keys: `"unblocker":false,"Unblocker":true`, want: true},
		{name: "legacy decision beats alias", keys: `"ASP":true,"asp":false,"Unblocker":true`},
		{name: "null legacy falls back to alias", keys: `"ASP":false,"asp":null,"unblocker":true`, want: true},
		{name: "null alias falls back to flag", keys: `"Unblocker":false,"unblocker":null`, flag: true, want: true},
		{name: "duplicate key opt-out", keys: `"asp":true,"asp":false`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := `{"URL":"https://a.test",` + tc.keys + `}`
			// Repeated identical input must make the same billing decision.
			for range 256 {
				configs, err := collectBatchConfigs(bufio.NewReader(strings.NewReader(line)), nil, "", "", "", false, tc.flag)
				if err != nil {
					t.Fatal(err)
				}
				if len(configs) != 1 || configs[0].ASP != tc.want || !unblockerFieldIsUnset(configs[0]) {
					t.Fatalf("JSONL %s: want bypass=%v with no remaining alias, got %+v", line, tc.want, configs)
				}
			}
		})
	}
}

func TestBatchJSONLRejectsNonBooleanUnblocker(t *testing.T) {
	line := bufio.NewReader(strings.NewReader(`{"url":"https://a.test","unblocker":"yes"}` + "\n"))
	_, err := collectBatchConfigs(line, nil, "", "", "", false, false)
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
// resolves os.Stderr at print time, so this is what actually proves where the
// deprecation notice lands.
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
