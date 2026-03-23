package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/nemuri/nemuri/eval/checker"
	"github.com/nemuri/nemuri/eval/fixture"
	"github.com/nemuri/nemuri/eval/recorder"
	"github.com/nemuri/nemuri/eval/types"
	"github.com/nemuri/nemuri/internal/agent"
	"github.com/nemuri/nemuri/internal/github"
	"github.com/nemuri/nemuri/internal/llm"
)

// Config holds runner configuration.
type Config struct {
	Trials       int
	ReviewConfig agent.ReviewConfig
	FixtureDir   string // base directory for fixture snapshots
	APIKey       string
	Model        string // Claude model to use (empty = default)
	ReviewModel  string // Claude model for review/rewrite
}

// Runner executes test cases against the agent.
type Runner struct {
	config Config
}

// New creates a new Runner.
func New(cfg Config) *Runner {
	return &Runner{config: cfg}
}

// RunAll executes all test cases and returns results keyed by case ID.
func (r *Runner) RunAll(ctx context.Context, testCases []types.TestCase) (map[string]types.CaseResult, error) {
	results := make(map[string]types.CaseResult, len(testCases))

	for i, tc := range testCases {
		slog.Info("running test case",
			"case", tc.ID,
			"progress", fmt.Sprintf("%d/%d", i+1, len(testCases)),
			"type", tc.Category.TaskType,
			"ambiguity", tc.Category.Ambiguity,
		)

		caseResult, err := r.RunCase(ctx, tc)
		if err != nil {
			slog.Error("test case failed", "case", tc.ID, "error", err)
			return results, fmt.Errorf("case %s: %w", tc.ID, err)
		}
		results[tc.ID] = *caseResult
	}

	return results, nil
}

// RunCase executes a single test case for the configured number of trials.
func (r *Runner) RunCase(ctx context.Context, tc types.TestCase) (*types.CaseResult, error) {
	trials := make([]types.TrialResult, 0, r.config.Trials)

	for trial := 1; trial <= r.config.Trials; trial++ {
		slog.Info("running trial", "case", tc.ID, "trial", trial, "of", r.config.Trials)

		result, err := r.runTrial(ctx, tc, trial)
		if err != nil {
			return nil, fmt.Errorf("trial %d: %w", trial, err)
		}
		trials = append(trials, *result)
	}

	summary := recorder.ComputeCaseSummary(trials, tc.Expectations)

	return &types.CaseResult{
		CaseVersion: tc.Version,
		Trials:      trials,
		Summary:     summary,
	}, nil
}

func (r *Runner) runTrial(ctx context.Context, tc types.TestCase, trialNum int) (*types.TrialResult, error) {
	// Build GitHub mock from fixture
	githubClient := r.buildGitHubClient(tc.Fixture)

	// Build LLM client
	llmClient := r.buildLLMClient()

	// Create agent
	ag := agent.New(llmClient, llm.NewClaudeClient(r.config.APIKey, r.config.ReviewModel), githubClient, "eval-owner")

	// Execute with timing
	start := time.Now()
	runResult, reviewResult, questionsAsked, err := r.executeWithQuestionHandling(ctx, ag, tc)
	duration := time.Since(start)

	if err != nil {
		return nil, err
	}

	// Build raw response JSON
	rawResp, _ := json.Marshal(runResult.Response)

	// Evaluate expectations
	expectations := make(map[string]types.CheckResult, len(tc.Expectations))
	allPassed := true
	for _, exp := range tc.Expectations {
		result := checker.CheckExpectation(exp, runResult.Response)
		expectations[exp.ID] = result
		if !result.Passed && !result.Skipped {
			allPassed = false
		}
	}

	// Evaluate rubric
	rubricResults, qualityScore := checker.ScoreRubric(tc.Rubric, runResult.Response)

	// Build metrics
	metrics := types.TrialMetrics{
		InputTokens:         runResult.TotalInputTokens,
		OutputTokens:        runResult.TotalOutputTokens,
		GatheringIterations: runResult.Iterations,
		DurationMs:          duration.Milliseconds(),
		OutputFileCount:     len(runResult.Response.Files),
		QuestionsAsked:      questionsAsked,
	}
	if reviewResult != nil {
		metrics.ReviewRevisions = reviewResult.Revisions
		metrics.ReviewPassed = reviewResult.Passed
	}

	return &types.TrialResult{
		Trial:         trialNum,
		Expectations:  expectations,
		Passed:        allPassed,
		RubricResults: rubricResults,
		QualityScore:  qualityScore,
		Metrics:       metrics,
		RawResponse:   rawResp,
	}, nil
}

// executeWithQuestionHandling runs the agent, handling ask_user_question by providing fixed answers.
func (r *Runner) executeWithQuestionHandling(
	ctx context.Context,
	ag *agent.Agent,
	tc types.TestCase,
) (*agent.RunResult, *agent.ReviewLoopResult, int, error) {
	qh := types.DefaultQuestionHandling()
	if tc.QuestionHandling != nil {
		qh = *tc.QuestionHandling
	}

	// First run
	runResult, reviewResult, err := ag.RunWithReview(ctx, tc.Prompt, r.config.ReviewConfig)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("agent run: %w", err)
	}

	questionsAsked := 0

	// Handle questions
	for runResult.Question != "" && questionsAsked < qh.MaxQuestions {
		questionsAsked++
		slog.Info("agent asked question, auto-responding",
			"question", runResult.Question,
			"questions_asked", questionsAsked,
		)

		// Inject the answer as a tool_result for the pending ask_user_question
		messages := runResult.Messages
		if runResult.PendingToolCallID != "" {
			// Replace the placeholder tool_result with the actual answer
			messages = replaceToolResult(messages, runResult.PendingToolCallID, qh.Answer)
		}

		// Resume the agent
		resumeResult, err := ag.Resume(ctx, messages, runResult.Phase, runResult.FileCache)
		if err != nil {
			return nil, nil, questionsAsked, fmt.Errorf("agent resume after question %d: %w", questionsAsked, err)
		}

		// Apply review loop if needed
		if resumeResult.Question == "" {
			switch resumeResult.Response.Type {
			case agent.ResponseTypeCode, agent.ResponseTypeNewRepo:
				loopResult, loopErr := ag.ReviewLoop(ctx, tc.Prompt, resumeResult.Response, r.config.ReviewConfig)
				if loopErr != nil {
					return nil, nil, questionsAsked, fmt.Errorf("review loop after resume: %w", loopErr)
				}
				resumeResult.Response = loopResult.Response
				resumeResult.TotalInputTokens += loopResult.TotalInputTokens
				resumeResult.TotalOutputTokens += loopResult.TotalOutputTokens
				reviewResult = loopResult
			}
		}

		runResult = resumeResult
	}

	if runResult.Question != "" {
		// Still asking questions after max — treat as completed with whatever we have
		slog.Warn("agent exceeded max questions, using last state",
			"max_questions", qh.MaxQuestions,
		)
		// Create a minimal response
		if runResult.Response == nil {
			runResult.Response = &agent.AgentResponse{
				Type:    "text",
				Content: "Agent exceeded maximum question limit without producing output.",
			}
		}
	}

	return runResult, reviewResult, questionsAsked, nil
}

// replaceToolResult replaces the content of a tool_result with the given answer.
func replaceToolResult(messages []llm.Message, toolCallID, answer string) []llm.Message {
	result := make([]llm.Message, len(messages))
	copy(result, messages)

	for i := len(result) - 1; i >= 0; i-- {
		msg := result[i]
		if msg.Role != llm.RoleUser {
			continue
		}
		results, ok := msg.Content.([]llm.ToolResultBlock)
		if !ok {
			continue
		}
		for j, r := range results {
			if r.ToolUseID == toolCallID {
				newResults := make([]llm.ToolResultBlock, len(results))
				copy(newResults, results)
				newResults[j] = llm.ToolResultBlock{
					Type:      "tool_result",
					ToolUseID: toolCallID,
					Content:   answer,
				}
				result[i] = llm.Message{Role: llm.RoleUser, Content: newResults}
				return result
			}
		}
	}
	return result
}

func (r *Runner) buildGitHubClient(fix types.Fixture) github.API {
	if fix.Type == "none" || fix.Snapshot == "" {
		return nil
	}
	snapshotDir := filepath.Join(r.config.FixtureDir, fix.Snapshot)
	if _, err := os.Stat(snapshotDir); err != nil {
		slog.Warn("fixture snapshot not found, running without GitHub mock",
			"snapshot", fix.Snapshot,
			"dir", snapshotDir,
		)
		return nil
	}
	return fixture.NewMockFromDirectory(snapshotDir)
}

func (r *Runner) buildLLMClient() llm.Client {
	return llm.NewClaudeClient(r.config.APIKey, r.config.Model)
}

// LoadTestCases loads all test case JSON files from a directory.
func LoadTestCases(dir string) ([]types.TestCase, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read test cases directory: %w", err)
	}

	var cases []types.TestCase
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		var tc types.TestCase
		if err := json.Unmarshal(data, &tc); err != nil {
			return nil, fmt.Errorf("parse %s: %w", entry.Name(), err)
		}
		cases = append(cases, tc)
	}

	return cases, nil
}

// FilterTestCases filters test cases by ID. If caseID is empty, returns all.
func FilterTestCases(cases []types.TestCase, caseID string) []types.TestCase {
	if caseID == "" {
		return cases
	}
	for _, tc := range cases {
		if tc.ID == caseID {
			return []types.TestCase{tc}
		}
	}
	return nil
}
