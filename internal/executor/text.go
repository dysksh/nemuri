package executor

import (
	"context"
	"log/slog"

	"github.com/nemuri/nemuri/internal/state"
)

func (e *Executor) executeTextJob(ctx context.Context, job *state.Job, content string) error {
	if content == "" {
		content = "（結果を生成できませんでした。リクエストの内容を変えて再度お試しください。）"
	}

	channelID := job.ChannelID
	if job.ThreadID != "" {
		channelID = job.ThreadID
	}
	if err := e.Discord.SendResult(ctx, job.ApplicationID, job.InteractionToken, channelID, content); err != nil {
		return err
	}
	slog.Info("text result sent to Discord", "job_id", job.JobID)
	return nil
}
