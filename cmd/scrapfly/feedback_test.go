package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBuildFeedbackRequestValidation(t *testing.T) {
	longEnough := "docs search returned German pages for an English locale query"

	cases := []struct {
		name    string
		in      feedbackFlags
		wantErr string
	}{
		{
			"sentiment required",
			feedbackFlags{message: longEnough, context: longEnough},
			"--sentiment is required",
		},
		{
			"sentiment enum",
			feedbackFlags{sentiment: "grumpy", message: longEnough, context: longEnough},
			"--sentiment is required and must be one of: positive, neutral, negative",
		},
		{
			"message too short",
			feedbackFlags{sentiment: "neutral", message: "broke", context: longEnough},
			"--message must be 10-2000 characters, got 5",
		},
		{
			"message word floor",
			feedbackFlags{sentiment: "neutral", message: "completely-broken-thing", context: longEnough},
			"--message must be at least 5 words",
		},
		{
			"context too short",
			feedbackFlags{sentiment: "neutral", message: longEnough, context: "x"},
			"--context must be 10-2000 characters, got 1",
		},
		{
			"unknown feature lists the legal values",
			feedbackFlags{sentiment: "neutral", message: longEnough, context: longEnough, feature: "disputes"},
			`--feature "disputes" is not a product area`,
		},
		{
			"unknown actor",
			feedbackFlags{sentiment: "neutral", message: longEnough, context: longEnough, actor: "robot"},
			`--actor "robot" is not valid`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := buildFeedbackRequest(&c.in)
			if err == nil {
				t.Fatalf("expected an error containing %q, got none", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("got %q, want it to contain %q", err.Error(), c.wantErr)
			}
		})
	}
}

// The character bounds are counted in runes: a report written in a non-ASCII
// language must not pass locally and then be rejected server-side.
func TestBuildFeedbackRequestCountsRunesNotBytes(t *testing.T) {
	// 9 runes, 27 bytes in UTF-8: under the floor either way, but the error has
	// to report the rune count the server will also compute.
	_, err := buildFeedbackRequest(&feedbackFlags{sentiment: "neutral", message: "日本語のテスト報告", context: "日本語のテスト報告です"})
	if err == nil || !strings.Contains(err.Error(), "got 9") {
		t.Fatalf("want a rune-counted rejection, got %v", err)
	}
}

func TestBuildFeedbackRequestNormalizesAndStampsClient(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")

	req, err := buildFeedbackRequest(&feedbackFlags{
		sentiment: " NEGATIVE ",
		feature:   " Docs ",
		message:   "  docs search served German content to an English-locale fetch  ",
		context:   "hardening a chargeback response for a metered SaaS account",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Sentiment != "negative" || req.Feature != "docs" {
		t.Errorf("enums not normalized: %+v", req)
	}
	if strings.HasPrefix(req.Message, " ") || strings.HasSuffix(req.Message, " ") {
		t.Errorf("message not trimmed: %q", req.Message)
	}
	// A coding agent in the environment is what decides the default actor.
	if req.Actor != "agent" {
		t.Errorf("actor = %q, want agent when a coding agent env var is set", req.Actor)
	}
	if req.Surface != "cli" || req.ClientVersion == "" || req.ClientOS == "" {
		t.Errorf("client stamp incomplete: %+v", req)
	}
}

func TestDefaultFeedbackActorFallsBackToHuman(t *testing.T) {
	for _, env := range agentEnvVars {
		t.Setenv(env, "")
	}
	if got := defaultFeedbackActor(); got != "human" {
		t.Errorf("got %q, want human with no agent env present", got)
	}
}

func TestWebBaseForHost(t *testing.T) {
	cases := map[string]string{
		"":                         "https://scrapfly.io",
		"https://api.scrapfly.io":  "https://scrapfly.io",
		"https://api.scrapfly.io/": "https://scrapfly.io",
		"https://api.example.test": "https://example.test",
		// Already a base domain (self-hosted stack): left alone.
		"https://scrapfly.internal:8443": "https://scrapfly.internal:8443",
	}
	for host, want := range cases {
		if got := webBaseForHost(host); got != want {
			t.Errorf("webBaseForHost(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestPostFeedbackSendsPayloadAndKey(t *testing.T) {
	var gotPath, gotAuth, gotRawQuery, gotAccept, gotUA string
	var gotBody feedbackRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotRawQuery = r.URL.RawQuery
		gotAccept = r.Header.Get("Accept")
		gotUA = r.Header.Get("User-Agent")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"uuid":"fb-1","sentiment":"negative","feature":"docs","actor":"agent"}`))
	}))
	defer srv.Close()

	res, err := postFeedback(context.Background(), &rootFlags{timeout: 5 * time.Second}, srv.URL, "scp-live-key", &feedbackRequest{
		Sentiment: "negative", Message: "m", Context: "c", Feature: "docs", Actor: "agent", Surface: "cli",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/dashboard/api/feedback" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer scp-live-key" {
		t.Errorf("authorization = %q", gotAuth)
	}
	// The key must never ride the query string: web-app logs request URIs.
	if strings.Contains(gotRawQuery, "scp-live-key") {
		t.Errorf("api key leaked into the query string: %q", gotRawQuery)
	}
	if gotAccept != "application/json" {
		t.Errorf("accept = %q", gotAccept)
	}
	if !strings.HasPrefix(gotUA, "scrapfly-cli/") {
		t.Errorf("user agent = %q", gotUA)
	}
	if gotBody.Sentiment != "negative" || gotBody.Surface != "cli" {
		t.Errorf("body = %+v", gotBody)
	}
	if res.UUID != "fb-1" {
		t.Errorf("uuid = %q", res.UUID)
	}
}

func TestFeedbackErrorFoldsAllowedValuesAndRateLimitAdvice(t *testing.T) {
	err := feedbackError(http.StatusBadRequest, []byte(`{"error":"invalid_feature","message":"feature \"disputes\" is not a known product area.","allowed_features":["docs","cli"]}`))
	if !strings.Contains(err.Error(), "Allowed: cli, docs.") {
		t.Errorf("allowed list not folded in: %v", err)
	}

	// The server already says what to do, so the CLI must not say it twice.
	limited := feedbackError(http.StatusTooManyRequests, []byte(`{"error":"rate_limited","message":"Feedback limit of 10 per hour reached for this account; do not retry."}`))
	if strings.Count(strings.ToLower(limited.Error()), "do not retry") != 1 {
		t.Errorf("rate-limit advice duplicated or missing: %v", limited)
	}

	// A bare 429 from a proxy carries no advice, so the CLI supplies it.
	bare := feedbackError(http.StatusTooManyRequests, nil)
	if !strings.Contains(bare.Error(), "do not retry") {
		t.Errorf("rate-limit advice missing on an empty body: %v", bare)
	}

	// A proxy or an HTML error page must still produce a readable error rather
	// than an empty message.
	opaque := feedbackError(http.StatusBadGateway, []byte("<html>502</html>"))
	if !strings.Contains(opaque.Error(), "502") {
		t.Errorf("opaque body lost: %v", opaque)
	}
}

// An unparsable body is echoed, so its size and shape are the server's to
// choose: a dev-mode stack trace or a proxy's error page must not become the
// error message.
func TestFeedbackErrorBoundsAnUnparsableBody(t *testing.T) {
	huge := []byte("<html>\n" + strings.Repeat("goroutine 1 [running]: frame\n", 20000) + "</html>")

	msg := feedbackError(http.StatusBadGateway, huge).Error()
	if len(msg) > feedbackErrorBodyMax+128 {
		t.Errorf("message is %d bytes, want it capped near %d", len(msg), feedbackErrorBodyMax)
	}
	if strings.ContainsAny(msg, "\n\r") {
		t.Errorf("message spans several lines: %q", msg)
	}
	if !strings.Contains(msg, "goroutine 1 [running]:") {
		t.Errorf("body dropped instead of truncated: %q", msg)
	}
}
