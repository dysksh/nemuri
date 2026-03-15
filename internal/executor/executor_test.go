package executor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nemuri/nemuri/internal/agent"
	"github.com/nemuri/nemuri/internal/llm"
)

func TestTruncateJobID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"abcdefghij", "abcdefgh"},
		{"short", "short"},
		{"abcdefgh", "abcdefgh"},
		{"", ""},
	}
	for _, tt := range tests {
		got := truncateJobID(tt.input)
		if got != tt.want {
			t.Errorf("truncateJobID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestBuildPRNotificationMessage(t *testing.T) {
	files := []agent.OutputFile{
		{Path: "main.go", Name: "main.go"},
		{Path: "util.go", Name: "util.go"},
	}

	msg := buildPRNotificationMessage("https://github.com/pr/1", "Add feature", "Description here", files, nil)

	if msg == "" {
		t.Fatal("expected non-empty message")
	}
	if !strings.Contains(msg, "https://github.com/pr/1") {
		t.Error("expected PR URL in message")
	}
	if !strings.Contains(msg, "Add feature") {
		t.Error("expected title in message")
	}
	if !strings.Contains(msg, "`main.go`") {
		t.Error("expected file path in message")
	}
}

func TestBuildPRNotificationMessage_WithReview(t *testing.T) {
	files := []agent.OutputFile{{Path: "main.go"}}
	review := &agent.ReviewLoopResult{
		Passed:    true,
		Revisions: 1,
		Reviews: []agent.ReviewResult{
			{
				Scores: agent.ReviewScores{
					Correctness:     8.0,
					Security:        9.0,
					Maintainability: 7.5,
					Completeness:    8.5,
				},
			},
		},
	}

	msg := buildPRNotificationMessage("url", "title", "", files, review)
	if !strings.Contains(msg, "[Review PASS]") {
		t.Error("expected PASS review status")
	}
	if !strings.Contains(msg, "8.0") {
		t.Error("expected correctness score")
	}
}

func TestFormatFilePaths(t *testing.T) {
	files := []agent.OutputFile{
		{Path: "src/main.go"},
		{Name: "report.txt"},
		{Path: "lib/util.go", Name: "util.go"},
	}
	got := formatFilePaths(files)
	if got != "`src/main.go`, `report.txt`, `lib/util.go`" {
		t.Errorf("unexpected result: %s", got)
	}
}

func TestFormatReviewSummary_Empty(t *testing.T) {
	result := &agent.ReviewLoopResult{Reviews: nil}
	got := formatReviewSummary(result)
	if got != "" {
		t.Errorf("expected empty string for no reviews, got %q", got)
	}
}

func TestFormatReviewSummary_Warn(t *testing.T) {
	result := &agent.ReviewLoopResult{
		Passed:    false,
		Revisions: 2,
		Reviews: []agent.ReviewResult{
			{Scores: agent.ReviewScores{Correctness: 5, Security: 6, Maintainability: 5, Completeness: 4}},
		},
	}
	got := formatReviewSummary(result)
	if !strings.Contains(got, "WARN") {
		t.Errorf("expected WARN status, got %q", got)
	}
}

func TestReplaceToolResult_TypedSlice(t *testing.T) {
	toolID := "tool_123"
	messages := []llm.Message{
		{Role: llm.RoleUser, Content: "hello"},
		{Role: llm.RoleAssistant, Content: "thinking..."},
		{Role: llm.RoleUser, Content: []llm.ToolResultBlock{
			{Type: "tool_result", ToolUseID: "other", Content: "file content"},
			{Type: "tool_result", ToolUseID: toolID, Content: "placeholder"},
		}},
	}

	ReplaceToolResult(messages, toolID, "User responded: yes")

	results := messages[2].Content.([]llm.ToolResultBlock)
	if results[1].Content != "User responded: yes" {
		t.Errorf("expected replaced content, got %q", results[1].Content)
	}
	if results[0].Content != "file content" {
		t.Errorf("other result was modified: %q", results[0].Content)
	}
}

func TestReplaceToolResult_JSONDeserialized(t *testing.T) {
	toolID := "tool_456"
	messages := []llm.Message{
		{Role: llm.RoleUser, Content: []any{
			map[string]any{"type": "tool_result", "tool_use_id": "other", "content": "data"},
			map[string]any{"type": "tool_result", "tool_use_id": toolID, "content": "placeholder"},
		}},
	}

	ReplaceToolResult(messages, toolID, "answer")

	items := messages[0].Content.([]any)
	m := items[1].(map[string]any)
	if m["content"] != "answer" {
		t.Errorf("expected replaced content, got %q", m["content"])
	}
}

func TestReplaceToolResult_NotFound(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleUser, Content: "hello"},
	}
	// Should not panic
	ReplaceToolResult(messages, "nonexistent", "answer")
}

func TestReplaceToolResult_JSONRoundTrip(t *testing.T) {
	// End-to-end: simulate the actual save/restore cycle via JSON
	toolID := "tool_roundtrip"

	original := []llm.Message{
		{Role: llm.RoleUser, Content: "original prompt"},
		{Role: llm.RoleAssistant, Content: "assistant response"},
		{Role: llm.RoleUser, Content: []llm.ToolResultBlock{
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

	ReplaceToolResult(restored, toolID, "User responded: the actual answer")

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
