package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/nemuri/nemuri/internal/llm"
)

// ReviewConfig controls the review loop behavior.
type ReviewConfig struct {
	MaxRevisions         int     // maximum number of review-rewrite iterations
	PassThreshold        float64 // minimum average score to pass review
	MinImprovementRounds int     // number of rounds to check for stall
	MinImprovement       float64 // minimum score improvement to not be considered stalled
	MaxSameIssueCount    int     // stop if same issue is flagged this many times
}

// DefaultReviewConfig returns the default review configuration.
func DefaultReviewConfig() ReviewConfig {
	return ReviewConfig{
		MaxRevisions:         3,
		PassThreshold:        7.0,
		MinImprovementRounds: 2,
		MinImprovement:       0.1,
		MaxSameIssueCount:    3,
	}
}

// ReviewScores holds quality scores for a review.
type ReviewScores struct {
	Correctness     float64 `json:"correctness"`
	Security        float64 `json:"security"`
	Maintainability float64 `json:"maintainability"`
	Completeness    float64 `json:"completeness"`
}

// Average returns the mean of all scores.
func (s ReviewScores) Average() float64 {
	scores := []float64{s.Correctness, s.Security, s.Maintainability, s.Completeness}
	var sum float64
	for _, v := range scores {
		sum += v
	}
	return sum / float64(len(scores))
}

// ReviewIssue describes a single issue found during review.
type ReviewIssue struct {
	File     string `json:"file"`
	Line     string `json:"line,omitempty"`
	Severity string `json:"severity"` // "critical", "major", "minor"
	Category string `json:"category"` // "correctness", "security", "maintainability", "completeness"
	Message  string `json:"message"`
}

// ReviewResult is the structured output from a review.
type ReviewResult struct {
	Scores  ReviewScores  `json:"scores"`
	Issues  []ReviewIssue `json:"issues"`
	Summary string        `json:"summary"`
}

// ReviewLoopResult tracks the full review loop execution.
type ReviewLoopResult struct {
	Response          *AgentResponse // final (possibly rewritten) response
	Reviews           []ReviewResult // all review results
	Revisions         int            // number of rewrites performed
	Passed            bool           // true if final review passed threshold
	TotalInputTokens  int            // cumulative input tokens used by review/rewrite
	TotalOutputTokens int            // cumulative output tokens used by review/rewrite
}

// Review evaluates the given agent response using the LLM.
func (a *Agent) Review(ctx context.Context, prompt string, resp *AgentResponse) (*ReviewResult, int, int, error) {
	reviewInput := buildReviewInput(prompt, resp)
	messages := []llm.Message{{Role: llm.RoleUser, Content: reviewInput}}
	opts := buildReviewSendOptions()

	llmResp, err := a.llm.SendMessage(ctx, reviewPrompt, messages, opts)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("review LLM call: %w", err)
	}

	for _, tc := range llmResp.ToolCalls {
		if tc.Name == reviewToolName {
			var result ReviewResult
			if err := json.Unmarshal([]byte(tc.InputJSON), &result); err != nil {
				return nil, llmResp.Usage.InputTokens, llmResp.Usage.OutputTokens,
					fmt.Errorf("parse review result: %w", err)
			}
			return &result, llmResp.Usage.InputTokens, llmResp.Usage.OutputTokens, nil
		}
	}

	return nil, llmResp.Usage.InputTokens, llmResp.Usage.OutputTokens,
		fmt.Errorf("review did not produce submit_review tool call")
}

// Rewrite fixes flagged issues in the agent response using the LLM.
func (a *Agent) Rewrite(ctx context.Context, prompt string, resp *AgentResponse, review *ReviewResult) (*AgentResponse, int, int, error) {
	rewriteInput := buildRewriteInput(prompt, resp, review)
	messages := []llm.Message{{Role: llm.RoleUser, Content: rewriteInput}}
	opts := buildRewriteSendOptions()

	llmResp, err := a.llm.SendMessage(ctx, rewritePrompt, messages, opts)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("rewrite LLM call: %w", err)
	}

	for _, tc := range llmResp.ToolCalls {
		if tc.Name == toolName {
			var rewritten AgentResponse
			if err := json.Unmarshal([]byte(tc.InputJSON), &rewritten); err != nil {
				return nil, llmResp.Usage.InputTokens, llmResp.Usage.OutputTokens,
					fmt.Errorf("parse rewrite result: %w", err)
			}
			return &rewritten, llmResp.Usage.InputTokens, llmResp.Usage.OutputTokens, nil
		}
	}

	return nil, llmResp.Usage.InputTokens, llmResp.Usage.OutputTokens,
		fmt.Errorf("rewrite did not produce deliver_result tool call")
}

// RunWithReview runs the agent and then applies the review loop for code-producing responses.
// Only code and new_repo responses are reviewed; text and file responses are returned as-is.
func (a *Agent) RunWithReview(ctx context.Context, prompt string, cfg ReviewConfig) (*RunResult, *ReviewLoopResult, error) {
	runResult, err := a.Run(ctx, prompt)
	if err != nil {
		return nil, nil, err
	}

	// If the agent asked a question, return immediately (no review needed)
	if runResult.Question != "" {
		return runResult, nil, nil
	}

	// Only review code and new_repo responses
	switch runResult.Response.Type {
	case ResponseTypeCode, ResponseTypeNewRepo:
		// proceed to review
	default:
		return runResult, nil, nil
	}

	loopResult, err := a.ReviewLoop(ctx, prompt, runResult.Response, cfg)
	if err != nil {
		return runResult, nil, err
	}

	// Update response and accumulate token usage
	runResult.Response = loopResult.Response
	runResult.TotalInputTokens += loopResult.TotalInputTokens
	runResult.TotalOutputTokens += loopResult.TotalOutputTokens

	return runResult, loopResult, nil
}

// ReviewLoop runs the review-rewrite cycle until convergence or limits are hit.
func (a *Agent) ReviewLoop(ctx context.Context, prompt string, resp *AgentResponse, cfg ReviewConfig) (*ReviewLoopResult, error) {
	result := &ReviewLoopResult{
		Response: resp,
	}

	var scoreHistory []float64

	for revision := range cfg.MaxRevisions {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		slog.Info("review iteration", "revision", revision+1, "max", cfg.MaxRevisions)

		// Review
		review, inputTok, outputTok, err := a.Review(ctx, prompt, result.Response)
		result.TotalInputTokens += inputTok
		result.TotalOutputTokens += outputTok
		if err != nil {
			slog.Warn("review failed, stopping review loop", "revision", revision+1, "error", err)
			return result, nil // return current best effort
		}

		result.Reviews = append(result.Reviews, *review)
		avgScore := review.Scores.Average()
		scoreHistory = append(scoreHistory, avgScore)

		slog.Info("review result",
			"revision", revision+1,
			"avg_score", avgScore,
			"threshold", cfg.PassThreshold,
			"issues", len(review.Issues),
		)

		// Check 1: Pass threshold
		if avgScore >= cfg.PassThreshold {
			slog.Info("review passed threshold", "avg_score", avgScore, "threshold", cfg.PassThreshold)
			result.Passed = true
			return result, nil
		}

		// Check 2: Score stall detection
		if len(scoreHistory) >= cfg.MinImprovementRounds {
			recent := scoreHistory[len(scoreHistory)-cfg.MinImprovementRounds:]
			improvement := recent[len(recent)-1] - recent[0]
			if improvement < cfg.MinImprovement {
				slog.Info("review score stalled", "improvement", improvement, "min_required", cfg.MinImprovement)
				return result, nil
			}
		}

		// Check 3: Repeated issue detection
		// NOTE: Uses exact message match. LLM may rephrase the same issue differently,
		// which would bypass this check. This is a known limitation.
		if hasRepeatedIssues(result.Reviews, cfg.MaxSameIssueCount) {
			slog.Info("repeated issues detected, stopping review loop")
			return result, nil
		}

		// Check 4: Only minor issues remain — skip rewrite
		// (only applies when there are actual issues; empty issues with low score
		// means the LLM gave a low score without actionable feedback, so we continue)
		if len(review.Issues) > 0 && hasOnlyMinorIssues(review.Issues) {
			slog.Info("only minor issues remain, accepting output")
			result.Passed = true
			return result, nil
		}

		// Filter to non-minor issues for rewrite
		significantIssues := filterSignificantIssues(review.Issues)
		if len(significantIssues) == 0 {
			// No actionable issues to fix (low score but no specific feedback).
			// Rewriting won't help, so continue to next review iteration.
			slog.Info("no actionable issues to rewrite, skipping rewrite")
			continue
		}

		filteredReview := &ReviewResult{
			Scores:  review.Scores,
			Issues:  significantIssues,
			Summary: review.Summary,
		}

		rewritten, inputTok, outputTok, err := a.Rewrite(ctx, prompt, result.Response, filteredReview)
		result.TotalInputTokens += inputTok
		result.TotalOutputTokens += outputTok
		if err != nil {
			slog.Warn("rewrite failed, stopping review loop", "revision", revision+1, "error", err)
			return result, nil // return current best effort
		}

		result.Response = rewritten
		result.Revisions++
	}

	slog.Warn("review loop reached max revisions", "max", cfg.MaxRevisions)
	return result, nil
}

// hasRepeatedIssues checks if any issue message has been flagged maxCount or more times.
// NOTE: Uses exact message string match; rephrased duplicates are not detected.
func hasRepeatedIssues(reviews []ReviewResult, maxCount int) bool {
	counts := make(map[string]int)
	for _, r := range reviews {
		for _, issue := range r.Issues {
			counts[issue.Message]++
			if counts[issue.Message] >= maxCount {
				return true
			}
		}
	}
	return false
}

// hasOnlyMinorIssues returns true if all issues are "minor" severity.
// Returns false for empty slices (no issues is not the same as only minor issues).
func hasOnlyMinorIssues(issues []ReviewIssue) bool {
	for _, issue := range issues {
		if issue.Severity != "minor" {
			return false
		}
	}
	return true
}

// filterSignificantIssues returns only non-minor issues.
func filterSignificantIssues(issues []ReviewIssue) []ReviewIssue {
	var significant []ReviewIssue
	for _, issue := range issues {
		if issue.Severity != "minor" {
			significant = append(significant, issue)
		}
	}
	return significant
}
