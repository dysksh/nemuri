package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/nemuri/nemuri/internal/github"
	"github.com/nemuri/nemuri/internal/llm"
)

const (
	maxToolIterations    = 20
	maxOutputTokens      = 32768
	perCallMaxTokens     = 16384
	keepRecentIterations = 3   // number of recent iterations to keep full tool results
	trimContentThreshold = 500 // tool results shorter than this are always kept
	trimPreviewLines     = 20  // number of lines to keep as preview in trimmed content
)

// Agent orchestrates LLM calls with a tool loop.
type Agent struct {
	llm    llm.Client
	github *github.Client
	owner  string // default GitHub owner for repo tools
}

// RunResult holds the agent response and cumulative token usage.
type RunResult struct {
	Response          *AgentResponse
	TotalInputTokens  int
	TotalOutputTokens int
	Iterations        int
}

// New creates an Agent.
func New(llmClient llm.Client, githubClient *github.Client, defaultOwner string) *Agent {
	return &Agent{llm: llmClient, github: githubClient, owner: defaultOwner}
}

// Run executes the agent loop: Claude can call repo tools to read code,
// then calls deliver_result to produce the final response.
func (a *Agent) Run(ctx context.Context, prompt string) (*RunResult, error) {
	messages := []llm.Message{{Role: "user", Content: prompt}}
	opts := a.buildSendOptions()

	var totalInput, totalOutput int

	for i := range maxToolIterations {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// Set per-call max_tokens to min(remaining budget, per-call cap)
		remaining := maxOutputTokens - totalOutput
		if remaining <= 0 {
			return nil, fmt.Errorf("output token budget exhausted (%d/%d used)", totalOutput, maxOutputTokens)
		}
		opts.MaxTokens = min(remaining, perCallMaxTokens)

		slog.Info("agent iteration",
			"iteration", i+1,
			"input_tokens_used", totalInput,
			"output_tokens_used", totalOutput,
			"output_tokens_remaining", remaining,
		)

		// Trim old tool results to reduce input tokens
		trimmedMessages := trimConversation(messages, i)

		resp, err := a.llm.SendMessage(ctx, SystemPrompt, trimmedMessages, opts)
		if err != nil {
			return nil, fmt.Errorf("llm call (iteration %d): %w", i+1, err)
		}

		totalInput += resp.Usage.InputTokens
		totalOutput += resp.Usage.OutputTokens

		// Text-only response (shouldn't happen with tool_choice=any, but handle gracefully)
		if !resp.HasToolUse() {
			result := &RunResult{
				Response:          &AgentResponse{Type: "text", Content: resp.Content},
				TotalInputTokens:  totalInput,
				TotalOutputTokens: totalOutput,
				Iterations:        i + 1,
			}
			return result, nil
		}

		// Check if deliver_result is among the tool calls
		for _, tc := range resp.ToolCalls {
			if tc.Name == toolName {
				var agentResp AgentResponse
				if err := json.Unmarshal([]byte(tc.InputJSON), &agentResp); err != nil {
					return nil, fmt.Errorf("parse deliver_result: %w", err)
				}
				result := &RunResult{
					Response:          &agentResp,
					TotalInputTokens:  totalInput,
					TotalOutputTokens: totalOutput,
					Iterations:        i + 1,
				}
				return result, nil
			}
		}

		// Execute all repo tools and collect results
		messages = append(messages, resp.AssistantMessage())
		var toolResults []llm.ToolResultBlock
		for _, tc := range resp.ToolCalls {
			toolResult, toolErr := a.executeTool(ctx, tc.Name, tc.InputJSON)
			if toolErr != nil {
				slog.Warn("tool error", "tool", tc.Name, "error", toolErr)
				toolResults = append(toolResults, llm.ToolResultBlock{
					Type:      "tool_result",
					ToolUseID: tc.ID,
					Content:   toolErr.Error(),
					IsError:   true,
				})
			} else {
				slog.Info("tool executed", "tool", tc.Name, "result_length", len(toolResult))
				toolResults = append(toolResults, llm.ToolResultBlock{
					Type:      "tool_result",
					ToolUseID: tc.ID,
					Content:   toolResult,
				})
			}
		}
		messages = append(messages, llm.NewToolResultsMessage(toolResults))
	}

	return nil, fmt.Errorf("agent exceeded maximum tool iterations (%d)", maxToolIterations)
}

func (a *Agent) executeTool(ctx context.Context, name, inputJSON string) (string, error) {
	if a.github == nil {
		return "", fmt.Errorf("GitHub client is not configured")
	}

	switch name {
	case "list_repo_files":
		return a.execListRepoFiles(ctx, inputJSON)
	case "read_repo_file":
		return a.execReadRepoFile(ctx, inputJSON)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

func (a *Agent) execListRepoFiles(ctx context.Context, inputJSON string) (string, error) {
	var input struct {
		Repo string `json:"repo"`
		Ref  string `json:"ref"`
	}
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		return "", fmt.Errorf("parse input: %w", err)
	}
	if input.Repo == "" {
		return "", fmt.Errorf("repo is required")
	}

	ref := input.Ref
	if ref == "" {
		branch, err := a.github.GetDefaultBranch(ctx, a.owner, input.Repo)
		if err != nil {
			return "", err
		}
		ref = branch
	}

	entries, err := a.github.GetTree(ctx, a.owner, input.Repo, ref)
	if err != nil {
		return "", err
	}

	result, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	return string(result), nil
}

func (a *Agent) execReadRepoFile(ctx context.Context, inputJSON string) (string, error) {
	var input struct {
		Repo string `json:"repo"`
		Path string `json:"path"`
		Ref  string `json:"ref"`
	}
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		return "", fmt.Errorf("parse input: %w", err)
	}
	if input.Repo == "" || input.Path == "" {
		return "", fmt.Errorf("repo and path are required")
	}

	ref := input.Ref
	if ref == "" {
		branch, err := a.github.GetDefaultBranch(ctx, a.owner, input.Repo)
		if err != nil {
			return "", err
		}
		ref = branch
	}

	content, err := a.github.GetFileContent(ctx, a.owner, input.Repo, input.Path, ref)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

// trimConversation replaces large tool result content from older iterations with previews.
// This prevents the conversation from growing unboundedly as more files are read,
// while preserving the first trimPreviewLines lines so the LLM retains partial context.
//
// Conversation structure: [user_prompt, assistant1, tool_results1, assistant2, tool_results2, ...]
// Each iteration adds 2 messages (assistant + tool_results), so iteration index maps to message pairs.
// Messages at index 0 is the initial user prompt (always kept).
//
// Heuristic: tool results shorter than trimContentThreshold are always kept (errors, file lists, etc.).
// Only large content (file reads) from old iterations is replaced.
func trimConversation(messages []llm.Message, currentIteration int) []llm.Message {
	if currentIteration <= keepRecentIterations {
		return messages
	}

	// Calculate the message index cutoff: keep messages from recent iterations.
	// Messages layout: [user_prompt, (assistant, tool_results) × N]
	// Iteration i corresponds to messages at indices 1+2*i and 2+2*i.
	// We want to trim iterations older than (currentIteration - keepRecentIterations).
	oldestKept := currentIteration - keepRecentIterations
	cutoffIndex := 1 + 2*oldestKept // first message index to keep in full

	trimmed := make([]llm.Message, len(messages))
	copy(trimmed, messages)

	for idx := 1; idx < len(trimmed) && idx < cutoffIndex; idx++ {
		msg := trimmed[idx]
		if msg.Role != "user" {
			continue
		}

		// Tool result messages have Content as []ToolResultBlock
		results, ok := msg.Content.([]llm.ToolResultBlock)
		if !ok {
			continue
		}

		var newResults []llm.ToolResultBlock
		changed := false
		for _, r := range results {
			if !r.IsError && len(r.Content) > trimContentThreshold {
				newResults = append(newResults, llm.ToolResultBlock{
					Type:      r.Type,
					ToolUseID: r.ToolUseID,
					Content:   trimLargeContent(r.Content),
				})
				changed = true
			} else {
				newResults = append(newResults, r)
			}
		}
		if changed {
			trimmed[idx] = llm.Message{Role: "user", Content: newResults}
		}
	}

	return trimmed
}

// trimLargeContent keeps the first trimPreviewLines lines of content as a preview,
// appending metadata about the total size so the LLM knows how much was omitted.
func trimLargeContent(content string) string {
	lines := strings.Split(content, "\n")
	totalLines := len(lines)

	if totalLines <= trimPreviewLines {
		return content
	}

	preview := strings.Join(lines[:trimPreviewLines], "\n")
	return fmt.Sprintf("%s\n\n[... trimmed: showing first %d of %d lines, %d chars total]",
		preview, trimPreviewLines, totalLines, len(content))
}
