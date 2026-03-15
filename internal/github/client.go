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

// API is the interface for GitHub operations. Implemented by Client.
type API interface {
	GetDefaultBranch(ctx context.Context, owner, repo string) (string, error)
	CreateBranch(ctx context.Context, owner, repo, baseBranch, newBranch string) error
	CommitFiles(ctx context.Context, owner, repo, branch, message string, files []FileEntry) error
	CreatePR(ctx context.Context, input PRInput) (*PRResult, error)
	CreateRepo(ctx context.Context, input CreateRepoInput) (*CreateRepoResult, error)
	DeleteRepo(ctx context.Context, owner, repo string) error
	WaitForRepoReady(ctx context.Context, owner, repo, defaultBranch string) error
	GetTree(ctx context.Context, owner, repo, ref string) ([]TreeEntry, error)
	GetFileContent(ctx context.Context, owner, repo, path, ref string) ([]byte, error)
}

// Client interacts with the GitHub API using a Fine-grained Personal Access Token.
type Client struct {
	token      string
	baseURL    string // defaults to githubAPIBase
	httpClient *http.Client
}

// NewClient creates a GitHub client from a Fine-grained PAT.
func NewClient(token string) *Client {
	return &Client{
		token:      token,
		baseURL:    githubAPIBase,
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
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("send request: %w", err)
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
