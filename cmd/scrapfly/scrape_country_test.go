package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestScrapeCountryListsReachAPI(t *testing.T) {
	for _, country := range []string{"us", "BR,pt,es", "-us,-ca", "us:1,ca:2", "us,-ca"} {
		t.Run(country, func(t *testing.T) {
			var requests atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != "/scrape" || r.URL.Query().Get("country") != strings.ToLower(country) {
					t.Errorf("unexpected path/country: %s %q", r.URL.Path, r.URL.Query().Get("country"))
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"uuid":"00000000-0000-0000-0000-000000000000","config":{"url":"https://web-scraping.dev"},"context":{"cost":{"total":0,"details":[]}},"result":{"success":true,"status":"DONE","status_code":200,"content":"synthetic","content_type":"text/plain","format":"text"}}`))
			}))
			defer api.Close()
			cmd := newScrapeCmd(&rootFlags{apiKey: "test-key", host: api.URL})
			cmd.SetArgs([]string{"https://web-scraping.dev", "--country", country})
			var err error
			_, _ = captureStdio(t, func() { err = cmd.Execute() })
			if err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 1 {
				t.Fatalf("requests=%d", requests.Load())
			}
		})
	}
}

func TestScrapeInvalidCountryDoesNotReachAPI(t *testing.T) {
	for _, country := range []string{"us,", "us,,ca", "us:0", "us:128", "-"} {
		t.Run(country, func(t *testing.T) {
			var requests atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusBadRequest)
			}))
			defer api.Close()
			cmd := newScrapeCmd(&rootFlags{apiKey: "test-key", host: api.URL})
			cmd.SetArgs([]string{"https://web-scraping.dev", "--country", country})
			var err error
			_, _ = captureStdio(t, func() { err = cmd.Execute() })
			if err == nil {
				t.Fatal("invalid country accepted")
			}
			if requests.Load() != 0 {
				t.Fatalf("invalid country sent %d requests", requests.Load())
			}
		})
	}
}
