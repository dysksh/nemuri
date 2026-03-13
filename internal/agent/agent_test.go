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

func repoToolResponse(toolName, toolID string, input map[string]string) *llm.Response {
	inputJSON, _ := json.Marshal(input)
	rawContent, _ := json.Marshal([]map[string]any{
		{
			"type":  "tool_use",
			"id":    toolID,
			"name":  toolName,
			"input": json.RawMessage(inputJSON),
		},
	})
	return &llm.Response{
		ToolCalls: []llm.ToolCall{
			{ID: toolID, Name: toolName, InputJSON: string(inputJSON)},
		},
		RawContent: rawContent,
		Usage:      llm.Usage{InputTokens: 100, OutputTokens: 50},
	}
}

func askQuestionResponse(question, toolID string) *llm.Response {
	input, _ := json.Marshal(map[string]string{"question": question})
	rawContent, _ := json.Marshal([]map[string]any{
		{
			"type":  "tool_use",
			"id":    toolID,
			"name":  "ask_user_question",
			"input": json.RawMessage(input),
		},
	})
	return &llm.Response{
		ToolCalls: []llm.ToolCall{
			{ID: toolID, Name: "ask_user_question", InputJSON: string(input)},
		},
		RawContent: rawContent,
		Usage:      llm.Usage{InputTokens: 100, OutputTokens: 50},
	}
}

// TestAgent_Run_DeliverResult tests the two-phase flow: gathering text → generating deliver_result.
func TestAgent_Run_DeliverResult(t *testing.T) {
	mock := &mockLLMClient{
		responses: []*llm.Response{
			// Phase 1 (gathering): text-only response ends gathering
			textResponse("Summary: no files needed."),
			// Phase 2 (generating): deliver_result
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
	if mock.calls != 2 {
		t.Errorf("LLM calls = %d, want 2", mock.calls)
	}
	// Token accumulation: gathering (100+50) + generating (100+50)
	if result.TotalInputTokens != 200 {
		t.Errorf("input tokens = %d, want 200", result.TotalInputTokens)
	}
	if result.TotalOutputTokens != 100 {
		t.Errorf("output tokens = %d, want 100", result.TotalOutputTokens)
	}
}

// TestAgent_Run_NoGithub_SkipsGathering tests that with no GitHub client,
// gathering ends immediately with text, then generating produces output.
func TestAgent_Run_NoGithub_SkipsGathering(t *testing.T) {
	mock := &mockLLMClient{
		responses: []*llm.Response{
			textResponse("No repo tools, plan: create new_repo."),
			deliverResultResponse("new_repo", ""),
		},
	}

	a := agent.New(mock, nil, "")
	result, err := a.Run(context.Background(), "create a new repo")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Response.Type != "new_repo" {
		t.Errorf("response type = %q, want %q", result.Response.Type, "new_repo")
	}
	if mock.calls != 2 {
		t.Errorf("LLM calls = %d, want 2 (gathering text + generating)", mock.calls)
	}
}

// TestAgent_Run_GatheringReadsFiles tests that file reads during gathering
// are cached and the agent progresses through both phases.
func TestAgent_Run_GatheringReadsFiles(t *testing.T) {
	mock := &mockLLMClient{
		responses: []*llm.Response{
			// Gathering: read a file (will fail since no real github, but tests the flow)
			repoToolResponse("read_repo_file", "tool-1", map[string]string{"repo": "test", "path": "main.go"}),
			// Gathering: text summary ends phase
			textResponse("Read main.go. Plan: add endpoint.\nNEEDED_FILES:\n- test:main.go"),
			// Generating: deliver_result
			deliverResultResponse("code", ""),
		},
	}

	// github=nil, so tool execution will fail, but the loop should continue
	a := agent.New(mock, nil, "")
	result, err := a.Run(context.Background(), "add endpoint")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Response.Type != "code" {
		t.Errorf("response type = %q, want %q", result.Response.Type, "code")
	}
	if mock.calls != 3 {
		t.Errorf("LLM calls = %d, want 3", mock.calls)
	}
	// Iterations: 2 gathering + 1 generating = 3
	if result.Iterations != 3 {
		t.Errorf("iterations = %d, want 3", result.Iterations)
	}
}

// TestAgent_Run_LLMError tests error propagation from the LLM.
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

// TestAgent_Run_ContextCancellation tests that a cancelled context is handled.
func TestAgent_Run_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	mock := &mockLLMClient{}
	a := agent.New(mock, nil, "")
	_, err := a.Run(ctx, "test")
	if err == nil {
		t.Fatal("Run() expected error on cancelled context")
	}
}

// TestAgent_Run_MaxGatheringIterations tests that the gathering phase
// forces a summary when max iterations are reached.
func TestAgent_Run_MaxGatheringIterations(t *testing.T) {
	// 15 repo tool calls + 1 forced summary + 1 generating = 17 total
	responses := make([]*llm.Response, 17)
	for i := range 15 {
		responses[i] = repoToolResponse("list_repo_files", fmt.Sprintf("tool-%d", i), map[string]string{"repo": "test"})
	}
	responses[15] = textResponse("Forced summary after max iterations.")
	responses[16] = deliverResultResponse("text", "done")

	mock := &mockLLMClient{responses: responses}
	a := agent.New(mock, nil, "")
	result, err := a.Run(context.Background(), "keep looping")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Response.Content != "done" {
		t.Errorf("response content = %q, want %q", result.Response.Content, "done")
	}
}

// TestAgent_Run_TokenUsageAccumulated tests that token usage from both phases is accumulated.
func TestAgent_Run_TokenUsageAccumulated(t *testing.T) {
	mock := &mockLLMClient{
		responses: []*llm.Response{
			// Gathering: 1 repo tool + 1 text summary = 2 calls
			repoToolResponse("list_repo_files", "tool-1", map[string]string{"repo": "test"}),
			textResponse("Summary ready."),
			// Generating: 1 deliver_result
			deliverResultResponse("text", "done"),
		},
	}

	a := agent.New(mock, nil, "")
	result, err := a.Run(context.Background(), "multi-step")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// 3 calls × 100 input tokens = 300
	if result.TotalInputTokens != 300 {
		t.Errorf("total input tokens = %d, want 300", result.TotalInputTokens)
	}
	// 3 calls × 50 output tokens = 150
	if result.TotalOutputTokens != 150 {
		t.Errorf("total output tokens = %d, want 150", result.TotalOutputTokens)
	}
	// 2 gathering iterations + 1 generating iteration = 3
	if result.Iterations != 3 {
		t.Errorf("iterations = %d, want 3", result.Iterations)
	}
}

// TestAgent_GatheringPhase_AskQuestion tests that ask_user_question during
// gathering returns Phase="gathering" and FileCache.
func TestAgent_GatheringPhase_AskQuestion(t *testing.T) {
	mock := &mockLLMClient{
		responses: []*llm.Response{
			askQuestionResponse("Which branch?", "ask-1"),
		},
	}

	a := agent.New(mock, nil, "")
	result, err := a.Run(context.Background(), "deploy something")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Question != "Which branch?" {
		t.Errorf("question = %q, want %q", result.Question, "Which branch?")
	}
	if result.Phase != "gathering" {
		t.Errorf("phase = %q, want %q", result.Phase, "gathering")
	}
	if result.PendingToolCallID != "ask-1" {
		t.Errorf("pending tool call ID = %q, want %q", result.PendingToolCallID, "ask-1")
	}
	if result.FileCache == nil {
		t.Error("FileCache should not be nil")
	}
}

// TestAgent_Resume_FromGathering tests resuming from a gathering-phase question.
func TestAgent_Resume_FromGathering(t *testing.T) {
	mock := &mockLLMClient{
		responses: []*llm.Response{
			// After resume: gathering text summary
			textResponse("User said main branch. Plan: add feature."),
			// Generating: deliver_result
			deliverResultResponse("code", ""),
		},
	}

	// Simulate saved state: messages with original prompt + ask tool call + user answer
	messages := []llm.Message{
		{Role: "user", Content: "add feature to repo"},
	}
	fileCache := map[string]string{
		"repo:main.go": "package main\n",
	}

	a := agent.New(mock, nil, "")
	result, err := a.Resume(context.Background(), messages, "gathering", fileCache)
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if result.Response.Type != "code" {
		t.Errorf("response type = %q, want %q", result.Response.Type, "code")
	}
	if mock.calls != 2 {
		t.Errorf("LLM calls = %d, want 2", mock.calls)
	}
}
