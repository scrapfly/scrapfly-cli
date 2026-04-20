package main

import "testing"

func TestSemverGreater(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.3.0", "0.2.0", true},
		{"v0.3.0", "0.2.0", true},
		{"0.2.0", "v0.2.0", false},
		{"0.2.0", "0.2.0", false},
		{"1.0.0", "0.99.99", true},
		{"0.2.1", "0.2.0", true},
		{"0.2.0", "0.2.1", false},
		{"0.2.0-rc1", "0.2.0", false}, // pre-release suffix dropped
		{"0.3.0-rc1", "0.2.0", true},
	}
	for _, c := range cases {
		got := semverGreater(normalizeVersion(c.a), normalizeVersion(c.b))
		if got != c.want {
			t.Errorf("semverGreater(%s > %s) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestAssetCandidates(t *testing.T) {
	cases := []struct {
		os, arch string
		want     []string
	}{
		{"darwin", "arm64", []string{"scrapfly-darwin-universal.tar.gz", "scrapfly-darwin-arm64.tar.gz"}},
		{"darwin", "amd64", []string{"scrapfly-darwin-universal.tar.gz", "scrapfly-darwin-amd64.tar.gz"}},
		{"linux", "amd64", []string{"scrapfly-linux-amd64.tar.gz"}},
		{"linux", "arm64", []string{"scrapfly-linux-arm64.tar.gz"}},
		{"windows", "amd64", []string{"scrapfly-windows-amd64.zip"}},
	}
	for _, c := range cases {
		got := assetCandidates(c.os, c.arch)
		if len(got) != len(c.want) {
			t.Errorf("assetCandidates(%s,%s) = %v, want %v", c.os, c.arch, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("assetCandidates(%s,%s)[%d] = %q, want %q", c.os, c.arch, i, got[i], c.want[i])
			}
		}
	}
}

func TestNormalizeVersion(t *testing.T) {
	cases := []struct{ in, want string }{
		{"v0.2.0", "0.2.0"},
		{"0.2.0", "0.2.0"},
		{"  v1.2.3  ", "1.2.3"},
	}
	for _, c := range cases {
		if got := normalizeVersion(c.in); got != c.want {
			t.Errorf("normalizeVersion(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
