package types

import (
	"encoding/json"

	"github.com/nemuri/nemuri/internal/agent"
)

// TestCase represents an immutable test case definition.
// Prompts and expectations must not be changed after creation (only added).
type TestCase struct {
	ID        string   `json:"id"`
	Version   int      `json:"version"`
	CreatedAt string   `json:"created_at"`
	Prompt    string   `json:"prompt"`
	Category  Category `json:"category"`
	Fixture   Fixture  `json:"fixture"`

	QuestionHandling *QuestionHandling `json:"question_handling,omitempty"`
	Expectations     []Expectation     `json:"expectations"`
	Rubric           []RubricItem      `json:"rubric"`
}

// Category classifies a test case by task type and ambiguity level.
type Category struct {
	TaskType  string `json:"task_type"` // "code", "new_repo", "file", "text"
	Ambiguity string `json:"ambiguity"` // "low", "medium", "high"
}

// Fixture defines the repository data for a test case.
type Fixture struct {
	Type     string `json:"type"`               // "repo_snapshot" or "none"
	Repo     string `json:"repo,omitempty"`     // repository name
	Snapshot string `json:"snapshot,omitempty"` // snapshot directory name
}

// QuestionHandling defines how to respond when the agent asks a question.
type QuestionHandling struct {
	Answer       string `json:"answer"`
	MaxQuestions int    `json:"max_questions"`
}

// DefaultQuestionHandling returns the default question handling config.
func DefaultQuestionHandling() QuestionHandling {
	return QuestionHandling{
		Answer:       "あなたの判断に任せます。最善と思われる方法で進めてください。",
		MaxQuestions: 2,
	}
}

// Expectation defines a deterministic check on the agent output.
type Expectation struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Expected    string `json:"expected,omitempty"`
	Path        string `json:"path,omitempty"`
	Pattern     string `json:"pattern,omitempty"`
	NamePattern string `json:"name_pattern,omitempty"`
	Min         int    `json:"min,omitempty"`
	MinChars    int    `json:"min_chars,omitempty"`
	Language    string `json:"language,omitempty"`
}

// RubricItem defines a graduated quality criterion.
type RubricItem struct {
	ID         string             `json:"id"`
	Criterion  string             `json:"criterion"`
	CheckType  string             `json:"check_type"`
	Path       string             `json:"path,omitempty"`
	Pattern    string             `json:"pattern,omitempty"`
	Glob       string             `json:"glob,omitempty"`
	Indicators *Indicators        `json:"indicators,omitempty"`
	Patterns   []LabeledPattern   `json:"patterns,omitempty"`
	Scoring    map[string]float64 `json:"scoring,omitempty"`
	Weight     int                `json:"weight"`
}

// Indicators holds strong and weak regex patterns for rubric matching.
type Indicators struct {
	Strong string `json:"strong,omitempty"`
	Weak   string `json:"weak,omitempty"`
}

// LabeledPattern associates a label with a regex pattern for distinct counting.
type LabeledPattern struct {
	Label   string `json:"label"`
	Pattern string `json:"pattern"`
}

// --- Result Types ---

// CheckResult holds the outcome of a single expectation check.
type CheckResult struct {
	Passed  bool   `json:"passed"`
	Skipped bool   `json:"skipped,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Matched string `json:"matched,omitempty"`
}

// RubricResult holds the outcome of a single rubric evaluation.
type RubricResult struct {
	Score     float64 `json:"score"`
	MatchType string  `json:"match_type"` // "strong", "weak", "none", "scored", "skipped"
	Matched   string  `json:"matched,omitempty"`
}

// TrialResult holds all data from a single trial execution.
type TrialResult struct {
	Trial         int                     `json:"trial"`
	Expectations  map[string]CheckResult  `json:"expectations"`
	Passed        bool                    `json:"passed"` // all-or-nothing
	RubricResults map[string]RubricResult `json:"rubric_results"`
	QualityScore  float64                 `json:"quality_score"`
	Metrics       TrialMetrics            `json:"metrics"`
	RawResponse   json.RawMessage         `json:"raw_response"`
}

// TrialMetrics holds measured metrics from a single trial.
type TrialMetrics struct {
	InputTokens              int   `json:"input_tokens"`
	OutputTokens             int   `json:"output_tokens"`
	CacheCreationInputTokens int   `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int   `json:"cache_read_input_tokens"`
	GatheringIterations      int   `json:"gathering_iterations"`
	ReviewRevisions          int   `json:"review_revisions"`
	ReviewPassed             bool  `json:"review_passed"`
	DurationMs               int64 `json:"duration_ms"`
	OutputFileCount          int   `json:"output_file_count"`
	QuestionsAsked           int   `json:"questions_asked"`
}

// Stats holds aggregated statistics.
type Stats struct {
	Mean   float64 `json:"mean"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	Median float64 `json:"median"`
}

// CaseResult holds all trial results and summary for a single test case.
type CaseResult struct {
	CaseVersion int           `json:"case_version"`
	Trials      []TrialResult `json:"trials"`
	Summary     CaseSummary   `json:"summary"`
}

// CaseSummary holds aggregated statistics for a test case.
type CaseSummary struct {
	PassRate             Stats              `json:"pass_rate"`
	QualityScore         Stats              `json:"quality_score"`
	InputTokens          Stats              `json:"input_tokens"`
	OutputTokens         Stats              `json:"output_tokens"`
	DurationMs           Stats              `json:"duration_ms"`
	ExpectationPassRates map[string]float64 `json:"expectation_pass_rates"`
}

// RunRecord holds the complete results of an evaluation run.
type RunRecord struct {
	SchemaVersion  int                   `json:"schema_version"`
	RunID          string                `json:"run_id"`
	StartedAt      string                `json:"started_at"`
	FinishedAt     string                `json:"finished_at"`
	Environment    Environment           `json:"environment"`
	Cases          map[string]CaseResult `json:"cases"`
	OverallSummary OverallSummary        `json:"overall_summary"`
}

// Environment records the conditions under which a run was executed.
type Environment struct {
	Commit       string             `json:"commit"`
	Model        string             `json:"model"`
	ReviewModel  string             `json:"review_model,omitempty"`
	ReviewConfig agent.ReviewConfig `json:"review_config"`
	CustomParams map[string]string  `json:"custom_params,omitempty"`
}

// OverallSummary aggregates results across all test cases.
type OverallSummary struct {
	TotalCases      int                     `json:"total_cases"`
	TotalTrials     int                     `json:"total_trials"`
	OverallPassRate float64                 `json:"overall_pass_rate"`
	OverallQuality  Stats                   `json:"overall_quality"`
	ByTaskType      map[string]GroupSummary `json:"by_task_type"`
	ByAmbiguity     map[string]GroupSummary `json:"by_ambiguity"`
}

// GroupSummary holds aggregated metrics for a group of test cases.
type GroupSummary struct {
	PassRate     float64 `json:"pass_rate"`
	QualityScore Stats   `json:"quality_score"`
	Cases        int     `json:"cases"`
}
