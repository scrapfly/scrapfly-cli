package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/scrapfly/scrapfly-cli/internal/sessiond"
	"github.com/spf13/cobra"
)

func newBrowserResponseCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "response",
		Short: "Show the navigation response chain (redirect hops + final status/headers) via Page.getNavigationResponse",
		Long: `Returns every HTTP response the browser saw while loading the current page,
including intermediate redirects (301/302/307/308) and the final 200. Each
entry contains url, statusCode, and response headers.

Scrapfly-browser-only (custom CDP domain).`,
		Example: `  scrapfly browser navigate https://httpbin.dev/redirect/3
  scrapfly browser response --pretty`,
		RunE: func(cmd *cobra.Command, args []string) error {
			sid, ok := sessiond.Resolve(sessionIDFlag)
			if !ok {
				return fmt.Errorf("no active session")
			}
			resp, err := sessiond.Send(sid, sessiond.Request{Action: "response"})
			if err != nil {
				return err
			}
			var data any
			_ = json.Unmarshal(resp.Data, &data)
			if flags.pretty {
				if m, ok := data.(map[string]any); ok {
					if entries, ok := m["entries"].([]any); ok {
						for _, e := range entries {
							em, _ := e.(map[string]any)
							out.Pretty(os.Stdout, "%v %v", em["statusCode"], em["url"])
						}
						return nil
					}
				}
			}
			return out.WriteSuccess(os.Stdout, false, "browser.response", data)
		},
	}
}
