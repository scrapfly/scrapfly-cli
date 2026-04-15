// Package anthropic adapts github.com/anthropics/anthropic-sdk-go to the
// llm.Provider interface. System prompt and tool definitions are cached
// automatically (ephemeral prompt cache) to cut cost on long agent loops.
package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	anthropicopt "github.com/anthropics/anthropic-sdk-go/option"
	"github.com/scrapfly/scrapfly-cli/internal/llm"
)

const defaultModel = "claude-sonnet-4-6"

type provider struct {
	client anthropic.Client
	model  string
}

func init() {
	llm.Register("anthropic", func(opts llm.Options) (llm.Provider, error) {
		key := opts.APIKey
		if key == "" {
			key = os.Getenv("ANTHROPIC_API_KEY")
		}
		if key == "" {
			return nil, fmt.Errorf("anthropic: %w (set ANTHROPIC_API_KEY)", llm.ErrNoAPIKey)
		}
		sdkOpts := []anthropicopt.RequestOption{anthropicopt.WithAPIKey(key)}
		if opts.BaseURL != "" {
			sdkOpts = append(sdkOpts, anthropicopt.WithBaseURL(opts.BaseURL))
		}
		c := anthropic.NewClient(sdkOpts...)
		model := opts.Model
		if model == "" {
			model = defaultModel
		}
		return &provider{client: c, model: model}, nil
	})
}

func (p *provider) Name() string { return "anthropic" }

func (p *provider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: int64(ifZero(req.MaxTokens, 4096)),
	}
	if req.Temperature > 0 {
		params.Temperature = anthropic.Float(req.Temperature)
	}

	// System: cacheable.
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{
			Text: req.System,
			CacheControl: anthropic.CacheControlEphemeralParam{
				Type: "ephemeral",
			},
		}}
	}

	// Tools: marked cacheable (only the last tool carries cache_control to
	// cover the whole tool list per Anthropic's caching rules).
	if len(req.Tools) > 0 {
		tools := make([]anthropic.ToolUnionParam, len(req.Tools))
		for i, t := range req.Tools {
			var schema anthropic.ToolInputSchemaParam
			if err := json.Unmarshal(t.InputSchema, &schema); err != nil {
				return nil, fmt.Errorf("anthropic: tool %q schema: %w", t.Name, err)
			}
			tp := anthropic.ToolParam{
				Name:        t.Name,
				Description: anthropic.String(t.Description),
				InputSchema: schema,
			}
			if i == len(req.Tools)-1 {
				tp.CacheControl = anthropic.CacheControlEphemeralParam{Type: "ephemeral"}
			}
			tools[i] = anthropic.ToolUnionParam{OfTool: &tp}
		}
		params.Tools = tools
	}

	// Messages.
	msgs, err := toAnthropicMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	params.Messages = msgs

	resp, err := p.client.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("anthropic: messages.new: %w", err)
	}

	out := &llm.ChatResponse{
		Usage: llm.Usage{
			InputTokens:      int(resp.Usage.InputTokens),
			OutputTokens:     int(resp.Usage.OutputTokens),
			CacheReadTokens:  int(resp.Usage.CacheReadInputTokens),
			CacheWriteTokens: int(resp.Usage.CacheCreationInputTokens),
		},
	}
	for _, block := range resp.Content {
		switch v := block.AsAny().(type) {
		case anthropic.TextBlock:
			out.Text += v.Text
		case anthropic.ToolUseBlock:
			raw, _ := json.Marshal(v.Input)
			out.ToolCalls = append(out.ToolCalls, llm.ToolCall{
				ID: v.ID, Name: v.Name, Input: raw,
			})
		}
	}
	switch resp.StopReason {
	case anthropic.StopReasonEndTurn:
		out.StopReason = llm.StopEndTurn
	case anthropic.StopReasonToolUse:
		out.StopReason = llm.StopToolUse
	case anthropic.StopReasonMaxTokens:
		out.StopReason = llm.StopMaxTokens
	default:
		out.StopReason = llm.StopOther
	}
	return out, nil
}

func toAnthropicMessages(msgs []llm.Message) ([]anthropic.MessageParam, error) {
	var out []anthropic.MessageParam
	// Tool results are carried as user messages with ToolResultBlock per SDK.
	// We may coalesce a run of tool results into a single user message, which
	// matches the Anthropic wire format.
	var pendingToolResults []anthropic.ContentBlockParamUnion

	flushToolResults := func() {
		if len(pendingToolResults) > 0 {
			out = append(out, anthropic.MessageParam{
				Role:    anthropic.MessageParamRoleUser,
				Content: pendingToolResults,
			})
			pendingToolResults = nil
		}
	}

	for _, m := range msgs {
		switch m.Role {
		case llm.RoleUser:
			flushToolResults()
			out = append(out, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Content)))
		case llm.RoleAssistant:
			flushToolResults()
			blocks := []anthropic.ContentBlockParamUnion{}
			if m.Content != "" {
				blocks = append(blocks, anthropic.NewTextBlock(m.Content))
			}
			for _, tc := range m.ToolCalls {
				var input any
				if len(tc.Input) > 0 {
					_ = json.Unmarshal(tc.Input, &input)
				}
				blocks = append(blocks, anthropic.NewToolUseBlock(tc.ID, input, tc.Name))
			}
			if len(blocks) > 0 {
				out = append(out, anthropic.MessageParam{
					Role:    anthropic.MessageParamRoleAssistant,
					Content: blocks,
				})
			}
		case llm.RoleTool:
			pendingToolResults = append(pendingToolResults,
				anthropic.NewToolResultBlock(m.ToolCallID, m.Content, m.IsError))
		case llm.RoleSystem:
			// System is set via req.System; ignore system messages here.
		default:
			return nil, fmt.Errorf("anthropic: unknown role %q", m.Role)
		}
	}
	flushToolResults()
	return out, nil
}

func ifZero(v, fallback int) int {
	if v == 0 {
		return fallback
	}
	return v
}
