// Package ollama re-exports the OpenAI adapter pointed at a local Ollama
// server. Ollama speaks the OpenAI chat-completion wire format, so the
// adapter is a 10-line shim.
package ollama

import (
	"os"
	"strings"

	"github.com/scrapfly/scrapfly-cli/internal/llm"
	"github.com/scrapfly/scrapfly-cli/internal/llm/openai"
)

const (
	defaultBaseURL = "http://localhost:11434/v1/"
	defaultModel   = "llama3.1"
)

func init() {
	llm.Register("ollama", func(opts llm.Options) (llm.Provider, error) {
		if opts.BaseURL == "" {
			if host := os.Getenv("OLLAMA_HOST"); host != "" {
				if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
					host = "http://" + host
				}
				host = strings.TrimRight(host, "/")
				if !strings.HasSuffix(host, "/v1") {
					host += "/v1/"
				} else {
					host += "/"
				}
				opts.BaseURL = host
			} else {
				opts.BaseURL = defaultBaseURL
			}
		}
		if opts.APIKey == "" {
			opts.APIKey = "ollama" // Ollama ignores the key but the SDK wants one.
		}
		return openai.New(opts, "ollama", "OLLAMA_API_KEY", defaultModel)
	})
}
