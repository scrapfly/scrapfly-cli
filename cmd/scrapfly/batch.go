package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	scrapfly "github.com/scrapfly/go-scrapfly"
	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"
)

// newBatchCmd exposes the real POST /scrape/batch endpoint. Input is a list
// of URLs (flag, file, or stdin); each URL is turned into a ScrapeConfig
// with an auto-generated correlation_id of the form "item-<N>". Extra
// per-config knobs flow from flags (asp, render_js, country, proxy_pool)
// and apply uniformly to every entry — for heterogeneous batches, pipe
// a JSONL stream of ScrapeConfig objects on stdin.
//
// Output: one NDJSON envelope per result as it streams off the server
// (classic batch semantics, not a full slice collected at the end).
func newBatchCmd(flags *rootFlags) *cobra.Command {
	var (
		urls      []string
		urlFile   string
		country   string
		proxyPool string
		renderJS  bool
		asp       bool
		msgpack   bool
	)

	cmd := &cobra.Command{
		Use:   "batch",
		Short: "Scrape up to 100 URLs in one streaming batch request",
		Long: `Issue POST /scrape/batch with up to 100 configs. Results stream back as
each scrape completes (not at the end), so the wall time is bounded by the
slowest scrape, not the sum of them.

Each --url or line of --url-file becomes a ScrapeConfig with a synthetic
correlation_id of the form "item-N". For heterogeneous batches (per-config
country, asp, headers, etc.) pipe a JSONL stream of ScrapeConfig objects on
stdin; shared flags on the command line supply defaults for fields missing
from each config.`,
		Example: `  scrapfly batch --url https://httpbin.dev/get?a=1 --url https://httpbin.dev/get?b=2 --asp
  scrapfly batch --url-file urls.txt --country us
  jq -c '.[]' configs.json | scrapfly batch`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}

			configs, err := collectBatchConfigs(cmd.InOrStdin(), urls, urlFile, country, proxyPool, renderJS, asp)
			if err != nil {
				return err
			}
			if len(configs) == 0 {
				return fmt.Errorf("no configs provided — use --url, --url-file, or pipe JSONL on stdin")
			}
			if len(configs) > 100 {
				return fmt.Errorf("batch size %d exceeds the 100-config limit", len(configs))
			}

			opts := scrapfly.BatchOptions{}
			if msgpack {
				opts.Format = scrapfly.BatchFormatMsgpack
			}

			ch, err := client.ScrapeBatchWithOptions(configs, opts)
			if err != nil {
				return err
			}

			enc := json.NewEncoder(os.Stdout)
			for item := range ch {
				env := out.Envelope{Product: "batch"}
				if item.Err != nil {
					env.Success = false
					env.Error = &out.EnvelopeError{Message: item.Err.Error()}
					env.Data = map[string]any{"correlation_id": item.CorrelationID}
				} else {
					env.Success = true
					env.Data = map[string]any{
						"correlation_id": item.CorrelationID,
						"result":         item.Result,
					}
				}
				if err := enc.Encode(env); err != nil {
					return err
				}
			}
			return nil
		},
	}

	cmd.Flags().StringArrayVar(&urls, "url", nil, "URL to scrape (repeatable)")
	cmd.Flags().StringVar(&urlFile, "url-file", "", "path to a newline-delimited file of URLs")
	cmd.Flags().StringVar(&country, "country", "", "proxy country (ISO 3166-1 alpha-2) applied to every config")
	cmd.Flags().StringVar(&proxyPool, "proxy-pool", "", "proxy pool name applied to every config")
	cmd.Flags().BoolVar(&renderJS, "render-js", false, "render JavaScript on every URL in the batch")
	cmd.Flags().BoolVar(&asp, "asp", false, "enable anti-scraping protection on every URL in the batch")
	cmd.Flags().BoolVar(&msgpack, "msgpack", false, "negotiate per-part msgpack instead of JSON")

	return cmd
}

func collectBatchConfigs(
	stdin io.Reader,
	urls []string,
	urlFile, country, proxyPool string,
	renderJS, asp bool,
) ([]*scrapfly.ScrapeConfig, error) {
	var configs []*scrapfly.ScrapeConfig
	template := func(u string, idx int) *scrapfly.ScrapeConfig {
		return &scrapfly.ScrapeConfig{
			URL:           u,
			Country:       country,
			ProxyPool:     scrapfly.ProxyPool(proxyPool),
			RenderJS:      renderJS,
			ASP:           asp,
			CorrelationID: fmt.Sprintf("item-%d", idx+1),
		}
	}

	for i, u := range urls {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		configs = append(configs, template(u, i))
	}

	if urlFile != "" {
		f, err := os.Open(urlFile)
		if err != nil {
			return nil, fmt.Errorf("open --url-file %s: %w", urlFile, err)
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			configs = append(configs, template(line, len(configs)))
		}
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("read --url-file %s: %w", urlFile, err)
		}
	}

	// JSONL stdin mode — only when no --url/--url-file and stdin is piped.
	if len(configs) == 0 && isStdinPiped(stdin) {
		scanner := bufio.NewScanner(stdin)
		scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var cfg scrapfly.ScrapeConfig
			if err := json.Unmarshal([]byte(line), &cfg); err != nil {
				return nil, fmt.Errorf("parse JSONL line: %w", err)
			}
			if cfg.CorrelationID == "" {
				cfg.CorrelationID = fmt.Sprintf("item-%d", len(configs)+1)
			}
			if cfg.Country == "" {
				cfg.Country = country
			}
			if cfg.ProxyPool == "" && proxyPool != "" {
				cfg.ProxyPool = scrapfly.ProxyPool(proxyPool)
			}
			if !cfg.RenderJS {
				cfg.RenderJS = renderJS
			}
			if !cfg.ASP {
				cfg.ASP = asp
			}
			configs = append(configs, &cfg)
		}
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("read stdin JSONL: %w", err)
		}
	}

	return configs, nil
}
