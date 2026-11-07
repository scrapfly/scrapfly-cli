package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scrapfly/scrapfly-cli/internal/out"
)

func screenshotAPI(t *testing.T, status int, contentType, body string) *rootFlags {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/screenshot" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(api.Close)
	return &rootFlags{apiKey: "test-key", host: api.URL}
}

const screenshotEnqueued = `{"success":true,"webhook_enqueued":true,"job_uuid":"job-1","webhook_name":"my-webhook"}`

func TestScreenshotWithAWebhookPrintsItsJobAndWritesNothing(t *testing.T) {
	flags := screenshotAPI(t, http.StatusCreated, "application/json; charset=utf-8", screenshotEnqueued)
	flags.outputPath = filepath.Join(t.TempDir(), "shot.png")
	cmd := newScreenshotCmd(flags)
	cmd.SetArgs([]string{"https://web-scraping.dev/product/1", "--webhook", "my-webhook"})

	var err error
	stdout, _ := captureStdio(t, func() { err = cmd.Execute() })

	if err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if _, statErr := os.Stat(flags.outputPath); !os.IsNotExist(statErr) {
		t.Fatalf("wrote %s for a screenshot delivered to its webhook", flags.outputPath)
	}
	var env out.Envelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("envelope %q: %v", stdout, err)
	}
	data, _ := env.Data.(map[string]any)
	if !env.Success || data["job_uuid"] != "job-1" || data["webhook_enqueued"] != true {
		t.Fatalf("envelope = %+v", env)
	}
}

func TestScreenshotWithAWebhookPrettyNamesTheJob(t *testing.T) {
	flags := screenshotAPI(t, http.StatusCreated, "application/json", screenshotEnqueued)
	flags.pretty = true
	cmd := newScreenshotCmd(flags)
	cmd.SetArgs([]string{"https://web-scraping.dev/product/1", "--webhook", "my-webhook"})

	var err error
	stdout, _ := captureStdio(t, func() { err = cmd.Execute() })

	if err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if !strings.Contains(stdout, "my-webhook") || !strings.Contains(stdout, "job-1") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestScreenshotImageIsStillWritten(t *testing.T) {
	flags := screenshotAPI(t, http.StatusOK, "image/png", "\x89PNG")
	flags.outputPath = filepath.Join(t.TempDir(), "shot.png")
	cmd := newScreenshotCmd(flags)
	cmd.SetArgs([]string{"https://web-scraping.dev/product/1"})

	var err error
	_, _ = captureStdio(t, func() { err = cmd.Execute() })

	if err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	written, readErr := os.ReadFile(flags.outputPath)
	if readErr != nil || string(written) != "\x89PNG" {
		t.Fatalf("wrote %q (%v)", written, readErr)
	}
}
