package out

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	scrapfly "github.com/scrapfly/go-scrapfly"
)

func TestWriteSuccess_JSONEnvelope(t *testing.T) {
	var buf bytes.Buffer
	data := map[string]any{"hello": "world"}
	if err := WriteSuccess(&buf, false, "scrape", data); err != nil {
		t.Fatal(err)
	}
	var env Envelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("parse envelope: %v", err)
	}
	if !env.Success || env.Product != "scrape" {
		t.Fatalf("unexpected envelope: %+v", env)
	}
}

func TestWriteSuccess_PrettyModeIsSilent(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteSuccess(&buf, true, "scrape", map[string]any{"x": 1}); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("pretty mode should emit nothing (callers supply their own summary); got %q", buf.String())
	}
}

func TestWriteError_MapsAPIError(t *testing.T) {
	var buf bytes.Buffer
	sdkErr := &scrapfly.APIError{
		Message:        "forbidden",
		Code:           "ERR::SCRAPE::FORBIDDEN",
		HTTPStatusCode: 403,
		Retryable:      false,
		Hint:           "check your api key",
	}
	if err := WriteError(&buf, false, "scrape", sdkErr); err != nil {
		t.Fatal(err)
	}
	var env Envelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if env.Success || env.Error == nil {
		t.Fatalf("expected failure envelope, got %+v", env)
	}
	if env.Error.Code != "ERR::SCRAPE::FORBIDDEN" {
		t.Errorf("code: got %q want ERR::SCRAPE::FORBIDDEN", env.Error.Code)
	}
	if env.Error.HTTPStatus != 403 {
		t.Errorf("http_status: got %d want 403", env.Error.HTTPStatus)
	}
	if env.Error.Hint != "check your api key" {
		t.Errorf("hint: got %q", env.Error.Hint)
	}
}

func TestWriteError_MapsSentinels(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"bad key", scrapfly.ErrBadAPIKey, "ERR::CLI::BAD_API_KEY"},
		{"scrape cfg", scrapfly.ErrScrapeConfig, "ERR::CLI::SCRAPE_CONFIG"},
		{"extract cfg", scrapfly.ErrExtractionConfig, "ERR::CLI::EXTRACTION_CONFIG"},
		{"crawl cfg", scrapfly.ErrCrawlerConfig, "ERR::CLI::CRAWLER_CONFIG"},
		{"quota", scrapfly.ErrQuotaLimitReached, "ERR::CLI::QUOTA_LIMIT"},
		{"rate limit", scrapfly.ErrTooManyRequests, "ERR::CLI::RATE_LIMITED"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WriteError(&buf, false, "scrape", c.err); err != nil {
				t.Fatal(err)
			}
			var env Envelope
			if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
				t.Fatalf("parse: %v", err)
			}
			if env.Error == nil || env.Error.Code != c.code {
				t.Fatalf("code: got %q want %q (err=%v)", env.Error.Code, c.code, env.Error)
			}
		})
	}
}

func TestWriteError_UnknownError(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteError(&buf, false, "scrape", errors.New("something went wrong")); err != nil {
		t.Fatal(err)
	}
	var env Envelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Error.Message != "something went wrong" {
		t.Errorf("message passthrough: got %q", env.Error.Message)
	}
	if env.Error.Code != "" {
		t.Errorf("should have no code for unknown errors; got %q", env.Error.Code)
	}
}
