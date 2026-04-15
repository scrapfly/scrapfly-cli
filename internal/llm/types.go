// Package llm is a provider-agnostic interface over LLM chat + tool-use APIs.
//
// The design mirrors browser-use's Python Protocol: one Chat method,
// provider-specific adapters translate between this common shape and the
// respective official SDK (Anthropic, OpenAI, Gemini, Ollama/OpenAI-compat).
//
// The agent loop in cmd/scrapfly imports THIS package, never a provider
// package directly. Selection is by name at construction time.
package llm

import (
	"context"
	"encoding/json"
	"errors"
)

// Role values on a Message.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// StopReason classifies why the model stopped producing output.
type StopReason string

const (
	StopEndTurn   StopReason = "end_turn"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	StopOther     StopReason = "other"
)

// Message is a turn in the conversation.
//
//   - role=system      : Content is the system prompt (usually set via
//     ChatRequest.System; this role exists for providers
//     that don't separate it, e.g. OpenAI).
//   - role=user        : Content is the user text.
//   - role=assistant   : Content is free text; ToolCalls carries any tool calls.
//   - role=tool        : ToolCallID + Content (stringified tool result).
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	IsError    bool       `json:"is_error,omitempty"` // only for role=tool
}

// Tool is a provider-agnostic function definition.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"` // JSON Schema (draft 2020-12 works for all)
}

// ToolCall is a single function invocation emitted by the model.
type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ChatRequest is the common input to Provider.Chat.
type ChatRequest struct {
	Model       string    // per-provider model id; empty → provider default
	System      string    // system prompt (cacheable where supported)
	Messages    []Message // rolling conversation
	Tools       []Tool
	MaxTokens   int     // 0 → provider default
	Temperature float64 // 0 → provider default
	ToolChoice  string  // "auto" | "any" | "required" | "" (= auto)
}

// ChatResponse is the common output from Provider.Chat.
type ChatResponse struct {
	Text       string
	ToolCalls  []ToolCall
	Usage      Usage
	StopReason StopReason
}

// Usage holds token accounting (best-effort per provider).
type Usage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

// Provider is the single interface each LLM adapter must satisfy.
type Provider interface {
	Name() string
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}

// ErrNoAPIKey is returned when a provider is selected but its credential is
// missing from both flags and env.
var ErrNoAPIKey = errors.New("no API key configured for this provider")
