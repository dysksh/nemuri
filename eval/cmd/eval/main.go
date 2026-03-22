package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/nemuri/nemuri/eval/checker"
	"github.com/nemuri/nemuri/eval/recorder"
	"github.com/nemuri/nemuri/eval/runner"
	"github.com/nemuri/nemuri/eval/types"
	"github.com/nemuri/nemuri/internal/agent"
)

const (
	defaultTrials      = 5
	defaultTestCaseDir = "eval/testcases"
	defaultFixtureDir  = "eval/fixtures/snapshots"
	defaultRunsDir     = "eval/runs"
	schemaVersion      = 1
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "run":
		if err := runCmd(os.Args[2:]); err != nil {
			slog.Error("run failed", "error", err)
			os.Exit(1)
		}
	case "compare":
		if err := compareCmd(os.Args[2:]); err != nil {
			slog.Error("compare failed", "error", err)
			os.Exit(1)
		}
	case "recheck":
		if err := recheckCmd(os.Args[2:]); err != nil {
			slog.Error("recheck failed", "error", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `Usage: eval <command> [flags]

Commands:
  run       Run evaluation test cases
  compare   Compare two run results
  recheck   Re-evaluate past results with current expectations/rubrics`)
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	trials := fs.Int("trials", defaultTrials, "number of trials per test case")
	caseID := fs.String("case", "", "run a specific test case (default: all)")
	testCaseDir := fs.String("testcase-dir", defaultTestCaseDir, "directory containing test case JSON files")
	fixtureDir := fs.String("fixture-dir", defaultFixtureDir, "directory containing fixture snapshots")
	runsDir := fs.String("runs-dir", defaultRunsDir, "directory to save run results")
	if err := fs.Parse(args); err != nil {
		return err
	}

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("ANTHROPIC_API_KEY environment variable is required")
	}

	// Load test cases
	testCases, err := runner.LoadTestCases(*testCaseDir)
	if err != nil {
		return fmt.Errorf("load test cases: %w", err)
	}

	testCases = runner.FilterTestCases(testCases, *caseID)
	if len(testCases) == 0 {
		return fmt.Errorf("no test cases found (filter: %q)", *caseID)
	}

	slog.Info("starting evaluation",
		"cases", len(testCases),
		"trials", *trials,
	)

	// Build test case map for summary
	tcMap := make(map[string]types.TestCase, len(testCases))
	for _, tc := range testCases {
		tcMap[tc.ID] = tc
	}

	// Get git commit
	commit := getGitCommit()

	// Create runner
	r := runner.New(runner.Config{
		Trials:       *trials,
		ReviewConfig: agent.DefaultReviewConfig(),
		FixtureDir:   *fixtureDir,
		APIKey:       apiKey,
	})

	// Run with context
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	startedAt := time.Now().UTC()
	results, err := r.RunAll(ctx, testCases)
	finishedAt := time.Now().UTC()
	if err != nil {
		return err
	}

	// Build run record
	record := &types.RunRecord{
		SchemaVersion: schemaVersion,
		RunID:         fmt.Sprintf("run-%s", startedAt.Format("2006-01-02T15-04-05Z")),
		StartedAt:     startedAt.Format(time.RFC3339),
		FinishedAt:    finishedAt.Format(time.RFC3339),
		Environment: types.Environment{
			Commit:       commit,
			Model:        "claude-sonnet-4-6",
			ReviewConfig: agent.DefaultReviewConfig(),
		},
		Cases:          results,
		OverallSummary: recorder.ComputeOverallSummary(results, tcMap),
	}

	// Save
	path, err := recorder.SaveRunRecord(record, *runsDir)
	if err != nil {
		return fmt.Errorf("save results: %w", err)
	}

	// Print summary
	printRunSummary(record)
	fmt.Printf("\nResults saved to: %s\n", path)

	return nil
}

func compareCmd(args []string) error {
	fs := flag.NewFlagSet("compare", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: eval compare <run-a.json> <run-b.json>")
	}

	recordA, err := recorder.LoadRunRecord(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("load run A: %w", err)
	}
	recordB, err := recorder.LoadRunRecord(fs.Arg(1))
	if err != nil {
		return fmt.Errorf("load run B: %w", err)
	}

	fmt.Printf("Comparing: %s vs %s\n", recordA.RunID, recordB.RunID)
	fmt.Printf("Commits:   %s vs %s\n\n", recordA.Environment.Commit, recordB.Environment.Commit)

	// Per-case comparison
	allCases := make(map[string]bool)
	for k := range recordA.Cases {
		allCases[k] = true
	}
	for k := range recordB.Cases {
		allCases[k] = true
	}

	fmt.Printf("%-12s  %10s  %10s  %8s  %12s  %12s  %8s\n",
		"Case", "PassRate_A", "PassRate_B", "Δ Pass", "Quality_A", "Quality_B", "Δ Qual")
	fmt.Println(strings.Repeat("-", 82))

	for caseID := range allCases {
		casA, okA := recordA.Cases[caseID]
		casB, okB := recordB.Cases[caseID]

		if !okA || !okB {
			fmt.Printf("%-12s  %10s  %10s  %8s\n", caseID, fmtPresent(okA), fmtPresent(okB), "N/A")
			continue
		}

		passA := casA.Summary.PassRate.Mean
		passB := casB.Summary.PassRate.Mean
		qualA := casA.Summary.QualityScore.Mean
		qualB := casB.Summary.QualityScore.Mean
		deltaPR := passB - passA
		deltaQ := qualB - qualA

		marker := " "
		if deltaPR < -0.1 || deltaQ < -0.1 {
			marker = "← REGRESSION"
		} else if deltaPR > 0.1 || deltaQ > 0.1 {
			marker = "✓"
		}

		fmt.Printf("%-12s  %10.2f  %10.2f  %+8.2f  %12.4f  %12.4f  %+8.4f  %s\n",
			caseID, passA, passB, deltaPR, qualA, qualB, deltaQ, marker)
	}

	// Overall
	fmt.Println()
	fmt.Printf("Overall pass_rate: %.2f → %.2f (%+.2f)\n",
		recordA.OverallSummary.OverallPassRate,
		recordB.OverallSummary.OverallPassRate,
		recordB.OverallSummary.OverallPassRate-recordA.OverallSummary.OverallPassRate)
	fmt.Printf("Overall quality:   %.4f → %.4f (%+.4f)\n",
		recordA.OverallSummary.OverallQuality.Mean,
		recordB.OverallSummary.OverallQuality.Mean,
		recordB.OverallSummary.OverallQuality.Mean-recordA.OverallSummary.OverallQuality.Mean)

	return nil
}

func recheckCmd(args []string) error {
	fs := flag.NewFlagSet("recheck", flag.ExitOnError)
	testCaseDir := fs.String("testcase-dir", defaultTestCaseDir, "directory containing test case JSON files")
	runsDir := fs.String("runs-dir", defaultRunsDir, "directory to save recheck results")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: eval recheck [flags] <run.json>")
	}

	record, err := recorder.LoadRunRecord(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("load run: %w", err)
	}

	testCases, err := runner.LoadTestCases(*testCaseDir)
	if err != nil {
		return fmt.Errorf("load test cases: %w", err)
	}

	tcMap := make(map[string]types.TestCase, len(testCases))
	for _, tc := range testCases {
		tcMap[tc.ID] = tc
	}

	// Re-evaluate each trial's raw_response
	rechecked := 0
	for caseID, caseResult := range record.Cases {
		tc, ok := tcMap[caseID]
		if !ok {
			slog.Warn("test case not found, skipping", "case", caseID)
			continue
		}

		for i := range caseResult.Trials {
			trial := &caseResult.Trials[i]

			// Deserialize raw_response
			var resp agent.AgentResponse
			if err := json.Unmarshal(trial.RawResponse, &resp); err != nil {
				slog.Warn("cannot unmarshal raw_response", "case", caseID, "trial", trial.Trial, "error", err)
				continue
			}

			// Re-evaluate expectations
			expectations := make(map[string]types.CheckResult, len(tc.Expectations))
			allPassed := true
			for _, exp := range tc.Expectations {
				result := checker.CheckExpectation(exp, &resp)
				expectations[exp.ID] = result
				if !result.Passed && !result.Skipped {
					allPassed = false
				}
			}
			trial.Expectations = expectations
			trial.Passed = allPassed

			// Re-evaluate rubric
			rubricResults, qualityScore := checker.ScoreRubric(tc.Rubric, &resp)
			trial.RubricResults = rubricResults
			trial.QualityScore = qualityScore

			rechecked++
		}

		// Recompute case summary
		caseResult.Summary = recorder.ComputeCaseSummary(caseResult.Trials, tc.Expectations)
		record.Cases[caseID] = caseResult
	}

	// Recompute overall summary
	record.OverallSummary = recorder.ComputeOverallSummary(record.Cases, tcMap)

	// Save with new ID
	record.RunID = fmt.Sprintf("recheck-%s", time.Now().UTC().Format("2006-01-02T15-04-05Z"))

	path, err := recorder.SaveRunRecord(record, *runsDir)
	if err != nil {
		return fmt.Errorf("save rechecked results: %w", err)
	}

	fmt.Printf("Rechecked %d trials across %d cases\n", rechecked, len(record.Cases))
	printRunSummary(record)
	fmt.Printf("\nResults saved to: %s\n", path)

	return nil
}

func printRunSummary(record *types.RunRecord) {
	fmt.Println()
	fmt.Printf("=== Evaluation Summary (%s) ===\n", record.RunID)
	fmt.Printf("Model: %s | Commit: %s\n", record.Environment.Model, record.Environment.Commit)
	fmt.Printf("Duration: %s → %s\n\n", record.StartedAt, record.FinishedAt)

	fmt.Printf("%-12s  %10s  %12s  %10s  %10s\n",
		"Case", "PassRate", "QualityScore", "InTokens", "OutTokens")
	fmt.Println(strings.Repeat("-", 64))

	for caseID, caseResult := range record.Cases {
		s := caseResult.Summary
		fmt.Printf("%-12s  %10.2f  %12.4f  %10.0f  %10.0f\n",
			caseID,
			s.PassRate.Mean,
			s.QualityScore.Mean,
			s.InputTokens.Mean,
			s.OutputTokens.Mean,
		)
	}

	fmt.Println(strings.Repeat("-", 64))
	o := record.OverallSummary
	fmt.Printf("%-12s  %10.2f  %12.4f\n", "OVERALL", o.OverallPassRate, o.OverallQuality.Mean)
	fmt.Printf("\nTotal: %d cases, %d trials\n", o.TotalCases, o.TotalTrials)
}

func getGitCommit() string {
	// Try to read from git
	data, err := os.ReadFile(".git/HEAD")
	if err != nil {
		return "unknown"
	}
	head := strings.TrimSpace(string(data))
	if strings.HasPrefix(head, "ref: ") {
		refPath := ".git/" + strings.TrimPrefix(head, "ref: ")
		refData, err := os.ReadFile(refPath)
		if err != nil {
			return "unknown"
		}
		trimmed := strings.TrimSpace(string(refData))
		if len(trimmed) >= 7 {
			return trimmed[:7]
		}
		return trimmed
	}
	if len(head) >= 7 {
		return head[:7]
	}
	return head
}

func fmtPresent(ok bool) string {
	if ok {
		return "present"
	}
	return "missing"
}
