package sessiond

import (
	"context"
	"testing"
	"time"

	"github.com/scrapfly/scrapfly-cli/internal/cdp/cdptest"
)

func TestServeOpensTheStartURLBeforeItIsReady(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	browser := &cdptest.Browser{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ready := make(chan []string, 1)
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, "unblock-test", browser.Serve(t), "https://web-scraping.dev/products", 5*time.Second, func(string, string) {
			ready <- browser.Navigations()
		})
	}()
	select {
	case got := <-ready:
		if len(got) != 1 || got[0] != "Page.navigate https://web-scraping.dev/products on S1" {
			t.Fatalf("navigations when ready = %q, want the start URL opened on the attached page", got)
		}
	case err := <-done:
		t.Fatalf("Serve returned before it was ready: %v", err)
	case <-ctx.Done():
		t.Fatal("Serve never reported ready")
	}
	cancel()
	<-done
}
