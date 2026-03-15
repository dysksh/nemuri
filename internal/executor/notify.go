package executor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/nemuri/nemuri/internal/agent"
	"github.com/nemuri/nemuri/internal/discord"
	"github.com/nemuri/nemuri/internal/state"
)

// createThreadAndNotify creates a Discord thread (or posts to existing thread) and returns the thread ID.
func (e *Executor) createThreadAndNotify(ctx context.Context, job *state.Job, message string) (string, error) {
	message = discord.Truncate(message, discord.MaxContentLen, "")

	// If thread already exists (from a previous question), post result there via follow-up
	if job.ThreadID != "" {
		if err := e.Discord.SendResult(ctx, job.ApplicationID, job.InteractionToken, job.ThreadID, message); err != nil {
			return job.ThreadID, fmt.Errorf("send to existing thread: %w", err)
		}
		return job.ThreadID, nil
	}

	// Create new thread
	threadName := fmt.Sprintf("Nemuri: %s", discord.Truncate(job.Prompt, 80, ""))
	threadID, err := e.Discord.CreateThread(ctx, job.ChannelID, threadName, message)
	if err != nil {
		slog.Warn("thread creation failed, falling back to direct message", "error", err, "job_id", job.JobID)
		if sendErr := e.Discord.SendResult(ctx, job.ApplicationID, job.InteractionToken, job.ChannelID, message); sendErr != nil {
			return "", fmt.Errorf("thread creation and fallback both failed: thread: %w, fallback: %v", err, sendErr)
		}
		return job.ChannelID, nil
	}
	slog.Info("Discord thread created", "job_id", job.JobID, "thread_id", threadID)

	followUpErr := e.Discord.SendFollowUp(ctx, job.ApplicationID, job.InteractionToken,
		"スレッドで結果を投稿しました。確認してください。")
	if followUpErr != nil {
		slog.Warn("failed to follow up original interaction", "error", followUpErr, "job_id", job.JobID)
	}

	return threadID, nil
}

func buildPRNotificationMessage(prURL, title, description string, files []agent.OutputFile, reviewResult *agent.ReviewLoopResult) string {
	message := fmt.Sprintf("PRを作成しました: %s\n\n**%s**", prURL, title)
	if description != "" {
		message += "\n" + description
	}
	message += "\n\n変更ファイル: " + formatFilePaths(files)
	if reviewResult != nil {
		message += "\n\n" + formatReviewSummary(reviewResult)
	}
	message += "\n\n---\nPRを確認してmergeしてください。完了したらこのスレッドで `/agent approve` と返信してください。"
	return message
}

func formatFilePaths(files []agent.OutputFile) string {
	names := make([]string, len(files))
	for i, f := range files {
		if f.Path != "" {
			names[i] = "`" + f.Path + "`"
		} else {
			names[i] = "`" + f.Name + "`"
		}
	}
	return strings.Join(names, ", ")
}

func formatReviewSummary(result *agent.ReviewLoopResult) string {
	if len(result.Reviews) == 0 {
		return ""
	}
	lastReview := result.Reviews[len(result.Reviews)-1]
	status := "PASS"
	if !result.Passed {
		status = "WARN"
	}
	return fmt.Sprintf("[Review %s] スコア: 正確性=%.1f / セキュリティ=%.1f / 保守性=%.1f / 完全性=%.1f (修正回数: %d)",
		status,
		lastReview.Scores.Correctness,
		lastReview.Scores.Security,
		lastReview.Scores.Maintainability,
		lastReview.Scores.Completeness,
		result.Revisions,
	)
}
