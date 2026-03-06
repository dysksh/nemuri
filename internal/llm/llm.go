package llm

import "context"

// Message represents a single message in a conversation.
type Message struct {
	Role    string `json:"role"` // "user" or "assistant"
	Content string `json:"content"`
}

// Response represents the result of an LLM call.
type Response struct {
	Content string
}

// Client is the interface for LLM providers.
type Client interface {
	// SendMessage sends a prompt and returns the LLM response.
	SendMessage(ctx context.Context, systemPrompt string, messages []Message) (*Response, error)
}
