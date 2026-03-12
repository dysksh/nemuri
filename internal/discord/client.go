package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const (
	discordBaseURL      = "https://discord.com/api/v10"
	maxContentLen       = 2000 // Discord message content limit
	maxErrorResponseLen = 4096 // max bytes of API error response to read
)

// Client sends messages to Discord.
type Client struct {
	botToken   string
	httpClient *http.Client
}

// NewClient creates a new Discord client.
func NewClient(botToken string) *Client {
	return &Client{
		botToken:   botToken,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

type messagePayload struct {
	Content string `json:"content"`
}

type createThreadPayload struct {
	Name                string `json:"name"`
	Type                int    `json:"type"` // 11 = public thread
	AutoArchiveDuration int    `json:"auto_archive_duration,omitempty"`
}

type threadResponse struct {
	ID string `json:"id"`
}

// SendFollowUp sends a follow-up message using the interaction webhook.
// This works within 15 minutes of the original interaction.
func (c *Client) SendFollowUp(ctx context.Context, applicationID, interactionToken, content string) error {
	url := fmt.Sprintf("%s/webhooks/%s/%s", discordBaseURL, applicationID, interactionToken)
	return c.postMessage(ctx, url, content, false)
}

// SendChannelMessage sends a message to a channel using the bot token.
// Use this when the interaction token has expired.
func (c *Client) SendChannelMessage(ctx context.Context, channelID, content string) error {
	url := fmt.Sprintf("%s/channels/%s/messages", discordBaseURL, channelID)
	return c.postMessage(ctx, url, content, true)
}

// SendResult tries follow-up first, falls back to channel message on failure.
func (c *Client) SendResult(ctx context.Context, applicationID, interactionToken, channelID, content string) error {
	content = truncate(content, maxContentLen)

	followUpErr := c.SendFollowUp(ctx, applicationID, interactionToken, content)
	if followUpErr == nil {
		return nil
	}

	slog.Warn("follow-up failed, falling back to channel message", "error", followUpErr)

	// Fall back to bot token channel message
	return c.SendChannelMessage(ctx, channelID, content)
}

// CreateThread creates a new public thread in the given channel and posts an initial message.
// For GUILD_TEXT channels, the thread is created without a starter message, then the
// initial message is sent separately (the `message` field in thread creation is only
// supported for GUILD_FORUM / GUILD_MEDIA channels).
// Returns the thread ID (channel ID of the thread).
func (c *Client) CreateThread(ctx context.Context, channelID, name, initialMessage string) (string, error) {
	url := fmt.Sprintf("%s/channels/%s/threads", discordBaseURL, channelID)

	payload := createThreadPayload{
		Name:                name,
		Type:                11, // PUBLIC_THREAD
		AutoArchiveDuration: 1440,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bot "+c.botToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorResponseLen))

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("discord API error (%d): %s", resp.StatusCode, string(respBody))
	}

	var thread threadResponse
	if err := json.Unmarshal(respBody, &thread); err != nil {
		return "", fmt.Errorf("unmarshal thread response: %w", err)
	}

	// Post the initial message to the newly created thread
	if initialMessage != "" {
		if err := c.SendChannelMessage(ctx, thread.ID, initialMessage); err != nil {
			return thread.ID, fmt.Errorf("send initial thread message: %w", err)
		}
	}

	return thread.ID, nil
}

func (c *Client) postMessage(ctx context.Context, url, content string, useBotAuth bool) error {
	payload := messagePayload{Content: content}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if useBotAuth {
		req.Header.Set("Authorization", "Bot "+c.botToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorResponseLen))
		return fmt.Errorf("discord API error (%d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	suffix := "\n...(truncated)"
	return string(runes[:max-len([]rune(suffix))]) + suffix
}
