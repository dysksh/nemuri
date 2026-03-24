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

	// Proactive rate limit thresholds: sleep until reset if remaining drops below these.
	rateLimitTokenThreshold   = 1000
	rateLimitRequestThreshold = 2
	rateLimitMaxWait          = 60 * time.Second // cap proactive sleep to avoid excessive blocking
)

const (
	anthropicAPIURL     = "https://api.anthropic.com/v1/messages"
	anthropicAPIVersion = "2023-06-01"
	defaultModel        = "claude-sonnet-4-6"
	defaultMaxTokens    = 16384
	maxResponseBytes    = 10 * 1024 * 1024 // 10 MB

	// ModelOpus is the Claude Opus 4.6 model identifier.
	ModelOpus = "claude-opus-4-6"
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
// If model is empty, the default model is used.
func NewClaudeClient(apiKey, model string) Client {
	if model == "" {
		model = defaultModel
	}
	return NewClaudeClientWithURL(apiKey, model, anthropicAPIURL)
}

// NewClaudeClientWithURL creates a Claude API client with a custom API endpoint.
func NewClaudeClientWithURL(apiKey, model, apiURL string) Client {
	if model == "" {
		model = defaultModel
	}
	return &ClaudeClient{
		apiKey:     apiKey,
		apiURL:     apiURL,
		model:      model,
		maxTokens:  defaultMaxTokens,
		httpClient: &http.Client{Timeout: 5 * time.Minute},
	}
}

// cacheControl marks a content block as a prompt cache breakpoint.
type cacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

var ephemeralCache = &cacheControl{Type: "ephemeral"}

// systemBlock is a content block in the system prompt array.
type systemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

// apiMessage is an API-level message with role and content.
// Unlike llm.Message, this type is used only for JSON serialization to the Anthropic API,
// and may contain content blocks with cache_control.
type apiMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// apiRequest is the request body for the Anthropic Messages API.
type apiRequest struct {
	Model      string           `json:"model"`
	MaxTokens  int              `json:"max_tokens"`
	System     []systemBlock    `json:"system,omitempty"`
	Messages   []apiMessage     `json:"messages"`
	Tools      []ToolDefinition `json:"tools,omitempty"`
	ToolChoice *ToolChoice      `json:"tool_choice,omitempty"`
}

// apiResponse is the response body from the Anthropic Messages API.
type apiResponse struct {
	Content    []apiContentBlock `json:"content"`
	StopReason string            `json:"stop_reason"`
	Usage      struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
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
	disableCache := opts != nil && opts.DisableCache
	reqBody := apiRequest{
		Model:     c.model,
		MaxTokens: c.maxTokens,
		System:    buildSystemBlocksOpt(systemPrompt, !disableCache),
		Messages:  buildMessagesOpt(messages, !disableCache),
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
			// Proactive rate limit avoidance: if remaining tokens/requests are low,
			// sleep until the reset time before returning.
			if wait := rateLimitWait(resp.Header); wait > 0 {
				slog.Info("proactive rate limit sleep", "wait_sec", wait.Seconds())
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(wait):
				}
			}
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
		InputTokens:              apiResp.Usage.InputTokens,
		OutputTokens:             apiResp.Usage.OutputTokens,
		CacheCreationInputTokens: apiResp.Usage.CacheCreationInputTokens,
		CacheReadInputTokens:     apiResp.Usage.CacheReadInputTokens,
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

// rateLimitWait checks the Anthropic rate limit headers on a successful response.
// If any of the remaining counters (input tokens, output tokens, requests) are below
// their threshold, it returns the duration to wait until the corresponding reset time.
// Returns 0 if no waiting is needed.
func rateLimitWait(h http.Header) time.Duration {
	type limit struct {
		remaining string
		reset     string
		threshold int
	}
	limits := []limit{
		{"anthropic-ratelimit-input-tokens-remaining", "anthropic-ratelimit-input-tokens-reset", rateLimitTokenThreshold},
		{"anthropic-ratelimit-output-tokens-remaining", "anthropic-ratelimit-output-tokens-reset", rateLimitTokenThreshold},
		{"anthropic-ratelimit-requests-remaining", "anthropic-ratelimit-requests-reset", rateLimitRequestThreshold},
	}

	var maxWait time.Duration
	for _, l := range limits {
		remStr := h.Get(l.remaining)
		if remStr == "" {
			continue
		}
		rem, err := strconv.Atoi(remStr)
		if err != nil || rem >= l.threshold {
			continue
		}

		resetStr := h.Get(l.reset)
		if resetStr == "" {
			continue
		}
		resetAt, err := time.Parse(time.RFC3339, resetStr)
		if err != nil {
			continue
		}
		if wait := time.Until(resetAt); wait > maxWait {
			maxWait = min(wait, rateLimitMaxWait)
		}
	}
	return maxWait
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

// buildSystemBlocksOpt converts a system prompt string to content blocks.
// When cache is true, adds cache_control for prompt caching.
func buildSystemBlocksOpt(systemPrompt string, cache bool) []systemBlock {
	if systemPrompt == "" {
		return nil
	}
	block := systemBlock{Type: "text", Text: systemPrompt}
	if cache {
		block.CacheControl = ephemeralCache
	}
	return []systemBlock{block}
}

// buildMessagesOpt converts messages for the API.
// When cache is true, adds cache_control to the last message for prefix caching.
func buildMessagesOpt(messages []Message, cache bool) []apiMessage {
	if len(messages) == 0 {
		return nil
	}

	result := make([]apiMessage, len(messages))
	for i, msg := range messages {
		if cache && i == len(messages)-1 {
			result[i] = buildCachedMessage(msg)
		} else {
			result[i] = apiMessage(msg)
		}
	}
	return result
}

// buildCachedMessage adds cache_control to the last content block of a message.
func buildCachedMessage(msg Message) apiMessage {
	cc := map[string]string{"type": "ephemeral"}

	switch v := msg.Content.(type) {
	case string:
		return apiMessage{
			Role: msg.Role,
			Content: []map[string]any{
				{"type": "text", "text": v, "cache_control": cc},
			},
		}
	case json.RawMessage:
		var blocks []map[string]any
		if err := json.Unmarshal(v, &blocks); err != nil || len(blocks) == 0 {
			return apiMessage(msg)
		}
		blocks[len(blocks)-1]["cache_control"] = cc
		return apiMessage{Role: msg.Role, Content: blocks}
	case []ToolResultBlock:
		if len(v) == 0 {
			return apiMessage(msg)
		}
		blocks := make([]map[string]any, len(v))
		for i, r := range v {
			block := map[string]any{
				"type":        r.Type,
				"tool_use_id": r.ToolUseID,
				"content":     r.Content,
			}
			if r.IsError {
				block["is_error"] = true
			}
			if i == len(v)-1 {
				block["cache_control"] = cc
			}
			blocks[i] = block
		}
		return apiMessage{Role: msg.Role, Content: blocks}
	default:
		return apiMessage(msg)
	}
}
