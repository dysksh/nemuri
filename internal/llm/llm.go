package llm

import (
	"context"
	"encoding/json"
)

// Role constants for conversation messages.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Message represents a single message in a conversation.
type Message struct {
	Role    string `json:"role"` // RoleUser or RoleAssistant
	Content any    `json:"content"`
}

// ToolResultBlock is a content block for sending tool results back to the LLM.
type ToolResultBlock struct {
	Type      string `json:"type"` // always "tool_result"
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error,omitempty"`
}

// ToolDefinition defines a tool that the LLM can call.
type ToolDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"input_schema"`
}

// ToolChoice specifies which tool the LLM must use.
type ToolChoice struct {
	Type string `json:"type"` // "auto", "any", or "tool"
	Name string `json:"name,omitempty"`
}

// SendOptions holds optional parameters for SendMessage.
type SendOptions struct {
	Tools        []ToolDefinition
	ToolChoice   *ToolChoice
	MaxTokens    int  // per-call max output tokens (0 = use client default)
	DisableCache bool // when true, skip prompt cache breakpoints (cache_control)
}

// Usage holds token usage for a single API call.
type Usage struct {
	InputTokens              int
	OutputTokens             int
	CacheCreationInputTokens int
	CacheReadInputTokens     int
}

// TotalInputTokens returns the total input tokens including cached tokens.
// When prompt caching is active, InputTokens only counts uncached tokens.
// The true total is InputTokens + CacheCreationInputTokens + CacheReadInputTokens.
func (u Usage) TotalInputTokens() int {
	return u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

// ToolCall represents a single tool_use block from the LLM response.
type ToolCall struct {
	ID        string // tool_use ID
	Name      string // tool name
	InputJSON string // raw JSON input
}

// Response represents the result of an LLM call.
type Response struct {
	Content    string          // text content (empty when tool_use)
	ToolCalls  []ToolCall      // tool calls (one or more when stop_reason=tool_use)
	RawContent json.RawMessage // raw content blocks from API (for conversation history)
	Usage      Usage           // token usage for this call
}

// HasToolUse returns true if the response contains any tool calls.
func (r *Response) HasToolUse() bool {
	return len(r.ToolCalls) > 0
}

// AssistantMessage creates a Message from this response for conversation history.
func (r *Response) AssistantMessage() Message {
	return Message{Role: RoleAssistant, Content: r.RawContent}
}

// NewToolResultsMessage creates a user message containing multiple tool results.
func NewToolResultsMessage(results []ToolResultBlock) Message {
	return Message{Role: RoleUser, Content: results}
}

// Client is the interface for LLM providers.
type Client interface {
	// SendMessage sends a prompt and returns the LLM response.
	SendMessage(ctx context.Context, systemPrompt string, messages []Message, opts *SendOptions) (*Response, error)
}
