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
// per-config knobs flow from flags (unblocker, render_js, country, proxy_pool)
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
		unblocker bool
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
country, unblocker, headers, etc.) pipe a JSONL stream of ScrapeConfig objects
on stdin; shared flags on the command line supply defaults for fields missing
from each config. A JSONL line may carry either "unblocker" or the deprecated
"asp" key; an explicit "asp" wins over "unblocker" on the same line.`,
		Example: `  scrapfly scrape batch --url https://httpbin.dev/get?a=1 --url https://httpbin.dev/get?b=2 --unblocker
  scrapfly scrape batch --url-file urls.txt --country us
  jq -c '.[]' configs.json | scrapfly scrape batch`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}

			configs, err := collectBatchConfigs(cmd.InOrStdin(), urls, urlFile, country, proxyPool, renderJS, unblocker)
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
	bindUnblockerFlag(cmd, &unblocker, "enable the unblocker (anti-bot bypass) on every URL in the batch")
	cmd.Flags().BoolVar(&msgpack, "msgpack", false, "negotiate per-part msgpack instead of JSON")

	return cmd
}

func collectBatchConfigs(
	stdin io.Reader,
	urls []string,
	urlFile, country, proxyPool string,
	renderJS, unblocker bool,
) ([]*scrapfly.ScrapeConfig, error) {
	var configs []*scrapfly.ScrapeConfig
	template := func(u string, idx int) *scrapfly.ScrapeConfig {
		return &scrapfly.ScrapeConfig{
			URL:           u,
			Country:       country,
			ProxyPool:     scrapfly.ProxyPool(proxyPool),
			RenderJS:      renderJS,
			ASP:           unblocker, // SDK field frozen; wire key stays "asp"
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
			cfg, lineUnblocker, err := decodeBatchConfigLine([]byte(line))
			if err != nil {
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
			// A line that names either key decides for itself, false included;
			// the flag is only a default for lines that name neither.
			if lineUnblocker != nil {
				cfg.ASP = *lineUnblocker
			} else {
				cfg.ASP = unblocker
			}
			configs = append(configs, cfg)
		}
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("read stdin JSONL: %w", err)
		}
	}

	return configs, nil
}

// decodeBatchConfigLine decodes one JSONL ScrapeConfig and returns, separately,
// the line's own anti-bot decision as a tri-state (nil = the line named
// neither key).
//
// ScrapeConfig carries no json struct tags, so encoding/json case-folds "asp"
// and "unblocker" onto two *different* fields and leaves precedence unstated.
// Worse, the SDK's own fallback consults its Unblocker field whenever the
// legacy bool is false — which cannot distinguish "absent" from "explicitly
// false", so a line saying {"asp": false, "unblocker": true} would come out
// enabled. Only this layer can see which keys the line actually contained, so
// it decides here and clears the SDK's Unblocker field after decoding, leaving
// ASP as the single field carrying the answer. Decode the original JSON into
// both structs so differently cased or repeated keys retain document order.
func decodeBatchConfigLine(line []byte) (*scrapfly.ScrapeConfig, *bool, error) {
	var bypass struct {
		ASP       *bool `json:"asp"`
		Unblocker *bool `json:"unblocker"`
	}
	if err := json.Unmarshal(line, &bypass); err != nil {
		return nil, nil, err
	}

	var cfg scrapfly.ScrapeConfig
	if err := json.Unmarshal(line, &cfg); err != nil {
		return nil, nil, err
	}
	cfg.Unblocker = nil

	if bypass.ASP == nil && bypass.Unblocker == nil {
		return &cfg, nil, nil
	}
	resolved := resolveUnblocker(bypass.ASP, bypass.Unblocker)
	return &cfg, &resolved, nil
}
