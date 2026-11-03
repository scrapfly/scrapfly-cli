package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	scrapfly "github.com/scrapfly/go-scrapfly"
	"github.com/spf13/cobra"
)

const answerSearch = `{"completeness":"exact","crawls_requested":1,"crawls_searched":1,"crawls_pruned_exact":0,"skipped":[]}`

// runCrawlCmd runs a crawl subcommand against a JSON stub, and returns the request body it sent and what it printed.
func runCrawlCmd(t *testing.T, build func(*rootFlags) *cobra.Command, reply string, args ...string) (map[string]any, map[string]any, error) {
	t.Helper()

	var sent map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &sent)
		sent["_path"] = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
	defer server.Close()

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	realStdout := os.Stdout
	os.Stdout = write
	defer func() { os.Stdout = realStdout }()

	cmd := build(&rootFlags{apiKey: "test-key", host: server.URL})
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	execErr := cmd.Execute()

	_ = write.Close()
	captured, _ := io.ReadAll(read)
	os.Stdout = realStdout

	var printed map[string]any
	if len(captured) > 0 {
		if err := json.Unmarshal(captured, &printed); err != nil {
			t.Fatalf("output is not one JSON envelope: %v\n%s", err, captured)
		}
	}
	return sent, printed, execErr
}

func TestCrawlPromptNoStreamAsksForOneObject(t *testing.T) {
	reply := `{"prompt":"q","answer":"Chocolate boxes.","sources":[],"sources_used":[],"sources_dropped":0,"truncated":false,"search":` + answerSearch + `,"api_credit":5}`
	sent, printed, err := runCrawlCmd(t, newCrawlPromptCmd, reply, "01HX7K4", "--prompt", "q", "--no-stream")
	if err != nil {
		t.Fatal(err)
	}
	if generation := sent["generation"].(map[string]any); generation["stream"] != false {
		t.Errorf("generation %v", generation)
	}
	data := printed["data"].(map[string]any)
	if printed["product"] != "crawl.prompt" || data["answer"] != "Chocolate boxes." || data["api_credit"] != float64(5) {
		t.Errorf("envelope %v", printed)
	}
	if data["search"].(map[string]any)["completeness"] != "exact" {
		t.Errorf("the no-stream envelope lost the completeness: %v", data)
	}
}

func TestCrawlExtractSendsTheSchema(t *testing.T) {
	reply := `{"prompt":"products","data":{"products":[{"name":"Box","price":9.99}]},"sources":[],"sources_used":[],"sources_dropped":0,"truncated":false,"search":` + answerSearch + `}`
	sent, printed, err := runCrawlCmd(t, newCrawlExtractCmd, reply,
		"01HX7K4", "01HX7M9", "--prompt", "products", "--schema", `{"type":"object","required":["products"]}`, "--limit", "20")
	if err != nil {
		t.Fatal(err)
	}
	if sent["_path"] != "/crawl/extract" || sent["schema"].(map[string]any)["type"] != "object" {
		t.Errorf("request %v", sent)
	}
	if ids := sent["crawl_ids"].([]any); len(ids) != 2 || sent["search"].(map[string]any)["limit"] != float64(20) {
		t.Errorf("request %v", sent)
	}
	data := printed["data"].(map[string]any)
	if printed["product"] != "crawl.extract" || data["data"].(map[string]any)["products"] == nil {
		t.Errorf("envelope %v", printed)
	}
}

func TestCrawlExtractReadsTheSchemaFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "products.schema.json")
	if err := os.WriteFile(path, []byte(`{"type":"object","properties":{"products":{"type":"array"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	reply := `{"prompt":"p","data":{},"sources":[],"sources_used":[],"sources_dropped":0,"truncated":false,"search":` + answerSearch + `}`
	sent, _, err := runCrawlCmd(t, newCrawlExtractCmd, reply, "01HX7K4", "--prompt", "p", "--schema-file", path)
	if err != nil {
		t.Fatal(err)
	}
	if sent["schema"].(map[string]any)["properties"] == nil {
		t.Errorf("schema file not sent: %v", sent)
	}
}

func TestCrawlExtractRejectsBadInputBeforeAnyRequest(t *testing.T) {
	for name, args := range map[string][]string{
		"no prompt":          {"01HX7K4", "--schema", `{"type":"object"}`},
		"no schema":          {"01HX7K4", "--prompt", "p"},
		"both schema inputs": {"01HX7K4", "--prompt", "p", "--schema", `{"type":"object"}`, "--schema-file", "x.json"},
		"schema not json":    {"01HX7K4", "--prompt", "p", "--schema", `{type: object}`},
		"schema not object":  {"01HX7K4", "--prompt", "p", "--schema", `["a"]`},
	} {
		sent, _, err := runCrawlCmd(t, newCrawlExtractCmd, `{}`, args...)
		if !errors.Is(err, scrapfly.ErrCrawlerConfig) {
			t.Errorf("%s: expected ErrCrawlerConfig, got %v", name, err)
		}
		if sent != nil {
			t.Errorf("%s: a request was sent: %v", name, sent)
		}
	}
}
