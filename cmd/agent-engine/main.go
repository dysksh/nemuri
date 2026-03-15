package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"

	"github.com/nemuri/nemuri/internal/agent"
	"github.com/nemuri/nemuri/internal/discord"
	"github.com/nemuri/nemuri/internal/executor"
	"github.com/nemuri/nemuri/internal/github"
	"github.com/nemuri/nemuri/internal/llm"
	"github.com/nemuri/nemuri/internal/secrets"
	"github.com/nemuri/nemuri/internal/state"
	"github.com/nemuri/nemuri/internal/storage"
)

const (
	heartbeatInterval = 3 * time.Minute
	visibilityExtend  = 10 * time.Minute // must be > heartbeatInterval
)

func main() {
	jobID := os.Getenv("JOB_ID")
	if jobID == "" {
		slog.Error("JOB_ID is not set")
		os.Exit(1)
	}

	sqsReceiptHandle := os.Getenv("SQS_RECEIPT_HANDLE")
	sqsQueueURL := os.Getenv("SQS_QUEUE_URL")
	tableName := os.Getenv("DYNAMODB_TABLE_NAME")
	anthropicKeyName := os.Getenv("ANTHROPIC_API_KEY_SECRET_NAME")
	discordTokenName := os.Getenv("DISCORD_BOT_TOKEN_SECRET_NAME")
	githubPATName := os.Getenv("GITHUB_PAT_SECRET_NAME")
	s3BucketName := os.Getenv("S3_BUCKET_NAME")
	defaultGithubOwner := os.Getenv("DEFAULT_GITHUB_OWNER")

	slog.Info("agent-engine started", "job_id", jobID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		slog.Info("received signal, shutting down", "signal", sig)
		cancel()
	}()

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		slog.Error("failed to load AWS config", "error", err)
		os.Exit(1)
	}

	store := state.NewStore(dynamodb.NewFromConfig(cfg), tableName)
	sqsClient := sqs.NewFromConfig(cfg)
	secretsClient := secrets.NewClient(secretsmanager.NewFromConfig(cfg))

	// Fetch secrets
	anthropicKey, err := secretsClient.GetSecret(ctx, anthropicKeyName)
	if err != nil {
		slog.Error("failed to get Anthropic API key", "error", err)
		os.Exit(1)
	}
	discordToken, err := secretsClient.GetSecret(ctx, discordTokenName)
	if err != nil {
		slog.Error("failed to get Discord bot token", "error", err)
		os.Exit(1)
	}

	llmClient := llm.NewClaudeClient(anthropicKey)
	discordClient := discord.NewClient(discordToken)

	// Initialize GitHub client (optional — skip if secret not configured)
	var githubClient github.API
	if githubPATName != "" {
		githubPAT, err := secretsClient.GetSecret(ctx, githubPATName)
		if err != nil {
			slog.Warn("GitHub PAT not available, code generation disabled", "error", err)
		} else {
			githubClient = github.NewClient(githubPAT)
		}
	}

	// Initialize S3 storage client
	var storageClient *storage.Client
	if s3BucketName != "" {
		storageClient = storage.NewClient(s3.NewFromConfig(cfg), s3BucketName)
	}

	workerID := uuid.New().String()

	// 1. Acquire lock (transitions INIT/FAILED/WAITING_USER_INPUT → RUNNING)
	if err := store.AcquireLock(ctx, jobID, workerID); err != nil {
		slog.Error("failed to acquire lock", "error", err, "job_id", jobID)
		os.Exit(1)
	}
	slog.Info("lock acquired", "job_id", jobID, "worker_id", workerID)

	// 2. Load job state
	job, err := store.GetJob(ctx, jobID)
	if err != nil {
		slog.Error("failed to load job", "error", err, "job_id", jobID)
		os.Exit(1)
	}

	// 3. Start heartbeat and SQS visibility extension goroutines
	var wg sync.WaitGroup
	heartbeatCtx, heartbeatCancel := context.WithCancel(ctx)
	defer heartbeatCancel()

	wg.Add(1)
	go func() {
		defer wg.Done()
		runHeartbeat(heartbeatCtx, store, jobID, workerID)
	}()

	if sqsReceiptHandle != "" && sqsQueueURL != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runVisibilityExtender(heartbeatCtx, sqsClient, sqsQueueURL, sqsReceiptHandle)
		}()
	}

	// 4. Execute job logic
	exec := &executor.Executor{
		Agent:          agent.New(llmClient, githubClient, defaultGithubOwner),
		Discord:        discordClient,
		GitHub:         githubClient,
		Storage:        storageClient,
		DefaultGHOwner: defaultGithubOwner,
	}
	reviewCfg := agent.DefaultReviewConfig()

	isResume := job.UserResponse != ""
	outcome, jobErr := exec.Execute(ctx, job, isResume, reviewCfg)

	// 5. Stop heartbeat
	heartbeatCancel()
	wg.Wait()

	// 6. Handle outcome
	if jobErr != nil {
		slog.Error("job failed", "error", jobErr, "job_id", jobID)
		if err := store.MarkFailed(ctx, jobID, workerID, jobErr.Error(), job.Version, job.State); err != nil {
			slog.Error("failed to mark job as failed", "error", err, "job_id", jobID)
		}
		notifyErr := discordClient.SendResult(ctx, job.ApplicationID, job.InteractionToken, job.ChannelID,
			"ジョブの実行中にエラーが発生しました。管理者にお問い合わせください。(job_id: "+jobID+")")
		if notifyErr != nil {
			slog.Error("failed to send error notification to Discord", "error", notifyErr, "job_id", jobID)
		}
		os.Exit(1)
	}

	// Re-read to get current version after AcquireLock incremented it
	job, err = store.GetJob(ctx, jobID)
	if err != nil {
		slog.Error("failed to reload job", "error", err, "job_id", jobID)
		os.Exit(1)
	}

	// Handle question: save context, create thread, transition to WAITING_USER_INPUT
	if outcome != nil && outcome.Question != "" {
		if err := exec.HandleQuestionOutcome(ctx, job, workerID, outcome, store); err != nil {
			slog.Error("failed to handle question outcome", "error", err, "job_id", jobID)
			_ = store.MarkFailed(ctx, jobID, workerID, err.Error(), job.Version, job.State)
			os.Exit(1)
		}
		deleteSQSMessage(ctx, sqsClient, sqsQueueURL, sqsReceiptHandle, jobID)
		slog.Info("agent-engine paused for user input", "job_id", jobID)
		return
	}

	// Handle PR with WAITING_APPROVAL
	if outcome != nil && outcome.PRCreated {
		threadID := outcome.ThreadID
		if threadID == "" {
			threadID = job.ThreadID
		}
		if err := store.MarkWaitingApproval(ctx, jobID, workerID, job.Version, threadID); err != nil {
			slog.Error("failed to mark waiting approval", "error", err, "job_id", jobID)
			os.Exit(1)
		}
		deleteSQSMessage(ctx, sqsClient, sqsQueueURL, sqsReceiptHandle, jobID)
		slog.Info("agent-engine waiting for PR approval", "job_id", jobID)
		return
	}

	// Normal completion
	if err := store.MarkDone(ctx, jobID, workerID, job.Version, job.State); err != nil {
		slog.Error("failed to mark job as done", "error", err, "job_id", jobID)
		os.Exit(1)
	}
	deleteSQSMessage(ctx, sqsClient, sqsQueueURL, sqsReceiptHandle, jobID)
	slog.Info("agent-engine finished successfully", "job_id", jobID)
}

func deleteSQSMessage(ctx context.Context, sqsClient *sqs.Client, queueURL, receiptHandle, jobID string) {
	if receiptHandle == "" || queueURL == "" {
		return
	}
	if _, err := sqsClient.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(queueURL),
		ReceiptHandle: aws.String(receiptHandle),
	}); err != nil {
		slog.Error("failed to delete SQS message", "error", err, "job_id", jobID)
	}
}

func runHeartbeat(ctx context.Context, store *state.Store, jobID, workerID string) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := store.Heartbeat(ctx, jobID, workerID); err != nil {
				slog.Error("heartbeat failed", "error", err, "job_id", jobID)
				return
			}
			slog.Debug("heartbeat sent", "job_id", jobID)
		}
	}
}

func runVisibilityExtender(ctx context.Context, sqsClient *sqs.Client, queueURL, receiptHandle string) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, err := sqsClient.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
				QueueUrl:          aws.String(queueURL),
				ReceiptHandle:     aws.String(receiptHandle),
				VisibilityTimeout: int32(visibilityExtend.Seconds()),
			})
			if err != nil {
				slog.Error("failed to extend SQS visibility", "error", err)
				return
			}
			slog.Debug("SQS visibility extended")
		}
	}
}
