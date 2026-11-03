package main

import (
	"strings"
	"testing"
	"time"
)

func TestDownloadBundleErrorDoesNotPrintTheKey(t *testing.T) {
	_, _, err := downloadBundle("http://127.0.0.1:1", "scp-live-0123456789abcdef", "linux", "amd64", false, time.Second)
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), "0123456789abcdef") || !strings.Contains(err.Error(), "api_key=REDACTED") {
		t.Errorf("error %q", err)
	}
}
