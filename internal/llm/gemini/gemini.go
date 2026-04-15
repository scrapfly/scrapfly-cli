// Package gemini adapts google.golang.org/genai to the llm.Provider
// interface. Uses the Gemini Developer API by default (GEMINI_API_KEY env);
// set BaseURL or switch credentials for Vertex AI.
package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/scrapfly/scrapfly-cli/internal/llm"
	"google.golang.org/genai"
)

const defaultModel = "gemini-2.5-flash"

type provider struct {
	client *genai.Client
	model  string
}

func init() {
	llm.Register("gemini", func(opts llm.Options) (llm.Provider, error) {
		key := opts.APIKey
		if key == "" {
			key = os.Getenv("GEMINI_API_KEY")
		}
		if key == "" {
			key = os.Getenv("GOOGLE_API_KEY")
		}
		if key == "" {
			return nil, fmt.Errorf("gemini: %w (set GEMINI_API_KEY)", llm.ErrNoAPIKey)
		}
		c, err := genai.NewClient(context.Background(), &genai.ClientConfig{
			APIKey:  key,
			Backend: genai.BackendGeminiAPI,
		})
		if err != nil {
			return nil, fmt.Errorf("gemini: new client: %w", err)
		}
		model := opts.Model
		if model == "" {
			model = defaultModel
		}
		return &provider{client: c, model: model}, nil
	})
}

func (p *provider) Name() string { return "gemini" }

func (p *provider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}

	contents, err := toGeminiContents(req.Messages)
	if err != nil {
		return nil, err
	}

	cfg := &genai.GenerateContentConfig{}
	if req.System != "" {
		cfg.SystemInstruction = &genai.Content{
			Parts: []*genai.Part{{Text: req.System}},
		}
	}
	if req.MaxTokens > 0 {
		max := int32(req.MaxTokens)
		cfg.MaxOutputTokens = max
	}
	if req.Temperature > 0 {
		t := float32(req.Temperature)
		cfg.Temperature = &t
	}
	if len(req.Tools) > 0 {
		var decls []*genai.FunctionDeclaration
		for _, t := range req.Tools {
			var schema any
			if err := json.Unmarshal(t.InputSchema, &schema); err != nil {
				return nil, fmt.Errorf("gemini: tool %q schema: %w", t.Name, err)
			}
			decls = append(decls, &genai.FunctionDeclaration{
				Name:                 t.Name,
				Description:          t.Description,
				ParametersJsonSchema: schema,
			})
		}
		cfg.Tools = []*genai.Tool{{FunctionDeclarations: decls}}
	}

	resp, err := p.client.Models.GenerateContent(ctx, model, contents, cfg)
	if err != nil {
		return nil, fmt.Errorf("gemini: generateContent: %w", err)
	}

	out := &llm.ChatResponse{}
	if resp.UsageMetadata != nil {
		out.Usage.InputTokens = int(resp.UsageMetadata.PromptTokenCount)
		out.Usage.OutputTokens = int(resp.UsageMetadata.CandidatesTokenCount)
		out.Usage.CacheReadTokens = int(resp.UsageMetadata.CachedContentTokenCount)
	}
	if len(resp.Candidates) == 0 {
		return out, nil
	}
	cand := resp.Candidates[0]
	if cand.Content != nil {
		for _, part := range cand.Content.Parts {
			switch {
			case part.Text != "":
				out.Text += part.Text
			case part.FunctionCall != nil:
				raw, _ := json.Marshal(part.FunctionCall.Args)
				id := part.FunctionCall.ID
				if id == "" {
					id = part.FunctionCall.Name
				}
				out.ToolCalls = append(out.ToolCalls, llm.ToolCall{
					ID:    id,
					Name:  part.FunctionCall.Name,
					Input: raw,
				})
			}
		}
	}
	switch cand.FinishReason {
	case genai.FinishReasonStop:
		if len(out.ToolCalls) > 0 {
			out.StopReason = llm.StopToolUse
		} else {
			out.StopReason = llm.StopEndTurn
		}
	case genai.FinishReasonMaxTokens:
		out.StopReason = llm.StopMaxTokens
	default:
		out.StopReason = llm.StopOther
	}
	return out, nil
}

func toGeminiContents(msgs []llm.Message) ([]*genai.Content, error) {
	var out []*genai.Content
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleUser:
			out = append(out, &genai.Content{
				Role:  genai.RoleUser,
				Parts: []*genai.Part{{Text: m.Content}},
			})
		case llm.RoleAssistant:
			parts := []*genai.Part{}
			if m.Content != "" {
				parts = append(parts, &genai.Part{Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				var args map[string]any
				_ = json.Unmarshal(tc.Input, &args)
				parts = append(parts, &genai.Part{
					FunctionCall: &genai.FunctionCall{
						ID:   tc.ID,
						Name: tc.Name,
						Args: args,
					},
				})
			}
			if len(parts) > 0 {
				out = append(out, &genai.Content{Role: genai.RoleModel, Parts: parts})
			}
		case llm.RoleTool:
			var respMap map[string]any
			if err := json.Unmarshal([]byte(m.Content), &respMap); err != nil {
				respMap = map[string]any{"result": m.Content}
			}
			out = append(out, &genai.Content{
				Role: genai.RoleUser,
				Parts: []*genai.Part{{
					FunctionResponse: &genai.FunctionResponse{
						ID:       m.ToolCallID,
						Name:     m.ToolCallID, // Gemini needs a name; caller can override via ID convention.
						Response: respMap,
					},
				}},
			})
		case llm.RoleSystem:
			// System instruction is passed via cfg; ignore here.
		default:
			return nil, fmt.Errorf("gemini: unknown role %q", m.Role)
		}
	}
	return out, nil
}
