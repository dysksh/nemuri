package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	githubAPIBase = "https://api.github.com"

	httpTimeout     = 30 * time.Second
	maxErrorBodyLen = 512 // max bytes of API response body included in error messages
)

// Client interacts with the GitHub API using a Fine-grained Personal Access Token.
type Client struct {
	token      string
	httpClient *http.Client
}

// NewClient creates a GitHub client from a Fine-grained PAT.
func NewClient(token string) *Client {
	return &Client{
		token:      token,
		httpClient: &http.Client{Timeout: httpTimeout},
	}
}

// doAPI performs an authenticated GitHub API request.
func (c *Client) doAPI(ctx context.Context, method, url string, reqBody any) ([]byte, int, error) {
	var bodyReader io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response body: %w", err)
	}
	return body, resp.StatusCode, nil
}

// truncateBody returns a string representation of body, truncated to maxErrorBodyLen bytes.
func truncateBody(body []byte) string {
	if len(body) <= maxErrorBodyLen {
		return string(body)
	}
	return string(body[:maxErrorBodyLen]) + "...(truncated)"
}
