package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"

	"github.com/nemuri/nemuri/internal/github"
	"github.com/nemuri/nemuri/internal/llm"
)

const (
	maxGatheringIterations = 15
	maxOutputTokens        = 32768
	perCallMaxTokens       = 16384
	generatingMaxTokens    = 16384 // independent of gathering budget; generating is a single call with fresh context
	keepRecentIterations   = 3     // number of recent iterations to keep full tool results
	trimContentThreshold   = 500   // tool results shorter than this are always kept
	trimPreviewLines       = 20    // number of lines to keep as preview in trimmed content
)

// Agent orchestrates LLM calls with a two-phase loop (gathering → generating).
type Agent struct {
	llm    llm.Client
	github github.API
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
	Phase             string            // "gathering" when paused during gathering (for resume)
	FileCache         map[string]string // cached file contents (for resume from gathering)
}

// gatheringResult holds the output of the gathering phase.
type gatheringResult struct {
	summary      string            // LLM's text response (findings + plan + needed files)
	fileCache    map[string]string // key: "repo:path" → full file content
	messages     []llm.Message     // full gathering conversation (for extracting Q&A)
	inputTokens  int
	outputTokens int
	iterations   int
}

// New creates an Agent.
func New(llmClient llm.Client, githubClient github.API, defaultOwner string) *Agent {
	return &Agent{llm: llmClient, github: githubClient, owner: defaultOwner}
}

// Run executes the two-phase agent loop: gathering (read code) → generating (produce output).
func (a *Agent) Run(ctx context.Context, prompt string) (*RunResult, error) {
	messages := []llm.Message{{Role: llm.RoleUser, Content: prompt}}
	return a.run(ctx, prompt, messages, make(map[string]string))
}

// Resume continues the agent from saved conversation state.
// phase indicates which phase to resume ("gathering" or empty for legacy).
// fileCache contains previously cached file contents (may be nil).
func (a *Agent) Resume(ctx context.Context, messages []llm.Message, phase string, fileCache map[string]string) (*RunResult, error) {
	if fileCache == nil {
		fileCache = make(map[string]string)
	}

	prompt := extractOriginalPrompt(messages)
	return a.run(ctx, prompt, messages, fileCache)
}

// run orchestrates the two-phase loop.
func (a *Agent) run(ctx context.Context, prompt string, messages []llm.Message, fileCache map[string]string) (*RunResult, error) {
	// Phase 1: Gathering
	gathering, questionResult, err := a.gatheringPhase(ctx, messages, fileCache)
	if err != nil {
		return nil, err
	}
	if questionResult != nil {
		return questionResult, nil
	}

	// Phase 2: Generating
	result, err := a.generatingPhase(ctx, prompt, gathering)
	if err != nil {
		return nil, err
	}

	// Accumulate tokens from both phases
	result.TotalInputTokens += gathering.inputTokens
	result.TotalOutputTokens += gathering.outputTokens
	result.Iterations += gathering.iterations
	return result, nil
}

// gatheringPhase reads the codebase using repo tools until the LLM returns a text summary.
// Returns either a gatheringResult (phase complete) or a RunResult with a question (paused).
func (a *Agent) gatheringPhase(ctx context.Context, messages []llm.Message, fileCache map[string]string) (*gatheringResult, *RunResult, error) {
	opts := a.buildGatheringSendOptions()

	var totalInput, totalOutput int

	for i := range maxGatheringIterations {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}

		remaining := maxOutputTokens - totalOutput
		if remaining <= 0 {
			return nil, nil, fmt.Errorf("output token budget exhausted (%d/%d used)", totalOutput, maxOutputTokens)
		}
		opts.MaxTokens = min(remaining, perCallMaxTokens)

		slog.Info("agent iteration",
			"phase", "gathering",
			"iteration", i+1,
			"input_tokens_used", totalInput,
			"output_tokens_used", totalOutput,
			"output_tokens_remaining", remaining,
		)

		trimmedMessages := trimConversation(messages, i)

		resp, err := a.llm.SendMessage(ctx, GatheringSystemPrompt, trimmedMessages, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("gathering llm call (iteration %d): %w", i+1, err)
		}

		totalInput += resp.Usage.InputTokens
		totalOutput += resp.Usage.OutputTokens

		// Text-only response: gathering is complete
		if !resp.HasToolUse() {
			return &gatheringResult{
				summary:      resp.Content,
				fileCache:    fileCache,
				messages:     messages,
				inputTokens:  totalInput,
				outputTokens: totalOutput,
				iterations:   i + 1,
			}, nil, nil
		}

		// Execute repo tools first (even if ask_user_question is also present),
		// then handle ask_user_question so file reads are not lost.
		messages = append(messages, resp.AssistantMessage())
		var toolResults []llm.ToolResultBlock
		var pendingAsk *llm.ToolCall
		for _, tc := range resp.ToolCalls {
			if tc.Name == askToolName {
				pendingAsk = &llm.ToolCall{ID: tc.ID, Name: tc.Name, InputJSON: tc.InputJSON}
				continue
			}
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

				// Cache file content for read_repo_file
				if tc.Name == "read_repo_file" {
					cacheKey := fileCacheKey(tc.InputJSON)
					if cacheKey != "" {
						fileCache[cacheKey] = toolResult
					}
				}
			}
		}

		// If ask_user_question was called, return after processing repo tools.
		// All tool_result blocks (repo + ask placeholder) go in a single user message
		// so the conversation stays valid for the Claude API.
		if pendingAsk != nil {
			var askInput struct {
				Question string `json:"question"`
			}
			if err := json.Unmarshal([]byte(pendingAsk.InputJSON), &askInput); err != nil {
				return nil, nil, fmt.Errorf("parse ask_user_question: %w", err)
			}
			// Add a placeholder tool_result for ask_user_question; the real answer
			// will replace this block's content on resume.
			toolResults = append(toolResults, llm.ToolResultBlock{
				Type:      "tool_result",
				ToolUseID: pendingAsk.ID,
				Content:   "Waiting for user response.",
			})
			messages = append(messages, llm.NewToolResultsMessage(toolResults))
			return nil, &RunResult{
				Question:          askInput.Question,
				PendingToolCallID: pendingAsk.ID,
				Messages:          messages,
				TotalInputTokens:  totalInput,
				TotalOutputTokens: totalOutput,
				Iterations:        i + 1,
				Phase:             "gathering",
				FileCache:         fileCache,
			}, nil
		}

		messages = append(messages, llm.NewToolResultsMessage(toolResults))
	}

	// Max iterations reached: force a summary by calling with no tools
	slog.Warn("gathering phase reached max iterations, forcing summary", "iterations", maxGatheringIterations)
	remaining := maxOutputTokens - totalOutput
	if remaining <= 0 {
		return nil, nil, fmt.Errorf("output token budget exhausted after gathering (%d/%d used)", totalOutput, maxOutputTokens)
	}

	forceOpts := &llm.SendOptions{
		MaxTokens: min(remaining, perCallMaxTokens),
	}
	trimmedMessages := trimConversation(messages, maxGatheringIterations)
	forceMessages := make([]llm.Message, len(trimmedMessages)+1)
	copy(forceMessages, trimmedMessages)
	forceMessages[len(trimmedMessages)] = llm.Message{
		Role:    llm.RoleUser,
		Content: "You have reached the maximum number of tool calls. Please provide your summary now as a text response: findings, implementation plan, and NEEDED_FILES list.",
	}

	resp, err := a.llm.SendMessage(ctx, GatheringSystemPrompt, forceMessages, forceOpts)
	if err != nil {
		return nil, nil, fmt.Errorf("gathering forced summary: %w", err)
	}
	totalInput += resp.Usage.InputTokens
	totalOutput += resp.Usage.OutputTokens

	return &gatheringResult{
		summary:      resp.Content,
		fileCache:    fileCache,
		messages:     messages,
		inputTokens:  totalInput,
		outputTokens: totalOutput,
		iterations:   maxGatheringIterations + 1,
	}, nil, nil
}

// generatingPhase produces the final output in a single LLM call.
func (a *Agent) generatingPhase(ctx context.Context, prompt string, gathering *gatheringResult) (*RunResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	contextMsg := buildGeneratingContext(prompt, gathering.summary, gathering.fileCache, gathering.messages)
	messages := []llm.Message{{Role: llm.RoleUser, Content: contextMsg}}

	opts := buildGeneratingSendOptions()
	opts.MaxTokens = generatingMaxTokens

	slog.Info("agent generating phase",
		"file_cache_size", len(gathering.fileCache),
		"context_length", len(contextMsg),
	)

	resp, err := a.llm.SendMessage(ctx, GeneratingSystemPrompt, messages, opts)
	if err != nil {
		return nil, fmt.Errorf("generating llm call: %w", err)
	}

	// With ToolChoice "tool", we expect exactly deliver_result
	for _, tc := range resp.ToolCalls {
		if tc.Name == toolName {
			var agentResp AgentResponse
			if err := json.Unmarshal([]byte(tc.InputJSON), &agentResp); err != nil {
				return nil, fmt.Errorf("parse deliver_result: %w", err)
			}
			return &RunResult{
				Response:          &agentResp,
				TotalInputTokens:  resp.Usage.InputTokens,
				TotalOutputTokens: resp.Usage.OutputTokens,
				Iterations:        1,
			}, nil
		}
	}

	// Fallback: if text response, treat as text result
	if resp.Content != "" {
		return &RunResult{
			Response:          &AgentResponse{Type: "text", Content: resp.Content},
			TotalInputTokens:  resp.Usage.InputTokens,
			TotalOutputTokens: resp.Usage.OutputTokens,
			Iterations:        1,
		}, nil
	}

	return nil, fmt.Errorf("generating phase produced no deliver_result")
}

// buildGeneratingContext creates the user message for the generating phase.
func buildGeneratingContext(originalPrompt, gatheringSummary string, fileCache map[string]string, gatheringMessages []llm.Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Original Request\n\n%s\n\n", originalPrompt)

	// Include user clarifications from Q&A exchanges during gathering
	if qaPairs := extractQAPairs(gatheringMessages); len(qaPairs) > 0 {
		b.WriteString("## User Clarifications\n\n")
		for _, qa := range qaPairs {
			fmt.Fprintf(&b, "- **Q:** %s\n  **A:** %s\n", qa[0], qa[1])
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "## Your Analysis (from gathering phase)\n\n%s\n\n", gatheringSummary)

	if len(fileCache) > 0 {
		b.WriteString("## File Contents\n\n")
		keys := make([]string, 0, len(fileCache))
		for k := range fileCache {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(&b, "### %s\n```\n%s\n```\n\n", key, fileCache[key])
		}
	}

	b.WriteString("Now call deliver_result with your implementation.")
	return b.String()
}

// extractQAPairs scans gathering messages for ask_user_question tool calls
// and their corresponding tool_result answers. Returns [question, answer] pairs.
// Handles both in-memory types (json.RawMessage, []ToolResultBlock) and
// JSON-deserialized types ([]any, map[string]any) for save/resume round-trips.
func extractQAPairs(messages []llm.Message) [][2]string {
	pendingQuestions := make(map[string]string)
	var pairs [][2]string

	for _, msg := range messages {
		switch msg.Role {
		case llm.RoleAssistant:
			extractAskToolUses(msg.Content, pendingQuestions)
		case llm.RoleUser:
			extractToolResults(msg.Content, pendingQuestions, &pairs)
		}
	}

	return pairs
}

// extractAskToolUses finds ask_user_question tool_use blocks in an assistant message's content
// and records their IDs and question text into pending.
func extractAskToolUses(content any, pending map[string]string) {
	// Normalize content to JSON bytes regardless of the underlying Go type.
	var raw []byte
	switch v := content.(type) {
	case json.RawMessage:
		raw = v
	default:
		// Covers []any, map[string]any, etc. from JSON deserialization.
		var err error
		raw, err = json.Marshal(v)
		if err != nil {
			return
		}
	}

	var blocks []struct {
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return
	}
	for _, block := range blocks {
		if block.Type == "tool_use" && block.Name == askToolName {
			var askInput struct {
				Question string `json:"question"`
			}
			if err := json.Unmarshal(block.Input, &askInput); err == nil {
				pending[block.ID] = askInput.Question
			}
		}
	}
}

// extractToolResults finds tool_result blocks that match pending questions
// and appends [question, answer] pairs.
func extractToolResults(content any, pending map[string]string, pairs *[][2]string) {
	// Try typed slice first (in-memory path)
	if results, ok := content.([]llm.ToolResultBlock); ok {
		for _, r := range results {
			if question, found := pending[r.ToolUseID]; found {
				*pairs = append(*pairs, [2]string{question, r.Content})
				delete(pending, r.ToolUseID)
			}
		}
		return
	}

	// Fallback: re-marshal and parse as generic tool_result blocks (deserialized path)
	raw, err := json.Marshal(content)
	if err != nil {
		return
	}
	var results []struct {
		ToolUseID string `json:"tool_use_id"`
		Content   string `json:"content"`
	}
	if err := json.Unmarshal(raw, &results); err != nil {
		return
	}
	for _, r := range results {
		if question, found := pending[r.ToolUseID]; found {
			*pairs = append(*pairs, [2]string{question, r.Content})
			delete(pending, r.ToolUseID)
		}
	}
}

// fileCacheKey extracts "repo:path" from a read_repo_file tool call JSON.
func fileCacheKey(inputJSON string) string {
	var input struct {
		Repo string `json:"repo"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		return ""
	}
	if input.Repo == "" || input.Path == "" {
		return ""
	}
	return input.Repo + ":" + input.Path
}

// extractOriginalPrompt gets the user's original prompt from the first message.
func extractOriginalPrompt(messages []llm.Message) string {
	if len(messages) == 0 {
		return ""
	}
	if s, ok := messages[0].Content.(string); ok {
		return s
	}
	return ""
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
func trimConversation(messages []llm.Message, currentIteration int) []llm.Message {
	if currentIteration <= keepRecentIterations {
		return messages
	}

	oldestKept := currentIteration - keepRecentIterations
	cutoffIndex := 1 + 2*oldestKept

	trimmed := slices.Clone(messages)

	for idx := 1; idx < len(trimmed) && idx < cutoffIndex; idx++ {
		msg := trimmed[idx]
		if msg.Role != llm.RoleUser {
			continue
		}

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
			trimmed[idx] = llm.Message{Role: llm.RoleUser, Content: newResults}
		}
	}

	return trimmed
}

// trimLargeContent keeps the first trimPreviewLines lines of content as a preview.
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
