package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"
)

const (
	maxRetries       = 5
	initialBackoffMs = 1000 // 1 second
)

const (
	anthropicAPIURL     = "https://api.anthropic.com/v1/messages"
	anthropicAPIVersion = "2023-06-01"
	defaultModel        = "claude-sonnet-4-6"
	defaultMaxTokens    = 16384
	maxResponseBytes    = 10 * 1024 * 1024 // 10 MB
)

// ClaudeClient implements the Client interface using the Anthropic Messages API.
type ClaudeClient struct {
	apiKey     string
	apiURL     string
	model      string
	maxTokens  int
	httpClient *http.Client
}

// NewClaudeClient creates a new Claude API client.
func NewClaudeClient(apiKey string) Client {
	return NewClaudeClientWithURL(apiKey, anthropicAPIURL)
}

// NewClaudeClientWithURL creates a Claude API client with a custom API endpoint.
func NewClaudeClientWithURL(apiKey, apiURL string) Client {
	return &ClaudeClient{
		apiKey:     apiKey,
		apiURL:     apiURL,
		model:      defaultModel,
		maxTokens:  defaultMaxTokens,
		httpClient: &http.Client{Timeout: 5 * time.Minute},
	}
}

// apiRequest is the request body for the Anthropic Messages API.
type apiRequest struct {
	Model      string           `json:"model"`
	MaxTokens  int              `json:"max_tokens"`
	System     string           `json:"system,omitempty"`
	Messages   []Message        `json:"messages"`
	Tools      []ToolDefinition `json:"tools,omitempty"`
	ToolChoice *ToolChoice      `json:"tool_choice,omitempty"`
}

// apiResponse is the response body from the Anthropic Messages API.
type apiResponse struct {
	Content    []apiContentBlock `json:"content"`
	StopReason string            `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type apiContentBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id,omitempty"`
	Text  string          `json:"text,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// apiError is the error response from the Anthropic API.
type apiError struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *ClaudeClient) SendMessage(ctx context.Context, systemPrompt string, messages []Message, opts *SendOptions) (*Response, error) {
	reqBody := apiRequest{
		Model:     c.model,
		MaxTokens: c.maxTokens,
		System:    systemPrompt,
		Messages:  messages,
	}
	if opts != nil {
		reqBody.Tools = opts.Tools
		reqBody.ToolChoice = opts.ToolChoice
		if opts.MaxTokens > 0 {
			reqBody.MaxTokens = opts.MaxTokens
		}
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	var respBody []byte
	for attempt := range maxRetries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		var req *http.Request
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", c.apiKey)
		req.Header.Set("anthropic-version", anthropicAPIVersion)

		var resp *http.Response
		resp, err = c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("send request: %w", err)
		}

		respBody, err = io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read response: %w", err)
		}
		if len(respBody) > maxResponseBytes {
			return nil, fmt.Errorf("response too large (exceeded %d bytes)", maxResponseBytes)
		}

		if resp.StatusCode == http.StatusOK {
			break
		}

		// Retry on 429 (rate limit) and 529 (overloaded)
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == 529 {
			if attempt == maxRetries-1 {
				var apiErr apiError
				if jsonErr := json.Unmarshal(respBody, &apiErr); jsonErr == nil && apiErr.Error.Message != "" {
					return nil, fmt.Errorf("claude API error (%d) after %d retries: %s: %s", resp.StatusCode, maxRetries, apiErr.Error.Type, apiErr.Error.Message)
				}
				return nil, fmt.Errorf("claude API error (%d) after %d retries: %s", resp.StatusCode, maxRetries, string(respBody))
			}

			backoff := retryBackoff(attempt, resp.Header.Get("retry-after"))
			slog.Warn("rate limited, retrying", "status", resp.StatusCode, "attempt", attempt+1, "backoff_sec", backoff.Seconds())
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
			continue
		}

		// Non-retryable error
		var apiErr apiError
		if jsonErr := json.Unmarshal(respBody, &apiErr); jsonErr == nil && apiErr.Error.Message != "" {
			return nil, fmt.Errorf("claude API error (%d): %s: %s", resp.StatusCode, apiErr.Error.Type, apiErr.Error.Message)
		}
		return nil, fmt.Errorf("claude API error (%d): %s", resp.StatusCode, string(respBody))
	}

	var apiResp apiResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	// If the response was truncated, the tool call JSON may be incomplete
	if apiResp.StopReason == "max_tokens" {
		return nil, fmt.Errorf("response truncated: max_tokens (%d) reached, increase limit or simplify the request", c.maxTokens)
	}

	// Serialize raw content blocks for conversation history
	rawContent, err := json.Marshal(apiResp.Content)
	if err != nil {
		return nil, fmt.Errorf("marshal raw content: %w", err)
	}

	usage := Usage{
		InputTokens:  apiResp.Usage.InputTokens,
		OutputTokens: apiResp.Usage.OutputTokens,
	}

	// Collect all tool_use blocks
	var toolCalls []ToolCall
	for _, block := range apiResp.Content {
		if block.Type == "tool_use" {
			toolCalls = append(toolCalls, ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				InputJSON: string(block.Input),
			})
		}
	}

	if len(toolCalls) > 0 {
		return &Response{
			ToolCalls:  toolCalls,
			RawContent: rawContent,
			Usage:      usage,
		}, nil
	}

	var text string
	for _, block := range apiResp.Content {
		if block.Type == "text" {
			text += block.Text
		}
	}

	if text == "" {
		return nil, fmt.Errorf("claude returned empty response (stop_reason: %s)", apiResp.StopReason)
	}

	return &Response{Content: text, RawContent: rawContent, Usage: usage}, nil
}

// retryBackoff calculates the wait duration for a retry attempt.
// Uses the Retry-After header if present, otherwise exponential backoff.
func retryBackoff(attempt int, retryAfter string) time.Duration {
	if retryAfter != "" {
		if secs, err := strconv.Atoi(retryAfter); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	backoffMs := float64(initialBackoffMs) * math.Pow(2, float64(attempt))
	return time.Duration(backoffMs) * time.Millisecond
}
