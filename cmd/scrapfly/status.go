package main

import (
	"os"

	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"
)

type statusPayload struct {
	CLIVersion string `json:"cli_version"`
	Host       string `json:"host"`
	AuthOK     bool   `json:"auth_ok"`
	AccountID  string `json:"account_id,omitempty"`
	Plan       string `json:"plan,omitempty"`
	ScrapeUsed int    `json:"scrape_used,omitempty"`
	ScrapeMax  int    `json:"scrape_limit,omitempty"`
	Remaining  int    `json:"scrape_remaining,omitempty"`
	Concurrent int    `json:"concurrent_usage,omitempty"`
	MaxConcur  int    `json:"concurrent_limit,omitempty"`
}

func resolveHost(flags *rootFlags) string {
	if flags.host != "" {
		return flags.host
	}
	if h := os.Getenv("SCRAPFLY_API_HOST"); h != "" {
		return h
	}
	if cfg, _ := loadConfig(); cfg != nil && cfg.Host != "" {
		return cfg.Host
	}
	return "https://api.scrapfly.io"
}

func runStatus(flags *rootFlags) error {
	p := statusPayload{CLIVersion: version, Host: resolveHost(flags)}

	client, err := buildClient(flags)
	if err != nil {
		return err
	}
	verify, err := client.VerifyAPIKey()
	if err != nil {
		return err
	}
	p.AuthOK = verify.Valid

	if p.AuthOK {
		if acct, err := client.Account(); err == nil {
			p.AccountID = acct.Account.AccountID
			p.Plan = acct.Subscription.PlanName
			p.ScrapeUsed = acct.Subscription.Usage.Scrape.Current
			p.ScrapeMax = acct.Subscription.Usage.Scrape.Limit
			p.Remaining = acct.Subscription.Usage.Scrape.Remaining
			p.Concurrent = acct.Subscription.Usage.Scrape.ConcurrentUsage
			p.MaxConcur = acct.Subscription.Usage.Scrape.ConcurrentLimit
		}
	}

	if flags.pretty {
		out.Pretty(os.Stdout, "scrapfly %s  host=%s  auth=%v  plan=%s  scrape=%d/%d (remaining %d)  concurrency=%d/%d",
			p.CLIVersion, p.Host, p.AuthOK, p.Plan,
			p.ScrapeUsed, p.ScrapeMax, p.Remaining, p.Concurrent, p.MaxConcur)
		return nil
	}
	return out.WriteSuccess(os.Stdout, false, "status", p)
}

func newStatusCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "status",
		Short:   "Print CLI version, auth status, and account usage",
		Example: `  scrapfly status --pretty`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(flags)
		},
	}
}
