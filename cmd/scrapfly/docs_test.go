package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestBuildDocsSearchURL(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		query   string
		product string
		limit   int
		want    string
	}{
		{
			"query only, zero limit omitted",
			"https://api.scrapfly.io", "fairness policy", "", 0,
			"https://api.scrapfly.io/docs/search?q=fairness+policy",
		},
		{
			"product adds include_generic",
			"https://api.scrapfly.io", "session", "web_scraping_api", 5,
			"https://api.scrapfly.io/docs/search?include_generic=1&limit=5&product=web_scraping_api&q=session",
		},
		{
			"trailing slash on host",
			"https://api.scrapfly.io/", "vnc", "cloud_browser", 3,
			"https://api.scrapfly.io/docs/search?include_generic=1&limit=3&product=cloud_browser&q=vnc",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := buildDocsSearchURL(c.host, c.query, c.product, c.limit); got != c.want {
				t.Errorf("got %s, want %s", got, c.want)
			}
		})
	}
}

// `batch` is registered under `scrape`, so a bare `scrapfly batch ...` in its
// Example block is a copy-paste that exits with "unknown command". Assert every
// example invocation names the path the command is actually reachable at.
func TestCommandExamplesNameTheRealPath(t *testing.T) {
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		for _, line := range strings.Split(cmd.Example, "\n") {
			_, invocation, found := strings.Cut(strings.TrimSpace(line), "scrapfly ")
			if !found {
				continue
			}
			// Examples legitimately invoke other commands (pipelines, or an
			// alias of a parent). Only flag the copy-paste trap: the command's
			// own leaf name used as if it were a top-level command.
			if strings.HasPrefix(invocation, cmd.Name()) && !strings.HasPrefix(invocation, cmd.CommandPath()) {
				t.Errorf("%q example invokes %q; the command is only reachable as %q",
					cmd.CommandPath(), invocation, cmd.CommandPath())
			}
		}
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(newScrapeCmd(&rootFlags{}))
}
