package checker

import (
	"math"
	"testing"

	"github.com/nemuri/nemuri/eval/types"
	"github.com/nemuri/nemuri/internal/agent"
)

func TestScoreRubric_WeightedAverage(t *testing.T) {
	resp := &agent.AgentResponse{
		Content: "This has strong and weak content",
		Files: []agent.OutputFile{
			{Path: "main.go", Content: "package main\n\nfunc main() { strong_signal() }"},
		},
	}

	rubric := []types.RubricItem{
		{
			ID:        "r1",
			CheckType: "content_match",
			Path:      "main.go",
			Weight:    3,
			Indicators: &types.Indicators{
				Strong: `strong_signal`,
				Weak:   `weak_signal`,
			},
		},
		{
			ID:        "r2",
			CheckType: "text_match",
			Weight:    1,
			Indicators: &types.Indicators{
				Strong: `not_present`,
				Weak:   `weak content`,
			},
		},
	}

	results, score := ScoreRubric(rubric, resp)

	if results["r1"].Score != 1.0 {
		t.Errorf("r1 score = %v, want 1.0", results["r1"].Score)
	}
	if results["r2"].Score != 0.5 {
		t.Errorf("r2 score = %v, want 0.5", results["r2"].Score)
	}
	// Weighted: (1.0*3 + 0.5*1) / (3+1) = 3.5/4 = 0.875
	if math.Abs(score-0.875) > 0.001 {
		t.Errorf("quality_score = %v, want 0.875", score)
	}
}

func TestScoreRubric_SkippedItemExcluded(t *testing.T) {
	resp := &agent.AgentResponse{
		Files: []agent.OutputFile{
			{Path: "main.go", Content: "package main\n\nfunc main() { good() }"},
		},
	}

	rubric := []types.RubricItem{
		{
			ID:         "r1",
			CheckType:  "content_match",
			Path:       "main.go",
			Weight:     2,
			Indicators: &types.Indicators{Strong: `good`},
		},
		{
			ID:        "r2",
			CheckType: "unknown_type",
			Weight:    2,
		},
	}

	results, score := ScoreRubric(rubric, resp)

	if results["r2"].MatchType != "skipped" {
		t.Errorf("r2 should be skipped, got %s", results["r2"].MatchType)
	}
	// Only r1 counts: 1.0*2 / 2 = 1.0
	if math.Abs(score-1.0) > 0.001 {
		t.Errorf("quality_score = %v, want 1.0", score)
	}
}

func TestMatchIndicators(t *testing.T) {
	tests := []struct {
		name       string
		indicators *types.Indicators
		content    string
		wantScore  float64
		wantType   string
	}{
		{"strong match", &types.Indicators{Strong: `func main`, Weak: `func`}, "func main() {}", 1.0, "strong"},
		{"weak only", &types.Indicators{Strong: `class`, Weak: `func`}, "func helper() {}", 0.5, "weak"},
		{"no match", &types.Indicators{Strong: `class`, Weak: `object`}, "func main() {}", 0, "none"},
		{"nil indicators", nil, "content", 0, "none"},
		{"case insensitive", &types.Indicators{Strong: `FUNC MAIN`}, "func main() {}", 1.0, "strong"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchIndicators(tt.indicators, tt.content)
			if got.Score != tt.wantScore {
				t.Errorf("Score = %v, want %v", got.Score, tt.wantScore)
			}
			if got.MatchType != tt.wantType {
				t.Errorf("MatchType = %q, want %q", got.MatchType, tt.wantType)
			}
		})
	}
}

func TestScoreByScoringMap(t *testing.T) {
	tests := []struct {
		name      string
		scoring   map[string]float64
		count     int
		wantScore float64
	}{
		{"exact match", map[string]float64{"0": 0, "1": 0.5, "2": 1.0}, 1, 0.5},
		{"plus pattern", map[string]float64{"0": 0, "3+": 1.0}, 5, 1.0},
		{"plus threshold not met", map[string]float64{"0": 0, "3+": 1.0}, 2, 0},
		{"nil scoring", nil, 5, 0},
		{"no matching key", map[string]float64{"1": 0.5, "2": 1.0}, 3, 0},
		{"plus higher wins", map[string]float64{"2+": 0.5, "4+": 1.0}, 5, 1.0},
		{"plus exact threshold", map[string]float64{"3+": 1.0}, 3, 1.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scoreByScoringMap(tt.scoring, tt.count)
			if got.Score != tt.wantScore {
				t.Errorf("Score = %v, want %v (matched: %s)", got.Score, tt.wantScore, got.Matched)
			}
		})
	}
}

func TestScoreContentNotContains(t *testing.T) {
	resp := &agent.AgentResponse{
		Files: []agent.OutputFile{
			{Path: "main.go", Content: "package main\n\npanic(\"oh no\")"},
		},
	}

	t.Run("unwanted present", func(t *testing.T) {
		item := types.RubricItem{
			ID:         "r1",
			CheckType:  "content_not_contains",
			Path:       "main.go",
			Weight:     1,
			Indicators: &types.Indicators{Strong: `panic`},
		}
		result := scoreRubricItem(item, resp)
		if result.Score != 0 {
			t.Errorf("Score = %v, want 0", result.Score)
		}
	})

	t.Run("unwanted absent", func(t *testing.T) {
		item := types.RubricItem{
			ID:         "r1",
			CheckType:  "content_not_contains",
			Path:       "main.go",
			Weight:     1,
			Indicators: &types.Indicators{Strong: `os\.Exit`},
		}
		result := scoreRubricItem(item, resp)
		if result.Score != 1.0 {
			t.Errorf("Score = %v, want 1.0", result.Score)
		}
	})

	t.Run("file not found", func(t *testing.T) {
		item := types.RubricItem{
			ID:         "r1",
			CheckType:  "content_not_contains",
			Path:       "other.go",
			Weight:     1,
			Indicators: &types.Indicators{Strong: `panic`},
		}
		result := scoreRubricItem(item, resp)
		if result.Score != 1.0 {
			t.Errorf("Score = %v, want 1.0 (file not found means no unwanted content)", result.Score)
		}
	})
}

func TestScoreContentContainsAnyFile(t *testing.T) {
	resp := &agent.AgentResponse{
		Files: []agent.OutputFile{
			{Path: "handler.go", Content: "func HandleRequest(w http.ResponseWriter, r *http.Request) {}"},
			{Path: "README.md", Content: "# Project"},
		},
	}

	t.Run("strong match in filtered files", func(t *testing.T) {
		item := types.RubricItem{
			ID:         "r1",
			CheckType:  "content_contains_any_file",
			Glob:       "*.go",
			Weight:     1,
			Indicators: &types.Indicators{Strong: `HandleRequest`},
		}
		result := scoreRubricItem(item, resp)
		if result.Score != 1.0 {
			t.Errorf("Score = %v, want 1.0", result.Score)
		}
	})

	t.Run("no match with glob filter", func(t *testing.T) {
		item := types.RubricItem{
			ID:         "r1",
			CheckType:  "content_contains_any_file",
			Glob:       "*.md",
			Weight:     1,
			Indicators: &types.Indicators{Strong: `HandleRequest`},
		}
		result := scoreRubricItem(item, resp)
		if result.Score != 0 {
			t.Errorf("Score = %v, want 0", result.Score)
		}
	})
}

func TestScoreContentCountDistinct(t *testing.T) {
	resp := &agent.AgentResponse{
		Files: []agent.OutputFile{
			{Path: "main.go", Content: "package main\nimport (\n\t\"fmt\"\n\t\"os\"\n\t\"strings\"\n)\n"},
		},
	}

	item := types.RubricItem{
		ID:        "r1",
		CheckType: "content_count_distinct",
		Weight:    1,
		Patterns: []types.LabeledPattern{
			{Label: "fmt", Pattern: `"fmt"`},
			{Label: "os", Pattern: `"os"`},
			{Label: "io", Pattern: `"io"`},
		},
		Scoring: map[string]float64{"0": 0, "1": 0.3, "2": 0.7, "3+": 1.0},
	}

	result := scoreRubricItem(item, resp)
	// 2 out of 3 patterns match (fmt, os)
	if result.Score != 0.7 {
		t.Errorf("Score = %v, want 0.7 (matched: %s)", result.Score, result.Matched)
	}
}

func TestScoreFilePresentByPattern(t *testing.T) {
	resp := &agent.AgentResponse{
		Files: []agent.OutputFile{
			{Path: "cmd/server/main.go"},
			{Path: "internal/handler.go"},
		},
	}

	tests := []struct {
		name      string
		strong    string
		weak      string
		wantScore float64
	}{
		{"strong match", `cmd/server/main\.go`, `main\.go`, 1.0},
		{"weak only", `cmd/client/main\.go`, `main\.go`, 0.5},
		{"no match", `app\.py`, `server\.py`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := types.RubricItem{
				ID:         "r1",
				CheckType:  "file_present_by_pattern",
				Weight:     1,
				Indicators: &types.Indicators{Strong: tt.strong, Weak: tt.weak},
			}
			result := scoreRubricItem(item, resp)
			if result.Score != tt.wantScore {
				t.Errorf("Score = %v, want %v", result.Score, tt.wantScore)
			}
		})
	}
}

func TestScoreFileCountMin(t *testing.T) {
	resp := &agent.AgentResponse{
		Files: []agent.OutputFile{{Path: "a.go"}, {Path: "b.go"}, {Path: "c.go"}},
	}

	t.Run("with scoring map", func(t *testing.T) {
		item := types.RubricItem{
			ID:        "r1",
			CheckType: "file_count_min",
			Weight:    1,
			Scoring:   map[string]float64{"0": 0, "1": 0.3, "3+": 1.0},
		}
		result := scoreRubricItem(item, resp)
		if result.Score != 1.0 {
			t.Errorf("Score = %v, want 1.0", result.Score)
		}
	})

	t.Run("binary with weight threshold", func(t *testing.T) {
		item := types.RubricItem{
			ID:        "r1",
			CheckType: "file_count_min",
			Weight:    5,
		}
		result := scoreRubricItem(item, resp)
		if result.Score != 0 {
			t.Errorf("Score = %v, want 0 (3 files < weight 5)", result.Score)
		}
	})
}

func TestScoreTextHeadingCount(t *testing.T) {
	resp := &agent.AgentResponse{
		Content: "# Title\n\nSome content\n\n## Section 1\n\nText\n\n## Section 2\n\nMore text",
	}

	item := types.RubricItem{
		ID:        "r1",
		CheckType: "text_heading_count",
		Pattern:   `^#{1,3}\s+`,
		Weight:    1,
		Scoring:   map[string]float64{"0": 0, "1": 0.3, "2": 0.6, "3+": 1.0},
	}

	result := scoreRubricItem(item, resp)
	if result.Score != 1.0 {
		t.Errorf("Score = %v, want 1.0 (3 headings)", result.Score)
	}
}

func TestMatchGlob(t *testing.T) {
	tests := []struct {
		pattern string
		name    string
		want    bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "main.py", false},
		{"handler", "internal/handler.go", true},
		{"handler", "main.go", false},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.name, func(t *testing.T) {
			if got := matchGlob(tt.pattern, tt.name); got != tt.want {
				t.Errorf("matchGlob(%q, %q) = %v, want %v", tt.pattern, tt.name, got, tt.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"short", "hello", 10, "hello"},
		{"exact", "hello", 5, "hello"},
		{"truncated", "hello world", 5, "hello..."},
		{"multibyte", "こんにちは世界", 3, "こんに..."},
		{"empty", "", 5, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}
