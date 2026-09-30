package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"unicode"

	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"
)

// Product areas a report can be filed against. Mirrors Feedback::CLI_FEATURES
// in web-app; validated here as well as server-side so an agent that guesses a
// name is corrected without spending a round trip on it.
var feedbackFeatures = []string{
	"web_scraping_api",
	"screenshot_api",
	"extraction_api",
	"crawler_api",
	"cloud_browser",
	"rpa",
	"data_api",
	"proxy_saver",
	"vault",
	"alerting",
	"scheduler",
	"account",
	"billing",
	"dashboard",
	"docs",
	"cli",
	"mcp",
	"skills",
	"sdk",
	"other",
}

var feedbackSentiments = []string{"positive", "neutral", "negative"}

const (
	feedbackTextMin      = 10
	feedbackTextMax      = 2000
	feedbackMessageWords = 5
	// A body that is not the server's JSON rejection is an error page, and it
	// gets the same 1 KiB the BYOP bundle download allows for the same case.
	feedbackErrorBodyMax = 1024
)

// Environment variables set by coding agents that shell out to this CLI. Their
// presence is what flips the default actor to "agent": a report written by a
// model while driving the product is read differently from a developer's own
// words, and the distinction is worth having without asking for a flag.
var agentEnvVars = []string{
	"CLAUDECODE",
	"CLAUDE_CODE_ENTRYPOINT",
	"CURSOR_AGENT",
	"CURSOR_TRACE_ID",
	"AIDER_MODEL",
	"GEMINI_CLI",
	"CODEX_SANDBOX",
	"OPENCODE",
	"WINDSURF_SESSION_ID",
	"CLINE_SESSION_ID",
	"OPENHANDS_WORKSPACE_BASE",
}

type feedbackFlags struct {
	sentiment string
	message   string
	context   string
	feature   string
	actor     string
}

func newFeedbackCmd(flags *rootFlags) *cobra.Command {
	var f feedbackFlags

	cmd := &cobra.Command{
		Use:   "feedback",
		Short: "Report what worked or broke in a Scrapfly product, from the terminal",
		Long: `File product feedback on Scrapfly from the terminal: the CLI itself, an API,
the docs, the dashboard, the MCP server or a skill.

This is not support. Nothing here opens a ticket and no reply comes back. For
an account or integration problem use https://scrapfly.io/dashboard/support.

When to file (agents included):
  negative  a product defect or blocker stopped a command from working
  neutral   friction or a workaround, but the task completed
  positive  it worked, or a step behaved better than expected

File positive reports too, and file at most one report per session, at the end,
about what you actually observed. Name the command and the result in --message
and the goal in --context; "it broke" is not a report anyone can act on.

Report the product surface, not your own environment, not the model's
behaviour, and not an error you recovered from on the next try. Show the draft
to the person you are working for before filing on their key.

A failed submission is never a reason to fail or retry the task it came from.
On 429 the account's hourly budget is spent: drop the report, do not retry.

Feature areas:
  ` + strings.Join(feedbackFeatures, ", "),
		Example: `  scrapfly feedback --sentiment negative --feature docs \
    --message "docs search returned German pages for an English query on the disputes page" \
    --context "Writing chargeback evidence for a metered SaaS account, CLI 0.4.1"

  scrapfly feedback --sentiment positive --feature crawler_api \
    --message "crawl run --max-pages 20 finished in one call and the ndjson piped straight into jq" \
    --context "Evaluating the crawler for a 20-page docs mirror"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runFeedback(cmd.Context(), flags, &f)
		},
	}

	cmd.Flags().StringVar(&f.sentiment, "sentiment", "", "required: "+strings.Join(feedbackSentiments, "|"))
	cmd.Flags().StringVar(&f.message, "message", "", fmt.Sprintf("required: what happened (%d-%d chars, at least %d words)", feedbackTextMin, feedbackTextMax, feedbackMessageWords))
	cmd.Flags().StringVar(&f.context, "context", "", fmt.Sprintf("required: what you were trying to do (%d-%d chars)", feedbackTextMin, feedbackTextMax))
	cmd.Flags().StringVar(&f.feature, "feature", "", "product area the report is about (see list in --help)")
	cmd.Flags().StringVar(&f.actor, "actor", "", "who is filing: human|agent (default: agent when run by a recognized coding agent)")

	return cmd
}

type feedbackRequest struct {
	Sentiment     string `json:"sentiment"`
	Message       string `json:"message"`
	Context       string `json:"context"`
	Feature       string `json:"feature,omitempty"`
	Actor         string `json:"actor"`
	Surface       string `json:"surface"`
	ClientVersion string `json:"client_version,omitempty"`
	ClientOS      string `json:"client_os,omitempty"`
}

type feedbackResponse struct {
	Success   bool   `json:"success"`
	UUID      string `json:"uuid"`
	Sentiment string `json:"sentiment"`
	Feature   string `json:"feature,omitempty"`
	Actor     string `json:"actor"`
}

func runFeedback(ctx context.Context, flags *rootFlags, f *feedbackFlags) error {
	req, err := buildFeedbackRequest(f)
	if err != nil {
		return err
	}

	apiKey, webBase, err := resolveFeedbackTarget(flags)
	if err != nil {
		return err
	}

	res, err := postFeedback(ctx, flags, webBase, apiKey, req)
	if err != nil {
		return err
	}

	if flags.pretty {
		out.Pretty(os.Stdout, "feedback %s filed (%s%s)", res.UUID, res.Sentiment, featureSuffix(res.Feature))
		return nil
	}
	return out.WriteSuccess(os.Stdout, false, "feedback", res)
}

func featureSuffix(feature string) string {
	if feature == "" {
		return ""
	}
	return ", " + feature
}

// buildFeedbackRequest validates locally with the same bounds the server
// enforces. Every rejection names the field and lists the legal values, so the
// caller can fix the command from the error alone.
func buildFeedbackRequest(f *feedbackFlags) (*feedbackRequest, error) {
	sentiment := strings.ToLower(strings.TrimSpace(f.sentiment))
	if !contains(feedbackSentiments, sentiment) {
		return nil, fmt.Errorf("--sentiment is required and must be one of: %s", strings.Join(feedbackSentiments, ", "))
	}

	message := strings.TrimSpace(f.message)
	if err := validateFeedbackText("--message", message, feedbackMessageWords); err != nil {
		return nil, err
	}

	reportContext := strings.TrimSpace(f.context)
	if err := validateFeedbackText("--context", reportContext, 0); err != nil {
		return nil, err
	}

	feature := strings.ToLower(strings.TrimSpace(f.feature))
	if feature != "" && !contains(feedbackFeatures, feature) {
		return nil, fmt.Errorf("--feature %q is not a product area; one of: %s", feature, strings.Join(feedbackFeatures, ", "))
	}

	actor := strings.ToLower(strings.TrimSpace(f.actor))
	switch actor {
	case "":
		actor = defaultFeedbackActor()
	case "human", "agent":
	default:
		return nil, fmt.Errorf("--actor %q is not valid; one of: human, agent", actor)
	}

	return &feedbackRequest{
		Sentiment:     sentiment,
		Message:       message,
		Context:       reportContext,
		Feature:       feature,
		Actor:         actor,
		Surface:       "cli",
		ClientVersion: version,
		ClientOS:      runtime.GOOS + "/" + runtime.GOARCH,
	}, nil
}

func validateFeedbackText(flag, value string, minWords int) error {
	// Runes, not bytes: the server counts characters, and a report written in
	// a non-ASCII language would otherwise pass here and be rejected there.
	length := len([]rune(value))
	if length < feedbackTextMin || length > feedbackTextMax {
		return fmt.Errorf("%s must be %d-%d characters, got %d", flag, feedbackTextMin, feedbackTextMax, length)
	}
	if minWords > 0 && countWords(value) < minWords {
		return fmt.Errorf("%s must be at least %d words: name the command and what it did", flag, minWords)
	}
	return nil
}

func countWords(value string) int {
	return len(strings.FieldsFunc(value, unicode.IsSpace))
}

func defaultFeedbackActor() string {
	for _, env := range agentEnvVars {
		if os.Getenv(env) != "" {
			return "agent"
		}
	}
	return "human"
}

// resolveFeedbackTarget returns the api key plus the web base URL that serves
// the feedback endpoint. Feedback lives in web-app (it shares the table the
// dashboard widget and the admin review list already use), not on the API
// host, so the api host is mapped to its base domain the same way
// dashboardURLForHost does for the browser.
func resolveFeedbackTarget(flags *rootFlags) (apiKey, webBase string, err error) {
	apiKey = flags.apiKey
	if apiKey == "" {
		apiKey = os.Getenv("SCRAPFLY_API_KEY")
	}
	host := flags.host
	if host == "" {
		host = os.Getenv("SCRAPFLY_API_HOST")
	}
	if apiKey == "" || host == "" {
		if cfg, cfgErr := loadConfig(); cfgErr == nil && cfg != nil {
			if apiKey == "" {
				apiKey = cfg.APIKey
			}
			if host == "" {
				host = cfg.Host
			}
		}
	}
	if apiKey == "" {
		return "", "", errMissingAPIKey
	}
	return apiKey, webBaseForHost(host), nil
}

func postFeedback(ctx context.Context, flags *rootFlags, webBase, apiKey string, payload *feedbackRequest) (*feedbackResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	url := strings.TrimRight(webBase, "/") + "/dashboard/api/feedback"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	// Bearer, not ?api_key=: a query-string key would be written to web-app's
	// access logs for every report filed.
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	// Marks the caller as a machine client, so any guard in front of the route
	// answers with a JSON status instead of a redirect to the login page.
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "scrapfly-cli/"+version)

	tr := http.DefaultTransport.(*http.Transport).Clone()
	if flags.insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	client := &http.Client{Timeout: flags.timeout, Transport: tr}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, feedbackError(resp.StatusCode, raw)
	}

	var res feedbackResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("feedback endpoint returned unreadable body (http %d): %w", resp.StatusCode, err)
	}
	return &res, nil
}

// feedbackError turns the server's rejection into a structured CLI error,
// folding whichever "allowed_*" list it echoed into the message so the caller
// can correct the flag without a second request.
func feedbackError(status int, raw []byte) error {
	var payload struct {
		Error             string   `json:"error"`
		Message           string   `json:"message"`
		AllowedSentiments []string `json:"allowed_sentiments"`
		AllowedFeatures   []string `json:"allowed_features"`
		AllowedActors     []string `json:"allowed_actors"`
		AllowedSurfaces   []string `json:"allowed_surfaces"`
	}

	apiErr := &out.APIError{HTTPStatus: status, Message: fmt.Sprintf("feedback rejected (http %d)", status)}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.Message == "" {
		if body := condenseFeedbackErrorBody(raw); body != "" {
			apiErr.Message = fmt.Sprintf("feedback rejected (http %d): %s", status, body)
		}
		return annotateFeedbackError(apiErr, status)
	}

	apiErr.Code = payload.Error
	apiErr.Message = payload.Message
	for _, allowed := range [][]string{payload.AllowedSentiments, payload.AllowedFeatures, payload.AllowedActors, payload.AllowedSurfaces} {
		if len(allowed) > 0 {
			values := append([]string(nil), allowed...)
			sort.Strings(values)
			apiErr.Message += " Allowed: " + strings.Join(values, ", ") + "."
		}
	}
	return annotateFeedbackError(apiErr, status)
}

// condenseFeedbackErrorBody caps an opaque body and flattens it to one line, so
// an HTML error page from a proxy or a dev-mode stack trace cannot become the
// error message. Invalid UTF-8 goes with it: the message is marshalled as JSON.
func condenseFeedbackErrorBody(raw []byte) string {
	if len(raw) > feedbackErrorBodyMax {
		raw = raw[:feedbackErrorBodyMax]
	}
	return strings.Join(strings.Fields(strings.ToValidUTF8(string(raw), "")), " ")
}

// annotateFeedbackError spells out that a throttled report is to be abandoned,
// for the case where the server said 429 without saying so itself (a proxy, or
// an error page). A courtesy endpoint must never hold up the task that produced
// the report, and an agent reads the advice out of this one message.
func annotateFeedbackError(apiErr *out.APIError, status int) error {
	if status == http.StatusTooManyRequests && !strings.Contains(strings.ToLower(apiErr.Message), "retry") {
		apiErr.Message += " Drop this report and carry on; do not retry."
	}
	return apiErr
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
