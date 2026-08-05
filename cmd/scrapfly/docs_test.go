package main

import "testing"

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
			"https://scrapfly.home/", "vnc", "cloud_browser", 3,
			"https://scrapfly.home/docs/search?include_generic=1&limit=3&product=cloud_browser&q=vnc",
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
