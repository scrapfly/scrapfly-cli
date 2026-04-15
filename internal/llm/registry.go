package llm

import (
	"fmt"
	"os"
	"strings"
)

// Options holds user-supplied overrides for provider construction.
// Empty fields fall back to env / defaults.
type Options struct {
	Provider string // anthropic|openai|gemini|ollama
	APIKey   string
	Model    string
	BaseURL  string // for OpenAI-compatible endpoints (ollama, vLLM, ...)
}

// Factory is a per-provider constructor registered at init time. Adapters
// self-register to avoid import cycles.
type Factory func(Options) (Provider, error)

var factories = map[string]Factory{}

// Register adds a provider factory by name. Called from provider packages'
// init().
func Register(name string, f Factory) {
	factories[strings.ToLower(name)] = f
}

// Registered returns sorted provider names. Useful for help text.
func Registered() []string {
	out := make([]string, 0, len(factories))
	for k := range factories {
		out = append(out, k)
	}
	return out
}

// New constructs a Provider by name.
func New(name string, opts Options) (Provider, error) {
	if name == "" {
		return nil, fmt.Errorf("provider name is required")
	}
	f, ok := factories[strings.ToLower(name)]
	if !ok {
		return nil, fmt.Errorf("unknown LLM provider %q (registered: %v)", name, Registered())
	}
	opts.Provider = strings.ToLower(name)
	return f(opts)
}

// Autodetect picks a provider by inspecting env vars, in order of preference:
// ANTHROPIC_API_KEY → OPENAI_API_KEY → GEMINI_API_KEY → OLLAMA_HOST → first
// registered. Opts (including explicit Provider) always win.
func Autodetect(opts Options) (Provider, error) {
	name := opts.Provider
	if name == "" {
		switch {
		case os.Getenv("ANTHROPIC_API_KEY") != "":
			name = "anthropic"
		case os.Getenv("OPENAI_API_KEY") != "":
			name = "openai"
		case os.Getenv("GEMINI_API_KEY") != "" || os.Getenv("GOOGLE_API_KEY") != "":
			name = "gemini"
		case os.Getenv("OLLAMA_HOST") != "":
			name = "ollama"
		default:
			if len(factories) == 0 {
				return nil, fmt.Errorf("no LLM providers registered — rebuild with provider support")
			}
			name = Registered()[0]
		}
	}
	return New(name, opts)
}
