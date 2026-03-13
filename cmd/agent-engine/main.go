package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
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
	"github.com/nemuri/nemuri/internal/converter"
	"github.com/nemuri/nemuri/internal/discord"
	"github.com/nemuri/nemuri/internal/github"
	"github.com/nemuri/nemuri/internal/llm"
	"github.com/nemuri/nemuri/internal/secrets"
	"github.com/nemuri/nemuri/internal/state"
	"github.com/nemuri/nemuri/internal/storage"
)

const (
	heartbeatInterval = 3 * time.Minute
	visibilityExtend  = 10 * time.Minute // must be > heartbeatInterval

	branchJobIDLen = 8 // number of job ID characters used in branch names

	conversationContextFile = "conversation_context.json"
)

// conversationContext is the saved state for resuming after a question.
type conversationContext struct {
	Messages          []llm.Message     `json:"messages"`
	PendingToolCallID string            `json:"pending_tool_call_id"`
	Phase             string            `json:"phase,omitempty"`
	FileCache         map[string]string `json:"file_cache,omitempty"`
}

// jobOutcome describes how a job execution ended.
type jobOutcome struct {
	// For question flow
	question      string
	messages      []llm.Message
	pendingToolID string
	phase         string            // agent phase when paused ("gathering")
	fileCache     map[string]string // cached file contents for resume

	// For PR flow
	prCreated bool
	threadID  string // thread created during execution (for WAITING_APPROVAL)
}

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
	var githubClient *github.Client
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
	agentRunner := agent.New(llmClient, githubClient, defaultGithubOwner)
	reviewCfg := agent.DefaultReviewConfig()

	isResume := job.UserResponse != ""
	outcome, jobErr := executeJob(ctx, job, isResume, agentRunner, reviewCfg, discordClient, githubClient, storageClient, defaultGithubOwner)

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
	if outcome != nil && outcome.question != "" {
		if err := handleQuestionOutcome(ctx, job, workerID, outcome, store, discordClient, storageClient); err != nil {
			slog.Error("failed to handle question outcome", "error", err, "job_id", jobID)
			_ = store.MarkFailed(ctx, jobID, workerID, err.Error(), job.Version, job.State)
			os.Exit(1)
		}
		deleteSQSMessage(ctx, sqsClient, sqsQueueURL, sqsReceiptHandle, jobID)
		slog.Info("agent-engine paused for user input", "job_id", jobID)
		return
	}

	// Handle PR with WAITING_APPROVAL
	if outcome != nil && outcome.prCreated {
		threadID := outcome.threadID
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

func handleQuestionOutcome(ctx context.Context, job *state.Job, workerID string, outcome *jobOutcome, store *state.Store, discordClient *discord.Client, storageClient *storage.Client) error {
	// Save conversation context to S3
	if storageClient == nil {
		return fmt.Errorf("S3 storage not configured, cannot save conversation context")
	}
	convCtx := conversationContext{
		Messages:          outcome.messages,
		PendingToolCallID: outcome.pendingToolID,
		Phase:             outcome.phase,
		FileCache:         outcome.fileCache,
	}
	data, err := json.Marshal(convCtx)
	if err != nil {
		return fmt.Errorf("marshal conversation context: %w", err)
	}
	if err := storageClient.UploadArtifact(ctx, job.JobID, conversationContextFile, data); err != nil {
		return fmt.Errorf("save conversation context: %w", err)
	}
	slog.Info("conversation context saved", "job_id", job.JobID)

	// Create Discord thread and post question
	threadName := fmt.Sprintf("Nemuri: %s", truncateString(job.Prompt, 80))
	questionMsg := fmt.Sprintf("**質問があります:**\n\n%s\n\n---\nこのスレッドで `/agent <回答>` と返信してください。", outcome.question)

	threadID, err := discordClient.CreateThread(ctx, job.ChannelID, threadName, questionMsg)
	if err != nil {
		return fmt.Errorf("create Discord thread: %w", err)
	}
	slog.Info("Discord thread created", "job_id", job.JobID, "thread_id", threadID)

	// Transition to WAITING_USER_INPUT
	if err := store.MarkWaitingUserInput(ctx, job.JobID, workerID, job.Version, threadID); err != nil {
		return fmt.Errorf("mark waiting user input: %w", err)
	}

	// Follow up the original deferred ACK to resolve "thinking..." message
	followUpErr := discordClient.SendFollowUp(ctx, job.ApplicationID, job.InteractionToken,
		"スレッドで質問しました。確認してください。")
	if followUpErr != nil {
		slog.Warn("failed to follow up original interaction", "error", followUpErr, "job_id", job.JobID)
	}

	return nil
}

func executeJob(ctx context.Context, job *state.Job, isResume bool, agentRunner *agent.Agent, reviewCfg agent.ReviewConfig, discordClient *discord.Client, githubClient *github.Client, storageClient *storage.Client, defaultGithubOwner string) (*jobOutcome, error) {
	slog.Info("executing job", "job_id", job.JobID, "prompt", job.Prompt, "is_resume", isResume)

	var runResult *agent.RunResult
	var reviewResult *agent.ReviewLoopResult

	if isResume {
		slog.Info("resuming from saved state", "job_id", job.JobID, "user_response", job.UserResponse)

		resumeResult, err := resumeAgent(ctx, job, agentRunner, storageClient)
		if err != nil {
			return nil, err
		}

		// Check if the resumed agent asked another question
		if resumeResult.Question != "" {
			return &jobOutcome{
				question:      resumeResult.Question,
				messages:      resumeResult.Messages,
				pendingToolID: resumeResult.PendingToolCallID,
				phase:         resumeResult.Phase,
				fileCache:     resumeResult.FileCache,
			}, nil
		}

		// Run review on the result
		runResult = resumeResult
		switch runResult.Response.Type {
		case agent.ResponseTypeCode, agent.ResponseTypeNewRepo:
			loopResult, err := agentRunner.ReviewLoop(ctx, job.Prompt, runResult.Response, reviewCfg)
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
		runResult, reviewResult, err = agentRunner.RunWithReview(ctx, job.Prompt, reviewCfg)
		if err != nil {
			return nil, err
		}

		// Check if the agent asked a question (before review)
		if runResult.Question != "" {
			return &jobOutcome{
				question:      runResult.Question,
				messages:      runResult.Messages,
				pendingToolID: runResult.PendingToolCallID,
				phase:         runResult.Phase,
				fileCache:     runResult.FileCache,
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
		// Save review results as artifact (best-effort)
		if storageClient != nil {
			saveReviewArtifact(ctx, job.JobID, reviewResult, storageClient)
		}
	}

	switch agentResp.Type {
	case agent.ResponseTypeCode:
		threadID, err := executeCodeJob(ctx, job, *agentResp, reviewResult, defaultGithubOwner, discordClient, githubClient, storageClient)
		if err != nil {
			return nil, err
		}
		return &jobOutcome{prCreated: true, threadID: threadID}, nil
	case agent.ResponseTypeNewRepo:
		threadID, err := executeNewRepoJob(ctx, job, *agentResp, reviewResult, discordClient, githubClient, storageClient)
		if err != nil {
			return nil, err
		}
		return &jobOutcome{prCreated: true, threadID: threadID}, nil
	case agent.ResponseTypeFile:
		if len(agentResp.Files) == 0 {
			slog.Warn("file response has no files, falling back to text", "job_id", job.JobID)
			return nil, executeTextJob(ctx, job, agentResp.Content, discordClient)
		}
		return nil, executeFileJob(ctx, job, *agentResp, discordClient, storageClient)
	default:
		return nil, executeTextJob(ctx, job, agentResp.Content, discordClient)
	}
}

func resumeAgent(ctx context.Context, job *state.Job, agentRunner *agent.Agent, storageClient *storage.Client) (*agent.RunResult, error) {
	if storageClient == nil {
		return nil, fmt.Errorf("S3 storage not configured, cannot load conversation context")
	}

	data, err := storageClient.DownloadArtifact(ctx, job.JobID, conversationContextFile)
	if err != nil {
		return nil, fmt.Errorf("load conversation context: %w", err)
	}

	var convCtx conversationContext
	if err := json.Unmarshal(data, &convCtx); err != nil {
		return nil, fmt.Errorf("unmarshal conversation context: %w", err)
	}

	// Replace the placeholder tool_result for ask_user_question with the real answer.
	// The placeholder was included in the last user message alongside repo tool results.
	replaceToolResult(convCtx.Messages, convCtx.PendingToolCallID, fmt.Sprintf("User responded: %s", job.UserResponse))

	slog.Info("resuming agent with conversation context",
		"job_id", job.JobID,
		"messages", len(convCtx.Messages),
		"pending_tool_id", convCtx.PendingToolCallID,
	)

	return agentRunner.Resume(ctx, convCtx.Messages, convCtx.Phase, convCtx.FileCache)
}

func executeTextJob(ctx context.Context, job *state.Job, content string, discordClient *discord.Client) error {
	if content == "" {
		content = "（結果を生成できませんでした。リクエストの内容を変えて再度お試しください。）"
	}

	// Use SendResult to deliver via follow-up (resolves deferred ACK) with fallback
	channelID := job.ChannelID
	if job.ThreadID != "" {
		channelID = job.ThreadID
	}
	if err := discordClient.SendResult(ctx, job.ApplicationID, job.InteractionToken, channelID, content); err != nil {
		return err
	}
	slog.Info("text result sent to Discord", "job_id", job.JobID)
	return nil
}

func executeCodeJob(ctx context.Context, job *state.Job, resp agent.AgentResponse, reviewResult *agent.ReviewLoopResult, owner string, discordClient *discord.Client, githubClient *github.Client, storageClient *storage.Client) (string, error) {
	if githubClient == nil {
		return "", fmt.Errorf("code generation requested but GitHub client is not configured")
	}
	if owner == "" || resp.Repo == "" || len(resp.Files) == 0 {
		return "", fmt.Errorf("code response missing required fields (owner, repo, or files)")
	}

	branch := fmt.Sprintf("nemuri/%s", truncateJobID(job.JobID))
	slog.Info("creating code deliverable", "job_id", job.JobID, "owner", owner, "repo", resp.Repo, "branch", branch, "files", len(resp.Files))

	// 1. Get default branch and create feature branch
	defaultBranch, err := githubClient.GetDefaultBranch(ctx, owner, resp.Repo)
	if err != nil {
		return "", fmt.Errorf("get default branch: %w", err)
	}
	if err := githubClient.CreateBranch(ctx, owner, resp.Repo, defaultBranch, branch); err != nil {
		return "", fmt.Errorf("create branch: %w", err)
	}

	// 2. Commit files and create PR
	pr, err := commitAndCreatePR(ctx, job, resp, owner, defaultBranch, branch, githubClient, storageClient)
	if err != nil {
		return "", err
	}

	// 3. Create thread and notify
	message := buildPRNotificationMessage(pr.URL, resp.Title, resp.Description, resp.Files, reviewResult)

	threadID, err := createThreadAndNotify(ctx, job, message, discordClient)
	if err != nil {
		return "", fmt.Errorf("notify Discord: %w", err)
	}
	return threadID, nil
}

func executeNewRepoJob(ctx context.Context, job *state.Job, resp agent.AgentResponse, reviewResult *agent.ReviewLoopResult, discordClient *discord.Client, githubClient *github.Client, storageClient *storage.Client) (string, error) {
	if githubClient == nil {
		return "", fmt.Errorf("new repo requested but GitHub client is not configured")
	}
	if resp.Repo == "" || len(resp.Files) == 0 {
		return "", fmt.Errorf("new_repo response missing required fields (repo or files)")
	}

	slog.Info("creating new repository", "job_id", job.JobID, "repo", resp.Repo)

	// 1. Create repository (always private, under authenticated user)
	repoResult, err := githubClient.CreateRepo(ctx, github.CreateRepoInput{
		Name:        resp.Repo,
		Description: resp.RepoDescription,
		Private:     true,
	})
	if err != nil {
		return "", fmt.Errorf("create repo: %w", err)
	}
	slog.Info("repository created", "job_id", job.JobID, "full_name", repoResult.FullName)

	// Extract owner/repo from the API result (e.g. "user/repo-name")
	parts := strings.SplitN(repoResult.FullName, "/", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("unexpected repo full_name format: %s", repoResult.FullName)
	}
	owner, repo := parts[0], parts[1]

	// rollbackRepo deletes the newly created repo on failure.
	rollbackRepo := func(cause error) (string, error) {
		slog.Warn("rolling back repository creation", "owner", owner, "repo", repo, "cause", cause)
		if delErr := githubClient.DeleteRepo(ctx, owner, repo); delErr != nil {
			slog.Error("failed to delete repo during rollback", "error", delErr)
		}
		return "", cause
	}

	// 2. Wait for the initial commit to be ready (auto_init may be async)
	if err := githubClient.WaitForRepoReady(ctx, owner, repo, repoResult.DefaultBranch); err != nil {
		return rollbackRepo(fmt.Errorf("wait for repo ready: %w", err))
	}

	// 3. Create feature branch, commit files, and create PR
	branch := fmt.Sprintf("nemuri/%s", truncateJobID(job.JobID))
	if err := githubClient.CreateBranch(ctx, owner, repo, repoResult.DefaultBranch, branch); err != nil {
		return rollbackRepo(fmt.Errorf("create branch: %w", err))
	}

	resp.Repo = repo

	pr, err := commitAndCreatePR(ctx, job, resp, owner, repoResult.DefaultBranch, branch, githubClient, storageClient)
	if err != nil {
		return rollbackRepo(err)
	}

	// 4. Notify Discord
	message := "リポジトリを作成しました: " + repoResult.HTMLURL + "\n" +
		buildPRNotificationMessage(pr.URL, resp.Title, resp.Description, resp.Files, reviewResult)

	threadID, err := createThreadAndNotify(ctx, job, message, discordClient)
	if err != nil {
		return "", fmt.Errorf("notify Discord: %w", err)
	}
	return threadID, nil
}

// createThreadAndNotify creates a Discord thread (or posts to existing thread) and returns the thread ID.
// Messages are truncated to Discord's 2000-character limit to prevent API errors.
func createThreadAndNotify(ctx context.Context, job *state.Job, message string, discordClient *discord.Client) (string, error) {
	message = truncateDiscordMessage(message)

	// If thread already exists (from a previous question), post result there via follow-up
	if job.ThreadID != "" {
		if err := discordClient.SendResult(ctx, job.ApplicationID, job.InteractionToken, job.ThreadID, message); err != nil {
			return job.ThreadID, fmt.Errorf("send to existing thread: %w", err)
		}
		return job.ThreadID, nil
	}

	// Create new thread
	threadName := fmt.Sprintf("Nemuri: %s", truncateString(job.Prompt, 80))
	threadID, err := discordClient.CreateThread(ctx, job.ChannelID, threadName, message)
	if err != nil {
		// Fall back to direct message in the channel
		slog.Warn("thread creation failed, falling back to direct message", "error", err, "job_id", job.JobID)
		if sendErr := discordClient.SendResult(ctx, job.ApplicationID, job.InteractionToken, job.ChannelID, message); sendErr != nil {
			return "", fmt.Errorf("thread creation and fallback both failed: thread: %w, fallback: %v", err, sendErr)
		}
		// Return channelID so callers (e.g. MarkWaitingApproval) have a valid ID
		// for follow-up interactions even when thread creation failed.
		return job.ChannelID, nil
	}
	slog.Info("Discord thread created", "job_id", job.JobID, "thread_id", threadID)

	// Follow up the original deferred ACK to resolve "thinking..." message
	followUpErr := discordClient.SendFollowUp(ctx, job.ApplicationID, job.InteractionToken,
		"スレッドで結果を投稿しました。確認してください。")
	if followUpErr != nil {
		slog.Warn("failed to follow up original interaction", "error", followUpErr, "job_id", job.JobID)
	}

	return threadID, nil
}

func executeFileJob(ctx context.Context, job *state.Job, resp agent.AgentResponse, discordClient *discord.Client, storageClient *storage.Client) error {
	if storageClient == nil {
		return fmt.Errorf("file output requested but S3 storage is not configured")
	}
	if len(resp.Files) == 0 {
		return fmt.Errorf("file response has no files")
	}

	slog.Info("uploading file deliverables", "job_id", job.JobID, "files", len(resp.Files))

	canPDF := converter.Available()

	var lines []string
	for i, f := range resp.Files {
		if f.Name == "" {
			return fmt.Errorf("file response contains entry with empty name")
		}
		contentBytes := []byte(f.Content)
		if err := storageClient.UploadOutput(ctx, job.JobID, f.Name, contentBytes); err != nil {
			return fmt.Errorf("upload %s: %w", f.Name, err)
		}

		// Convert Markdown files to PDF; show PDF link instead of md.
		pdfDone := false
		if canPDF && converter.IsMarkdown(f.Name) {
			if pdfLine, ok := convertAndUploadPDF(ctx, job.JobID, f.Name, contentBytes, storageClient); ok {
				lines = append(lines, pdfLine)
				pdfDone = true
			}
		}
		if !pdfDone {
			// Show original file link (also used as fallback when PDF conversion fails).
			url, err := storageClient.GetOutputPresignedURL(ctx, job.JobID, f.Name)
			if err != nil {
				return fmt.Errorf("presign %s: %w", f.Name, err)
			}
			lines = append(lines, fmt.Sprintf("`%s`: %s", f.Name, url))
		}

		// Release content to allow GC to reclaim memory.
		resp.Files[i].Content = ""
	}

	message := fmt.Sprintf("ファイルを生成しました（24時間有効）:\n%s", strings.Join(lines, "\n"))

	channelID := job.ChannelID
	if job.ThreadID != "" {
		channelID = job.ThreadID
	}
	return discordClient.SendResult(ctx, job.ApplicationID, job.InteractionToken, channelID, message)
}

// convertAndUploadPDF converts a Markdown file to PDF, uploads it, and returns a Discord line.
// Returns ("", false) on any failure (logged as warning).
func convertAndUploadPDF(ctx context.Context, jobID, fileName string, mdContent []byte, storageClient *storage.Client) (string, bool) {
	pdfName := converter.PDFFilename(fileName)
	pdfData, err := converter.MarkdownToPDF(ctx, mdContent)
	if err != nil {
		slog.Warn("PDF conversion failed, skipping", "file", fileName, "error", err)
		return "", false
	}
	if err := storageClient.UploadOutput(ctx, jobID, pdfName, pdfData); err != nil {
		slog.Warn("PDF upload failed, skipping", "file", pdfName, "error", err)
		return "", false
	}
	pdfURL, err := storageClient.GetOutputPresignedURL(ctx, jobID, pdfName)
	if err != nil {
		slog.Warn("PDF presign failed, skipping", "file", pdfName, "error", err)
		return "", false
	}
	slog.Info("PDF generated", "job_id", jobID, "file", pdfName)
	return fmt.Sprintf("`%s` (PDF): %s", pdfName, pdfURL), true
}

// commitAndCreatePR commits files to a branch and creates a PR. Shared by code and new_repo jobs.
func commitAndCreatePR(ctx context.Context, job *state.Job, resp agent.AgentResponse, owner, baseBranch, branch string, githubClient *github.Client, storageClient *storage.Client) (*github.PRResult, error) {
	files := make([]github.FileEntry, len(resp.Files))
	for i, f := range resp.Files {
		files[i] = github.FileEntry{
			Path:    f.Path,
			Content: []byte(f.Content),
		}
		resp.Files[i].Content = ""
	}

	commitMsg := fmt.Sprintf("feat: %s\n\nGenerated by Nemuri (job: %s)", resp.Title, job.JobID)
	if err := githubClient.CommitFiles(ctx, owner, resp.Repo, branch, commitMsg, files); err != nil {
		return nil, fmt.Errorf("commit files: %w", err)
	}

	// Save artifacts to S3 (best-effort)
	if storageClient != nil {
		artifactData, _ := json.Marshal(resp)
		if err := storageClient.UploadArtifact(ctx, job.JobID, "code_response.json", artifactData); err != nil {
			slog.Warn("failed to upload artifact to S3", "error", err, "job_id", job.JobID)
		}
	}

	pr, err := githubClient.CreatePR(ctx, github.PRInput{
		Owner:  owner,
		Repo:   resp.Repo,
		Base:   baseBranch,
		Branch: branch,
		Title:  resp.Title,
		Body:   fmt.Sprintf("%s\n\n---\nGenerated by Nemuri (job: `%s`)", resp.Description, job.JobID),
	})
	if err != nil {
		return nil, fmt.Errorf("create PR: %w", err)
	}
	slog.Info("PR created", "job_id", job.JobID, "pr_url", pr.URL, "pr_number", pr.Number)
	return pr, nil
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

func truncateJobID(jobID string) string {
	if len(jobID) < branchJobIDLen {
		return jobID
	}
	return jobID[:branchJobIDLen]
}

func truncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-3]) + "..."
}

const discordMessageLimit = 2000

// truncateDiscordMessage ensures a message fits within Discord's 2000-character limit.
// If truncation is needed, the message is cut and an ellipsis appended.
func truncateDiscordMessage(message string) string {
	runes := []rune(message)
	if len(runes) <= discordMessageLimit {
		return message
	}
	return string(runes[:discordMessageLimit-3]) + "..."
}

// buildPRNotificationMessage constructs a Discord message for PR creation.
// Truncation is handled by createThreadAndNotify, so this builds the full message.
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

// replaceToolResult finds the placeholder tool_result for the given toolUseID
// in the last user message and replaces its content with the real answer.
// Handles both in-memory typed slices ([]llm.ToolResultBlock) and
// JSON-deserialized generic types ([]any with map[string]any elements).
func replaceToolResult(messages []llm.Message, toolUseID, content string) {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "user" {
			continue
		}

		// Try typed slice first (in-memory path)
		if results, ok := messages[i].Content.([]llm.ToolResultBlock); ok {
			for j := range results {
				if results[j].ToolUseID == toolUseID {
					results[j].Content = content
					return
				}
			}
			continue
		}

		// Fallback: JSON-deserialized path ([]any with map[string]any elements)
		items, ok := messages[i].Content.([]any)
		if !ok {
			continue
		}
		for _, item := range items {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			id, _ := m["tool_use_id"].(string)
			if id == toolUseID {
				m["content"] = content
				return
			}
		}
	}
	slog.Warn("replaceToolResult: placeholder not found for tool_use_id", "tool_use_id", toolUseID)
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
