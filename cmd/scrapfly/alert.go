package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	scrapfly "github.com/scrapfly/go-scrapfly"
	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"
)

func newAlertCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "alert",
		Short: "Manage threshold alerts on account metrics",
		Long: `Manage threshold alerts on Scrapfly account metrics.

Typical workflow:
  1. scrapfly alert metric-families         # discover legal metric_ids
  2. scrapfly alert preview --metric ...    # tune threshold against history
  3. scrapfly alert create --metric ...     # persist the rule
  4. scrapfly alert test <uuid>             # verify channels deliver
  5. scrapfly alert list / get / update / snooze / unsnooze / delete`,
	}
	cmd.AddCommand(
		newAlertListCmd(flags),
		newAlertGetCmd(flags),
		newAlertCountActiveCmd(flags),
		newAlertMetricFamiliesCmd(flags),
		newAlertSeriesCmd(flags),
		newAlertCreateCmd(flags),
		newAlertUpdateCmd(flags),
		newAlertDeleteCmd(flags),
		newAlertSnoozeCmd(flags),
		newAlertUnsnoozeCmd(flags),
		newAlertTestCmd(flags),
		newAlertPreviewCmd(flags),
	)
	return cmd
}

func newAlertListCmd(flags *rootFlags) *cobra.Command {
	var state, projectUUID, metric string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List alert definitions for the calling account",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			alerts, err := client.ListAlerts(scrapfly.AlertListOptions{
				ProjectUUID: projectUUID,
				State:       scrapfly.AlertState(state),
				MetricID:    metric,
			})
			if err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "alert", alerts)
		},
	}
	cmd.Flags().StringVar(&state, "state", "", "filter by state: ok|pending|triggered|recovering|no_data|snoozed")
	cmd.Flags().StringVar(&projectUUID, "project-uuid", "", "filter by project UUID")
	cmd.Flags().StringVar(&metric, "metric", "", "filter by metric_id")
	return cmd
}

func newAlertGetCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "get <alert-uuid>",
		Short: "Fetch one alert definition by UUID",
		Long: `Fetch one alert definition by UUID. The response includes the alert's
HMAC signing key — copy it into your webhook receiver's verifier config to
authenticate inbound notifications.

A 404 is returned for both "doesn't exist" and "exists but isn't yours" so
the API can't be used as an existence oracle.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			a, err := client.GetAlert(args[0])
			if err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "alert", a)
		},
	}
}

func newAlertCountActiveCmd(flags *rootFlags) *cobra.Command {
	var projectUUID string
	cmd := &cobra.Command{
		Use:   "count-active",
		Short: "Count alerts in actively-firing states (triggered|pending|recovering)",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			res, err := client.CountActiveAlerts(projectUUID)
			if err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "alert", res)
		},
	}
	cmd.Flags().StringVar(&projectUUID, "project-uuid", "", "restrict to a single project UUID")
	return cmd
}

func newAlertMetricFamiliesCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "metric-families",
		Short: "List every metric family available for alerting",
		Long: `Use this to discover legal --metric values for alert create and to see each
metric's allowed dimensions, native bucket grain, and default sustained window.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			fams, err := client.ListAlertMetricFamilies()
			if err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "alert", fams)
		},
	}
}

func newAlertSeriesCmd(flags *rootFlags) *cobra.Command {
	var rangeMinutes int
	cmd := &cobra.Command{
		Use:   "series <alert-uuid>",
		Short: "Fetch the metric time series + state-change markers for an alert",
		Long: `--range-minutes is the lookback window (default 240 = 4h, max 10080 = 7d).
Values outside that bound are silently clamped server-side.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			res, err := client.GetAlertSeries(args[0], rangeMinutes)
			if err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "alert", res)
		},
	}
	cmd.Flags().IntVar(&rangeMinutes, "range-minutes", 240, "lookback window in minutes")
	return cmd
}

func newAlertCreateCmd(flags *rootFlags) *cobra.Command {
	var (
		name, description, projectUUID    string
		metric, comparator, noDataPolicy  string
		dimensions, file                  string
		threshold                         float64
		sustainedMinutes, recoveryMinutes int
		evalWindowM, evalCadenceSeconds   int
		renotifyMinutes                   int
		channels                          []string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new alert definition",
		Long: `Required: --name, --metric, --comparator, --threshold, at least one --channel.
Channel format: kind:target (kind=email|webhook|inapp). Example:
  --channel email:alerts@example.com
  --channel webhook:https://hooks.example.com/scrapfly

Alternatively --file path.json reads a full AlertCreateRequest envelope from
disk — flags override file fields when both are present.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			req, err := buildAlertCreateRequest(cmd, file, name, description, projectUUID, metric, comparator, noDataPolicy, dimensions, threshold, sustainedMinutes, recoveryMinutes, evalWindowM, evalCadenceSeconds, renotifyMinutes, channels)
			if err != nil {
				return err
			}
			if err := scrapfly.ValidateAlertCreate(req); err != nil {
				return err
			}
			a, err := client.CreateAlert(req)
			if err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "alert", a)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "alert name")
	cmd.Flags().StringVar(&description, "description", "", "alert description")
	cmd.Flags().StringVar(&projectUUID, "project-uuid", "", "project UUID (default: caller's current)")
	cmd.Flags().StringVar(&metric, "metric", "", "metric_id (see `scrapfly alert metric-families`)")
	cmd.Flags().StringVar(&comparator, "comparator", "", "threshold comparator: gt|lt|gte|lte|eq|neq")
	cmd.Flags().Float64Var(&threshold, "threshold", 0, "threshold value")
	cmd.Flags().IntVar(&sustainedMinutes, "sustained-minutes", 0, "minutes the condition must hold")
	cmd.Flags().IntVar(&recoveryMinutes, "recovery-minutes", 0, "minutes of clean data before OK")
	cmd.Flags().IntVar(&evalWindowM, "evaluation-window-m", 0, "evaluation window minutes")
	cmd.Flags().IntVar(&evalCadenceSeconds, "eval-cadence-seconds", 0, "evaluator cadence in seconds")
	cmd.Flags().IntVar(&renotifyMinutes, "renotify-minutes", 0, "re-notify cadence while firing")
	cmd.Flags().StringVar(&noDataPolicy, "no-data-policy", "", "ok | triggered | ignore")
	cmd.Flags().StringVar(&dimensions, "dimensions", "", "metric dimension filter (JSON object)")
	cmd.Flags().StringArrayVar(&channels, "channel", nil, "notify channel kind:target (repeatable; commas are kept literally)")
	cmd.Flags().StringVar(&file, "file", "", "load full AlertCreateRequest envelope from JSON file")
	return cmd
}

func buildAlertCreateRequest(cmd *cobra.Command, file, name, description, projectUUID, metric, comparator, noDataPolicy, dimensions string, threshold float64, sustainedMinutes, recoveryMinutes, evalWindowM, evalCadenceSeconds, renotifyMinutes int, channels []string) (scrapfly.AlertCreateRequest, error) {
	var req scrapfly.AlertCreateRequest
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return req, fmt.Errorf("read --file: %w", err)
		}
		if err := json.Unmarshal(b, &req); err != nil {
			return req, fmt.Errorf("parse --file JSON: %w", err)
		}
	}
	if cmd.Flags().Changed("name") {
		req.Name = name
	}
	if cmd.Flags().Changed("description") {
		req.Description = description
	}
	if cmd.Flags().Changed("project-uuid") {
		req.ProjectUUID = projectUUID
	}
	if cmd.Flags().Changed("metric") {
		req.MetricID = metric
	}
	if cmd.Flags().Changed("comparator") {
		req.Comparator = scrapfly.AlertComparator(comparator)
	}
	if cmd.Flags().Changed("threshold") {
		req.Threshold = threshold
	}
	if cmd.Flags().Changed("sustained-minutes") {
		req.SustainedMinutes = sustainedMinutes
	}
	if cmd.Flags().Changed("recovery-minutes") {
		req.RecoveryMinutes = recoveryMinutes
	}
	if cmd.Flags().Changed("evaluation-window-m") {
		req.EvaluationWindowM = evalWindowM
	}
	if cmd.Flags().Changed("eval-cadence-seconds") {
		req.EvalCadenceSeconds = evalCadenceSeconds
	}
	if cmd.Flags().Changed("renotify-minutes") {
		req.RenotifyMinutes = renotifyMinutes
	}
	if cmd.Flags().Changed("no-data-policy") {
		req.NoDataPolicy = scrapfly.AlertNoDataPolicy(noDataPolicy)
	}
	if cmd.Flags().Changed("dimensions") {
		req.MetricDimensions = json.RawMessage(dimensions)
	}
	if cmd.Flags().Changed("channel") {
		parsed, err := parseAlertChannels(channels)
		if err != nil {
			return req, err
		}
		req.NotifyChannels = parsed
	}
	return req, nil
}

func parseAlertChannels(raw []string) ([]scrapfly.AlertNotifyChannel, error) {
	parsed := make([]scrapfly.AlertNotifyChannel, 0, len(raw))
	for _, s := range raw {
		s = strings.TrimSpace(s)
		idx := strings.Index(s, ":")
		if idx < 0 {
			return nil, fmt.Errorf("invalid --channel %q (expected kind:target)", s)
		}
		parsed = append(parsed, scrapfly.AlertNotifyChannel{
			Kind:   scrapfly.AlertNotifyKind(strings.TrimSpace(s[:idx])),
			Target: strings.TrimSpace(s[idx+1:]),
		})
	}
	return parsed, nil
}

func newAlertUpdateCmd(flags *rootFlags) *cobra.Command {
	var (
		name, description, comparator, noDataPolicy string
		threshold                                   float64
		enabled                                     bool
		sustainedMinutes, recoveryMinutes           int
		evalWindowM, renotifyMinutes                int
		channels                                    []string
	)
	cmd := &cobra.Command{
		Use:   "update <alert-uuid>",
		Short: "Patch fields on an existing alert",
		Long: `Only flags explicitly set are sent — unset flags preserve the server-side
value. After a successful update the alert is auto-snoozed for sustained-minutes
so a tuning iteration doesn't refire on the next evaluator tick.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			var req scrapfly.AlertUpdateRequest
			if cmd.Flags().Changed("name") {
				req.Name = &name
			}
			if cmd.Flags().Changed("description") {
				req.Description = &description
			}
			if cmd.Flags().Changed("enabled") {
				req.Enabled = &enabled
			}
			if cmd.Flags().Changed("comparator") {
				c := scrapfly.AlertComparator(comparator)
				req.Comparator = &c
			}
			if cmd.Flags().Changed("threshold") {
				req.Threshold = &threshold
			}
			if cmd.Flags().Changed("sustained-minutes") {
				req.SustainedMinutes = &sustainedMinutes
			}
			if cmd.Flags().Changed("recovery-minutes") {
				req.RecoveryMinutes = &recoveryMinutes
			}
			if cmd.Flags().Changed("evaluation-window-m") {
				req.EvaluationWindowM = &evalWindowM
			}
			if cmd.Flags().Changed("renotify-minutes") {
				req.RenotifyMinutes = &renotifyMinutes
			}
			if cmd.Flags().Changed("no-data-policy") {
				p := scrapfly.AlertNoDataPolicy(noDataPolicy)
				req.NoDataPolicy = &p
			}
			if cmd.Flags().Changed("channel") {
				parsed, err := parseAlertChannels(channels)
				if err != nil {
					return err
				}
				req.NotifyChannels = parsed
			}
			a, err := client.UpdateAlert(args[0], req)
			if err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "alert", a)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "new alert name")
	cmd.Flags().StringVar(&description, "description", "", "new description")
	cmd.Flags().BoolVar(&enabled, "enabled", true, "enable/disable the alert")
	cmd.Flags().StringVar(&comparator, "comparator", "", "new comparator")
	cmd.Flags().Float64Var(&threshold, "threshold", 0, "new threshold value")
	cmd.Flags().IntVar(&sustainedMinutes, "sustained-minutes", 0, "new sustained window")
	cmd.Flags().IntVar(&recoveryMinutes, "recovery-minutes", 0, "new recovery window")
	cmd.Flags().IntVar(&evalWindowM, "evaluation-window-m", 0, "new eval window")
	cmd.Flags().IntVar(&renotifyMinutes, "renotify-minutes", 0, "new re-notify cadence")
	cmd.Flags().StringVar(&noDataPolicy, "no-data-policy", "", "new no-data policy")
	cmd.Flags().StringArrayVar(&channels, "channel", nil, "replace notify channels (repeatable; commas are kept literally)")
	return cmd
}

func newAlertDeleteCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "delete <alert-uuid>",
		Aliases: []string{"rm"},
		Short:   "Delete an alert definition",
		Long: `Stops further evaluations and notifications immediately. The audit trail
(alert_event rows in ClickHouse) is preserved. Idempotent on the happy path
— a second delete for the same UUID returns 404.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			res, err := client.DeleteAlert(args[0])
			if err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "alert", res)
		},
	}
}

func newAlertSnoozeCmd(flags *rootFlags) *cobra.Command {
	var minutes int
	var untilResolved bool
	cmd := &cobra.Command{
		Use:   "snooze <alert-uuid>",
		Short: "Mute notifications for N minutes or until next OK",
		Long: `Exactly one of --minutes or --until-resolved must be set. The former is a
time-bound snooze; the latter mutes notifications until the next OK transition.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			a, err := client.SnoozeAlert(args[0], scrapfly.AlertSnoozeRequest{
				Minutes:       minutes,
				UntilResolved: untilResolved,
			})
			if err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "alert", a)
		},
	}
	cmd.Flags().IntVar(&minutes, "minutes", 0, "minutes to snooze")
	cmd.Flags().BoolVar(&untilResolved, "until-resolved", false, "snooze until next OK transition")
	cmd.MarkFlagsMutuallyExclusive("minutes", "until-resolved")
	cmd.MarkFlagsOneRequired("minutes", "until-resolved")
	return cmd
}

func newAlertUnsnoozeCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "unsnooze <alert-uuid>",
		Short: "Lift any active snooze so notifications resume",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			a, err := client.UnsnoozeAlert(args[0])
			if err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "alert", a)
		},
	}
}

func newAlertTestCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "test <alert-uuid>",
		Short: "Fire a synthetic notification on every configured channel",
		Long: `Verifies webhook URLs and email addresses end-to-end. Dedup: two test fires
within the same UTC minute share an event_id and the second is suppressed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			res, err := client.TestAlert(args[0])
			if err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "alert", res)
		},
	}
}

func newAlertPreviewCmd(flags *rootFlags) *cobra.Command {
	var (
		metric, comparator, noDataPolicy, dimensions, projectUUID string
		threshold                                                 float64
		sustainedMinutes, evalWindowM, rangeMinutes               int
	)
	cmd := &cobra.Command{
		Use:   "preview",
		Short: "Replay an unsaved rule against historical data (no persistence)",
		Long: `Report how many times a hypothetical rule WOULD have fired in the lookback
window, without creating the alert. Uses the same state-machine Tick() the
live evaluator uses, so the preview count matches what production would have
produced.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			req := scrapfly.AlertPreviewRequest{
				MetricID:          metric,
				Comparator:        scrapfly.AlertComparator(comparator),
				Threshold:         threshold,
				SustainedMinutes:  sustainedMinutes,
				EvaluationWindowM: evalWindowM,
				RangeMinutes:      rangeMinutes,
				NoDataPolicy:      scrapfly.AlertNoDataPolicy(noDataPolicy),
				ProjectUUID:       projectUUID,
			}
			if dimensions != "" {
				req.MetricDimensions = []byte(dimensions)
			}
			res, err := client.PreviewAlert(req)
			if err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "alert", res)
		},
	}
	cmd.Flags().StringVar(&metric, "metric", "", "metric_id (required)")
	cmd.Flags().StringVar(&comparator, "comparator", "", "comparator: gt|lt|gte|lte|eq|neq (required)")
	cmd.Flags().Float64Var(&threshold, "threshold", 0, "threshold value (required)")
	cmd.Flags().IntVar(&sustainedMinutes, "sustained-minutes", 0, "sustained window")
	cmd.Flags().IntVar(&evalWindowM, "evaluation-window-m", 0, "eval window")
	cmd.Flags().IntVar(&rangeMinutes, "range-minutes", 1440, "lookback window")
	cmd.Flags().StringVar(&noDataPolicy, "no-data-policy", "", "no-data policy")
	cmd.Flags().StringVar(&dimensions, "dimensions", "", "metric dimension filter (JSON object)")
	cmd.Flags().StringVar(&projectUUID, "project-uuid", "", "project UUID")
	_ = cmd.MarkFlagRequired("metric")
	_ = cmd.MarkFlagRequired("comparator")
	_ = cmd.MarkFlagRequired("threshold")
	return cmd
}
