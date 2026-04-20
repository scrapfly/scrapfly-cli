package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print CLI version",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("scrapfly %s (commit %s, built %s)\n", version, commit, date)
			// Non-blocking: cached to ~/.config/scrapfly-cli/update-check.json,
			// refreshed at most once per 24h, silent on network failure.
			if nag := maybeUpdateNag(); nag != "" {
				fmt.Fprintln(os.Stderr, nag)
			}
			return nil
		},
	}
}
