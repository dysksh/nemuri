package checker

import (
	"testing"

	"github.com/nemuri/nemuri/eval/types"
	"github.com/nemuri/nemuri/internal/agent"
)

func TestCheckResponseType(t *testing.T) {
	resp := &agent.AgentResponse{Type: "code"}

	tests := []struct {
		name     string
		expected string
		want     bool
	}{
		{"match", "code", true},
		{"mismatch", "text", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exp := types.Expectation{ID: "e1", Type: "response_type", Expected: tt.expected}
			got := CheckExpectation(exp, resp)
			if got.Passed != tt.want {
				t.Errorf("Passed = %v, want %v (reason: %s)", got.Passed, tt.want, got.Reason)
			}
		})
	}
}

func TestCheckFilePresent(t *testing.T) {
	resp := &agent.AgentResponse{
		Files: []agent.OutputFile{
			{Path: "cmd/main.go", Content: "package main"},
			{Name: "report.pdf"},
		},
	}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"found by path", "cmd/main.go", true},
		{"found by name", "report.pdf", true},
		{"not found", "missing.go", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exp := types.Expectation{ID: "e1", Type: "file_present", Path: tt.path}
			got := CheckExpectation(exp, resp)
			if got.Passed != tt.want {
				t.Errorf("Passed = %v, want %v (reason: %s)", got.Passed, tt.want, got.Reason)
			}
		})
	}
}

func TestCheckFilePresentByPattern(t *testing.T) {
	resp := &agent.AgentResponse{
		Files: []agent.OutputFile{
			{Path: "internal/handler/user.go"},
			{Path: "internal/handler/order.go"},
		},
	}

	tests := []struct {
		name    string
		pattern string
		want    bool
	}{
		{"match", `handler/.*\.go`, true},
		{"no match", `controller/.*\.go`, false},
		{"invalid regex", `[invalid`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exp := types.Expectation{ID: "e1", Type: "file_present_by_pattern", Pattern: tt.pattern}
			got := CheckExpectation(exp, resp)
			if got.Passed != tt.want {
				t.Errorf("Passed = %v, want %v (reason: %s)", got.Passed, tt.want, got.Reason)
			}
		})
	}
}

func TestCheckFileAbsent(t *testing.T) {
	resp := &agent.AgentResponse{
		Files: []agent.OutputFile{
			{Path: "main.go"},
		},
	}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"absent", "other.go", true},
		{"present", "main.go", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exp := types.Expectation{ID: "e1", Type: "file_absent", Path: tt.path}
			got := CheckExpectation(exp, resp)
			if got.Passed != tt.want {
				t.Errorf("Passed = %v, want %v (reason: %s)", got.Passed, tt.want, got.Reason)
			}
		})
	}
}

func TestCheckFileCountMin(t *testing.T) {
	resp := &agent.AgentResponse{
		Files: []agent.OutputFile{
			{Path: "a.go"}, {Path: "b.go"}, {Path: "c.go"},
		},
	}

	tests := []struct {
		name string
		min  int
		want bool
	}{
		{"enough", 3, true},
		{"more than enough", 1, true},
		{"not enough", 5, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exp := types.Expectation{ID: "e1", Type: "file_count_min", Min: tt.min}
			got := CheckExpectation(exp, resp)
			if got.Passed != tt.want {
				t.Errorf("Passed = %v, want %v (reason: %s)", got.Passed, tt.want, got.Reason)
			}
		})
	}
}

func TestCheckContentContains(t *testing.T) {
	resp := &agent.AgentResponse{
		Files: []agent.OutputFile{
			{Path: "main.go", Content: "package main\n\nfunc main() {}"},
		},
	}

	tests := []struct {
		name    string
		path    string
		pattern string
		want    bool
	}{
		{"match", "main.go", `func main`, true},
		{"case insensitive", "main.go", `PACKAGE MAIN`, true},
		{"no match", "main.go", `import`, false},
		{"file not found", "other.go", `func`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exp := types.Expectation{ID: "e1", Type: "content_contains", Path: tt.path, Pattern: tt.pattern}
			got := CheckExpectation(exp, resp)
			if got.Passed != tt.want {
				t.Errorf("Passed = %v, want %v (reason: %s)", got.Passed, tt.want, got.Reason)
			}
		})
	}
}

func TestCheckContentNotContains(t *testing.T) {
	resp := &agent.AgentResponse{
		Files: []agent.OutputFile{
			{Path: "main.go", Content: "package main\n\nfunc main() {}"},
		},
	}

	tests := []struct {
		name    string
		path    string
		pattern string
		want    bool
	}{
		{"not present", "main.go", `import`, true},
		{"present", "main.go", `func main`, false},
		{"file missing", "other.go", `func`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exp := types.Expectation{ID: "e1", Type: "content_not_contains", Path: tt.path, Pattern: tt.pattern}
			got := CheckExpectation(exp, resp)
			if got.Passed != tt.want {
				t.Errorf("Passed = %v, want %v (reason: %s)", got.Passed, tt.want, got.Reason)
			}
		})
	}
}

func TestCheckContentContainsAnyFile(t *testing.T) {
	resp := &agent.AgentResponse{
		Files: []agent.OutputFile{
			{Path: "a.go", Content: "package a"},
			{Path: "b.go", Content: "package b\nimport \"fmt\""},
		},
	}

	tests := []struct {
		name    string
		pattern string
		want    bool
	}{
		{"found in second file", `import`, true},
		{"not found anywhere", `os\.Exit`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exp := types.Expectation{ID: "e1", Type: "content_contains_any_file", Pattern: tt.pattern}
			got := CheckExpectation(exp, resp)
			if got.Passed != tt.want {
				t.Errorf("Passed = %v, want %v (reason: %s)", got.Passed, tt.want, got.Reason)
			}
		})
	}
}

func TestCheckContentContainsText(t *testing.T) {
	resp := &agent.AgentResponse{Content: "This is the answer to the question."}

	tests := []struct {
		name    string
		pattern string
		want    bool
	}{
		{"match", `answer`, true},
		{"no match", `error`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exp := types.Expectation{ID: "e1", Type: "content_contains_text", Pattern: tt.pattern}
			got := CheckExpectation(exp, resp)
			if got.Passed != tt.want {
				t.Errorf("Passed = %v, want %v (reason: %s)", got.Passed, tt.want, got.Reason)
			}
		})
	}

	t.Run("empty content", func(t *testing.T) {
		exp := types.Expectation{ID: "e1", Type: "content_contains_text", Pattern: `answer`}
		got := CheckExpectation(exp, &agent.AgentResponse{})
		if got.Passed {
			t.Error("should fail with empty content")
		}
	})
}

func TestCheckContentMinLength(t *testing.T) {
	resp := &agent.AgentResponse{Content: "こんにちは世界"} // 7 runes

	tests := []struct {
		name     string
		minChars int
		want     bool
	}{
		{"enough", 5, true},
		{"exact", 7, true},
		{"not enough", 10, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exp := types.Expectation{ID: "e1", Type: "content_min_length", MinChars: tt.minChars}
			got := CheckExpectation(exp, resp)
			if got.Passed != tt.want {
				t.Errorf("Passed = %v, want %v (reason: %s)", got.Passed, tt.want, got.Reason)
			}
		})
	}
}

func TestCheckHasTitle(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  bool
	}{
		{"has title", "My Title", true},
		{"empty title", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &agent.AgentResponse{Title: tt.title}
			exp := types.Expectation{ID: "e1", Type: "has_title"}
			got := CheckExpectation(exp, resp)
			if got.Passed != tt.want {
				t.Errorf("Passed = %v, want %v (reason: %s)", got.Passed, tt.want, got.Reason)
			}
		})
	}
}

func TestCheckSyntaxValid(t *testing.T) {
	tests := []struct {
		name    string
		lang    string
		path    string
		content string
		want    bool
	}{
		{"valid go", "go", "main.go", "package main\n\nfunc main() {}\n", true},
		{"invalid go", "go", "main.go", "package main\n\nfunc {}\n", false},
		{"valid json", "json", "data.json", `{"key": "value"}`, true},
		{"invalid json", "json", "data.json", `{"key": }`, false},
		{"unknown language", "ruby", "app.rb", "puts 'hello'", false}, // skipped but not passed
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &agent.AgentResponse{
				Files: []agent.OutputFile{{Path: tt.path, Content: tt.content}},
			}
			exp := types.Expectation{ID: "e1", Type: "syntax_valid", Path: tt.path, Language: tt.lang}
			got := CheckExpectation(exp, resp)
			if got.Passed != tt.want {
				t.Errorf("Passed = %v, want %v (reason: %s, skipped: %v)", got.Passed, tt.want, got.Reason, got.Skipped)
			}
		})
	}
}

func TestCheckSyntaxValidAll(t *testing.T) {
	t.Run("all valid", func(t *testing.T) {
		resp := &agent.AgentResponse{
			Files: []agent.OutputFile{
				{Path: "a.go", Content: "package a\n"},
				{Path: "b.go", Content: "package b\n"},
				{Path: "readme.md", Content: "# Hi"},
			},
		}
		exp := types.Expectation{ID: "e1", Type: "syntax_valid_all", Language: "go"}
		got := CheckExpectation(exp, resp)
		if !got.Passed {
			t.Errorf("should pass: %s", got.Reason)
		}
	})

	t.Run("one invalid", func(t *testing.T) {
		resp := &agent.AgentResponse{
			Files: []agent.OutputFile{
				{Path: "a.go", Content: "package a\n"},
				{Path: "b.go", Content: "package b\nfunc {}\n"},
			},
		}
		exp := types.Expectation{ID: "e1", Type: "syntax_valid_all", Language: "go"}
		got := CheckExpectation(exp, resp)
		if got.Passed {
			t.Error("should fail with invalid Go file")
		}
	})

	t.Run("no matching files", func(t *testing.T) {
		resp := &agent.AgentResponse{
			Files: []agent.OutputFile{
				{Path: "readme.md", Content: "# Hi"},
			},
		}
		exp := types.Expectation{ID: "e1", Type: "syntax_valid_all", Language: "go"}
		got := CheckExpectation(exp, resp)
		if got.Passed {
			t.Error("should not pass when no matching files")
		}
		if !got.Skipped {
			t.Error("should be skipped")
		}
	})
}

func TestCheckUnknownType(t *testing.T) {
	exp := types.Expectation{ID: "e1", Type: "nonexistent"}
	got := CheckExpectation(exp, &agent.AgentResponse{})
	if got.Passed {
		t.Error("unknown type should not pass")
	}
}
