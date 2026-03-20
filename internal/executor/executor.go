package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/nemuri/nemuri/internal/agent"
	"github.com/nemuri/nemuri/internal/discord"
	"github.com/nemuri/nemuri/internal/github"
	"github.com/nemuri/nemuri/internal/llm"
	"github.com/nemuri/nemuri/internal/state"
	"github.com/nemuri/nemuri/internal/storage"
)

const (
	branchJobIDLen          = 8
	conversationContextFile = "conversation_context.json"
)

// conversationContext is the saved state for resuming after a question.
type conversationContext struct {
	Messages          []llm.Message     `json:"messages"`
	PendingToolCallID string            `json:"pending_tool_call_id"`
	Phase             string            `json:"phase,omitempty"`
	FileCache         map[string]string `json:"file_cache,omitempty"`
}

// Outcome describes how a job execution ended.
type Outcome struct {
	// For question flow
	Question      string
	Messages      []llm.Message
	PendingToolID string
	Phase         string            // agent phase when paused ("gathering")
	FileCache     map[string]string // cached file contents for resume

	// For PR flow
	PRCreated bool
	ThreadID  string // thread created during execution (for WAITING_APPROVAL)
}

// Executor orchestrates job execution, delivering results via Discord/GitHub/S3.
type Executor struct {
	Agent          *agent.Agent
	Discord        *discord.Client
	GitHub         github.API
	Storage        *storage.Client
	DefaultGHOwner string
}

// Execute runs the job logic and returns an outcome.
func (e *Executor) Execute(ctx context.Context, job *state.Job, isResume bool, reviewCfg agent.ReviewConfig) (*Outcome, error) {
	slog.Info("executing job", "job_id", job.JobID, "prompt", job.Prompt, "is_resume", isResume)

	var runResult *agent.RunResult
	var reviewResult *agent.ReviewLoopResult

	if isResume {
		slog.Info("resuming from saved state", "job_id", job.JobID, "user_response", job.UserResponse)

		resumeResult, err := e.resumeAgent(ctx, job)
		if err != nil {
			return nil, err
		}

		if resumeResult.Question != "" {
			return &Outcome{
				Question:      resumeResult.Question,
				Messages:      resumeResult.Messages,
				PendingToolID: resumeResult.PendingToolCallID,
				Phase:         resumeResult.Phase,
				FileCache:     resumeResult.FileCache,
			}, nil
		}

		runResult = resumeResult
		switch runResult.Response.Type {
		case agent.ResponseTypeCode, agent.ResponseTypeNewRepo:
			loopResult, err := e.Agent.ReviewLoop(ctx, job.Prompt, runResult.Response, reviewCfg)
			if err != nil {
				return nil, err
			}
			runResult.Response = loopResult.Response
			runResult.TotalInputTokens += loopResult.TotalInputTokens
			runResult.TotalOutputTokens += loopResult.TotalOutputTokens
			reviewResult = loopResult
		}
	} else {
		var err error
		runResult, reviewResult, err = e.Agent.RunWithReview(ctx, job.Prompt, reviewCfg)
		if err != nil {
			return nil, err
		}

		if runResult.Question != "" {
			return &Outcome{
				Question:      runResult.Question,
				Messages:      runResult.Messages,
				PendingToolID: runResult.PendingToolCallID,
				Phase:         runResult.Phase,
				FileCache:     runResult.FileCache,
			}, nil
		}
	}

	agentResp := runResult.Response
	slog.Info("agent completed",
		"job_id", job.JobID,
		"type", agentResp.Type,
		"iterations", runResult.Iterations,
		"input_tokens", runResult.TotalInputTokens,
		"output_tokens", runResult.TotalOutputTokens,
	)

	if reviewResult != nil {
		slog.Info("review completed",
			"job_id", job.JobID,
			"passed", reviewResult.Passed,
			"revisions", reviewResult.Revisions,
			"reviews", len(reviewResult.Reviews),
		)
		if e.Storage != nil {
			saveReviewArtifact(ctx, job.JobID, reviewResult, e.Storage)
		}
	}

	switch agentResp.Type {
	case agent.ResponseTypeCode:
		threadID, err := e.executeCodeJob(ctx, job, *agentResp, reviewResult)
		if err != nil {
			return nil, err
		}
		return &Outcome{PRCreated: true, ThreadID: threadID}, nil
	case agent.ResponseTypeNewRepo:
		threadID, err := e.executeNewRepoJob(ctx, job, *agentResp, reviewResult)
		if err != nil {
			return nil, err
		}
		return &Outcome{PRCreated: true, ThreadID: threadID}, nil
	case agent.ResponseTypeFile:
		if len(agentResp.Files) == 0 {
			slog.Warn("file response has no files, falling back to text", "job_id", job.JobID)
			return nil, e.executeTextJob(ctx, job, agentResp.Content)
		}
		return nil, e.executeFileJob(ctx, job, *agentResp)
	default:
		return nil, e.executeTextJob(ctx, job, agentResp.Content)
	}
}

// HandleQuestionOutcome saves conversation context, creates a Discord thread, and transitions state.
func (e *Executor) HandleQuestionOutcome(ctx context.Context, job *state.Job, workerID string, outcome *Outcome, store *state.Store) error {
	if e.Storage == nil {
		return fmt.Errorf("S3 storage not configured, cannot save conversation context")
	}
	convCtx := conversationContext{
		Messages:          outcome.Messages,
		PendingToolCallID: outcome.PendingToolID,
		Phase:             outcome.Phase,
		FileCache:         outcome.FileCache,
	}
	data, err := json.Marshal(convCtx)
	if err != nil {
		return fmt.Errorf("marshal conversation context: %w", err)
	}
	if err := e.Storage.UploadArtifact(ctx, job.JobID, conversationContextFile, data); err != nil {
		return fmt.Errorf("save conversation context: %w", err)
	}
	slog.Info("conversation context saved", "job_id", job.JobID)

	threadName := fmt.Sprintf("Nemuri: %s", discord.Truncate(job.Prompt, 80, ""))
	questionMsg := fmt.Sprintf("**質問があります:**\n\n%s\n\n---\nこのスレッドで `/nemuri <回答>` と返信してください。", outcome.Question)

	threadID, err := e.Discord.CreateThread(ctx, job.ChannelID, threadName, questionMsg)
	if err != nil {
		return fmt.Errorf("create Discord thread: %w", err)
	}
	slog.Info("Discord thread created", "job_id", job.JobID, "thread_id", threadID)

	if err := store.MarkWaitingUserInput(ctx, job.JobID, workerID, job.Version, threadID); err != nil {
		return fmt.Errorf("mark waiting user input: %w", err)
	}

	followUpErr := e.Discord.SendFollowUp(ctx, job.ApplicationID, job.InteractionToken,
		"スレッドで質問しました。確認してください。")
	if followUpErr != nil {
		slog.Warn("failed to follow up original interaction", "error", followUpErr, "job_id", job.JobID)
	}

	return nil
}

func saveReviewArtifact(ctx context.Context, jobID string, result *agent.ReviewLoopResult, storageClient *storage.Client) {
	data, err := json.Marshal(result)
	if err != nil {
		slog.Warn("failed to marshal review result", "error", err)
		return
	}
	if err := storageClient.UploadArtifact(ctx, jobID, "review_result.json", data); err != nil {
		slog.Warn("failed to upload review artifact", "error", err, "job_id", jobID)
	}
}

func truncateJobID(jobID string) string {
	if len(jobID) < branchJobIDLen {
		return jobID
	}
	return jobID[:branchJobIDLen]
}
