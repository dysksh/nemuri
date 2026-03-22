package checker

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"regexp"
	"strings"

	"github.com/nemuri/nemuri/eval/types"
	"github.com/nemuri/nemuri/internal/agent"
)

// CheckExpectation evaluates a single expectation against an agent response.
func CheckExpectation(exp types.Expectation, resp *agent.AgentResponse) types.CheckResult {
	switch exp.Type {
	case "response_type":
		return checkResponseType(exp, resp)
	case "file_present":
		return checkFilePresent(exp, resp)
	case "file_present_by_pattern":
		return checkFilePresentByPattern(exp, resp)
	case "file_absent":
		return checkFileAbsent(exp, resp)
	case "file_count_min":
		return checkFileCountMin(exp, resp)
	case "content_contains":
		return checkContentContains(exp, resp)
	case "content_not_contains":
		return checkContentNotContains(exp, resp)
	case "content_contains_any_file":
		return checkContentContainsAnyFile(exp, resp)
	case "content_contains_text":
		return checkContentContainsText(exp, resp)
	case "content_min_length":
		return checkContentMinLength(exp, resp)
	case "has_title":
		return checkHasTitle(resp)
	case "syntax_valid":
		return checkSyntaxValid(exp, resp)
	case "syntax_valid_all":
		return checkSyntaxValidAll(exp, resp)
	default:
		return types.CheckResult{Passed: false, Reason: "unknown expectation type: " + exp.Type}
	}
}

func checkResponseType(exp types.Expectation, resp *agent.AgentResponse) types.CheckResult {
	passed := resp.Type == exp.Expected
	result := types.CheckResult{Passed: passed, Matched: resp.Type}
	if !passed {
		result.Reason = fmt.Sprintf("expected %q, got %q", exp.Expected, resp.Type)
	}
	return result
}

func checkFilePresent(exp types.Expectation, resp *agent.AgentResponse) types.CheckResult {
	for _, f := range resp.Files {
		if f.Path == exp.Path || f.Name == exp.Path {
			return types.CheckResult{Passed: true, Matched: exp.Path}
		}
	}
	return types.CheckResult{Passed: false, Reason: fmt.Sprintf("file %q not found in output", exp.Path)}
}

func checkFilePresentByPattern(exp types.Expectation, resp *agent.AgentResponse) types.CheckResult {
	re, err := regexp.Compile(exp.Pattern)
	if err != nil {
		return types.CheckResult{Passed: false, Reason: fmt.Sprintf("invalid pattern: %v", err)}
	}
	for _, f := range resp.Files {
		name := f.Path
		if name == "" {
			name = f.Name
		}
		if re.MatchString(name) {
			return types.CheckResult{Passed: true, Matched: name}
		}
	}
	return types.CheckResult{Passed: false, Reason: fmt.Sprintf("no file matching %q", exp.Pattern)}
}

func checkFileAbsent(exp types.Expectation, resp *agent.AgentResponse) types.CheckResult {
	for _, f := range resp.Files {
		if f.Path == exp.Path || f.Name == exp.Path {
			return types.CheckResult{Passed: false, Reason: fmt.Sprintf("file %q should not be present", exp.Path)}
		}
	}
	return types.CheckResult{Passed: true}
}

func checkFileCountMin(exp types.Expectation, resp *agent.AgentResponse) types.CheckResult {
	count := len(resp.Files)
	passed := count >= exp.Min
	result := types.CheckResult{Passed: passed, Matched: fmt.Sprintf("%d files", count)}
	if !passed {
		result.Reason = fmt.Sprintf("expected at least %d files, got %d", exp.Min, count)
	}
	return result
}

func checkContentContains(exp types.Expectation, resp *agent.AgentResponse) types.CheckResult {
	content := findFileContent(resp, exp.Path)
	if content == "" {
		return types.CheckResult{Passed: false, Reason: fmt.Sprintf("file %q not found", exp.Path)}
	}
	return matchPattern(exp.Pattern, content)
}

func checkContentNotContains(exp types.Expectation, resp *agent.AgentResponse) types.CheckResult {
	content := findFileContent(resp, exp.Path)
	if content == "" {
		return types.CheckResult{Passed: true, Reason: "file not found, so pattern cannot match"}
	}
	result := matchPattern(exp.Pattern, content)
	if result.Passed {
		return types.CheckResult{Passed: false, Reason: fmt.Sprintf("pattern %q should not match, but found: %s", exp.Pattern, result.Matched)}
	}
	return types.CheckResult{Passed: true}
}

func checkContentContainsAnyFile(exp types.Expectation, resp *agent.AgentResponse) types.CheckResult {
	for _, f := range resp.Files {
		result := matchPattern(exp.Pattern, f.Content)
		if result.Passed {
			name := f.Path
			if name == "" {
				name = f.Name
			}
			return types.CheckResult{Passed: true, Matched: fmt.Sprintf("%s: %s", name, result.Matched)}
		}
	}
	return types.CheckResult{Passed: false, Reason: fmt.Sprintf("pattern %q not found in any file", exp.Pattern)}
}

func checkContentContainsText(exp types.Expectation, resp *agent.AgentResponse) types.CheckResult {
	if resp.Content == "" {
		return types.CheckResult{Passed: false, Reason: "response has no text content"}
	}
	return matchPattern(exp.Pattern, resp.Content)
}

func checkContentMinLength(exp types.Expectation, resp *agent.AgentResponse) types.CheckResult {
	length := len([]rune(resp.Content))
	passed := length >= exp.MinChars
	result := types.CheckResult{Passed: passed, Matched: fmt.Sprintf("%d chars", length)}
	if !passed {
		result.Reason = fmt.Sprintf("expected at least %d chars, got %d", exp.MinChars, length)
	}
	return result
}

func checkHasTitle(resp *agent.AgentResponse) types.CheckResult {
	passed := resp.Title != ""
	result := types.CheckResult{Passed: passed}
	if passed {
		result.Matched = resp.Title
	} else {
		result.Reason = "title is empty"
	}
	return result
}

func checkSyntaxValid(exp types.Expectation, resp *agent.AgentResponse) types.CheckResult {
	content := findFileContent(resp, exp.Path)
	if content == "" {
		return types.CheckResult{Passed: false, Reason: fmt.Sprintf("file %q not found", exp.Path)}
	}
	return validateSyntax(exp.Language, exp.Path, content)
}

func checkSyntaxValidAll(exp types.Expectation, resp *agent.AgentResponse) types.CheckResult {
	checked := 0
	for _, f := range resp.Files {
		name := f.Path
		if name == "" {
			name = f.Name
		}
		if !isLanguageFile(exp.Language, name) {
			continue
		}
		result := validateSyntax(exp.Language, name, f.Content)
		if !result.Passed && !result.Skipped {
			return result
		}
		if !result.Skipped {
			checked++
		}
	}
	if checked == 0 {
		return types.CheckResult{Skipped: true, Reason: "no matching files for language: " + exp.Language}
	}
	return types.CheckResult{Passed: true, Matched: fmt.Sprintf("%d files validated", checked)}
}

func validateSyntax(language, path, content string) types.CheckResult {
	switch language {
	case "go":
		fset := token.NewFileSet()
		_, err := parser.ParseFile(fset, path, content, parser.AllErrors)
		if err != nil {
			return types.CheckResult{Passed: false, Reason: fmt.Sprintf("Go syntax error: %v", err)}
		}
		return types.CheckResult{Passed: true}
	case "json":
		if json.Valid([]byte(content)) {
			return types.CheckResult{Passed: true}
		}
		return types.CheckResult{Passed: false, Reason: "invalid JSON"}
	default:
		return types.CheckResult{Skipped: true, Reason: "no validator for language: " + language}
	}
}

func isLanguageFile(language, filename string) bool {
	switch language {
	case "go":
		return strings.HasSuffix(filename, ".go")
	case "python":
		return strings.HasSuffix(filename, ".py")
	case "typescript":
		return strings.HasSuffix(filename, ".ts")
	case "json":
		return strings.HasSuffix(filename, ".json")
	default:
		return false
	}
}

// findFileContent looks up file content by path or name.
func findFileContent(resp *agent.AgentResponse, path string) string {
	for _, f := range resp.Files {
		if f.Path == path || f.Name == path {
			return f.Content
		}
	}
	return ""
}

// matchPattern compiles and matches a regex against content.
func matchPattern(pattern, content string) types.CheckResult {
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return types.CheckResult{Passed: false, Reason: fmt.Sprintf("invalid pattern %q: %v", pattern, err)}
	}
	match := re.FindString(content)
	if match != "" {
		// Truncate long matches
		if len(match) > 100 {
			match = match[:100] + "..."
		}
		return types.CheckResult{Passed: true, Matched: match}
	}
	return types.CheckResult{Passed: false, Reason: fmt.Sprintf("pattern %q not found", pattern)}
}
