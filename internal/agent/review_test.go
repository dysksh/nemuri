package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/nemuri/nemuri/internal/agent"
	"github.com/nemuri/nemuri/internal/llm"
)

func makeReviewResponse(scores agent.ReviewScores, issues []agent.ReviewIssue, summary string) *llm.Response {
	result := agent.ReviewResult{
		Scores:  scores,
		Issues:  issues,
		Summary: summary,
	}
	input, _ := json.Marshal(result)
	rawContent, _ := json.Marshal([]map[string]any{
		{
			"type":  "tool_use",
			"id":    "review-1",
			"name":  "submit_review",
			"input": json.RawMessage(input),
		},
	})
	return &llm.Response{
		ToolCalls: []llm.ToolCall{
			{ID: "review-1", Name: "submit_review", InputJSON: string(input)},
		},
		RawContent: rawContent,
		Usage:      llm.Usage{InputTokens: 200, OutputTokens: 100},
	}
}

func makeRewriteResponse(resp agent.AgentResponse) *llm.Response {
	input, _ := json.Marshal(resp)
	rawContent, _ := json.Marshal([]map[string]any{
		{
			"type":  "tool_use",
			"id":    "rewrite-1",
			"name":  "deliver_result",
			"input": json.RawMessage(input),
		},
	})
	return &llm.Response{
		ToolCalls: []llm.ToolCall{
			{ID: "rewrite-1", Name: "deliver_result", InputJSON: string(input)},
		},
		RawContent: rawContent,
		Usage:      llm.Usage{InputTokens: 300, OutputTokens: 200},
	}
}

func TestReviewScores_Average(t *testing.T) {
	scores := agent.ReviewScores{
		Correctness:     8.0,
		Security:        6.0,
		Maintainability: 7.0,
		Completeness:    9.0,
	}
	avg := scores.Average()
	if avg != 7.5 {
		t.Errorf("expected 7.5, got %f", avg)
	}
}

func TestAgent_Review(t *testing.T) {
	mock := &mockLLMClient{}
	a := agent.New(mock, nil, "")

	mock.responses = []*llm.Response{
		makeReviewResponse(
			agent.ReviewScores{Correctness: 8, Security: 7, Maintainability: 8, Completeness: 7},
			[]agent.ReviewIssue{{File: "main.go", Severity: "minor", Category: "maintainability", Message: "unused import"}},
			"Good overall",
		),
	}

	resp := &agent.AgentResponse{
		Type:  "code",
		Repo:  "test-repo",
		Title: "test",
		Files: []agent.OutputFile{{Path: "main.go", Content: "package main"}},
	}

	result, inputTok, outputTok, err := a.Review(context.Background(), "test prompt", resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Scores.Correctness != 8 {
		t.Errorf("expected correctness=8, got %f", result.Scores.Correctness)
	}
	if len(result.Issues) != 1 {
		t.Errorf("expected 1 issue, got %d", len(result.Issues))
	}
	if inputTok != 200 || outputTok != 100 {
		t.Errorf("unexpected token counts: %d, %d", inputTok, outputTok)
	}
}

func TestAgent_Rewrite(t *testing.T) {
	mock := &mockLLMClient{}
	a := agent.New(mock, nil, "")

	rewrittenResp := agent.AgentResponse{
		Type:  "code",
		Repo:  "test-repo",
		Title: "test",
		Files: []agent.OutputFile{{Path: "main.go", Content: "package main\n\nfunc main() {}"}},
	}
	mock.responses = []*llm.Response{makeRewriteResponse(rewrittenResp)}

	resp := &agent.AgentResponse{
		Type:  "code",
		Repo:  "test-repo",
		Title: "test",
		Files: []agent.OutputFile{{Path: "main.go", Content: "package main"}},
	}
	review := &agent.ReviewResult{
		Scores:  agent.ReviewScores{Correctness: 5, Security: 7, Maintainability: 6, Completeness: 5},
		Issues:  []agent.ReviewIssue{{File: "main.go", Severity: "major", Category: "completeness", Message: "missing main func"}},
		Summary: "Needs work",
	}

	result, _, _, err := a.Rewrite(context.Background(), "test prompt", resp, review)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Type != "code" {
		t.Errorf("expected type=code, got %s", result.Type)
	}
	if len(result.Files) != 1 {
		t.Errorf("expected 1 file, got %d", len(result.Files))
	}
}

func TestReviewLoop_PassOnFirstReview(t *testing.T) {
	mock := &mockLLMClient{}
	a := agent.New(mock, nil, "")

	// Agent run response
	agentResp := agent.AgentResponse{
		Type:  "code",
		Repo:  "test-repo",
		Title: "test",
		Files: []agent.OutputFile{{Path: "main.go", Content: "package main\n\nfunc main() {}"}},
	}
	agentInput, _ := json.Marshal(agentResp)
	agentRaw, _ := json.Marshal([]map[string]any{
		{"type": "tool_use", "id": "tc-1", "name": "deliver_result", "input": json.RawMessage(agentInput)},
	})

	mock.responses = []*llm.Response{
		// Gathering: text summary
		textResponse("Plan: create main.go with main func."),
		// Generating: deliver_result
		{
			ToolCalls:  []llm.ToolCall{{ID: "tc-1", Name: "deliver_result", InputJSON: string(agentInput)}},
			RawContent: agentRaw,
			Usage:      llm.Usage{InputTokens: 100, OutputTokens: 50},
		},
		// Review: high scores
		makeReviewResponse(
			agent.ReviewScores{Correctness: 9, Security: 8, Maintainability: 8, Completeness: 9},
			nil,
			"Excellent",
		),
	}

	cfg := agent.DefaultReviewConfig()
	runResult, reviewResult, err := a.RunWithReview(context.Background(), "test prompt", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runResult == nil {
		t.Fatal("expected run result")
	}
	if reviewResult == nil {
		t.Fatal("expected review result")
	}
	if !reviewResult.Passed {
		t.Error("expected review to pass")
	}
	if reviewResult.Revisions != 0 {
		t.Errorf("expected 0 revisions, got %d", reviewResult.Revisions)
	}
	// Token accumulation: gathering(100+50) + generating(100+50) + review(200+100) = (400, 200)
	if runResult.TotalInputTokens != 400 {
		t.Errorf("expected total input tokens=400, got %d", runResult.TotalInputTokens)
	}
	if runResult.TotalOutputTokens != 200 {
		t.Errorf("expected total output tokens=200, got %d", runResult.TotalOutputTokens)
	}
}

func TestReviewLoop_RewriteThenPass(t *testing.T) {
	mock := &mockLLMClient{}
	a := agent.New(mock, nil, "")

	resp := &agent.AgentResponse{
		Type:  "code",
		Repo:  "test-repo",
		Title: "test",
		Files: []agent.OutputFile{{Path: "main.go", Content: "package main"}},
	}

	rewrittenResp := agent.AgentResponse{
		Type:  "code",
		Repo:  "test-repo",
		Title: "test",
		Files: []agent.OutputFile{{Path: "main.go", Content: "package main\n\nfunc main() {}"}},
	}

	mock.responses = []*llm.Response{
		// Review 1: low score
		makeReviewResponse(
			agent.ReviewScores{Correctness: 5, Security: 7, Maintainability: 6, Completeness: 4},
			[]agent.ReviewIssue{{File: "main.go", Severity: "major", Category: "completeness", Message: "missing main"}},
			"Incomplete",
		),
		// Rewrite
		makeRewriteResponse(rewrittenResp),
		// Review 2: high score
		makeReviewResponse(
			agent.ReviewScores{Correctness: 9, Security: 8, Maintainability: 8, Completeness: 9},
			nil,
			"Excellent",
		),
	}

	cfg := agent.DefaultReviewConfig()
	result, err := a.ReviewLoop(context.Background(), "test prompt", resp, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Error("expected review to pass")
	}
	if result.Revisions != 1 {
		t.Errorf("expected 1 revision, got %d", result.Revisions)
	}
	if len(result.Reviews) != 2 {
		t.Errorf("expected 2 reviews, got %d", len(result.Reviews))
	}
}

func TestReviewLoop_MaxRevisions(t *testing.T) {
	mock := &mockLLMClient{}
	a := agent.New(mock, nil, "")

	resp := &agent.AgentResponse{
		Type:  "code",
		Repo:  "test-repo",
		Title: "test",
		Files: []agent.OutputFile{{Path: "main.go", Content: "package main"}},
	}

	cfg := agent.ReviewConfig{
		MaxRevisions:         2,
		PassThreshold:        9.0,
		MinImprovementRounds: 10, // high to avoid stall detection
		MinImprovement:       0.1,
		MaxSameIssueCount:    10, // high to avoid repeated issue detection
	}

	// Each iteration: review (low score) + rewrite
	for range cfg.MaxRevisions {
		mock.responses = append(mock.responses,
			makeReviewResponse(
				agent.ReviewScores{Correctness: 5 + float64(len(mock.responses)), Security: 6, Maintainability: 6, Completeness: 5},
				[]agent.ReviewIssue{{File: "main.go", Severity: "major", Category: "correctness", Message: fmt.Sprintf("issue-%d", len(mock.responses))}},
				"Needs work",
			),
			makeRewriteResponse(agent.AgentResponse{
				Type:  "code",
				Repo:  "test-repo",
				Title: "test",
				Files: []agent.OutputFile{{Path: "main.go", Content: "updated"}},
			}),
		)
	}

	result, err := a.ReviewLoop(context.Background(), "test prompt", resp, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Error("expected review to not pass")
	}
	if result.Revisions != 2 {
		t.Errorf("expected 2 revisions, got %d", result.Revisions)
	}
}

func TestReviewLoop_ScoreStall(t *testing.T) {
	mock := &mockLLMClient{}
	a := agent.New(mock, nil, "")

	resp := &agent.AgentResponse{
		Type:  "code",
		Repo:  "test-repo",
		Title: "test",
		Files: []agent.OutputFile{{Path: "main.go", Content: "package main"}},
	}

	cfg := agent.ReviewConfig{
		MaxRevisions:         10,
		PassThreshold:        9.0,
		MinImprovementRounds: 3,
		MinImprovement:       0.5,
		MaxSameIssueCount:    10,
	}

	// 3 rounds with nearly identical scores → stall
	scores := []float64{6.0, 6.05, 6.08}
	for i, s := range scores {
		mock.responses = append(mock.responses,
			makeReviewResponse(
				agent.ReviewScores{Correctness: s, Security: s, Maintainability: s, Completeness: s},
				[]agent.ReviewIssue{{File: "main.go", Severity: "major", Category: "correctness", Message: fmt.Sprintf("issue-%d", i)}},
				"Still needs work",
			),
		)
		if i < len(scores)-1 {
			mock.responses = append(mock.responses,
				makeRewriteResponse(agent.AgentResponse{
					Type:  "code",
					Repo:  "test-repo",
					Title: "test",
					Files: []agent.OutputFile{{Path: "main.go", Content: fmt.Sprintf("v%d", i+1)}},
				}),
			)
		}
	}

	result, err := a.ReviewLoop(context.Background(), "test prompt", resp, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Error("expected review to not pass (stalled)")
	}
	if result.Revisions != 2 {
		t.Errorf("expected 2 revisions, got %d", result.Revisions)
	}
}

func TestReviewLoop_RepeatedIssues(t *testing.T) {
	mock := &mockLLMClient{}
	a := agent.New(mock, nil, "")

	resp := &agent.AgentResponse{
		Type:  "code",
		Repo:  "test-repo",
		Title: "test",
		Files: []agent.OutputFile{{Path: "main.go", Content: "package main"}},
	}

	cfg := agent.ReviewConfig{
		MaxRevisions:         10,
		PassThreshold:        9.0,
		MinImprovementRounds: 10,
		MinImprovement:       0.1,
		MaxSameIssueCount:    3,
	}

	// Same issue flagged 3 times
	for i := range 3 {
		mock.responses = append(mock.responses,
			makeReviewResponse(
				agent.ReviewScores{Correctness: 5 + float64(i), Security: 7, Maintainability: 6, Completeness: 5},
				[]agent.ReviewIssue{{File: "main.go", Severity: "major", Category: "correctness", Message: "same issue every time"}},
				"Still has the same problem",
			),
		)
		if i < 2 {
			mock.responses = append(mock.responses,
				makeRewriteResponse(agent.AgentResponse{
					Type:  "code",
					Repo:  "test-repo",
					Title: "test",
					Files: []agent.OutputFile{{Path: "main.go", Content: fmt.Sprintf("attempt-%d", i+1)}},
				}),
			)
		}
	}

	result, err := a.ReviewLoop(context.Background(), "test prompt", resp, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Error("expected review to not pass (repeated issues)")
	}
}

func TestReviewLoop_MinorIssuesOnly(t *testing.T) {
	mock := &mockLLMClient{}
	a := agent.New(mock, nil, "")

	resp := &agent.AgentResponse{
		Type:  "code",
		Repo:  "test-repo",
		Title: "test",
		Files: []agent.OutputFile{{Path: "main.go", Content: "package main\n\nfunc main() {}"}},
	}

	mock.responses = []*llm.Response{
		makeReviewResponse(
			agent.ReviewScores{Correctness: 6, Security: 7, Maintainability: 6, Completeness: 6},
			[]agent.ReviewIssue{
				{File: "main.go", Severity: "minor", Category: "maintainability", Message: "could add comments"},
				{File: "main.go", Severity: "minor", Category: "maintainability", Message: "unused import"},
			},
			"Minor issues only",
		),
	}

	cfg := agent.DefaultReviewConfig()
	result, err := a.ReviewLoop(context.Background(), "test prompt", resp, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Error("expected review to pass (minor issues only)")
	}
	if result.Revisions != 0 {
		t.Errorf("expected 0 revisions, got %d", result.Revisions)
	}
}

func TestRunWithReview_SkipsTextResponse(t *testing.T) {
	mock := &mockLLMClient{}
	a := agent.New(mock, nil, "")

	mock.responses = []*llm.Response{
		// Gathering: text summary
		textResponse("No code needed, just a text answer."),
		// Generating: deliver_result with type=text
		deliverResultResponse("text", "Here is your answer"),
	}

	cfg := agent.DefaultReviewConfig()
	runResult, reviewResult, err := a.RunWithReview(context.Background(), "what is Go?", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runResult == nil {
		t.Fatal("expected run result")
	}
	if runResult.Response.Type != "text" {
		t.Errorf("expected type=text, got %s", runResult.Response.Type)
	}
	if reviewResult != nil {
		t.Error("expected nil review result for text response")
	}
}

func TestRunWithReview_CodeResponse(t *testing.T) {
	mock := &mockLLMClient{}
	a := agent.New(mock, nil, "")

	agentResp := agent.AgentResponse{
		Type:  "code",
		Repo:  "test-repo",
		Title: "test feature",
		Files: []agent.OutputFile{{Path: "main.go", Content: "package main\n\nfunc main() {}"}},
	}
	agentInput, _ := json.Marshal(agentResp)
	agentRaw, _ := json.Marshal([]map[string]any{
		{"type": "tool_use", "id": "tc-1", "name": "deliver_result", "input": json.RawMessage(agentInput)},
	})

	mock.responses = []*llm.Response{
		// Gathering: text summary
		textResponse("Plan: create main.go."),
		// Generating: deliver_result
		{
			ToolCalls:  []llm.ToolCall{{ID: "tc-1", Name: "deliver_result", InputJSON: string(agentInput)}},
			RawContent: agentRaw,
			Usage:      llm.Usage{InputTokens: 100, OutputTokens: 50},
		},
		// Review: passes
		makeReviewResponse(
			agent.ReviewScores{Correctness: 9, Security: 8, Maintainability: 8, Completeness: 9},
			nil,
			"Excellent",
		),
	}

	cfg := agent.DefaultReviewConfig()
	runResult, reviewResult, err := a.RunWithReview(context.Background(), "create a main.go", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runResult == nil {
		t.Fatal("expected run result")
	}
	if reviewResult == nil {
		t.Fatal("expected review result")
	}
	if !reviewResult.Passed {
		t.Error("expected review to pass")
	}
}

func TestRunWithReview_SkipsFileResponse(t *testing.T) {
	mock := &mockLLMClient{}
	a := agent.New(mock, nil, "")

	agentResp := agent.AgentResponse{
		Type:  "file",
		Files: []agent.OutputFile{{Name: "report.md", Content: "# Report"}},
	}
	agentInput, _ := json.Marshal(agentResp)
	agentRaw, _ := json.Marshal([]map[string]any{
		{"type": "tool_use", "id": "tc-1", "name": "deliver_result", "input": json.RawMessage(agentInput)},
	})

	mock.responses = []*llm.Response{
		// Gathering: text summary
		textResponse("Plan: write a report file."),
		// Generating: deliver_result
		{
			ToolCalls:  []llm.ToolCall{{ID: "tc-1", Name: "deliver_result", InputJSON: string(agentInput)}},
			RawContent: agentRaw,
			Usage:      llm.Usage{InputTokens: 100, OutputTokens: 50},
		},
	}

	cfg := agent.DefaultReviewConfig()
	runResult, reviewResult, err := a.RunWithReview(context.Background(), "write a report", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runResult.Response.Type != "file" {
		t.Errorf("expected type=file, got %s", runResult.Response.Type)
	}
	if reviewResult != nil {
		t.Error("expected nil review result for file response")
	}
}

func TestReviewLoop_EmptyIssuesLowScore(t *testing.T) {
	mock := &mockLLMClient{}
	a := agent.New(mock, nil, "")

	resp := &agent.AgentResponse{
		Type:  "code",
		Repo:  "test-repo",
		Title: "test",
		Files: []agent.OutputFile{{Path: "main.go", Content: "package main"}},
	}

	// LLM gives low score but no issues — should NOT pass, should continue to next iteration
	mock.responses = []*llm.Response{
		// Review 1: low score, no issues
		makeReviewResponse(
			agent.ReviewScores{Correctness: 3, Security: 3, Maintainability: 3, Completeness: 3},
			nil,
			"Low quality but no specific issues",
		),
		// Review 2: still low, no issues (will hit max revisions since no rewrite possible)
		makeReviewResponse(
			agent.ReviewScores{Correctness: 3, Security: 3, Maintainability: 3, Completeness: 3},
			nil,
			"Still low",
		),
	}

	cfg := agent.ReviewConfig{
		MaxRevisions:         2,
		PassThreshold:        7.0,
		MinImprovementRounds: 10,
		MinImprovement:       0.1,
		MaxSameIssueCount:    10,
	}

	result, err := a.ReviewLoop(context.Background(), "test prompt", resp, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Error("expected review to NOT pass (low score with empty issues)")
	}
}
