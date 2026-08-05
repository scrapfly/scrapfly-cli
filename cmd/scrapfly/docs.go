package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"
)

// newDocsCmd groups documentation helpers. `docs search` hits the public
// docs RAG endpoint (GET {host}/docs/search) — unauthenticated, no API key,
// no credits consumed.
func newDocsCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "docs",
		Short: "Search the Scrapfly documentation",
		// Without NoArgs a subcommand typo ("docs serach") would print
		// help with exit 0 — non-JSON on stdout that scripts read as success.
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newDocsSearchCmd(flags))
	return cmd
}

func newDocsSearchCmd(flags *rootFlags) *cobra.Command {
	var product string
	var limit int
	cmd := &cobra.Command{
		Use:   "search <query...>",
		Short: "Semantic (RAG) search over the public documentation",
		Long: `Searches the published documentation with the same hybrid semantic +
lexical engine as the site's Ctrl+K. Free and unauthenticated. Scope with
--product to reduce noise; an invalid product errors with the list of valid
values so agents can self-correct.`,
		Example: `  scrapfly docs search fairness policy
  scrapfly docs search --product web_scraping_api session reuse
  scrapfly docs search --product sdk-python --pretty retry`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := fetchDocsSearch(cmd.Context(), flags, strings.Join(args, " "), product, limit)
			if err != nil {
				return err
			}
			if flags.pretty {
				printDocsSearch(os.Stdout, raw)
				return nil
			}
			// Raw passthrough keeps the full server contract (id,
			// highlights, future fields) in the JSON envelope.
			return out.WriteSuccess(os.Stdout, false, "docs.search", raw)
		},
	}
	cmd.Flags().StringVar(&product, "product", "", "hard-scope results to one product (e.g. web_scraping_api); invalid values return the valid list")
	cmd.Flags().IntVar(&limit, "limit", 5, "max results (1-100)")
	return cmd
}

type docsSearchPayload struct {
	Query   string          `json:"query"`
	Count   int             `json:"count"`
	Results []docsSearchHit `json:"results"`
}

type docsSearchHit struct {
	Title    string  `json:"title"`
	Location string  `json:"location"`
	Text     string  `json:"text"`
	Product  string  `json:"product"`
	Score    float64 `json:"score"`
}

func buildDocsSearchURL(host, query, product string, limit int) string {
	q := url.Values{}
	q.Set("q", query)
	if product != "" {
		q.Set("product", product)
		// Product-less pages (billing, account, FAQ) stay included when
		// filtering — same behavior as the dashboard consumers.
		q.Set("include_generic", "1")
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	return strings.TrimRight(host, "/") + "/docs/search?" + q.Encode()
}

func fetchDocsSearch(ctx context.Context, flags *rootFlags, query, product string, limit int) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, buildDocsSearchURL(resolveHost(flags), query, product, limit), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "scrapfly-cli/docs")

	tr := http.DefaultTransport.(*http.Transport).Clone()
	if flags.insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	client := &http.Client{Timeout: flags.timeout, Transport: tr}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		var apiErr struct {
			Error             string   `json:"error"`
			Message           string   `json:"message"`
			AvailableProducts []string `json:"available_products"`
		}
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Message != "" {
			return nil, &out.APIError{
				Code:              apiErr.Error,
				Message:           apiErr.Message,
				HTTPStatus:        resp.StatusCode,
				AvailableProducts: apiErr.AvailableProducts,
			}
		}
		return nil, fmt.Errorf("docs search failed (http %d)", resp.StatusCode)
	}
	return body, nil
}

func printDocsSearch(w io.Writer, raw json.RawMessage) {
	var p docsSearchPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		out.Pretty(w, "%s", string(raw))
		return
	}
	out.Pretty(w, "Docs search %q  (%d result(s))", p.Query, p.Count)
	for i, r := range p.Results {
		product := r.Product
		if product == "" {
			product = "generic"
		}
		text := html.UnescapeString(r.Text)
		if runes := []rune(text); len(runes) > 220 {
			text = string(runes[:220]) + "…"
		}
		out.Pretty(w, "%2d. [%s] %s  (score %.3f)", i+1, product, html.UnescapeString(r.Title), r.Score)
		out.Pretty(w, "    %s", r.Location)
		out.Pretty(w, "    %s", text)
	}
}
