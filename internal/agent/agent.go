package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/nemuri/nemuri/internal/github"
	"github.com/nemuri/nemuri/internal/llm"
)

const (
	maxToolIterations = 20
	maxOutputTokens   = 32768
	perCallMaxTokens  = 16384
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
	Question          string        // non-empty if agent wants to ask user a question
	Messages          []llm.Message // conversation context (for save/resume)
	PendingToolCallID string        // tool_use ID for the question (for resume)
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
	return a.runLoop(ctx, messages)
}

// Resume continues the agent loop from saved conversation state.
// The messages should include the full prior conversation plus the tool_result
// containing the user's answer.
func (a *Agent) Resume(ctx context.Context, messages []llm.Message) (*RunResult, error) {
	return a.runLoop(ctx, messages)
}

func (a *Agent) runLoop(ctx context.Context, messages []llm.Message) (*RunResult, error) {
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

		slog.Info("agent iteration", "iteration", i+1, "output_tokens_used", totalOutput, "output_tokens_remaining", remaining)

		resp, err := a.llm.SendMessage(ctx, SystemPrompt, messages, opts)
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

		// Check if deliver_result or ask_user_question is among the tool calls
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

			if tc.Name == askToolName {
				var askInput struct {
					Question string `json:"question"`
				}
				if err := json.Unmarshal([]byte(tc.InputJSON), &askInput); err != nil {
					return nil, fmt.Errorf("parse ask_user_question: %w", err)
				}

				// Save conversation context including the assistant's ask message
				messages = append(messages, resp.AssistantMessage())

				result := &RunResult{
					Question:          askInput.Question,
					PendingToolCallID: tc.ID,
					Messages:          messages,
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
