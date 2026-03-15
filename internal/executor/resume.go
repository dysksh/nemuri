package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/nemuri/nemuri/internal/agent"
	"github.com/nemuri/nemuri/internal/llm"
	"github.com/nemuri/nemuri/internal/state"
)

func (e *Executor) resumeAgent(ctx context.Context, job *state.Job) (*agent.RunResult, error) {
	if e.Storage == nil {
		return nil, fmt.Errorf("S3 storage not configured, cannot load conversation context")
	}

	data, err := e.Storage.DownloadArtifact(ctx, job.JobID, conversationContextFile)
	if err != nil {
		return nil, fmt.Errorf("load conversation context: %w", err)
	}

	var convCtx conversationContext
	if err := json.Unmarshal(data, &convCtx); err != nil {
		return nil, fmt.Errorf("unmarshal conversation context: %w", err)
	}

	ReplaceToolResult(convCtx.Messages, convCtx.PendingToolCallID, fmt.Sprintf("User responded: %s", job.UserResponse))

	slog.Info("resuming agent with conversation context",
		"job_id", job.JobID,
		"messages", len(convCtx.Messages),
		"pending_tool_id", convCtx.PendingToolCallID,
	)

	return e.Agent.Resume(ctx, convCtx.Messages, convCtx.Phase, convCtx.FileCache)
}

// ReplaceToolResult finds the placeholder tool_result for the given toolUseID
// in the last user message and replaces its content with the real answer.
// Handles both in-memory typed slices ([]llm.ToolResultBlock) and
// JSON-deserialized generic types ([]any with map[string]any elements).
func ReplaceToolResult(messages []llm.Message, toolUseID, content string) {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != llm.RoleUser {
			continue
		}

		// Try typed slice first (in-memory path)
		if results, ok := messages[i].Content.([]llm.ToolResultBlock); ok {
			for j := range results {
				if results[j].ToolUseID == toolUseID {
					results[j].Content = content
					return
				}
			}
			continue
		}

		// Fallback: JSON-deserialized path ([]any with map[string]any elements)
		items, ok := messages[i].Content.([]any)
		if !ok {
			continue
		}
		for _, item := range items {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			id, _ := m["tool_use_id"].(string)
			if id == toolUseID {
				m["content"] = content
				return
			}
		}
	}
	slog.Warn("ReplaceToolResult: placeholder not found for tool_use_id", "tool_use_id", toolUseID)
}
