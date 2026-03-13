package main

import (
	"encoding/json"
	"testing"

	"github.com/nemuri/nemuri/internal/llm"
)

func TestReplaceToolResult_TypedSlice(t *testing.T) {
	toolID := "tool_123"
	messages := []llm.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "thinking..."},
		{Role: "user", Content: []llm.ToolResultBlock{
			{Type: "tool_result", ToolUseID: "other_tool", Content: "file content"},
			{Type: "tool_result", ToolUseID: toolID, Content: "Waiting for user response."},
		}},
	}

	replaceToolResult(messages, toolID, "User responded: yes")

	results := messages[2].Content.([]llm.ToolResultBlock)
	if results[1].Content != "User responded: yes" {
		t.Errorf("expected replaced content, got %q", results[1].Content)
	}
	// Other tool results should be untouched
	if results[0].Content != "file content" {
		t.Errorf("other tool result was modified: %q", results[0].Content)
	}
}

func TestReplaceToolResult_JSONDeserialized(t *testing.T) {
	toolID := "tool_456"

	// Simulate what json.Unmarshal produces for []ToolResultBlock
	// when the target field is `any`: []any with map[string]any elements.
	messages := []llm.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "thinking..."},
		{Role: "user", Content: []any{
			map[string]any{
				"type":        "tool_result",
				"tool_use_id": "other_tool",
				"content":     "file content",
			},
			map[string]any{
				"type":        "tool_result",
				"tool_use_id": toolID,
				"content":     "Waiting for user response.",
			},
		}},
	}

	replaceToolResult(messages, toolID, "User responded: 日本語の回答")

	items := messages[2].Content.([]any)
	replaced := items[1].(map[string]any)
	if replaced["content"] != "User responded: 日本語の回答" {
		t.Errorf("expected replaced content, got %q", replaced["content"])
	}
	other := items[0].(map[string]any)
	if other["content"] != "file content" {
		t.Errorf("other tool result was modified: %q", other["content"])
	}
}

func TestReplaceToolResult_NotFound(t *testing.T) {
	messages := []llm.Message{
		{Role: "user", Content: "hello"},
		{Role: "user", Content: []llm.ToolResultBlock{
			{Type: "tool_result", ToolUseID: "tool_999", Content: "some content"},
		}},
	}

	// Should not panic; logs a warning
	replaceToolResult(messages, "nonexistent_id", "answer")

	// Original content untouched
	results := messages[1].Content.([]llm.ToolResultBlock)
	if results[0].Content != "some content" {
		t.Errorf("content was unexpectedly modified: %q", results[0].Content)
	}
}

func TestReplaceToolResult_JSONRoundTrip(t *testing.T) {
	// End-to-end: simulate the actual save/restore cycle via JSON
	toolID := "tool_roundtrip"

	original := []llm.Message{
		{Role: "user", Content: "original prompt"},
		{Role: "assistant", Content: "assistant response"},
		{Role: "user", Content: []llm.ToolResultBlock{
			{Type: "tool_result", ToolUseID: "read_tool", Content: "file data"},
			{Type: "tool_result", ToolUseID: toolID, Content: "Waiting for user response."},
		}},
	}

	// Marshal and unmarshal to simulate S3 save/restore
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var restored []llm.Message
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Verify Content is now []any, not []llm.ToolResultBlock
	if _, ok := restored[2].Content.([]llm.ToolResultBlock); ok {
		t.Fatal("expected Content to lose its typed structure after JSON round-trip")
	}

	replaceToolResult(restored, toolID, "User responded: the actual answer")

	// Verify replacement worked on deserialized data
	items, ok := restored[2].Content.([]any)
	if !ok {
		t.Fatalf("expected []any after JSON round-trip, got %T", restored[2].Content)
	}
	target, ok := items[1].(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", items[1])
	}
	if target["content"] != "User responded: the actual answer" {
		t.Errorf("expected replaced content, got %q", target["content"])
	}
	// Other results untouched
	other := items[0].(map[string]any)
	if other["content"] != "file data" {
		t.Errorf("other tool result was modified: %q", other["content"])
	}
}
