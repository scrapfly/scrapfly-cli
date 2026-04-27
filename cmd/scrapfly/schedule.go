package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	scrapfly "github.com/scrapfly/go-scrapfly"
	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"
)

func newScheduleCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Manage Scrapfly schedules across all products",
		Long: `Manage schedules across the Web Scraping, Screenshot and Crawler APIs.

Use this top-level group for cross-kind operations (list every schedule on
the account, get/update/cancel/pause/resume/execute by id regardless of
kind). The per-product subcommands (scrapfly scrape schedule create …,
scrapfly screenshot schedule create …, scrapfly crawl schedule create …)
are used to create new schedules , each takes the matching config payload.`,
	}
	cmd.AddCommand(newScheduleListCmd(flags, "")) // empty kind = cross-kind
	cmd.AddCommand(newScheduleGetCmd(flags))
	cmd.AddCommand(newScheduleUpdateCmd(flags))
	cmd.AddCommand(newScheduleDeleteCmd(flags))
	cmd.AddCommand(newSchedulePauseCmd(flags))
	cmd.AddCommand(newScheduleResumeCmd(flags))
	cmd.AddCommand(newScheduleExecuteCmd(flags))
	return cmd
}

func attachScheduleSubgroups(flags *rootFlags, scrapeCmd, screenshotCmd, crawlCmd *cobra.Command) {
	scrapeCmd.AddCommand(newPerKindScheduleCmd(flags, "scrape", "scrape_config"))
	screenshotCmd.AddCommand(newPerKindScheduleCmd(flags, "screenshot", "screenshot_config"))
	crawlCmd.AddCommand(newPerKindScheduleCmd(flags, "crawler", "crawler_config"))
}

func newPerKindScheduleCmd(flags *rootFlags, kind, configKey string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: fmt.Sprintf("Manage %s schedules", kind),
	}
	cmd.AddCommand(newScheduleCreateCmd(flags, kind, configKey))
	cmd.AddCommand(newScheduleListCmd(flags, kindToInternal(kind)))
	cmd.AddCommand(newScheduleGetCmd(flags))
	cmd.AddCommand(newScheduleUpdateCmd(flags))
	cmd.AddCommand(newScheduleDeleteCmd(flags))
	cmd.AddCommand(newSchedulePauseCmd(flags))
	cmd.AddCommand(newScheduleResumeCmd(flags))
	cmd.AddCommand(newScheduleExecuteCmd(flags))
	return cmd
}

func kindToInternal(kind string) string {
	switch kind {
	case "scrape":
		return "api.scrape"
	case "screenshot":
		return "api.screenshot"
	case "crawler":
		return "api.crawler"
	}
	return ""
}

func newScheduleCreateCmd(flags *rootFlags, kind, configKey string) *cobra.Command {
	var (
		configFile       string
		configInline     string
		webhookName      string
		cron             string
		intervalN        int
		intervalUnit     string
		scheduledDate    string
		notes            string
		maxRetries       int
		allowConcurrency bool
		retryOnFailure   bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: fmt.Sprintf("Create a new %s schedule", kind),
		Long: fmt.Sprintf(`Create a new %s schedule.

The %s payload is read from --config <file> (use - for stdin) or
--config-inline '{"url": "..."}'. Recurrence is either --cron '<expr>'
(5-field cron) or --interval N --unit <minute|hour|day|week|month>.

Examples:
  scrapfly %s schedule create \
    --config-inline '{"url":"https://web-scraping.dev/products","render_js":true}' \
    --webhook my-webhook --cron '0 */6 * * *'

  scrapfly %s schedule create \
    --config ./%s.json --webhook my-webhook --interval 6 --unit hour \
    --notes "every 6 hours"`, kind, configKey, kind, kind, configKey),
		RunE: func(cmd *cobra.Command, args []string) error {
			if webhookName == "" {
				return fmt.Errorf("--webhook is required (schedules run async, results are published to a webhook)")
			}
			cfg, err := loadConfigPayload(configFile, configInline)
			if err != nil {
				return err
			}
			rec, err := buildRecurrenceFlag(cron, intervalN, intervalUnit)
			if err != nil {
				return err
			}
			req := &scrapfly.CreateScheduleRequest{
				WebhookName:      webhookName,
				Recurrence:       rec,
				ScheduledDate:    scheduledDate,
				AllowConcurrency: allowConcurrency,
				RetryOnFailure:   retryOnFailure,
				MaxRetries:       maxRetries,
				Notes:            notes,
			}
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			var sched *scrapfly.Schedule
			switch kind {
			case "scrape":
				sched, err = client.CreateScrapeSchedule(cfg, req)
			case "screenshot":
				sched, err = client.CreateScreenshotSchedule(cfg, req)
			case "crawler":
				sched, err = client.CreateCrawlerSchedule(cfg, req)
			}
			if err != nil {
				return err
			}
			return emitSchedule(flags, sched)
		},
	}
	cmd.Flags().StringVar(&configFile, "config", "", "path to JSON file with the "+configKey+" payload (use - for stdin)")
	cmd.Flags().StringVar(&configInline, "config-inline", "", "inline JSON for the "+configKey+" payload")
	cmd.Flags().StringVar(&webhookName, "webhook", "", "webhook name to receive each fire's result (required)")
	cmd.Flags().StringVar(&cron, "cron", "", "cron expression (5-field). Wins over --interval when set")
	cmd.Flags().IntVar(&intervalN, "interval", 0, "interval value (use with --unit)")
	cmd.Flags().StringVar(&intervalUnit, "unit", "", "interval unit: minute|hour|day|week|month")
	cmd.Flags().StringVar(&scheduledDate, "scheduled-date", "", "RFC3339 timestamp for the first fire (default: now)")
	cmd.Flags().StringVar(&notes, "notes", "", "free-form notes saved on the schedule")
	cmd.Flags().IntVar(&maxRetries, "max-retries", 0, "retries per failed fire (0..3)")
	cmd.Flags().BoolVar(&allowConcurrency, "allow-concurrency", false, "permit overlapping fires (default: skip if previous still running)")
	cmd.Flags().BoolVar(&retryOnFailure, "retry-on-failure", false, "retry a failed fire up to --max-retries times")
	return cmd
}

func newScheduleListCmd(flags *rootFlags, internalKind string) *cobra.Command {
	var status string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List schedules on the account",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			opts := &scrapfly.ListSchedulesOptions{Status: status, Kind: internalKind}
			var schedules []scrapfly.Schedule
			switch internalKind {
			case "":
				schedules, err = client.ListSchedules(opts)
			case "api.scrape":
				schedules, err = client.ListScrapeSchedules(opts)
			case "api.screenshot":
				schedules, err = client.ListScreenshotSchedules(opts)
			case "api.crawler":
				schedules, err = client.ListCrawlerSchedules(opts)
			}
			if err != nil {
				return err
			}
			if flags.pretty {
				if len(schedules) == 0 {
					out.Pretty(os.Stdout, "no schedules")
					return nil
				}
				for _, s := range schedules {
					next := "-"
					if s.NextScheduledDate != nil {
						next = *s.NextScheduledDate
					}
					out.Pretty(os.Stdout, "id=%s kind=%s status=%s next=%s",
						s.ID, s.Kind, s.Status, next)
				}
				return nil
			}
			return out.WriteSuccess(os.Stdout, false, "schedule", schedules)
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "filter by status (ACTIVE | PAUSED | CANCELLED)")
	return cmd
}

func newScheduleGetCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Show one schedule by id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			s, err := client.GetSchedule(args[0])
			if err != nil {
				return err
			}
			return emitSchedule(flags, s)
		},
	}
}

func newScheduleUpdateCmd(flags *rootFlags) *cobra.Command {
	var (
		notes        string
		setNotes     bool
		cron         string
		intervalN    int
		intervalUnit string
		setRec       bool
		maxRetries   int
		setMaxRetry  bool
	)
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Patch an active schedule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &scrapfly.UpdateScheduleRequest{}
			if setNotes {
				req.Notes = &notes
			}
			if setRec {
				rec, err := buildRecurrenceFlag(cron, intervalN, intervalUnit)
				if err != nil {
					return err
				}
				req.Recurrence = rec
			}
			if setMaxRetry {
				req.MaxRetries = &maxRetries
			}
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			s, err := client.UpdateSchedule(args[0], req)
			if err != nil {
				return err
			}
			return emitSchedule(flags, s)
		},
	}
	cmd.Flags().StringVar(&notes, "notes", "", "replace notes")
	cmd.Flags().BoolVar(&setNotes, "set-notes", false, "send --notes (allows clearing with empty value)")
	cmd.Flags().StringVar(&cron, "cron", "", "new cron expression")
	cmd.Flags().IntVar(&intervalN, "interval", 0, "new interval value")
	cmd.Flags().StringVar(&intervalUnit, "unit", "", "new interval unit")
	cmd.Flags().BoolVar(&setRec, "set-recurrence", false, "rebuild recurrence from --cron / --interval / --unit")
	cmd.Flags().IntVar(&maxRetries, "max-retries", 0, "new max-retries value")
	cmd.Flags().BoolVar(&setMaxRetry, "set-max-retries", false, "send --max-retries")
	return cmd
}

func newScheduleDeleteCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "delete <id>",
		Aliases: []string{"cancel", "rm"},
		Short:   "Cancel a schedule (terminal , schedule cannot be resumed)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			if err := client.CancelSchedule(args[0]); err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "schedule", map[string]any{"id": args[0], "cancelled": true})
		},
	}
}

func newSchedulePauseCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "pause <id>",
		Short: "Pause an active schedule (no future fires until resumed)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			s, err := client.PauseSchedule(args[0])
			if err != nil {
				return err
			}
			return emitSchedule(flags, s)
		},
	}
}

func newScheduleResumeCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "resume <id>",
		Short: "Resume a paused schedule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			s, err := client.ResumeSchedule(args[0])
			if err != nil {
				return err
			}
			return emitSchedule(flags, s)
		},
	}
}

func newScheduleExecuteCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "execute <id>",
		Short: "Trigger an immediate fire regardless of next_scheduled_date",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			s, err := client.ExecuteSchedule(args[0])
			if err != nil {
				return err
			}
			return emitSchedule(flags, s)
		},
	}
}

func loadConfigPayload(file, inline string) (map[string]interface{}, error) {
	var raw []byte
	var err error
	switch {
	case inline != "":
		raw = []byte(inline)
	case file == "-":
		raw, err = io.ReadAll(os.Stdin)
	case file != "":
		raw, err = os.ReadFile(file)
	default:
		return nil, fmt.Errorf("--config <file> or --config-inline <json> is required")
	}
	if err != nil {
		return nil, fmt.Errorf("could not read config: %w", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("config is not valid JSON: %w", err)
	}
	return out, nil
}

func buildRecurrenceFlag(cron string, interval int, unit string) (*scrapfly.ScheduleRecurrence, error) {
	if cron == "" && interval == 0 && unit == "" {
		return nil, nil
	}
	if cron != "" && (interval != 0 || unit != "") {
		return nil, fmt.Errorf("--cron and --interval/--unit are mutually exclusive")
	}
	rec := &scrapfly.ScheduleRecurrence{}
	if cron != "" {
		rec.Cron = cron
		return rec, nil
	}
	if interval > 0 && unit == "" {
		return nil, fmt.Errorf("--unit is required when --interval is set")
	}
	if unit != "" && interval == 0 {
		return nil, fmt.Errorf("--interval is required when --unit is set")
	}
	rec.Interval = interval
	rec.Unit = strings.ToLower(unit)
	return rec, nil
}

func emitSchedule(flags *rootFlags, s *scrapfly.Schedule) error {
	if flags.pretty {
		next := "-"
		if s.NextScheduledDate != nil {
			next = *s.NextScheduledDate
		}
		out.Pretty(os.Stdout, "id=%s kind=%s status=%s next=%s",
			s.ID, s.Kind, s.Status, next)
		return nil
	}
	return out.WriteSuccess(os.Stdout, false, "schedule", s)
}
