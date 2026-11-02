package cdp

import "testing"

// isRefLocator is the one-line predicate our Click/Fill path uses to decide
// whether a locator is an AXTree ref (Input.* dispatch) or a CSS/XPath/ax
// selector (Antibot dispatch). Lock down the boundaries.
func TestIsRefLocator(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"e", false}, // e alone (no digits) is NOT a ref
		{"e1", true},
		{"e42", true},
		{"e999", true},
		{"E1", false}, // uppercase isn't a ref
		{"foo", false},
		{"e1a", false}, // non-digit tail
		{"input[name=x]", false},
		{"//button", false},
		{"42", false}, // digits without e prefix
	}
	for _, c := range cases {
		if got := isRefLocator(c.in); got != c.want {
			t.Errorf("isRefLocator(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// CSSSelector / XPathSelector wrap a locator into the Antibot Selector
// shape. Regression test so the struct-tag contract doesn't silently drift.
func TestSelectorHelpers(t *testing.T) {
	s := CSSSelector("input#x")
	if s.Type != "css" || s.Query != "input#x" {
		t.Fatalf("CSSSelector: %+v", s)
	}
	x := XPathSelector("//button[text()='OK']")
	if x.Type != "xpath" || x.Query != "//button[text()='OK']" {
		t.Fatalf("XPathSelector: %+v", x)
	}
}

func bufferedEvents(methods ...string) *Client {
	c := &Client{}
	for _, m := range methods {
		c.events = append(c.events, &Message{Method: m})
	}

	return c
}

func methodsOf(events []*Message) []string {
	names := []string{}
	for _, e := range events {
		names = append(names, e.Method)
	}

	return names
}

// DrainEvents is how the execute REPL hands the protocol's events to a caller:
// it picks by method, keeps the order, and only clear removes what it returned.
func TestDrainEventsPicksByMethodAndKeepsTheOrder(t *testing.T) {
	c := bufferedEvents("Page.frameNavigated", "Page.lifecycleEvent", "Page.frameNavigated", "Page.loadEventFired")

	got := methodsOf(c.DrainEvents([]string{"Page.frameNavigated", "Page.loadEventFired"}, false))

	if len(got) != 3 || got[0] != "Page.frameNavigated" || got[1] != "Page.frameNavigated" || got[2] != "Page.loadEventFired" {
		t.Fatalf("picked %v", got)
	}
	if len(c.events) != 4 {
		t.Fatalf("without clear the buffer must keep every event, has %d", len(c.events))
	}
}

func TestDrainEventsClearRemovesOnlyWhatItReturns(t *testing.T) {
	c := bufferedEvents("Page.frameNavigated", "Page.lifecycleEvent", "Page.frameNavigated")

	picked := c.DrainEvents([]string{"Page.frameNavigated"}, true)

	if len(picked) != 2 {
		t.Fatalf("picked %v", methodsOf(picked))
	}
	if rest := methodsOf(c.events); len(rest) != 1 || rest[0] != "Page.lifecycleEvent" {
		t.Fatalf("the buffer must keep the events it did not return, has %v", rest)
	}
}

func TestDrainEventsWithoutMethodsReturnsEverything(t *testing.T) {
	c := bufferedEvents("a", "b", "c")

	if got := c.DrainEvents(nil, true); len(got) != 3 || len(c.events) != 0 {
		t.Fatalf("picked %v, left %v", methodsOf(got), methodsOf(c.events))
	}
}
