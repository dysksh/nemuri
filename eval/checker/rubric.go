package checker

import (
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"github.com/nemuri/nemuri/eval/types"
	"github.com/nemuri/nemuri/internal/agent"
)

// ScoreRubric evaluates all rubric items and returns individual results and a weighted quality score.
func ScoreRubric(rubric []types.RubricItem, resp *agent.AgentResponse) (map[string]types.RubricResult, float64) {
	results := make(map[string]types.RubricResult, len(rubric))
	var totalWeightedScore float64
	var totalWeight int

	for _, item := range rubric {
		result := scoreRubricItem(item, resp)
		results[item.ID] = result

		if result.MatchType != "skipped" {
			totalWeightedScore += result.Score * float64(item.Weight)
			totalWeight += item.Weight
		}
	}

	var qualityScore float64
	if totalWeight > 0 {
		qualityScore = totalWeightedScore / float64(totalWeight)
	}
	return results, qualityScore
}

func scoreRubricItem(item types.RubricItem, resp *agent.AgentResponse) types.RubricResult {
	switch item.CheckType {
	case "content_match":
		return scoreContentMatch(item, resp)
	case "content_contains_any_file":
		return scoreContentContainsAnyFile(item, resp)
	case "content_count":
		return scoreContentCount(item, resp)
	case "content_count_distinct", "content_count_distinct_any_file":
		return scoreContentCountDistinct(item, resp)
	case "content_not_contains":
		return scoreContentNotContains(item, resp)
	case "text_match":
		return scoreTextMatch(item, resp)
	case "text_count_distinct":
		return scoreTextCountDistinct(item, resp)
	case "text_heading_count":
		return scoreTextHeadingCount(item, resp)
	case "file_present_by_pattern":
		return scoreFilePresentByPattern(item, resp)
	case "file_count_min":
		return scoreFileCountMin(item, resp)
	default:
		return types.RubricResult{Score: 0, MatchType: "skipped", Matched: "unknown check_type: " + item.CheckType}
	}
}

func scoreContentMatch(item types.RubricItem, resp *agent.AgentResponse) types.RubricResult {
	content := findFileContent(resp, item.Path)
	if content == "" {
		// If no path specified, search all files
		if item.Path == "" {
			for _, f := range resp.Files {
				content += f.Content + "\n"
			}
		}
		if content == "" {
			return types.RubricResult{Score: 0, MatchType: "none", Matched: "file not found"}
		}
	}
	return matchIndicators(item.Indicators, content)
}

func scoreContentContainsAnyFile(item types.RubricItem, resp *agent.AgentResponse) types.RubricResult {
	var allContent strings.Builder
	for _, f := range resp.Files {
		name := f.Path
		if name == "" {
			name = f.Name
		}
		// Apply glob filter if specified
		if item.Glob != "" && !matchGlob(item.Glob, name) {
			continue
		}
		allContent.WriteString(f.Content)
		allContent.WriteByte('\n')
	}
	return matchIndicators(item.Indicators, allContent.String())
}

func scoreContentCount(item types.RubricItem, resp *agent.AgentResponse) types.RubricResult {
	var content string
	if item.Path != "" {
		content = findFileContent(resp, item.Path)
	} else {
		var b strings.Builder
		for _, f := range resp.Files {
			b.WriteString(f.Content)
			b.WriteByte('\n')
		}
		content = b.String()
	}
	if content == "" {
		return types.RubricResult{Score: 0, MatchType: "none"}
	}

	re, err := regexp.Compile("(?im)" + item.Pattern)
	if err != nil {
		return types.RubricResult{Score: 0, MatchType: "none", Matched: "invalid pattern"}
	}
	count := len(re.FindAllString(content, -1))
	return scoreByScoringMap(item.Scoring, count)
}

func scoreContentCountDistinct(item types.RubricItem, resp *agent.AgentResponse) types.RubricResult {
	var content string
	if item.CheckType == "content_count_distinct_any_file" || item.Path == "" {
		var b strings.Builder
		for _, f := range resp.Files {
			b.WriteString(f.Content)
			b.WriteByte('\n')
		}
		content = b.String()
	} else {
		content = findFileContent(resp, item.Path)
	}
	if content == "" {
		// For text type responses, also check Content field
		content = resp.Content
	}
	if content == "" {
		return types.RubricResult{Score: 0, MatchType: "none"}
	}

	matched := 0
	var matchedLabels []string
	for _, lp := range item.Patterns {
		re, err := regexp.Compile("(?im)" + lp.Pattern)
		if err != nil {
			continue
		}
		if re.MatchString(content) {
			matched++
			matchedLabels = append(matchedLabels, lp.Label)
		}
	}
	result := scoreByScoringMap(item.Scoring, matched)
	if len(matchedLabels) > 0 {
		result.Matched = strings.Join(matchedLabels, ", ")
	}
	return result
}

func scoreContentNotContains(item types.RubricItem, resp *agent.AgentResponse) types.RubricResult {
	content := findFileContent(resp, item.Path)
	if content == "" {
		return types.RubricResult{Score: 1.0, MatchType: "strong"}
	}
	if item.Indicators == nil || item.Indicators.Strong == "" {
		return types.RubricResult{Score: 0, MatchType: "none"}
	}
	re, err := regexp.Compile("(?im)" + item.Indicators.Strong)
	if err != nil {
		return types.RubricResult{Score: 0, MatchType: "none"}
	}
	if re.MatchString(content) {
		match := re.FindString(content)
		return types.RubricResult{Score: 0, MatchType: "none", Matched: "unwanted: " + truncate(match, 80)}
	}
	return types.RubricResult{Score: 1.0, MatchType: "strong"}
}

func scoreTextMatch(item types.RubricItem, resp *agent.AgentResponse) types.RubricResult {
	if resp.Content == "" {
		return types.RubricResult{Score: 0, MatchType: "none", Matched: "no text content"}
	}
	return matchIndicators(item.Indicators, resp.Content)
}

func scoreTextCountDistinct(item types.RubricItem, resp *agent.AgentResponse) types.RubricResult {
	if resp.Content == "" {
		return types.RubricResult{Score: 0, MatchType: "none"}
	}
	matched := 0
	var matchedLabels []string
	for _, lp := range item.Patterns {
		re, err := regexp.Compile("(?im)" + lp.Pattern)
		if err != nil {
			continue
		}
		if re.MatchString(resp.Content) {
			matched++
			matchedLabels = append(matchedLabels, lp.Label)
		}
	}
	result := scoreByScoringMap(item.Scoring, matched)
	if len(matchedLabels) > 0 {
		result.Matched = strings.Join(matchedLabels, ", ")
	}
	return result
}

func scoreTextHeadingCount(item types.RubricItem, resp *agent.AgentResponse) types.RubricResult {
	content := resp.Content
	// Also check file content for file-type responses
	if content == "" {
		for _, f := range resp.Files {
			content += f.Content + "\n"
		}
	}
	if content == "" {
		return types.RubricResult{Score: 0, MatchType: "none"}
	}

	re, err := regexp.Compile("(?m)" + item.Pattern)
	if err != nil {
		return types.RubricResult{Score: 0, MatchType: "none", Matched: "invalid pattern"}
	}
	count := len(re.FindAllString(content, -1))
	return scoreByScoringMap(item.Scoring, count)
}

func scoreFilePresentByPattern(item types.RubricItem, resp *agent.AgentResponse) types.RubricResult {
	if item.Indicators == nil {
		return types.RubricResult{Score: 0, MatchType: "none"}
	}
	// Check strong pattern first
	if item.Indicators.Strong != "" {
		re, err := regexp.Compile(item.Indicators.Strong)
		if err == nil {
			for _, f := range resp.Files {
				name := f.Path
				if name == "" {
					name = f.Name
				}
				if re.MatchString(name) {
					return types.RubricResult{Score: 1.0, MatchType: "strong", Matched: name}
				}
			}
		}
	}
	// Check weak pattern
	if item.Indicators.Weak != "" {
		re, err := regexp.Compile(item.Indicators.Weak)
		if err == nil {
			for _, f := range resp.Files {
				name := f.Path
				if name == "" {
					name = f.Name
				}
				if re.MatchString(name) {
					return types.RubricResult{Score: 0.5, MatchType: "weak", Matched: name}
				}
			}
		}
	}
	return types.RubricResult{Score: 0, MatchType: "none"}
}

func scoreFileCountMin(item types.RubricItem, resp *agent.AgentResponse) types.RubricResult {
	count := len(resp.Files)
	if item.Scoring != nil {
		return scoreByScoringMap(item.Scoring, count)
	}
	// Binary: pass if >= min from weight context (use indicators convention)
	if count >= item.Weight {
		return types.RubricResult{Score: 1.0, MatchType: "strong", Matched: fmt.Sprintf("%d files", count)}
	}
	return types.RubricResult{Score: 0, MatchType: "none", Matched: fmt.Sprintf("%d files", count)}
}

// matchIndicators checks strong then weak patterns against content.
func matchIndicators(indicators *types.Indicators, content string) types.RubricResult {
	if indicators == nil {
		return types.RubricResult{Score: 0, MatchType: "none"}
	}

	// Try strong match first
	if indicators.Strong != "" {
		re, err := regexp.Compile("(?im)" + indicators.Strong)
		if err != nil {
			slog.Warn("invalid strong indicator pattern", "pattern", indicators.Strong, "error", err)
		} else {
			match := re.FindString(content)
			if match != "" {
				return types.RubricResult{Score: 1.0, MatchType: "strong", Matched: truncate(match, 100)}
			}
		}
	}

	// Try weak match
	if indicators.Weak != "" {
		re, err := regexp.Compile("(?im)" + indicators.Weak)
		if err != nil {
			slog.Warn("invalid weak indicator pattern", "pattern", indicators.Weak, "error", err)
		} else {
			match := re.FindString(content)
			if match != "" {
				return types.RubricResult{Score: 0.5, MatchType: "weak", Matched: truncate(match, 100)}
			}
		}
	}

	return types.RubricResult{Score: 0, MatchType: "none"}
}

// scoreByScoringMap maps a count to a score using the scoring map.
// Supports keys like "0", "1", "2", "3", "4", "4+", "5+".
func scoreByScoringMap(scoring map[string]float64, count int) types.RubricResult {
	if scoring == nil {
		return types.RubricResult{Score: 0, MatchType: "none"}
	}

	// Try exact match first
	if score, ok := scoring[strconv.Itoa(count)]; ok {
		return types.RubricResult{Score: score, MatchType: "scored", Matched: fmt.Sprintf("count=%d", count)}
	}

	// Try "N+" patterns (e.g., "4+", "5+") from highest to lowest
	bestScore := -1.0
	for key, score := range scoring {
		if strings.HasSuffix(key, "+") {
			threshold, err := strconv.Atoi(strings.TrimSuffix(key, "+"))
			if err == nil && count >= threshold && score > bestScore {
				bestScore = score
			}
		}
	}
	if bestScore >= 0 {
		return types.RubricResult{Score: bestScore, MatchType: "scored", Matched: fmt.Sprintf("count=%d", count)}
	}

	return types.RubricResult{Score: 0, MatchType: "scored", Matched: fmt.Sprintf("count=%d (no matching score)", count)}
}

func matchGlob(pattern, name string) bool {
	// Simple glob: only supports *.ext
	if strings.HasPrefix(pattern, "*") {
		return strings.HasSuffix(name, pattern[1:])
	}
	return strings.Contains(name, pattern)
}

func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}
