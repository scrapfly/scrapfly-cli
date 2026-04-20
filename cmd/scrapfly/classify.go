package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	scrapfly "github.com/scrapfly/go-scrapfly"
	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"
)

// newClassifyCmd exposes POST /classify. Three input modes, same piping
// ergonomics as `scrapfly extract`:
//
//   --fetch           CLI fetches URL itself, forwards status/headers/body.
//   --file FILE       load body from a file; status and headers come from flags.
//   stdin             raw body piped in; status and headers come from flags.
//   --body STRING     inline body for short snippets; flags supply the rest.
func newClassifyCmd(flags *rootFlags) *cobra.Command {
	var (
		url        string
		statusCode int
		headerList []string
		bodyInline string
		file       string
		method     string
		fetch      bool
	)

	cmd := &cobra.Command{
		Use:   "classify",
		Short: "Classify an HTTP response for anti-bot blocking (1 API credit per call)",
		Long: `Feed an already-fetched HTTP response to the Scrapfly Classify API and get
back a verdict on whether the target blocked the request. 1 API credit per
call, billed under the Web Scraping API product. Response is three fields:
blocked (bool), antibot (the product name, e.g. "cloudflare", or null), cost.

Input modes (mirrors ` + "`scrapfly extract`" + ` piping):
  --fetch            let the CLI fetch --url itself, forward the captured
                     status/headers/body to /classify.
  --file FILE        load body from a file; pass --status-code + --header.
  stdin              pipe the body in; pass --status-code + --header.
  --body STRING      inline body for short snippets.`,
		Example: `  # Let the CLI do the fetch
  scrapfly scraper classify --url https://target.example.com/ --fetch

  # Pipe from any external fetcher
  curl -s https://target.example.com/ \
    | scrapfly scraper classify --url https://target.example.com/ \
        --status-code 403 --header server:cloudflare --header cf-mitigated:challenge

  # Pipe raw body from scrape --proxified (Scrapfly-fetched)
  scrapfly scrape https://target.example.com/ --asp --proxified \
    | scrapfly scraper classify --url https://target.example.com/ \
        --status-code 403 --header server:cloudflare

  # Local file
  scrapfly scraper classify --url https://target.example.com/ \
    --status-code 403 --header server:cloudflare --file response.html`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}

			if url == "" {
				return fmt.Errorf("--url is required")
			}

			req, err := buildClassifyRequest(cmd.InOrStdin(), url, method, statusCode, headerList, bodyInline, file, fetch, flags)
			if err != nil {
				return err
			}

			result, err := client.Classify(context.Background(), req)
			if err != nil {
				return err
			}

			if flags.pretty {
				antibot := "none"
				if result.Antibot != "" {
					antibot = result.Antibot
				}
				out.Pretty(os.Stdout, "blocked=%t antibot=%s cost=%d", result.Blocked, antibot, result.Cost)
				return nil
			}
			return out.WriteSuccess(os.Stdout, false, "classify", result)
		},
	}

	cmd.Flags().StringVar(&url, "url", "", "final URL the response came from (required)")
	cmd.Flags().IntVar(&statusCode, "status-code", 0, "HTTP status code of the response to classify (100-599)")
	cmd.Flags().StringArrayVar(&headerList, "header", nil, "response header in 'Name:Value' form (repeatable)")
	cmd.Flags().StringVar(&bodyInline, "body", "", "response body (text); use --file for large bodies or pipe via stdin")
	cmd.Flags().StringVar(&file, "file", "", "read the response body from file (default: stdin)")
	cmd.Flags().StringVar(&method, "method", "", "HTTP method the caller used (default GET)")
	cmd.Flags().BoolVar(&fetch, "fetch", false, "fetch --url over plain HTTP and use its response as the classify input")

	return cmd
}

func buildClassifyRequest(
	stdin io.Reader,
	url, method string,
	statusCode int,
	headerList []string,
	bodyInline, file string,
	fetch bool,
	flags *rootFlags,
) (*scrapfly.ClassifyRequest, error) {
	if fetch {
		return fetchForClassify(url, method, flags)
	}

	if statusCode == 0 {
		return nil, fmt.Errorf("--status-code is required (or use --fetch)")
	}

	headers, err := parseHeaderPairs(headerList)
	if err != nil {
		return nil, err
	}

	body, err := readClassifyBody(stdin, bodyInline, file)
	if err != nil {
		return nil, err
	}

	return &scrapfly.ClassifyRequest{
		URL:        url,
		StatusCode: statusCode,
		Headers:    headers,
		Body:       body,
		Method:     method,
	}, nil
}

// readClassifyBody resolves the response body from exactly one source,
// in priority order: --file > --body > stdin (when piped). Mirrors
// `scrapfly extract`'s body resolution so pipelines compose the same way:
//
//	curl -s URL | scrapfly scraper classify --status-code ... --header ...
//	scrapfly scrape URL --proxified | scrapfly scraper classify ...
func readClassifyBody(stdin io.Reader, inline, file string) (string, error) {
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("read --file %s: %w", file, err)
		}
		return string(data), nil
	}
	if inline != "" {
		return inline, nil
	}
	if isStdinPiped(stdin) {
		raw, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return string(raw), nil
	}
	// No body supplied. Allowed — some responses are header-only.
	return "", nil
}

func fetchForClassify(url, method string, flags *rootFlags) (*scrapfly.ClassifyRequest, error) {
	httpClient := &http.Client{Timeout: 30 * time.Second}
	m := strings.ToUpper(method)
	if m == "" {
		m = "GET"
	}
	req, err := http.NewRequest(m, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build fetch request: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read fetched body: %w", err)
	}
	headers := make(map[string]string, len(resp.Header))
	for k, v := range resp.Header {
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}
	return &scrapfly.ClassifyRequest{
		URL:        url,
		StatusCode: resp.StatusCode,
		Headers:    headers,
		Body:       string(body),
		Method:     m,
	}, nil
}

func parseHeaderPairs(entries []string) (map[string]string, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	headers := make(map[string]string, len(entries))
	for _, raw := range entries {
		idx := strings.Index(raw, ":")
		if idx <= 0 {
			return nil, fmt.Errorf("invalid --header %q, expected Name:Value", raw)
		}
		name := strings.TrimSpace(raw[:idx])
		value := strings.TrimSpace(raw[idx+1:])
		if name == "" {
			return nil, fmt.Errorf("invalid --header %q, empty name", raw)
		}
		headers[name] = value
	}
	return headers, nil
}

func isStdinPiped(stdin io.Reader) bool {
	// When cobra routes os.Stdin in, the real file is usable for Stat().
	// Any non-TTY (pipe / regular file / fifo) counts as piped input.
	if f, ok := stdin.(*os.File); ok {
		info, err := f.Stat()
		if err != nil {
			return false
		}
		return (info.Mode() & os.ModeCharDevice) == 0
	}
	// bufio-wrapped or custom reader: peek one byte.
	if br, ok := stdin.(*bufio.Reader); ok {
		_, err := br.Peek(1)
		return err == nil
	}
	return false
}
