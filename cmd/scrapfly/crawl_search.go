package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode"

	scrapfly "github.com/scrapfly/go-scrapfly"
	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"
)

// sanitizeTerminal strips the runes a crawled page could use to drive the
// terminal rather than be read by it: C0/C1 controls carry ANSI escapes, and
// the bidi overrides reorder a rendered URL without changing its bytes.
//
// Search results are the one place the CLI prints text from an arbitrary
// third-party site, so the escaping the JSON encoder gives the default output
// has to be done by hand on the --pretty path.
func sanitizeTerminal(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n':
			return r
		case unicode.IsControl(r):
			return -1
		case r >= '\u202a' && r <= '\u202e', r >= '\u2066' && r <= '\u2069',
			r == '\u200e', r == '\u200f':
			return -1
		}
		return r
	}, s)
}

// parseSearchFilters turns repeated key=value flags into the flat filter map
// the API accepts. Values that parse as JSON keep their type (so
// `--filter 'http_status=[200,299]'` and `--filter host='["a","b"]'` work);
// everything else stays a string.
func parseSearchFilters(pairs []string) (map[string]interface{}, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	filters := make(map[string]interface{}, len(pairs))
	for _, pair := range pairs {
		key, value, found := strings.Cut(pair, "=")
		if !found || key == "" {
			return nil, fmt.Errorf("expected key=value, got %q", pair)
		}
		var decoded interface{}
		if err := json.Unmarshal([]byte(value), &decoded); err == nil {
			filters[key] = decoded
			continue
		}
		filters[key] = value
	}
	return filters, nil
}

func newCrawlSearchCmd(flags *rootFlags) *cobra.Command {
	var (
		query   string
		limit   int
		mode    string
		filters []string
		cursor  string
	)
	cmd := &cobra.Command{
		Use:   "search <uuid...>",
		Short: "Search the content of one or more crawls",
		Long: `Semantic and keyword search over crawls that were started with --search.

Several UUIDs fan out into one merged ranking; a single UUID is the same call
with a one-element list. A crawl whose index is not ready yet is reported in
` + "`skipped`" + ` with a reason and does not fail the search, so check that
list before concluding a crawl had no match.

The response states its own completeness: "exact" with most crawls unopened is
the normal outcome: the fan-out proved the unopened crawls held nothing
better. Page with --cursor, never an offset.`,
		Example: `  scrapfly crawl search 01HX7K4... --query "pricing"
  scrapfly crawl search 01HX7K4... 01HX7M9... --query "TLS fingerprint" --limit 20
  scrapfly crawl search 01HX7K4... --query docs --filter url_prefix=https://example.com/docs/
  scrapfly crawl search 01HX7K4... --query docs --mode fts --pretty`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if query == "" {
				return fmt.Errorf("%w: --query is required", scrapfly.ErrCrawlerConfig)
			}
			parsed, err := parseSearchFilters(filters)
			if err != nil {
				return fmt.Errorf("--filter: %w", err)
			}
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			res, err := client.CrawlsSearch(args, query, &scrapfly.CrawlSearchOptions{
				Limit:   limit,
				Mode:    scrapfly.CrawlerSearchMode(mode),
				Filters: parsed,
				Cursor:  cursor,
			})
			if err != nil {
				return err
			}
			if flags.pretty {
				printCrawlSearch(res)
				return nil
			}
			return out.WriteSuccess(os.Stdout, false, "crawl.search", res)
		},
	}
	cmd.Flags().StringVar(&query, "query", "", "search query (required)")
	cmd.Flags().IntVar(&limit, "limit", 0, "max results, 1-50 (0 = server default)")
	cmd.Flags().StringVar(&mode, "mode", "", "vector|fts|hybrid (empty = server default hybrid)")
	cmd.Flags().StringArrayVar(&filters, "filter", nil, "key=value filter (repeatable): url_prefix, host, source_format, content_type, http_status, crawler_uuid")
	cmd.Flags().StringVar(&cursor, "cursor", "", "next-page token from a previous response")
	return cmd
}

func printCrawlSearch(res *scrapfly.CrawlerSearchResponse) {
	out.Pretty(os.Stdout, "%d result(s) | mode=%s completeness=%s searched=%d/%d in %dms",
		len(res.Results), res.Mode, res.Completeness,
		res.CrawlsSearched, res.CrawlsRequested, res.Stats.DurationMS)
	for _, hit := range res.Results {
		title := hit.Title
		if title == "" {
			title = hit.URL
		}
		out.Pretty(os.Stdout, "")
		out.Pretty(os.Stdout, "%d. %s  (%.3f)", hit.Rank, sanitizeTerminal(title), hit.Score)
		out.Pretty(os.Stdout, "   %s  chunk=%d", sanitizeTerminal(hit.URL), hit.ChunkID)
		out.Pretty(os.Stdout, "   %s", sanitizeTerminal(strings.Join(strings.Fields(hit.Text), " ")))
	}
	for _, skipped := range res.Skipped {
		out.Pretty(os.Stdout, "")
		out.Pretty(os.Stdout, "skipped %s: %s", skipped.CrawlerUUID, skipped.Reason)
	}
	if res.Cursor != "" {
		out.Pretty(os.Stdout, "")
		out.Pretty(os.Stdout, "next page: --cursor %s", res.Cursor)
	}
}

func newCrawlPromptCmd(flags *rootFlags) *cobra.Command {
	var (
		prompt   string
		model    string
		limit    int
		mode     string
		filters  []string
		noStream bool
	)
	cmd := &cobra.Command{
		Use:   "prompt <uuid...>",
		Short: "Ask a question answered from the content of one or more crawls",
		Long: `Retrieves from the same fan-out as ` + "`crawl search`" + `, then generates an
answer over the retrieved chunks. Requires crawls started with --search.

Tokens print to stdout as they arrive. In JSON mode (the default) the answer
and its sources are emitted as one envelope once the stream completes, so the
output stays parseable; use --pretty to see the sources and a live token
stream instead. --no-stream asks the API for the whole answer in one object,
which adds how much of the corpus the answer covers (completeness, skipped).

The request runs a fan-out and a generation, both billable, so it is never
retried.`,
		Example: `  scrapfly crawl prompt 01HX7K4... --prompt "What does this site sell?"
  scrapfly crawl prompt 01HX7K4... --prompt "What does this site sell?" --no-stream
  scrapfly crawl prompt 01HX7K4... 01HX7M9... --prompt "Compare the pricing models" --pretty
  scrapfly crawl prompt 01HX7K4... --prompt "Summarize the docs" --model gemini-2.5-flash`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if prompt == "" {
				return fmt.Errorf("%w: --prompt is required", scrapfly.ErrCrawlerConfig)
			}
			parsed, err := parseSearchFilters(filters)
			if err != nil {
				return fmt.Errorf("--filter: %w", err)
			}
			client, err := buildClient(flags)
			if err != nil {
				return err
			}

			opts := &scrapfly.CrawlPromptOptions{Model: model, Search: retrievalOptions(limit, mode, parsed)}

			if noStream {
				res, err := client.CrawlsPromptAnswer(args, prompt, opts)
				if err != nil {
					return err
				}
				if flags.pretty {
					for _, source := range res.Sources {
						out.Pretty(os.Stdout, "[%d] %s", source.ID, sanitizeTerminal(source.URL))
					}
					out.Pretty(os.Stdout, "%s", sanitizeTerminal(res.Answer))
					out.Pretty(os.Stdout, "")
					out.Pretty(os.Stdout, "sources_used=%v sources_dropped=%d truncated=%v api_credit=%s completeness=%s skipped=%d",
						res.SourcesUsed, res.SourcesDropped, res.Truncated, formatAPICredit(res.APICredit), res.Search.Completeness, len(res.Search.Skipped))
					return nil
				}
				return out.WriteSuccess(os.Stdout, false, "crawl.prompt", res)
			}

			var (
				answer  strings.Builder
				sources []scrapfly.CrawlerPromptSource
				done    scrapfly.CrawlerPromptDone
			)
			err = client.CrawlsPrompt(args, prompt, opts, func(ev scrapfly.CrawlerPromptEvent) error {
				switch ev.Type {
				case scrapfly.CrawlerPromptEventSource:
					sources = append(sources, ev.Source)
					if flags.pretty {
						out.Pretty(os.Stdout, "[%d] %s", ev.Source.ID, sanitizeTerminal(ev.Source.URL))
					}
				case scrapfly.CrawlerPromptEventToken:
					answer.WriteString(ev.Token)
					if flags.pretty {
						// Straight to stdout: out.Pretty appends a newline,
						// which would break the answer into one line per token.
						fmt.Print(sanitizeTerminal(ev.Token))
					}
				case scrapfly.CrawlerPromptEventDone:
					done = ev.Done
				}
				return nil
			})
			if err != nil {
				// A partial answer is worth showing: generation can fail after
				// tokens were already streamed.
				if flags.pretty && answer.Len() > 0 {
					out.Pretty(os.Stdout, "")
				}
				return err
			}

			// api_credit is what the run was actually charged. An engine too old
			// to report it sends no key, and that is not the same fact as a
			// zero charge, so it is reported as unknown rather than as 0.
			apiCredit := formatAPICredit(done.APICredit)

			if flags.pretty {
				out.Pretty(os.Stdout, "")
				out.Pretty(os.Stdout, "")
				out.Pretty(os.Stdout, "sources_used=%v sources_dropped=%d truncated=%v api_credit=%s",
					done.SourcesUsed, done.SourcesDropped, done.Truncated, apiCredit)
				return nil
			}
			return out.WriteSuccess(os.Stdout, false, "crawl.prompt", map[string]any{
				"answer":          answer.String(),
				"sources":         sources,
				"sources_used":    done.SourcesUsed,
				"sources_dropped": done.SourcesDropped,
				"truncated":       done.Truncated,
				"api_credit":      done.APICredit,
			})
		},
	}
	cmd.Flags().StringVar(&prompt, "prompt", "", "the question (required)")
	cmd.Flags().StringVar(&model, "model", "", "generation model id (empty = server default)")
	cmd.Flags().IntVar(&limit, "limit", 0, "retrieval: max chunks, 1-50 (0 = server default)")
	cmd.Flags().StringVar(&mode, "mode", "", "retrieval: vector|fts|hybrid (empty = server default hybrid)")
	cmd.Flags().StringArrayVar(&filters, "filter", nil, "retrieval: key=value filter (repeatable), same keys as crawl search")
	cmd.Flags().BoolVar(&noStream, "no-stream", false, "ask for the whole answer in one object instead of a token stream")
	return cmd
}

func newCrawlExtractCmd(flags *rootFlags) *cobra.Command {
	var (
		prompt     string
		schema     string
		schemaFile string
		limit      int
		mode       string
		filters    []string
	)
	cmd := &cobra.Command{
		Use:   "extract <uuid...>",
		Short: "Extract a structured object from the content of one or more crawls",
		Long: `Retrieves like ` + "`crawl prompt`" + ` and answers with an object shaped by a JSON
Schema, handed to the model as its response schema. Requires crawls started
with --search. There is no streaming variant.

The schema constrains the model as it generates; it is not validated again
afterwards, so validate anything you act on.

The request runs a fan-out and a generation, so it is never retried.`,
		Example: `  scrapfly crawl extract 01HX7K4... --prompt "Every product with its price" \
      --schema '{"type":"object","properties":{"products":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"},"price":{"type":"number"}}}}},"required":["products"]}'
  scrapfly crawl extract 01HX7K4... --prompt "Every plan with its monthly price" --schema-file plans.schema.json --pretty`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if prompt == "" {
				return fmt.Errorf("%w: --prompt is required", scrapfly.ErrCrawlerConfig)
			}
			if (schema == "") == (schemaFile == "") {
				return fmt.Errorf("%w: give the JSON Schema with exactly one of --schema and --schema-file", scrapfly.ErrCrawlerConfig)
			}
			raw := []byte(schema)
			if schemaFile != "" {
				var err error
				if raw, err = readFileOrStdin(schemaFile); err != nil {
					return fmt.Errorf("--schema-file: %w", err)
				}
			}
			var parsedSchema map[string]interface{}
			if err := json.Unmarshal(raw, &parsedSchema); err != nil {
				return fmt.Errorf("%w: the schema is not a JSON object: %v", scrapfly.ErrCrawlerConfig, err)
			}
			parsed, err := parseSearchFilters(filters)
			if err != nil {
				return fmt.Errorf("--filter: %w", err)
			}
			client, err := buildClient(flags)
			if err != nil {
				return err
			}

			res, err := client.CrawlsExtract(args, prompt, parsedSchema, &scrapfly.CrawlExtractOptions{Search: retrievalOptions(limit, mode, parsed)})
			if err != nil {
				return err
			}

			if flags.pretty {
				indented, _ := json.MarshalIndent(res.Data, "", "  ")
				out.Pretty(os.Stdout, "%s", sanitizeTerminal(string(indented)))
				out.Pretty(os.Stdout, "")
				out.Pretty(os.Stdout, "sources=%d sources_dropped=%d truncated=%v completeness=%s skipped=%d",
					len(res.Sources), res.SourcesDropped, res.Truncated, res.Search.Completeness, len(res.Search.Skipped))
				return nil
			}
			return out.WriteSuccess(os.Stdout, false, "crawl.extract", res)
		},
	}
	cmd.Flags().StringVar(&prompt, "prompt", "", "what to extract (required)")
	cmd.Flags().StringVar(&schema, "schema", "", "JSON Schema of the result, inline")
	cmd.Flags().StringVar(&schemaFile, "schema-file", "", "file holding the JSON Schema of the result (- for stdin)")
	cmd.Flags().IntVar(&limit, "limit", 0, "retrieval: max chunks, 1-50 (0 = server default)")
	cmd.Flags().StringVar(&mode, "mode", "", "retrieval: vector|fts|hybrid (empty = server default hybrid)")
	cmd.Flags().StringArrayVar(&filters, "filter", nil, "retrieval: key=value filter (repeatable), same keys as crawl search")
	return cmd
}

// retrievalOptions builds the search block of a prompt or an extraction, nil when every flag is left at its default.
func retrievalOptions(limit int, mode string, filters map[string]interface{}) *scrapfly.CrawlSearchOptions {
	if limit == 0 && mode == "" && filters == nil {
		return nil
	}
	return &scrapfly.CrawlSearchOptions{Limit: limit, Mode: scrapfly.CrawlerSearchMode(mode), Filters: filters}
}

// formatAPICredit reports what a prompt was charged. An engine too old to
// report it sends no key, and that is not the same fact as a zero charge.
func formatAPICredit(credit *int) string {
	if credit == nil {
		return "unknown"
	}
	return strconv.Itoa(*credit)
}
