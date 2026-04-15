// Package openai adapts github.com/openai/openai-go to the llm.Provider
// interface. Also drives OpenAI-compatible endpoints (Ollama, vLLM, ...)
// when Options.BaseURL is set — see the ollama subpackage for a convenience
// factory.
package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	oai "github.com/openai/openai-go"
	oaiopt "github.com/openai/openai-go/option"
	oaishared "github.com/openai/openai-go/shared"
	"github.com/scrapfly/scrapfly-cli/internal/llm"
)

const defaultModel = "gpt-4.1-mini"

type provider struct {
	client oai.Client
	model  string
	name   string
}

func init() {
	llm.Register("openai", func(opts llm.Options) (llm.Provider, error) {
		return newProvider(opts, "openai", "OPENAI_API_KEY", defaultModel)
	})
}

// New exposes construction for sibling packages (ollama) that reuse this
// adapter with a different base URL and env var name.
func New(opts llm.Options, providerName, envKey, fallbackModel string) (llm.Provider, error) {
	return newProvider(opts, providerName, envKey, fallbackModel)
}

func newProvider(opts llm.Options, name, envKey, fallbackModel string) (*provider, error) {
	key := opts.APIKey
	if key == "" {
		key = os.Getenv(envKey)
	}
	// Ollama and similar local servers don't require a key — tolerate an
	// empty one when a custom BaseURL is set.
	if key == "" && opts.BaseURL == "" {
		return nil, fmt.Errorf("%s: %w (set %s)", name, llm.ErrNoAPIKey, envKey)
	}

	sdkOpts := []oaiopt.RequestOption{}
	if key != "" {
		sdkOpts = append(sdkOpts, oaiopt.WithAPIKey(key))
	}
	if opts.BaseURL != "" {
		sdkOpts = append(sdkOpts, oaiopt.WithBaseURL(opts.BaseURL))
	}
	c := oai.NewClient(sdkOpts...)
	model := opts.Model
	if model == "" {
		model = fallbackModel
	}
	return &provider{client: c, model: model, name: name}, nil
}

func (p *provider) Name() string { return p.name }

func (p *provider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}
	params := oai.ChatCompletionNewParams{
		Model: oai.ChatModel(model),
	}
	if req.MaxTokens > 0 {
		params.MaxCompletionTokens = oai.Int(int64(req.MaxTokens))
	}
	if req.Temperature > 0 {
		params.Temperature = oai.Float(req.Temperature)
	}

	// Assemble messages: start with system (if any), then map the rolling
	// history.
	msgs := []oai.ChatCompletionMessageParamUnion{}
	if req.System != "" {
		msgs = append(msgs, oai.SystemMessage(req.System))
	}
	for _, m := range req.Messages {
		switch m.Role {
		case llm.RoleUser:
			msgs = append(msgs, oai.UserMessage(m.Content))
		case llm.RoleAssistant:
			asst := oai.ChatCompletionAssistantMessageParam{}
			if m.Content != "" {
				asst.Content.OfString = oai.String(m.Content)
			}
			for _, tc := range m.ToolCalls {
				asst.ToolCalls = append(asst.ToolCalls, oai.ChatCompletionMessageToolCallParam{
					ID: tc.ID,
					Function: oai.ChatCompletionMessageToolCallFunctionParam{
						Name:      tc.Name,
						Arguments: string(tc.Input),
					},
				})
			}
			msgs = append(msgs, oai.ChatCompletionMessageParamUnion{OfAssistant: &asst})
		case llm.RoleTool:
			msgs = append(msgs, oai.ToolMessage(m.Content, m.ToolCallID))
		case llm.RoleSystem:
			msgs = append(msgs, oai.SystemMessage(m.Content))
		default:
			return nil, fmt.Errorf("openai: unknown role %q", m.Role)
		}
	}
	params.Messages = msgs

	if len(req.Tools) > 0 {
		tools := make([]oai.ChatCompletionToolParam, len(req.Tools))
		for i, t := range req.Tools {
			var schema oaishared.FunctionParameters
			if err := json.Unmarshal(t.InputSchema, &schema); err != nil {
				return nil, fmt.Errorf("openai: tool %q schema: %w", t.Name, err)
			}
			tools[i] = oai.ChatCompletionToolParam{
				Function: oaishared.FunctionDefinitionParam{
					Name:        t.Name,
					Description: oai.String(t.Description),
					Parameters:  schema,
				},
			}
		}
		params.Tools = tools
	}

	resp, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("openai: completions.new: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("openai: no choices in response")
	}
	choice := resp.Choices[0]

	out := &llm.ChatResponse{
		Text: choice.Message.Content,
		Usage: llm.Usage{
			InputTokens:  int(resp.Usage.PromptTokens),
			OutputTokens: int(resp.Usage.CompletionTokens),
		},
	}
	for _, tc := range choice.Message.ToolCalls {
		// OpenAI v1.x SDK: ToolCallUnion with OfFunction; arguments is a JSON
		// string.
		if tc.Type == "function" || tc.Function.Name != "" {
			out.ToolCalls = append(out.ToolCalls, llm.ToolCall{
				ID:    tc.ID,
				Name:  tc.Function.Name,
				Input: json.RawMessage(tc.Function.Arguments),
			})
		}
	}
	switch choice.FinishReason {
	case "stop":
		out.StopReason = llm.StopEndTurn
	case "tool_calls", "function_call":
		out.StopReason = llm.StopToolUse
	case "length":
		out.StopReason = llm.StopMaxTokens
	default:
		out.StopReason = llm.StopOther
	}
	return out, nil
}
