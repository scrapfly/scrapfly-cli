package cdp_test

import (
	"context"
	"testing"
	"time"

	"github.com/scrapfly/scrapfly-cli/internal/cdp"
	"github.com/scrapfly/scrapfly-cli/internal/cdp/cdptest"
)

func TestAttachAtOpensTheUnblockedURLOnTheAttachedPage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	unblocked := &cdptest.Browser{}
	c, err := cdp.Dial(ctx, unblocked.Serve(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s, err := cdp.AttachAt(ctx, c, "https://web-scraping.dev/products")
	if err != nil {
		t.Fatalf("AttachAt: %v", err)
	}
	if s.TargetID != "T1" || s.SessionID != "S1" {
		t.Fatalf("attached %s/%s, want T1/S1", s.TargetID, s.SessionID)
	}
	if got := unblocked.Navigations(); len(got) != 1 || got[0] != "Page.navigate https://web-scraping.dev/products on S1" {
		t.Fatalf("navigations = %q, want the unblocked URL opened once on the attached page", got)
	}

	plain := &cdptest.Browser{}
	c2, err := cdp.Dial(ctx, plain.Serve(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if _, err := cdp.AttachAt(ctx, c2, ""); err != nil {
		t.Fatalf("AttachAt with no URL: %v", err)
	}
	if got := plain.Navigations(); len(got) != 0 {
		t.Fatalf("a session with no URL navigated: %q", got)
	}
}
