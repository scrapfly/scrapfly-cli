package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	scrapfly "github.com/scrapfly/go-scrapfly"
	"github.com/scrapfly/scrapfly-cli/internal/cdp"
	"github.com/scrapfly/scrapfly-cli/internal/llm"
	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"

	// Register providers via side-effect imports.
	_ "github.com/scrapfly/scrapfly-cli/internal/llm/anthropic"
	_ "github.com/scrapfly/scrapfly-cli/internal/llm/gemini"
	_ "github.com/scrapfly/scrapfly-cli/internal/llm/ollama"
	_ "github.com/scrapfly/scrapfly-cli/internal/llm/openai"
)

const defaultAgentSystem = `You are a browser automation agent. You drive a Chromium browser over CDP to accomplish the user's task.

Guidelines:
- Use "snapshot" to read the page as an accessibility tree with refs (e1, e2, ...). Refs are invalidated by the next snapshot.
- Always take a snapshot before using click/type/scroll with a ref.
- Prefer AXTree roles + names to identify elements; only use eval() when the AXTree is insufficient.
- Be concise. Don't take unnecessary screenshots — the AXTree is enough for most tasks.
- If the page appears blocked by anti-bot (Cloudflare / DataDome / PerimeterX challenge page, captcha, 403, empty body, "Access Denied", "Just a moment…"), call the "unblock" tool with the target URL. It returns a fresh browser session with anti-bot bypass applied. After unblock, take a new snapshot — the previous refs are invalid.
- When you have the answer, call "done" with your final result.
- Do not hallucinate element refs that weren't in the last snapshot.
- Respect the user's goal literally; don't go beyond what was asked.`

// agentRuntime holds the mutable browser state that tool handlers can swap
// (e.g. the unblock tool tears down the current CDP connection and attaches
// to a fresh /unblock-minted one).
type agentRuntime struct {
	ctx         context.Context
	sfClient    *scrapfly.Client
	cdpClient   *cdp.Client
	sess        *cdp.Session
	onReconnect func() // called after a successful reconnect (for defer cleanup)
}

// reconnect tears down the current CDP client/session and attaches to wsURL.
func (r *agentRuntime) reconnect(wsURL string) error {
	if r.sess != nil {
		_ = r.sess.Detach(context.Background())
	}
	if r.cdpClient != nil {
		_ = r.cdpClient.Close()
	}
	nc, err := cdp.Dial(r.ctx, wsURL)
	if err != nil {
		return fmt.Errorf("reconnect dial: %w", err)
	}
	r.cdpClient = nc
	ns, err := cdp.Attach(r.ctx, nc)
	if err != nil {
		return fmt.Errorf("reconnect attach: %w", err)
	}
	r.sess = ns
	return nil
}

// agentStep records a single turn for the transcript in the final envelope.
type agentStep struct {
	Role       string          `json:"role"`
	Text       string          `json:"text,omitempty"`
	ToolCalls  []llm.ToolCall  `json:"tool_calls,omitempty"`
	ToolResult json.RawMessage `json:"tool_result,omitempty"`
	ToolError  string          `json:"tool_error,omitempty"`
}

// agentResult is the final on-stdout envelope.
type agentResult struct {
	Answer     json.RawMessage `json:"answer,omitempty"`
	Provider   string          `json:"provider"`
	Model      string          `json:"model,omitempty"`
	Steps      int             `json:"steps"`
	Transcript []agentStep     `json:"transcript,omitempty"`
	Usage      llm.Usage       `json:"usage"`
	StopReason string          `json:"stop_reason"`
}

func newAgentCmd(flags *rootFlags) *cobra.Command {
	var (
		prompt         string
		provider       string
		modelFlag      string
		llmKey         string
		llmBaseURL     string
		maxSteps       int
		maxTokens      int
		temperature    float64
		schemaJSON     string
		schemaFile     string
		systemFlag     string
		systemFile     string
		transcriptFlag bool
		verbose        bool

		wsURL      string
		targetURL  string
		keepOpen   bool
		launchArgs browserLaunchFlags
	)

	cmd := &cobra.Command{
		Use:   "agent \"<prompt>\"",
		Short: "LLM agent that drives a Scrapfly Browser over CDP to accomplish a task",
		Long: `Runs an autonomous loop: the model picks tool calls (open, snapshot, click,
type, scroll, screenshot, eval, done), the CLI executes each against the
attached browser session, and the results feed back into the model until it
calls "done" or --max-steps is reached.

LLM provider:
  --provider auto-detects (ANTHROPIC_API_KEY > OPENAI_API_KEY > GEMINI_API_KEY
  > OLLAMA_HOST). Override with --provider and per-provider --model.

Browser target:
  --ws <url>       attach to an existing CDP WebSocket
  --url <url>      pre-navigate via /unblock then attach
  (neither)        mint a fresh CDP URL using the launch flags

Structured output:
  --schema '<json-schema>' forces the "done" tool's "answer" parameter to
  match the given schema. Without a schema, "answer" is free text.

Output: a JSON envelope on stdout (answer + usage + stop_reason). Per-step
traces go to stderr when --verbose is set.`,
		Example: `  # Simplest: Anthropic key + unblocked URL
  ANTHROPIC_API_KEY=... scrapfly agent "Get the title and price of the first product" \
    --url https://web-scraping.dev/products

  # OpenAI with structured output
  OPENAI_API_KEY=... scrapfly agent "Find the price of product 1" \
    --url https://web-scraping.dev/product/1 \
    --provider openai --model gpt-4.1-mini \
    --schema '{"type":"object","properties":{"price":{"type":"string"}},"required":["price"]}'

  # Local Llama via Ollama
  OLLAMA_HOST=http://localhost:11434 scrapfly agent "..." --provider ollama --model llama3.1`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				prompt = args[0]
			}
			if prompt == "" {
				return fmt.Errorf("prompt is required")
			}

			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			// Resolve system prompt.
			system := defaultAgentSystem
			if systemFile != "" {
				b, err := os.ReadFile(systemFile)
				if err != nil {
					return fmt.Errorf("read --system-file: %w", err)
				}
				system = string(b)
			} else if systemFlag != "" {
				system = systemFlag
			}

			// Resolve done-tool schema.
			var answerSchema json.RawMessage
			if schemaFile != "" {
				b, err := os.ReadFile(schemaFile)
				if err != nil {
					return fmt.Errorf("read --schema-file: %w", err)
				}
				answerSchema = b
			} else if schemaJSON != "" {
				answerSchema = []byte(schemaJSON)
			}

			// Build LLM provider.
			llmProvider, err := llm.Autodetect(llm.Options{
				Provider: provider, APIKey: llmKey, Model: modelFlag, BaseURL: llmBaseURL,
			})
			if err != nil {
				return fmt.Errorf("llm: %w", err)
			}

			// SDK client is always needed — the "unblock" tool may be called
			// mid-run to reconnect behind anti-bot bypass.
			sfClient, err := buildClient(flags)
			if err != nil {
				return err
			}

			// Resolve initial CDP endpoint.
			if wsURL == "" {
				if targetURL != "" {
					res, err := sfClient.CloudBrowserUnblock(scrapfly.UnblockConfig{URL: targetURL})
					if err != nil {
						return err
					}
					wsURL = res.WSURL
				} else {
					wsURL = sfClient.CloudBrowser(launchArgs.toConfig())
				}
			}

			cdpClient, err := cdp.Dial(ctx, wsURL)
			if err != nil {
				return err
			}
			sess, err := cdp.Attach(ctx, cdpClient)
			if err != nil {
				_ = cdpClient.Close()
				return err
			}
			rt := &agentRuntime{
				ctx:       ctx,
				sfClient:  sfClient,
				cdpClient: cdpClient,
				sess:      sess,
			}
			defer func() {
				if !keepOpen && rt.sess != nil {
					_ = rt.sess.Detach(context.Background())
				}
				if rt.cdpClient != nil {
					_ = rt.cdpClient.Close()
				}
			}()

			// Tool definitions exposed to the model.
			tools := buildAgentTools(answerSchema)

			messages := []llm.Message{
				{Role: llm.RoleUser, Content: prompt},
			}

			result := agentResult{Provider: llmProvider.Name(), Model: modelFlag}
			transcript := []agentStep{}

			for step := 0; step < maxSteps; step++ {
				result.Steps = step + 1
				resp, err := llmProvider.Chat(ctx, llm.ChatRequest{
					Model: modelFlag, System: system, Messages: messages, Tools: tools,
					MaxTokens: maxTokens, Temperature: temperature,
				})
				if err != nil {
					return fmt.Errorf("llm chat: %w", err)
				}
				result.Usage.InputTokens += resp.Usage.InputTokens
				result.Usage.OutputTokens += resp.Usage.OutputTokens
				result.Usage.CacheReadTokens += resp.Usage.CacheReadTokens
				result.Usage.CacheWriteTokens += resp.Usage.CacheWriteTokens

				transcript = append(transcript, agentStep{
					Role: "assistant", Text: resp.Text, ToolCalls: resp.ToolCalls,
				})
				if verbose && resp.Text != "" {
					fmt.Fprintf(os.Stderr, "[assistant] %s\n", strings.TrimSpace(resp.Text))
				}

				// Persist assistant turn.
				messages = append(messages, llm.Message{
					Role: llm.RoleAssistant, Content: resp.Text, ToolCalls: resp.ToolCalls,
				})

				if len(resp.ToolCalls) == 0 {
					result.StopReason = string(resp.StopReason)
					result.Answer = json.RawMessage(fmt.Sprintf("%q", resp.Text))
					break
				}

				// Dispatch tool calls.
				done := false
				for _, tc := range resp.ToolCalls {
					if tc.Name == "done" {
						// Final answer — extract the "answer" field.
						var parsed struct {
							Answer json.RawMessage `json:"answer"`
						}
						_ = json.Unmarshal(tc.Input, &parsed)
						result.Answer = parsed.Answer
						if len(result.Answer) == 0 {
							result.Answer = tc.Input
						}
						result.StopReason = "done"
						done = true
						transcript = append(transcript, agentStep{
							Role: "tool", ToolResult: tc.Input,
						})
						break
					}
					resStr, toolErr := runAgentTool(rt, tc)
					if verbose {
						fmt.Fprintf(os.Stderr, "[tool %s] %s\n", tc.Name, truncate(resStr, 200))
					}
					isErr := toolErr != nil
					content := resStr
					if isErr {
						content = toolErr.Error()
					}
					messages = append(messages, llm.Message{
						Role: llm.RoleTool, ToolCallID: tc.ID, Content: content, IsError: isErr,
					})
					step := agentStep{Role: "tool"}
					if isErr {
						step.ToolError = toolErr.Error()
					} else {
						step.ToolResult = json.RawMessage(resStr)
					}
					transcript = append(transcript, step)
				}
				if done {
					break
				}
			}
			if result.StopReason == "" {
				result.StopReason = "max_steps"
			}
			if transcriptFlag {
				result.Transcript = transcript
			}

			return out.WriteSuccess(os.Stdout, flags.pretty, "agent", result)
		},
	}

	cmd.Flags().StringVar(&prompt, "prompt", "", "prompt (alternative to positional arg)")
	cmd.Flags().StringVar(&provider, "provider", "", "anthropic|openai|gemini|ollama (default: auto-detect from env)")
	cmd.Flags().StringVar(&modelFlag, "model", "", "model id (provider-specific default)")
	cmd.Flags().StringVar(&llmKey, "llm-key", "", "override LLM provider API key")
	cmd.Flags().StringVar(&llmBaseURL, "llm-base-url", "", "custom base URL (ollama, vLLM, Anthropic proxy, ...)")
	cmd.Flags().IntVar(&maxSteps, "max-steps", 15, "maximum tool-call loops before giving up")
	cmd.Flags().IntVar(&maxTokens, "max-tokens", 4096, "per-call max output tokens")
	cmd.Flags().Float64Var(&temperature, "temperature", 0, "sampling temperature (0 = provider default)")
	cmd.Flags().StringVar(&schemaJSON, "schema", "", "JSON Schema for the final answer (enforced via done tool)")
	cmd.Flags().StringVar(&schemaFile, "schema-file", "", "read answer schema from file")
	cmd.Flags().StringVar(&systemFlag, "system", "", "override system prompt")
	cmd.Flags().StringVar(&systemFile, "system-file", "", "read system prompt from file")
	cmd.Flags().BoolVar(&transcriptFlag, "transcript", false, "include full step transcript in the result envelope")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "stream per-step traces to stderr")

	cmd.Flags().StringVar(&wsURL, "ws", "", "connect to an existing CDP WebSocket URL")
	cmd.Flags().StringVar(&targetURL, "url", "", "call /unblock on this URL, then attach")
	cmd.Flags().BoolVar(&keepOpen, "keep-open", false, "don't close the tab when agent exits")
	bindBrowserLaunchFlags(cmd, &launchArgs)
	cmd.Flags().StringVar(&launchArgs.session, "session", "", "browser session id (pin for reuse across runs)")

	return cmd
}

func buildAgentTools(answerSchema json.RawMessage) []llm.Tool {
	tools := []llm.Tool{
		{Name: "open", Description: "Navigate the browser to a URL and wait for load.",
			InputSchema: mustSchema(`{"type":"object","properties":{"url":{"type":"string","description":"Fully-qualified http/https URL"}},"required":["url"]}`)},
		{Name: "snapshot", Description: "Capture the accessibility tree of the current page. Returns flat list of {ref, role, name, value, children}. Refs are valid only until the next snapshot.",
			InputSchema: mustSchema(`{"type":"object","properties":{}}`)},
		{Name: "click", Description: "Click the element at the given ref from the most recent snapshot.",
			InputSchema: mustSchema(`{"type":"object","properties":{"ref":{"type":"string","description":"Element ref like e3"}},"required":["ref"]}`)},
		{Name: "type", Description: "Focus the element at ref and insert text.",
			InputSchema: mustSchema(`{"type":"object","properties":{"ref":{"type":"string"},"text":{"type":"string"}},"required":["ref","text"]}`)},
		{Name: "scroll", Description: "Scroll the page in a direction. If ref is given, scrolls with that element as anchor.",
			InputSchema: mustSchema(`{"type":"object","properties":{"direction":{"type":"string","enum":["up","down","left","right"]},"amount":{"type":"number","description":"pixels; default 500"},"ref":{"type":"string"}},"required":["direction"]}`)},
		{Name: "screenshot", Description: "Capture a PNG of the viewport. Use sparingly — AXTree is usually enough. Result is base64.",
			InputSchema: mustSchema(`{"type":"object","properties":{"fullpage":{"type":"boolean"}}}`)},
		{Name: "eval", Description: "Run a JS expression in the page and return the result.",
			InputSchema: mustSchema(`{"type":"object","properties":{"js":{"type":"string"}},"required":["js"]}`)},
	}
	// The "done" tool signals the final answer.
	doneSchema := `{"type":"object","properties":{"answer":{"type":"string","description":"Final answer as free text"}},"required":["answer"]}`
	if len(answerSchema) > 0 {
		doneSchema = fmt.Sprintf(`{"type":"object","properties":{"answer":%s},"required":["answer"]}`, answerSchema)
	}
	tools = append(tools,
		llm.Tool{
			Name:        "unblock",
			Description: "Bypass anti-bot protection by re-launching the browser through Scrapfly's /unblock endpoint and pre-navigating to `url`. Returns a fresh session — take a new snapshot immediately after, previous refs are invalid. Use this ONLY when the current page looks blocked (challenge, captcha, 403, empty body).",
			InputSchema: mustSchema(`{"type":"object","properties":{"url":{"type":"string","description":"Target URL to unblock and navigate to"}},"required":["url"]}`),
		},
		llm.Tool{
			Name:        "done",
			Description: "Emit the final answer and end the run. Call this when the task is complete.",
			InputSchema: json.RawMessage(doneSchema),
		},
	)
	return tools
}

func mustSchema(s string) json.RawMessage { return json.RawMessage(s) }

// runAgentTool executes one tool call against the attached CDP session and
// returns the stringified JSON result (or an error). The runtime holds the
// mutable session so tools like "unblock" can swap the CDP connection.
func runAgentTool(rt *agentRuntime, tc llm.ToolCall) (string, error) {
	ctx := rt.ctx
	s := rt.sess
	var args struct {
		URL       string  `json:"url"`
		Ref       string  `json:"ref"`
		Text      string  `json:"text"`
		Direction string  `json:"direction"`
		Amount    float64 `json:"amount"`
		FullPage  bool    `json:"fullpage"`
		JS        string  `json:"js"`
	}
	if len(tc.Input) > 0 {
		if err := json.Unmarshal(tc.Input, &args); err != nil {
			return "", fmt.Errorf("invalid tool args: %w", err)
		}
	}
	switch tc.Name {
	case "open":
		res, err := s.Open(ctx, args.URL)
		return jsonStr(res), err
	case "snapshot":
		nodes, err := s.Snapshot(ctx)
		return jsonStr(map[string]any{"nodes": nodes}), err
	case "click":
		res, err := s.Click(ctx, args.Ref)
		return jsonStr(res), err
	case "type", "fill":
		res, err := s.Fill(ctx, args.Ref, args.Text)
		return jsonStr(res), err
	case "scroll":
		amt := args.Amount
		if amt == 0 {
			amt = 500
		}
		res, err := s.Scroll(ctx, args.Direction, amt, args.Ref)
		return jsonStr(res), err
	case "screenshot":
		png, err := s.Screenshot(ctx, args.FullPage)
		if err != nil {
			return "", err
		}
		return jsonStr(map[string]any{"image_base64": stdBase64(png), "bytes": len(png)}), nil
	case "eval":
		v, err := s.Eval(ctx, args.JS)
		return jsonStr(map[string]any{"value": v}), err
	case "unblock":
		if args.URL == "" {
			return "", fmt.Errorf("unblock requires url")
		}
		res, err := rt.sfClient.CloudBrowserUnblock(scrapfly.UnblockConfig{URL: args.URL})
		if err != nil {
			return "", fmt.Errorf("unblock: %w", err)
		}
		if err := rt.reconnect(res.WSURL); err != nil {
			return "", err
		}
		return jsonStr(map[string]any{
			"session_id": res.SessionID,
			"run_id":     res.RunID,
			"status":     "reconnected — take a new snapshot",
		}), nil
	}
	return "", fmt.Errorf("unknown tool %q", tc.Name)
}

func jsonStr(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
