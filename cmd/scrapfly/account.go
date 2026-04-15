package main

import (
	"os"

	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"
)

func newAccountCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "account",
		Short:   "Show account info, quota and subscription usage",
		Long:    `Fetch the account envelope (plan, concurrency, scrape credits used/remaining). Useful as a cheap API-key sanity check before burning credits.`,
		Example: `  scrapfly account --pretty`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			data, err := client.Account()
			if err != nil {
				return err
			}
			if flags.pretty {
				u := data.Subscription.Usage.Scrape
				out.Pretty(os.Stdout, "account=%s plan=%s scrape=%d/%d (remaining %d) concurrency=%d/%d",
					data.Account.AccountID, data.Subscription.PlanName,
					u.Current, u.Limit, u.Remaining, u.ConcurrentUsage, u.ConcurrentLimit)
				return nil
			}
			return out.WriteSuccess(os.Stdout, false, "account", data)
		},
	}
}
