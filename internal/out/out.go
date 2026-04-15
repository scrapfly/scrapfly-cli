package out

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	scrapfly "github.com/scrapfly/go-scrapfly"
)

type Envelope struct {
	Success bool           `json:"success"`
	Product string         `json:"product,omitempty"`
	Data    any            `json:"data,omitempty"`
	Error   *EnvelopeError `json:"error,omitempty"`
}

type EnvelopeError struct {
	Code       string `json:"code,omitempty"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
	Hint       string `json:"hint,omitempty"`
	Retryable  bool   `json:"retryable,omitempty"`
}

// WriteSuccess writes a success envelope. When pretty is true, the caller is
// responsible for having written the human summary first; this function emits
// nothing in that case to avoid duplicating the payload.
func WriteSuccess(w io.Writer, pretty bool, product string, data any) error {
	if pretty {
		return nil
	}
	env := Envelope{Success: true, Product: product, Data: data}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}

// WriteError writes an error envelope to w. Always emits JSON even in pretty
// mode — easier for LLM callers to diagnose regardless of flag.
func WriteError(w io.Writer, pretty bool, product string, err error) error {
	env := Envelope{Success: false, Product: product, Error: toEnvelopeError(err)}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}

func toEnvelopeError(err error) *EnvelopeError {
	if err == nil {
		return nil
	}
	e := &EnvelopeError{Message: err.Error()}

	var apiErr *scrapfly.APIError
	if errors.As(err, &apiErr) {
		e.Code = apiErr.Code
		e.Message = apiErr.Message
		e.HTTPStatus = apiErr.HTTPStatusCode
		e.Hint = apiErr.Hint
		e.Retryable = apiErr.Retryable
		return e
	}

	switch {
	case errors.Is(err, scrapfly.ErrBadAPIKey):
		e.Code = "ERR::CLI::BAD_API_KEY"
	case errors.Is(err, scrapfly.ErrScrapeConfig):
		e.Code = "ERR::CLI::SCRAPE_CONFIG"
	case errors.Is(err, scrapfly.ErrScreenshotConfig):
		e.Code = "ERR::CLI::SCREENSHOT_CONFIG"
	case errors.Is(err, scrapfly.ErrExtractionConfig):
		e.Code = "ERR::CLI::EXTRACTION_CONFIG"
	case errors.Is(err, scrapfly.ErrCrawlerConfig):
		e.Code = "ERR::CLI::CRAWLER_CONFIG"
	case errors.Is(err, scrapfly.ErrQuotaLimitReached):
		e.Code = "ERR::CLI::QUOTA_LIMIT"
	case errors.Is(err, scrapfly.ErrTooManyRequests):
		e.Code = "ERR::CLI::RATE_LIMITED"
		e.Retryable = true
	}
	return e
}

// Pretty prints a short human line to w. Used by individual commands as their
// pretty-mode summary.
func Pretty(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, format+"\n", args...)
}
