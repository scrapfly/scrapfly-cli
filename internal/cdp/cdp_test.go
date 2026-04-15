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
