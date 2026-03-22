package recorder

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"

	"github.com/nemuri/nemuri/eval/types"
)

// SaveRunRecord writes a run record to a JSON file.
func SaveRunRecord(record *types.RunRecord, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create runs directory: %w", err)
	}

	filename := record.RunID + ".json"
	path := filepath.Join(dir, filename)

	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal run record: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write run record: %w", err)
	}
	return path, nil
}

// LoadRunRecord reads a run record from a JSON file.
func LoadRunRecord(path string) (*types.RunRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read run record: %w", err)
	}
	var record types.RunRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("unmarshal run record: %w", err)
	}
	return &record, nil
}

// ComputeStats calculates mean, min, max, and median for a slice of float64 values.
func ComputeStats(values []float64) types.Stats {
	if len(values) == 0 {
		return types.Stats{}
	}

	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)

	var sum float64
	for _, v := range sorted {
		sum += v
	}

	stats := types.Stats{
		Mean: sum / float64(len(sorted)),
		Min:  sorted[0],
		Max:  sorted[len(sorted)-1],
	}

	n := len(sorted)
	if n%2 == 0 {
		stats.Median = (sorted[n/2-1] + sorted[n/2]) / 2
	} else {
		stats.Median = sorted[n/2]
	}

	// Round to 4 decimal places
	stats.Mean = roundTo(stats.Mean, 4)
	stats.Min = roundTo(stats.Min, 4)
	stats.Max = roundTo(stats.Max, 4)
	stats.Median = roundTo(stats.Median, 4)

	return stats
}

// ComputeCaseSummary builds a CaseSummary from trial results and expectations.
func ComputeCaseSummary(trials []types.TrialResult, expectations []types.Expectation) types.CaseSummary {
	var passRates, qualityScores, inputTokens, outputTokens, durations []float64

	for _, t := range trials {
		if t.Passed {
			passRates = append(passRates, 1.0)
		} else {
			passRates = append(passRates, 0.0)
		}
		qualityScores = append(qualityScores, t.QualityScore)
		inputTokens = append(inputTokens, float64(t.Metrics.InputTokens))
		outputTokens = append(outputTokens, float64(t.Metrics.OutputTokens))
		durations = append(durations, float64(t.Metrics.DurationMs))
	}

	// Per-expectation pass rates
	expPassRates := make(map[string]float64, len(expectations))
	for _, exp := range expectations {
		var passed int
		for _, t := range trials {
			if r, ok := t.Expectations[exp.ID]; ok && r.Passed {
				passed++
			}
		}
		if len(trials) > 0 {
			expPassRates[exp.ID] = roundTo(float64(passed)/float64(len(trials)), 4)
		}
	}

	return types.CaseSummary{
		PassRate:             ComputeStats(passRates),
		QualityScore:         ComputeStats(qualityScores),
		InputTokens:          ComputeStats(inputTokens),
		OutputTokens:         ComputeStats(outputTokens),
		DurationMs:           ComputeStats(durations),
		ExpectationPassRates: expPassRates,
	}
}

// ComputeOverallSummary builds the overall summary from all case results and test cases.
func ComputeOverallSummary(cases map[string]types.CaseResult, testCases map[string]types.TestCase) types.OverallSummary {
	var totalTrials, totalPassed int
	var allQualityScores []float64
	byTaskType := make(map[string]*groupAccumulator)
	byAmbiguity := make(map[string]*groupAccumulator)

	for caseID, caseResult := range cases {
		tc, ok := testCases[caseID]
		if !ok {
			continue
		}

		for _, trial := range caseResult.Trials {
			totalTrials++
			if trial.Passed {
				totalPassed++
			}
			allQualityScores = append(allQualityScores, trial.QualityScore)

			// By task type
			acc := getOrCreate(byTaskType, tc.Category.TaskType)
			acc.add(trial)

			// By ambiguity
			acc2 := getOrCreate(byAmbiguity, tc.Category.Ambiguity)
			acc2.add(trial)
		}

		getOrCreate(byTaskType, tc.Category.TaskType).cases++
		getOrCreate(byAmbiguity, tc.Category.Ambiguity).cases++
	}

	summary := types.OverallSummary{
		TotalCases:      len(cases),
		TotalTrials:     totalTrials,
		OverallPassRate: roundTo(float64(totalPassed)/float64(max(totalTrials, 1)), 4),
		OverallQuality:  ComputeStats(allQualityScores),
		ByTaskType:      make(map[string]types.GroupSummary),
		ByAmbiguity:     make(map[string]types.GroupSummary),
	}

	for k, acc := range byTaskType {
		summary.ByTaskType[k] = acc.toSummary()
	}
	for k, acc := range byAmbiguity {
		summary.ByAmbiguity[k] = acc.toSummary()
	}

	return summary
}

type groupAccumulator struct {
	passed int
	total  int
	scores []float64
	cases  int
}

func getOrCreate(m map[string]*groupAccumulator, key string) *groupAccumulator {
	if acc, ok := m[key]; ok {
		return acc
	}
	acc := &groupAccumulator{}
	m[key] = acc
	return acc
}

func (a *groupAccumulator) add(trial types.TrialResult) {
	a.total++
	if trial.Passed {
		a.passed++
	}
	a.scores = append(a.scores, trial.QualityScore)
}

func (a *groupAccumulator) toSummary() types.GroupSummary {
	return types.GroupSummary{
		PassRate:     roundTo(float64(a.passed)/float64(max(a.total, 1)), 4),
		QualityScore: ComputeStats(a.scores),
		Cases:        a.cases,
	}
}

func roundTo(val float64, places int) float64 {
	pow := math.Pow(10, float64(places))
	return math.Round(val*pow) / pow
}
