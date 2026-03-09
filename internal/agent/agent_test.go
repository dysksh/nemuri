package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/nemuri/nemuri/internal/agent"
	"github.com/nemuri/nemuri/internal/llm"
)

// mockLLMClient implements llm.Client for testing.
type mockLLMClient struct {
	calls     int
	responses []*llm.Response
	errors    []error
}

func (m *mockLLMClient) SendMessage(_ context.Context, _ string, _ []llm.Message, _ *llm.SendOptions) (*llm.Response, error) {
	i := m.calls
	m.calls++
	if i < len(m.errors) && m.errors[i] != nil {
		return nil, m.errors[i]
	}
	if i < len(m.responses) {
		return m.responses[i], nil
	}
	return nil, fmt.Errorf("unexpected call #%d", i+1)
}

func deliverResultResponse(typ, content string) *llm.Response {
	input, _ := json.Marshal(map[string]any{
		"type":    typ,
		"content": content,
	})
	rawContent, _ := json.Marshal([]map[string]any{
		{
			"type":  "tool_use",
			"id":    "tool-1",
			"name":  "deliver_result",
			"input": json.RawMessage(input),
		},
	})
	return &llm.Response{
		ToolCalls: []llm.ToolCall{
			{ID: "tool-1", Name: "deliver_result", InputJSON: string(input)},
		},
		RawContent: rawContent,
		Usage:      llm.Usage{InputTokens: 100, OutputTokens: 50},
	}
}

func textResponse(text string) *llm.Response {
	rawContent, _ := json.Marshal([]map[string]any{
		{"type": "text", "text": text},
	})
	return &llm.Response{
		Content:    text,
		RawContent: rawContent,
		Usage:      llm.Usage{InputTokens: 100, OutputTokens: 50},
	}
}

func repoToolResponse(toolName, toolID string) *llm.Response {
	input, _ := json.Marshal(map[string]string{"repo": "test-repo"})
	rawContent, _ := json.Marshal([]map[string]any{
		{
			"type":  "tool_use",
			"id":    toolID,
			"name":  toolName,
			"input": json.RawMessage(input),
		},
	})
	return &llm.Response{
		ToolCalls: []llm.ToolCall{
			{ID: toolID, Name: toolName, InputJSON: string(input)},
		},
		RawContent: rawContent,
		Usage:      llm.Usage{InputTokens: 100, OutputTokens: 50},
	}
}

func TestAgent_Run_DeliverResult(t *testing.T) {
	mock := &mockLLMClient{
		responses: []*llm.Response{
			deliverResultResponse("text", "Hello, world!"),
		},
	}

	a := agent.New(mock, nil, "")
	result, err := a.Run(context.Background(), "say hello")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Response.Type != "text" {
		t.Errorf("response type = %q, want %q", result.Response.Type, "text")
	}
	if result.Response.Content != "Hello, world!" {
		t.Errorf("response content = %q, want %q", result.Response.Content, "Hello, world!")
	}
	if result.Iterations != 1 {
		t.Errorf("iterations = %d, want 1", result.Iterations)
	}
	if result.TotalInputTokens != 100 {
		t.Errorf("input tokens = %d, want 100", result.TotalInputTokens)
	}
	if result.TotalOutputTokens != 50 {
		t.Errorf("output tokens = %d, want 50", result.TotalOutputTokens)
	}
}

func TestAgent_Run_TextOnlyResponse(t *testing.T) {
	mock := &mockLLMClient{
		responses: []*llm.Response{
			textResponse("plain text answer"),
		},
	}

	a := agent.New(mock, nil, "")
	result, err := a.Run(context.Background(), "question")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Response.Type != "text" {
		t.Errorf("response type = %q, want %q", result.Response.Type, "text")
	}
	if result.Response.Content != "plain text answer" {
		t.Errorf("response content = %q, want %q", result.Response.Content, "plain text answer")
	}
}

func TestAgent_Run_LLMError(t *testing.T) {
	mock := &mockLLMClient{
		errors: []error{fmt.Errorf("API rate limit exceeded")},
	}

	a := agent.New(mock, nil, "")
	_, err := a.Run(context.Background(), "test")
	if err == nil {
		t.Fatal("Run() expected error")
	}
}

func TestAgent_Run_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	mock := &mockLLMClient{}
	a := agent.New(mock, nil, "")
	_, err := a.Run(ctx, "test")
	if err == nil {
		t.Fatal("Run() expected error on cancelled context")
	}
}

func TestAgent_Run_MaxIterationsExceeded(t *testing.T) {
	// Return a non-deliver_result tool call every time, but github is nil so executeTool will error.
	// The agent should still loop and eventually hit the max iterations limit.
	// However, with github=nil, executeTool returns an error which is sent back as tool_result.
	// The loop continues until maxToolIterations (20).
	responses := make([]*llm.Response, 21)
	for i := range responses {
		responses[i] = repoToolResponse("list_repo_files", fmt.Sprintf("tool-%d", i))
	}

	mock := &mockLLMClient{responses: responses}
	a := agent.New(mock, nil, "")
	_, err := a.Run(context.Background(), "keep looping")
	if err == nil {
		t.Fatal("Run() expected error for max iterations")
	}
}

func TestAgent_Run_TokenUsageAccumulated(t *testing.T) {
	// Two iterations: first a repo tool call (error because no github), then deliver_result
	mock := &mockLLMClient{
		responses: []*llm.Response{
			repoToolResponse("list_repo_files", "tool-1"),
			deliverResultResponse("text", "done"),
		},
	}

	a := agent.New(mock, nil, "")
	result, err := a.Run(context.Background(), "multi-step")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.TotalInputTokens != 200 {
		t.Errorf("total input tokens = %d, want 200", result.TotalInputTokens)
	}
	if result.TotalOutputTokens != 100 {
		t.Errorf("total output tokens = %d, want 100", result.TotalOutputTokens)
	}
	if result.Iterations != 2 {
		t.Errorf("iterations = %d, want 2", result.Iterations)
	}
}
