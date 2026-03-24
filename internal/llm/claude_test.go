package llm_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nemuri/nemuri/internal/llm"
)

func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(handler)
}

func testClaudeClient(t *testing.T, serverURL string) llm.Client {
	t.Helper()
	return llm.NewClaudeClientWithURL("test-key", "", serverURL)
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("failed to write JSON response: %v", err)
	}
}

func readJSON(t *testing.T, r *http.Request, v any) {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		t.Fatalf("failed to read JSON request: %v", err)
	}
}

func TestClaudeClient_TextResponse(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("x-api-key = %q, want %q", r.Header.Get("x-api-key"), "test-key")
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Error("anthropic-version header missing")
		}

		writeJSON(t, w, map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": "Hello!"},
			},
			"stop_reason": "end_turn",
			"usage": map[string]int{
				"input_tokens":  10,
				"output_tokens": 5,
			},
		})
	})
	defer server.Close()

	client := testClaudeClient(t, server.URL)
	resp, err := client.SendMessage(context.Background(), "system", []llm.Message{
		{Role: "user", Content: "hi"},
	}, nil)
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	if resp.Content != "Hello!" {
		t.Errorf("content = %q, want %q", resp.Content, "Hello!")
	}
	if resp.HasToolUse() {
		t.Error("unexpected tool use in text response")
	}
	if resp.Usage.InputTokens != 10 {
		t.Errorf("input tokens = %d, want 10", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 5 {
		t.Errorf("output tokens = %d, want 5", resp.Usage.OutputTokens)
	}
}

func TestClaudeClient_ToolUseResponse(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"content": []map[string]any{
				{
					"type":  "tool_use",
					"id":    "tool-123",
					"name":  "deliver_result",
					"input": map[string]string{"type": "text", "content": "result"},
				},
			},
			"stop_reason": "tool_use",
			"usage": map[string]int{
				"input_tokens":  20,
				"output_tokens": 15,
			},
		})
	})
	defer server.Close()

	client := testClaudeClient(t, server.URL)
	resp, err := client.SendMessage(context.Background(), "", []llm.Message{
		{Role: "user", Content: "test"},
	}, nil)
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	if !resp.HasToolUse() {
		t.Fatal("expected tool use response")
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "tool-123" {
		t.Errorf("tool ID = %q, want %q", tc.ID, "tool-123")
	}
	if tc.Name != "deliver_result" {
		t.Errorf("tool name = %q, want %q", tc.Name, "deliver_result")
	}
}

func TestClaudeClient_MultipleToolUse(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"content": []map[string]any{
				{
					"type":  "tool_use",
					"id":    "tool-1",
					"name":  "list_repo_files",
					"input": map[string]string{"repo": "test"},
				},
				{
					"type":  "tool_use",
					"id":    "tool-2",
					"name":  "read_repo_file",
					"input": map[string]string{"repo": "test", "path": "main.go"},
				},
			},
			"stop_reason": "tool_use",
			"usage":       map[string]int{"input_tokens": 30, "output_tokens": 20},
		})
	})
	defer server.Close()

	client := testClaudeClient(t, server.URL)
	resp, err := client.SendMessage(context.Background(), "", []llm.Message{
		{Role: "user", Content: "test"},
	}, nil)
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("tool calls = %d, want 2", len(resp.ToolCalls))
	}
}

func TestClaudeClient_APIError(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		writeJSON(t, w, map[string]any{
			"type": "error",
			"error": map[string]string{
				"type":    "rate_limit_error",
				"message": "rate limited",
			},
		})
	})
	defer server.Close()

	client := testClaudeClient(t, server.URL)
	_, err := client.SendMessage(context.Background(), "", []llm.Message{
		{Role: "user", Content: "test"},
	}, nil)
	if err == nil {
		t.Fatal("SendMessage() expected error for 429")
	}
}

func TestClaudeClient_MaxTokensTruncation(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": "partial..."},
			},
			"stop_reason": "max_tokens",
			"usage":       map[string]int{"input_tokens": 10, "output_tokens": 100},
		})
	})
	defer server.Close()

	client := testClaudeClient(t, server.URL)
	_, err := client.SendMessage(context.Background(), "", []llm.Message{
		{Role: "user", Content: "test"},
	}, nil)
	if err == nil {
		t.Fatal("SendMessage() expected error for max_tokens truncation")
	}
}

func TestClaudeClient_EmptyResponse(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"content":     []map[string]any{},
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 10, "output_tokens": 0},
		})
	})
	defer server.Close()

	client := testClaudeClient(t, server.URL)
	_, err := client.SendMessage(context.Background(), "", []llm.Message{
		{Role: "user", Content: "test"},
	}, nil)
	if err == nil {
		t.Fatal("SendMessage() expected error for empty response")
	}
}

func TestClaudeClient_SendsToolsAndToolChoice(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		readJSON(t, r, &req)

		tools, ok := req["tools"].([]any)
		if !ok || len(tools) == 0 {
			t.Error("expected tools in request")
		}

		tc, ok := req["tool_choice"].(map[string]any)
		if !ok {
			t.Error("expected tool_choice in request")
		} else if tc["type"] != "any" {
			t.Errorf("tool_choice type = %q, want %q", tc["type"], "any")
		}

		writeJSON(t, w, map[string]any{
			"content": []map[string]any{
				{
					"type":  "tool_use",
					"id":    "t-1",
					"name":  "my_tool",
					"input": map[string]string{},
				},
			},
			"stop_reason": "tool_use",
			"usage":       map[string]int{"input_tokens": 5, "output_tokens": 3},
		})
	})
	defer server.Close()

	client := testClaudeClient(t, server.URL)
	_, err := client.SendMessage(context.Background(), "", []llm.Message{
		{Role: "user", Content: "test"},
	}, &llm.SendOptions{
		Tools: []llm.ToolDefinition{
			{Name: "my_tool", Description: "test tool", InputSchema: map[string]any{"type": "object"}},
		},
		ToolChoice: &llm.ToolChoice{Type: "any"},
	})
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
}

func TestClaudeClient_MaxTokensOption(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		readJSON(t, r, &req)

		maxTokens, ok := req["max_tokens"].(float64)
		if !ok || int(maxTokens) != 1024 {
			t.Errorf("max_tokens = %v, want 1024", req["max_tokens"])
		}

		writeJSON(t, w, map[string]any{
			"content":     []map[string]any{{"type": "text", "text": "ok"}},
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 5, "output_tokens": 1},
		})
	})
	defer server.Close()

	client := testClaudeClient(t, server.URL)
	_, err := client.SendMessage(context.Background(), "", []llm.Message{
		{Role: "user", Content: "test"},
	}, &llm.SendOptions{MaxTokens: 1024})
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
}

func TestClaudeClient_PromptCaching_SystemFormat(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		readJSON(t, r, &req)

		// System should be an array with cache_control
		system, ok := req["system"].([]any)
		if !ok || len(system) == 0 {
			t.Fatal("system should be a non-empty array")
		}
		block, ok := system[0].(map[string]any)
		if !ok {
			t.Fatal("system block should be an object")
		}
		if block["type"] != "text" {
			t.Errorf("system block type = %v, want text", block["type"])
		}
		if block["text"] != "test system" {
			t.Errorf("system block text = %v, want 'test system'", block["text"])
		}
		cc, ok := block["cache_control"].(map[string]any)
		if !ok {
			t.Fatal("system block should have cache_control")
		}
		if cc["type"] != "ephemeral" {
			t.Errorf("cache_control type = %v, want ephemeral", cc["type"])
		}

		writeJSON(t, w, map[string]any{
			"content":     []map[string]any{{"type": "text", "text": "ok"}},
			"stop_reason": "end_turn",
			"usage": map[string]int{
				"input_tokens":                10,
				"output_tokens":               5,
				"cache_creation_input_tokens": 8,
				"cache_read_input_tokens":     2,
			},
		})
	})
	defer server.Close()

	client := testClaudeClient(t, server.URL)
	resp, err := client.SendMessage(context.Background(), "test system", []llm.Message{
		{Role: "user", Content: "test"},
	}, nil)
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	if resp.Usage.CacheCreationInputTokens != 8 {
		t.Errorf("cache creation tokens = %d, want 8", resp.Usage.CacheCreationInputTokens)
	}
	if resp.Usage.CacheReadInputTokens != 2 {
		t.Errorf("cache read tokens = %d, want 2", resp.Usage.CacheReadInputTokens)
	}
}

func TestClaudeClient_PromptCaching_MessageCacheControl(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		readJSON(t, r, &req)

		// Last message should have cache_control on its content
		msgs, ok := req["messages"].([]any)
		if !ok || len(msgs) == 0 {
			t.Fatal("messages should be a non-empty array")
		}
		lastMsg, ok := msgs[len(msgs)-1].(map[string]any)
		if !ok {
			t.Fatal("last message should be an object")
		}
		content, ok := lastMsg["content"].([]any)
		if !ok {
			t.Fatal("last message content should be an array (wrapped for cache_control)")
		}
		lastBlock, ok := content[len(content)-1].(map[string]any)
		if !ok {
			t.Fatal("last content block should be an object")
		}
		cc, ok := lastBlock["cache_control"].(map[string]any)
		if !ok {
			t.Fatal("last content block should have cache_control")
		}
		if cc["type"] != "ephemeral" {
			t.Errorf("cache_control type = %v, want ephemeral", cc["type"])
		}

		writeJSON(t, w, map[string]any{
			"content":     []map[string]any{{"type": "text", "text": "ok"}},
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 10, "output_tokens": 5},
		})
	})
	defer server.Close()

	client := testClaudeClient(t, server.URL)
	_, err := client.SendMessage(context.Background(), "sys", []llm.Message{
		{Role: "user", Content: "first message"},
		{Role: "assistant", Content: json.RawMessage(`[{"type":"text","text":"reply"}]`)},
		{Role: "user", Content: "second message"},
	}, nil)
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
}
