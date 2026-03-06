package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"

	"github.com/nemuri/nemuri/internal/state"
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

	workerID := uuid.New().String()

	// 1. Acquire lock
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
	jobErr := executeJob(ctx, job)

	// 5. Stop heartbeat
	heartbeatCancel()
	wg.Wait()

	// 6. Update final state
	if jobErr != nil {
		slog.Error("job failed", "error", jobErr, "job_id", jobID)
		if err := store.MarkFailed(ctx, jobID, workerID, jobErr.Error(), job.Version, job.State); err != nil {
			slog.Error("failed to mark job as failed", "error", err, "job_id", jobID)
		}
		os.Exit(1)
	}

	// Re-read to get current version after AcquireLock incremented it
	job, err = store.GetJob(ctx, jobID)
	if err != nil {
		slog.Error("failed to reload job", "error", err, "job_id", jobID)
		os.Exit(1)
	}

	if err := store.MarkDone(ctx, jobID, workerID, job.Version, job.State); err != nil {
		slog.Error("failed to mark job as done", "error", err, "job_id", jobID)
		os.Exit(1)
	}

	// 7. Delete SQS message
	if sqsReceiptHandle != "" && sqsQueueURL != "" {
		if _, err := sqsClient.DeleteMessage(ctx, &sqs.DeleteMessageInput{
			QueueUrl:      aws.String(sqsQueueURL),
			ReceiptHandle: aws.String(sqsReceiptHandle),
		}); err != nil {
			slog.Error("failed to delete SQS message", "error", err, "job_id", jobID)
		}
	}

	slog.Info("agent-engine finished successfully", "job_id", jobID)
}

func executeJob(ctx context.Context, job *state.Job) error {
	// Phase 4: placeholder — just log the prompt and simulate work
	slog.Info("executing job",
		"job_id", job.JobID,
		"prompt", job.Prompt,
		"state", job.State,
	)

	fmt.Println("hello from ECS")

	return nil
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
