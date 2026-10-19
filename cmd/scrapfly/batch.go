package main

import (
	"bufio"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
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
		urls     []string
		urlFile  string
		country  string
		pool     proxyPoolFlags
		renderJS bool
		msgpack  bool
	)

	cmd := &cobra.Command{
		Use:   "batch",
		Short: "Scrape up to 100 URLs in one streaming batch request",
		Long: `Issue POST /scrape/batch with up to 100 configs. Results stream back as
each scrape completes (not at the end), so the wall time is bounded by the
slowest scrape, not the sum of them.

Each --url or line of --url-file becomes a ScrapeConfig with the
correlation_id "item-N", N its position in the batch. For heterogeneous
batches (per-config country, proxy pool, headers, etc.) pipe a JSONL stream of
ScrapeConfig objects on stdin; shared flags on the command line supply
defaults for fields missing from each config.

A JSONL line keeps its own "correlation_id". A line without one gets "item-N"
too, unless another line names that ID: it then gets the first "item-N" past
the end of the batch that no line names. Two lines naming the same
correlation_id are refused before anything is sent, as the API refuses them.

A line's "proxy_pool", "asp" and "unblocker" keys mean what they mean to the
API ("asp" wins over "unblocker" on the same line), and the flags fill in only
what a line leaves out. A line naming "asp" or "unblocker" takes a pool word
from the flags only when the word matches its value: basic with false;
unblocker, unblocker_ultra or auto with true. The word then goes out in place
of that key. A line refused with ERR::PROXY::UNBLOCKER_ULTRA_REQUIRED that
names its own proxy_pool takes "proxy_pool": "unblocker_ultra" on the line
itself. "asp" and "unblocker" are deprecated: use "proxy_pool"
(unblocker_ultra for the Unblocker). The migration guide covers requests
written before the proxy pools: https://scrapfly.io/new-pricing#legacy-spellings

` + proxyPoolHelp,
		Example: `  scrapfly scrape batch --url https://httpbin.dev/get?a=1 --url https://httpbin.dev/get?b=2 --unblocker-ultra
  scrapfly scrape batch --url-file urls.txt --country us
  jq -c '.[]' configs.json | scrapfly scrape batch`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}

			configs, poolFromFlags, err := collectBatchConfigs(cmd.InOrStdin(), urls, urlFile, country, pool, renderJS)
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

			hint := batchUltraRefusalHints(poolFromFlags, pool)
			enc := json.NewEncoder(os.Stdout)
			for item := range ch {
				env := out.Envelope{Product: "batch"}
				if item.Err != nil {
					env.Success = false
					env.Error = &out.EnvelopeError{Message: item.Err.Error()}
					if isUltraRefusal(item.Err) {
						env.Error.Hint = hint(item.CorrelationID)
					}
					env.Data = map[string]any{"correlation_id": item.CorrelationID}
				} else {
					env.Success = true
					data := map[string]any{
						"correlation_id": item.CorrelationID,
						"result":         item.Result,
					}
					// A scrape the engine refused comes back as a result
					// carrying the error, not as an error part.
					if item.Result != nil && item.Result.Result.Error != nil && item.Result.Result.Error.Code == errUnblockerUltraRequired {
						if h := hint(item.CorrelationID); h != "" {
							data["hint"] = h
						}
					}
					env.Data = data
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
	bindProxyPoolFlags(cmd, &pool, " for every config")
	cmd.Flags().BoolVar(&renderJS, "render-js", false, "render JavaScript on every URL in the batch")
	cmd.Flags().BoolVar(&msgpack, "msgpack", false, "negotiate per-part msgpack instead of JSON")

	return cmd
}

// collectBatchConfigs builds the batch from --url and --url-file, or else from
// JSONL on stdin. poolFromFlags holds, by correlation ID, whether a config's
// proxy_pool came from the flags: always for a --url or --url-file entry,
// for a JSONL line as resolveLine says. Results, hints and envelopes are
// matched back by correlation ID, so every config leaves with one of its own.
func collectBatchConfigs(
	stdin io.Reader,
	urls []string,
	urlFile, country string,
	pool proxyPoolFlags,
	renderJS bool,
) (configs []*scrapfly.ScrapeConfig, poolFromFlags map[string]bool, err error) {
	proxyPool, asp := pool.resolve()
	// fromFlags[i] is configs[i]'s entry of poolFromFlags, keyed only once
	// every correlation ID is known.
	var fromFlags []bool
	addURL := func(u string) {
		configs = append(configs, &scrapfly.ScrapeConfig{
			URL:       u,
			Country:   country,
			ProxyPool: scrapfly.ProxyPool(proxyPool),
			RenderJS:  renderJS,
			ASP:       asp, // SDK field frozen; wire key stays "asp"
		})
		fromFlags = append(fromFlags, true)
	}

	for _, u := range urls {
		if u = strings.TrimSpace(u); u != "" {
			addURL(u)
		}
	}

	if urlFile != "" {
		f, err := os.Open(urlFile)
		if err != nil {
			return nil, nil, fmt.Errorf("open --url-file %s: %w", urlFile, err)
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			addURL(line)
		}
		if err := scanner.Err(); err != nil {
			return nil, nil, fmt.Errorf("read --url-file %s: %w", urlFile, err)
		}
	}

	// JSONL stdin mode — only when no --url/--url-file and stdin is piped.
	if len(configs) == 0 && isStdinPiped(stdin) {
		// The stdin lines naming each correlation_id.
		idLines := make(map[string][]int)
		scanner := bufio.NewScanner(stdin)
		scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
		for lineNo := 1; scanner.Scan(); lineNo++ {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			cfg, lineUnblocker, err := decodeBatchConfigLine([]byte(line))
			if err != nil {
				return nil, nil, fmt.Errorf("parse JSONL line: %w", err)
			}
			if cfg.CorrelationID != "" {
				idLines[cfg.CorrelationID] = append(idLines[cfg.CorrelationID], lineNo)
			}
			if cfg.Country == "" {
				cfg.Country = country
			}
			if !cfg.RenderJS {
				cfg.RenderJS = renderJS
			}
			// A line that names either key decides the Unblocker for itself,
			// false included; the flags are only a default.
			linePool, lineASP, lineFromFlags := pool.resolveLine(string(cfg.ProxyPool), lineUnblocker)
			cfg.ProxyPool = scrapfly.ProxyPool(linePool)
			cfg.ASP = lineASP
			configs = append(configs, cfg)
			fromFlags = append(fromFlags, lineFromFlags)
		}
		if err := scanner.Err(); err != nil {
			return nil, nil, fmt.Errorf("read stdin JSONL: %w", err)
		}
		if err := reusedCorrelationIDs(idLines); err != nil {
			return nil, nil, err
		}
	}

	assignBatchCorrelationIDs(configs)
	poolFromFlags = make(map[string]bool, len(configs))
	for i, cfg := range configs {
		poolFromFlags[cfg.CorrelationID] = fromFlags[i]
	}
	return configs, poolFromFlags, nil
}

// assignBatchCorrelationIDs gives each config without a correlation ID
// "item-N", N its 1-based position in the batch, one numbering whatever the
// source. When a line names that ID itself, the config takes the first
// "item-N" past the end of the batch that no config holds: no position
// reaches there, so a fallback never takes another config's ID.
func assignBatchCorrelationIDs(configs []*scrapfly.ScrapeConfig) {
	taken := make(map[string]bool, len(configs))
	for _, cfg := range configs {
		if cfg.CorrelationID != "" {
			taken[cfg.CorrelationID] = true
		}
	}
	past := len(configs)
	for i, cfg := range configs {
		if cfg.CorrelationID != "" {
			continue
		}
		id := fmt.Sprintf("item-%d", i+1)
		for taken[id] {
			past++
			id = fmt.Sprintf("item-%d", past)
		}
		cfg.CorrelationID = id
		taken[id] = true
	}
}

// reusedCorrelationIDs refuses a correlation_id named by more than one JSONL
// line, as the SDK and the API refuse it, before anything is sent. It names
// each such ID with its stdin lines (1-based, blank lines counted), where the
// SDK and the API name indexes into the batch. IDs compare as the API
// compares them: byte for byte, case included.
func reusedCorrelationIDs(idLines map[string][]int) error {
	var reused []string
	for id, lines := range idLines {
		if len(lines) > 1 {
			reused = append(reused, id)
		}
	}
	if len(reused) == 0 {
		return nil
	}
	// A line names one ID, so first lines tell the IDs apart.
	slices.SortFunc(reused, func(a, b string) int { return cmp.Compare(idLines[a][0], idLines[b][0]) })
	msgs := make([]string, len(reused))
	for i, id := range reused {
		lines := idLines[id]
		nums := make([]string, len(lines))
		for j, n := range lines {
			nums[j] = strconv.Itoa(n)
		}
		msgs[i] = fmt.Sprintf("correlation_id %q reused by JSONL lines %s and %s",
			id, strings.Join(nums[:len(nums)-1], ", "), nums[len(nums)-1])
	}
	return errors.New(strings.Join(msgs, "; ") + ": every config of a batch needs a correlation_id of its own")
}

// batchUltraRefusalHints returns, by correlation ID, the hint of a batch item
// refused with ERR::PROXY::UNBLOCKER_ULTRA_REQUIRED: the flags' hint when the
// item's pool came from the flags, else none. No flag changes the pool a JSONL
// line named, even the one the flags send, and the API's message names the
// pool to put on that line.
func batchUltraRefusalHints(poolFromFlags map[string]bool, pool proxyPoolFlags) func(correlationID string) string {
	hint := pool.ultraRefusalHint()
	return func(correlationID string) string {
		if poolFromFlags[correlationID] {
			return hint
		}
		return ""
	}
}

// decodeBatchConfigLine decodes one JSONL ScrapeConfig and returns, separately,
// the line's own anti-bot decision as a tri-state (nil = the line named
// neither key).
//
// ScrapeConfig carries no json struct tags, so encoding/json case-folds "asp"
// and "unblocker" onto two *different* fields and leaves precedence unstated,
// and never binds the wire spellings "proxy_pool" and "correlation_id" to
// ProxyPool and CorrelationID at all: this decodes them here, and each wins
// over the field-name key ("ProxyPool", "CorrelationID") on the same line.
// Worse, the SDK's own fallback consults its Unblocker field whenever the
// legacy bool is false — which cannot distinguish "absent" from "explicitly
// false", so a line saying {"asp": false, "unblocker": true} would come out
// enabled. Only this layer can see which keys the line actually contained, so
// it decides here and clears the SDK's Unblocker field after decoding, leaving
// ASP as the single field carrying the answer. Decode the original JSON into
// both structs so differently cased or repeated keys retain document order.
func decodeBatchConfigLine(line []byte) (*scrapfly.ScrapeConfig, *bool, error) {
	var bypass struct {
		ASP           *bool   `json:"asp"`
		Unblocker     *bool   `json:"unblocker"`
		ProxyPool     *string `json:"proxy_pool"`
		CorrelationID *string `json:"correlation_id"`
	}
	if err := json.Unmarshal(line, &bypass); err != nil {
		return nil, nil, err
	}

	var cfg scrapfly.ScrapeConfig
	if err := json.Unmarshal(line, &cfg); err != nil {
		return nil, nil, err
	}
	cfg.Unblocker = nil
	if bypass.ProxyPool != nil {
		cfg.ProxyPool = scrapfly.ProxyPool(*bypass.ProxyPool)
	}
	if bypass.CorrelationID != nil {
		cfg.CorrelationID = *bypass.CorrelationID
	}

	if bypass.ASP == nil && bypass.Unblocker == nil {
		return &cfg, nil, nil
	}
	resolved := resolveUnblocker(bypass.ASP, bypass.Unblocker)
	return &cfg, &resolved, nil
}
