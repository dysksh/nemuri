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
)

// Discord interaction types
const (
	InteractionTypePing               = 1
	InteractionTypeApplicationCommand = 2
)

// Discord interaction response types
const (
	ResponseTypePong                        = 1
	ResponseTypeChannelMessageWithSource    = 4
	ResponseTypeDeferredChannelMessageWithSource = 5
)

type discordInteraction struct {
	Type int             `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

type discordResponse struct {
	Type int                    `json:"type"`
	Data *discordResponseData   `json:"data,omitempty"`
}

type discordResponseData struct {
	Content string `json:"content"`
}

var publicKey ed25519.PublicKey

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
		slog.Info("received application command, responding")
		return respondJSON(http.StatusOK, discordResponse{
			Type: ResponseTypeChannelMessageWithSource,
			Data: &discordResponseData{
				Content: "受け付けました（現在未実装です）",
			},
		})

	default:
		slog.Warn("unknown interaction type", "type", interaction.Type)
		return respond(http.StatusBadRequest, fmt.Sprintf(`{"error":"unknown interaction type: %d"}`, interaction.Type))
	}
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
