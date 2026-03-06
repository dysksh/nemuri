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
	// Extract prompt from command options
	prompt := extractPrompt(interaction)
	if prompt == "" {
		slog.Warn("no prompt provided")
		return respondJSON(http.StatusOK, discordResponse{
			Type: ResponseTypeChannelMessageWithSource,
			Data: &discordResponseData{Content: "promptを指定してください。"},
		})
	}

	// Generate job ID
	jobID := uuid.New().String()

	// Create job record in DynamoDB (state=INIT)
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

	// Build SQS message
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

	// Send to SQS
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

	// Return deferred ACK (type=5) — Discord shows "Bot is thinking..."
	return respondJSON(http.StatusOK, discordResponse{
		Type: ResponseTypeDeferredChannelMessageWithSource,
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
