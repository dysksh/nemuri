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
	agentRunner := agent.New(llmClient, githubClient, defaultGithubOwner)
	jobErr := executeJob(ctx, job, agentRunner, discordClient, githubClient, storageClient, defaultGithubOwner)

	// 5. Stop heartbeat
	heartbeatCancel()
	wg.Wait()

	// 6. Update final state
	if jobErr != nil {
		slog.Error("job failed", "error", jobErr, "job_id", jobID)
		if err := store.MarkFailed(ctx, jobID, workerID, jobErr.Error(), job.Version, job.State); err != nil {
			slog.Error("failed to mark job as failed", "error", err, "job_id", jobID)
		}
		// Notify user of failure (do not expose internal error details)
		if notifyErr := discordClient.SendResult(ctx, job.ApplicationID, job.InteractionToken, job.ChannelID,
			"ジョブの実行中にエラーが発生しました。管理者にお問い合わせください。(job_id: "+jobID+")"); notifyErr != nil {
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

func executeJob(ctx context.Context, job *state.Job, agentRunner *agent.Agent, discordClient *discord.Client, githubClient *github.Client, storageClient *storage.Client, defaultGithubOwner string) error {
	slog.Info("executing job", "job_id", job.JobID, "prompt", job.Prompt)

	runResult, err := agentRunner.Run(ctx, job.Prompt)
	if err != nil {
		return err
	}

	agentResp := runResult.Response
	slog.Info("agent completed",
		"job_id", job.JobID,
		"type", agentResp.Type,
		"iterations", runResult.Iterations,
		"input_tokens", runResult.TotalInputTokens,
		"output_tokens", runResult.TotalOutputTokens,
	)

	switch agentResp.Type {
	case agent.ResponseTypeCode:
		return executeCodeJob(ctx, job, *agentResp, defaultGithubOwner, discordClient, githubClient, storageClient)
	case agent.ResponseTypeNewRepo:
		return executeNewRepoJob(ctx, job, *agentResp, discordClient, githubClient, storageClient)
	case agent.ResponseTypeFile:
		if len(agentResp.Files) == 0 {
			slog.Warn("file response has no files, falling back to text", "job_id", job.JobID)
			return executeTextJob(ctx, job, agentResp.Content, discordClient)
		}
		return executeFileJob(ctx, job, *agentResp, discordClient, storageClient)
	default:
		return executeTextJob(ctx, job, agentResp.Content, discordClient)
	}
}

func executeTextJob(ctx context.Context, job *state.Job, content string, discordClient *discord.Client) error {
	if content == "" {
		content = "（結果を生成できませんでした。リクエストの内容を変えて再度お試しください。）"
	}
	if err := discordClient.SendResult(ctx, job.ApplicationID, job.InteractionToken, job.ChannelID, content); err != nil {
		return err
	}
	slog.Info("text result sent to Discord", "job_id", job.JobID)
	return nil
}

func executeCodeJob(ctx context.Context, job *state.Job, resp agent.AgentResponse, owner string, discordClient *discord.Client, githubClient *github.Client, storageClient *storage.Client) error {
	if githubClient == nil {
		return fmt.Errorf("code generation requested but GitHub client is not configured")
	}
	if owner == "" || resp.Repo == "" || len(resp.Files) == 0 {
		return fmt.Errorf("code response missing required fields (owner, repo, or files)")
	}

	branch := fmt.Sprintf("nemuri/%s", truncateJobID(job.JobID))
	slog.Info("creating code deliverable", "job_id", job.JobID, "owner", owner, "repo", resp.Repo, "branch", branch, "files", len(resp.Files))

	// 1. Get default branch and create feature branch
	defaultBranch, err := githubClient.GetDefaultBranch(ctx, owner, resp.Repo)
	if err != nil {
		return fmt.Errorf("get default branch: %w", err)
	}
	if err := githubClient.CreateBranch(ctx, owner, resp.Repo, defaultBranch, branch); err != nil {
		return fmt.Errorf("create branch: %w", err)
	}

	// 2. Commit files and create PR
	pr, err := commitAndCreatePR(ctx, job, resp, owner, defaultBranch, branch, githubClient, storageClient)
	if err != nil {
		return err
	}

	// 3. Notify Discord
	message := fmt.Sprintf("PRを作成しました: %s\n\n**%s**\n%s\n\n変更ファイル: %s",
		pr.URL, resp.Title, resp.Description, formatFilePaths(resp.Files))
	return discordClient.SendResult(ctx, job.ApplicationID, job.InteractionToken, job.ChannelID, message)
}

func executeNewRepoJob(ctx context.Context, job *state.Job, resp agent.AgentResponse, discordClient *discord.Client, githubClient *github.Client, storageClient *storage.Client) error {
	if githubClient == nil {
		return fmt.Errorf("new repo requested but GitHub client is not configured")
	}
	if resp.Repo == "" || len(resp.Files) == 0 {
		return fmt.Errorf("new_repo response missing required fields (repo or files)")
	}

	slog.Info("creating new repository", "job_id", job.JobID, "repo", resp.Repo)

	// 1. Create repository (always private, under authenticated user)
	repoResult, err := githubClient.CreateRepo(ctx, github.CreateRepoInput{
		Name:        resp.Repo,
		Description: resp.RepoDescription,
		Private:     true,
	})
	if err != nil {
		return fmt.Errorf("create repo: %w", err)
	}
	slog.Info("repository created", "job_id", job.JobID, "full_name", repoResult.FullName)

	// Extract owner/repo from the API result (e.g. "user/repo-name")
	parts := strings.SplitN(repoResult.FullName, "/", 2)
	if len(parts) != 2 {
		return fmt.Errorf("unexpected repo full_name format: %s", repoResult.FullName)
	}
	owner, repo := parts[0], parts[1]

	// rollbackRepo deletes the newly created repo on failure.
	rollbackRepo := func(cause error) error {
		slog.Warn("rolling back repository creation", "owner", owner, "repo", repo, "cause", cause)
		if delErr := githubClient.DeleteRepo(ctx, owner, repo); delErr != nil {
			slog.Error("failed to delete repo during rollback", "error", delErr)
		}
		return cause
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
	message := fmt.Sprintf("リポジトリを作成しました: %s\nPRを作成しました: %s\n\n**%s**\n%s\n\n変更ファイル: %s",
		repoResult.HTMLURL, pr.URL, resp.Title, resp.Description, formatFilePaths(resp.Files))
	return discordClient.SendResult(ctx, job.ApplicationID, job.InteractionToken, job.ChannelID, message)
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
	return discordClient.SendResult(ctx, job.ApplicationID, job.InteractionToken, job.ChannelID, message)
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

func truncateJobID(jobID string) string {
	if len(jobID) < branchJobIDLen {
		return jobID
	}
	return jobID[:branchJobIDLen]
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
