package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	"github.com/nemuri/nemuri/internal/state"
)

// Discord interaction types
const (
	InteractionTypePing               = 1
	InteractionTypeApplicationCommand = 2
)

// Discord interaction response types
const (
	ResponseTypePong                             = 1
	ResponseTypeChannelMessageWithSource         = 4
	ResponseTypeDeferredChannelMessageWithSource = 5
)

type discordInteraction struct {
	Type          int                 `json:"type"`
	ID            string              `json:"id"`
	Token         string              `json:"token"`
	ApplicationID string              `json:"application_id"`
	ChannelID     string              `json:"channel_id"`
	Data          *discordCommandData `json:"data,omitempty"`
}

type discordCommandData struct {
	Name    string                 `json:"name"`
	Options []discordCommandOption `json:"options,omitempty"`
}

type discordCommandOption struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type discordResponse struct {
	Type int                  `json:"type"`
	Data *discordResponseData `json:"data,omitempty"`
}

type discordResponseData struct {
	Content string `json:"content"`
}

// SQS message payload
type sqsJobMessage struct {
	JobID            string `json:"job_id"`
	Prompt           string `json:"prompt"`
	InteractionToken string `json:"interaction_token"`
	ChannelID        string `json:"channel_id"`
	ApplicationID    string `json:"application_id"`
}

var (
	publicKey   ed25519.PublicKey
	sqsClient   *sqs.Client
	sqsQueueURL string
	jobStore    *state.Store
)

func init() {
	keyHex := os.Getenv("DISCORD_PUBLIC_KEY")
	if keyHex == "" {
		slog.Error("DISCORD_PUBLIC_KEY is not set")
		os.Exit(1)
	}

	var err error
	publicKey, err = hex.DecodeString(keyHex)
	if err != nil {
		slog.Error("failed to decode DISCORD_PUBLIC_KEY", "error", err)
		os.Exit(1)
	}

	sqsQueueURL = os.Getenv("SQS_QUEUE_URL")
	if sqsQueueURL == "" {
		slog.Error("SQS_QUEUE_URL is not set")
		os.Exit(1)
	}

	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		slog.Error("failed to load AWS config", "error", err)
		os.Exit(1)
	}
	sqsClient = sqs.NewFromConfig(cfg)

	dynamoTableName := os.Getenv("DYNAMODB_TABLE_NAME")
	if dynamoTableName == "" {
		slog.Error("DYNAMODB_TABLE_NAME is not set")
		os.Exit(1)
	}
	jobStore = state.NewStore(dynamodb.NewFromConfig(cfg), dynamoTableName)
}

func handler(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	// Verify Discord signature
	signature := getHeader(req.Headers, "x-signature-ed25519")
	timestamp := getHeader(req.Headers, "x-signature-timestamp")

	if !verifySignature(publicKey, signature, timestamp, req.Body) {
		slog.Warn("invalid signature")
		return respond(http.StatusUnauthorized, `{"error":"invalid request signature"}`)
	}

	// Parse interaction
	var interaction discordInteraction
	if err := json.Unmarshal([]byte(req.Body), &interaction); err != nil {
		slog.Error("failed to parse interaction", "error", err)
		return respond(http.StatusBadRequest, `{"error":"invalid request body"}`)
	}

	switch interaction.Type {
	case InteractionTypePing:
		slog.Info("received PING, responding with PONG")
		return respondJSON(http.StatusOK, discordResponse{Type: ResponseTypePong})

	case InteractionTypeApplicationCommand:
		return handleApplicationCommand(ctx, interaction)

	default:
		slog.Warn("unknown interaction type", "type", interaction.Type)
		return respond(http.StatusBadRequest, fmt.Sprintf(`{"error":"unknown interaction type: %d"}`, interaction.Type))
	}
}

func handleApplicationCommand(ctx context.Context, interaction discordInteraction) (events.APIGatewayV2HTTPResponse, error) {
	prompt := extractPrompt(interaction)
	if prompt == "" {
		slog.Warn("no prompt provided")
		return respondJSON(http.StatusOK, discordResponse{
			Type: ResponseTypeChannelMessageWithSource,
			Data: &discordResponseData{Content: "promptを指定してください。"},
		})
	}

	// Check if this is an "approve" command in a thread with a waiting job
	if strings.TrimSpace(strings.ToLower(prompt)) == "approve" {
		return handleApprove(ctx, interaction)
	}

	// Check if this channel_id is a thread with a waiting job
	existingJob, err := jobStore.QueryByThreadID(ctx, interaction.ChannelID)
	if err != nil {
		slog.Warn("failed to query by thread_id", "error", err, "channel_id", interaction.ChannelID)
		// Fall through to create a new job
	}

	if existingJob != nil && existingJob.State == state.StateWaitingUserInput {
		return handleResume(ctx, interaction, existingJob, prompt)
	}

	// Normal flow: create a new job
	return handleNewJob(ctx, interaction, prompt)
}

func handleNewJob(ctx context.Context, interaction discordInteraction, prompt string) (events.APIGatewayV2HTTPResponse, error) {
	jobID := uuid.New().String()

	err := jobStore.CreateJob(ctx, state.CreateJobInput{
		JobID:            jobID,
		Prompt:           prompt,
		ChannelID:        interaction.ChannelID,
		InteractionToken: interaction.Token,
		ApplicationID:    interaction.ApplicationID,
	})
	if err != nil {
		slog.Error("failed to create job record", "error", err, "job_id", jobID)
		return respondJSON(http.StatusOK, discordResponse{
			Type: ResponseTypeChannelMessageWithSource,
			Data: &discordResponseData{Content: "ジョブの登録に失敗しました。しばらくしてから再度お試しください。"},
		})
	}

	msg := sqsJobMessage{
		JobID:            jobID,
		Prompt:           prompt,
		InteractionToken: interaction.Token,
		ChannelID:        interaction.ChannelID,
		ApplicationID:    interaction.ApplicationID,
	}

	msgBytes, err := json.Marshal(msg)
	if err != nil {
		slog.Error("failed to marshal SQS message", "error", err)
		return respond(http.StatusInternalServerError, `{"error":"internal server error"}`)
	}

	_, err = sqsClient.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(sqsQueueURL),
		MessageBody: aws.String(string(msgBytes)),
	})
	if err != nil {
		slog.Error("failed to send SQS message", "error", err, "job_id", jobID)
		return respondJSON(http.StatusOK, discordResponse{
			Type: ResponseTypeChannelMessageWithSource,
			Data: &discordResponseData{Content: "ジョブの登録に失敗しました。しばらくしてから再度お試しください。"},
		})
	}

	slog.Info("job enqueued", "job_id", jobID, "prompt", prompt)

	return respondJSON(http.StatusOK, discordResponse{
		Type: ResponseTypeDeferredChannelMessageWithSource,
	})
}

func handleResume(ctx context.Context, interaction discordInteraction, existingJob *state.Job, userResponse string) (events.APIGatewayV2HTTPResponse, error) {
	if strings.TrimSpace(userResponse) == "" {
		return respondJSON(http.StatusOK, discordResponse{
			Type: ResponseTypeChannelMessageWithSource,
			Data: &discordResponseData{Content: "回答が空です。`/agent <回答>` の形式で入力してください。"},
		})
	}

	slog.Info("resuming job with user response",
		"job_id", existingJob.JobID,
		"thread_id", interaction.ChannelID,
		"user_response", userResponse,
	)

	// Save user response to DynamoDB
	if err := jobStore.SetUserResponse(ctx, existingJob.JobID, userResponse, interaction.Token); err != nil {
		slog.Error("failed to set user response", "error", err, "job_id", existingJob.JobID)
		return respondJSON(http.StatusOK, discordResponse{
			Type: ResponseTypeChannelMessageWithSource,
			Data: &discordResponseData{Content: "回答の保存に失敗しました。しばらくしてから再度お試しください。"},
		})
	}

	// Enqueue resume message to SQS.
	// Note: ChannelID here is the thread ID (Discord sets channel_id to the thread ID
	// for interactions within a thread). ECS reads the canonical ChannelID from DynamoDB,
	// so this value is not used for routing — it's included for logging/diagnostics only.
	msg := sqsJobMessage{
		JobID:            existingJob.JobID,
		Prompt:           existingJob.Prompt,
		InteractionToken: interaction.Token,
		ChannelID:        interaction.ChannelID,
		ApplicationID:    interaction.ApplicationID,
	}

	msgBytes, err := json.Marshal(msg)
	if err != nil {
		slog.Error("failed to marshal SQS message", "error", err)
		return respond(http.StatusInternalServerError, `{"error":"internal server error"}`)
	}

	_, err = sqsClient.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(sqsQueueURL),
		MessageBody: aws.String(string(msgBytes)),
	})
	if err != nil {
		slog.Error("failed to send resume SQS message", "error", err, "job_id", existingJob.JobID)
		return respondJSON(http.StatusOK, discordResponse{
			Type: ResponseTypeChannelMessageWithSource,
			Data: &discordResponseData{Content: "ジョブの再開に失敗しました。しばらくしてから再度お試しください。"},
		})
	}

	slog.Info("resume job enqueued", "job_id", existingJob.JobID)

	return respondJSON(http.StatusOK, discordResponse{
		Type: ResponseTypeDeferredChannelMessageWithSource,
	})
}

func handleApprove(ctx context.Context, interaction discordInteraction) (events.APIGatewayV2HTTPResponse, error) {
	existingJob, err := jobStore.QueryByThreadID(ctx, interaction.ChannelID)
	if err != nil {
		slog.Warn("failed to query by thread_id for approve", "error", err, "channel_id", interaction.ChannelID)
		return respondJSON(http.StatusOK, discordResponse{
			Type: ResponseTypeChannelMessageWithSource,
			Data: &discordResponseData{Content: "このスレッドに対応するジョブが見つかりませんでした。"},
		})
	}

	if existingJob == nil || existingJob.State != state.StateWaitingApproval {
		return respondJSON(http.StatusOK, discordResponse{
			Type: ResponseTypeChannelMessageWithSource,
			Data: &discordResponseData{Content: "承認待ちのジョブがありません。"},
		})
	}

	if err := jobStore.ApproveJob(ctx, existingJob.JobID); err != nil {
		slog.Error("failed to approve job", "error", err, "job_id", existingJob.JobID)
		return respondJSON(http.StatusOK, discordResponse{
			Type: ResponseTypeChannelMessageWithSource,
			Data: &discordResponseData{Content: "ジョブの承認に失敗しました。"},
		})
	}

	slog.Info("job approved", "job_id", existingJob.JobID)

	return respondJSON(http.StatusOK, discordResponse{
		Type: ResponseTypeChannelMessageWithSource,
		Data: &discordResponseData{Content: fmt.Sprintf("ジョブを承認しました。(job_id: %s)", existingJob.JobID)},
	})
}

func extractPrompt(interaction discordInteraction) string {
	if interaction.Data == nil {
		return ""
	}
	for _, opt := range interaction.Data.Options {
		if opt.Name == "prompt" {
			return opt.Value
		}
	}
	return ""
}

func getHeader(headers map[string]string, key string) string {
	if v, ok := headers[key]; ok {
		return v
	}
	for k, v := range headers {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

func verifySignature(pubKey ed25519.PublicKey, signature, timestamp, body string) bool {
	if signature == "" || timestamp == "" {
		return false
	}

	sig, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}

	msg := []byte(timestamp + body)
	return ed25519.Verify(pubKey, msg, sig)
}

func respond(status int, body string) (events.APIGatewayV2HTTPResponse, error) {
	return events.APIGatewayV2HTTPResponse{
		StatusCode: status,
		Headers:    map[string]string{"Content-Type": "application/json"},
		Body:       body,
	}, nil
}

func respondJSON(status int, v any) (events.APIGatewayV2HTTPResponse, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return respond(http.StatusInternalServerError, `{"error":"internal server error"}`)
	}
	return respond(status, string(body))
}

func main() {
	lambda.Start(handler)
}
