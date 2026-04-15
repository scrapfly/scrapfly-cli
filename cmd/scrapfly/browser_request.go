package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/scrapfly/scrapfly-cli/internal/sessiond"
	"github.com/spf13/cobra"
)

func newBrowserRequestCmd(flags *rootFlags) *cobra.Command {
	var (
		body     bool
		bodyOnly bool
	)
	cmd := &cobra.Command{
		Use:   "request <url-pattern>",
		Short: "List captured XHR/fetch/resource requests matching a URL glob",
		Long: `Scans the session's captured Network.responseReceived events for URLs
matching the given glob pattern. Supports * (any chars) and ? (single char).

The session must have navigated to a page first; Network events are captured
from the moment the session starts.

By default prints a summary (url, status, mime_type, type). --body includes
the response body via Network.getResponseBody. --body-only prints the raw
body of the first match to stdout (pipe-friendly).`,
		Example: `  scrapfly browser navigate https://web-scraping.dev/products
  scrapfly browser request '*'                          # all requests
  scrapfly browser request '*://web-scraping.dev/*'     # by host
  scrapfly browser request '*.css'                      # stylesheets
  scrapfly browser request '*/api/*' --body             # API calls with body
  scrapfly browser request '*/api/products*' --body-only > data.json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sid, ok := sessiond.Resolve(sessionIDFlag)
			if !ok {
				return fmt.Errorf("no active session")
			}
			includeBody := body || bodyOnly
			resp, err := sessiond.Send(sid, sessiond.Request{
				Action:   "request",
				Selector: args[0],
				Visible:  includeBody,
			})
			if err != nil {
				return err
			}
			var data struct {
				Matches []map[string]any `json:"matches"`
				Count   int              `json:"count"`
			}
			_ = json.Unmarshal(resp.Data, &data)

			if bodyOnly {
				if len(data.Matches) == 0 {
					return fmt.Errorf("no requests matched %q", args[0])
				}
				b, _ := data.Matches[0]["body"].(string)
				fmt.Fprint(os.Stdout, b)
				return nil
			}
			if flags.pretty {
				for _, m := range data.Matches {
					out.Pretty(os.Stdout, "%v %v %v", m["status"], m["mime_type"], m["url"])
				}
				out.Pretty(os.Stdout, "(%d matches)", data.Count)
				return nil
			}
			return out.WriteSuccess(os.Stdout, false, "browser.request", data)
		},
	}
	cmd.Flags().BoolVar(&body, "body", false, "include the response body for each match")
	cmd.Flags().BoolVar(&bodyOnly, "body-only", false, "output the raw body of the first match (pipe-friendly)")
	return cmd
}
