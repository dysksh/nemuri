package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// TreeEntry represents a file or directory in a repository tree.
type TreeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"` // "blob" or "tree"
	Size int    `json:"size,omitempty"`
}

// GetTree returns the full file tree of a repository at the given ref (branch/tag/SHA).
func (c *Client) GetTree(ctx context.Context, owner, repo, ref string) ([]TreeEntry, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/git/trees/%s?recursive=1", c.baseURL, owner, repo, ref)
	body, status, err := c.doAPI(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("get tree: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("get tree (%d): %s", status, truncateBody(body))
	}

	var result struct {
		Tree      []TreeEntry `json:"tree"`
		Truncated bool        `json:"truncated"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("get tree unmarshal: %w", err)
	}
	if result.Truncated {
		slog.Warn("repository tree was truncated by GitHub API", "owner", owner, "repo", repo, "ref", ref, "entries", len(result.Tree))
	}
	return result.Tree, nil
}

// GetFileContent returns the raw content of a file in a repository.
// Uses the GitHub Contents API (supports files up to 1 MB).
// Returns []byte to safely handle both text and binary files.
func (c *Client) GetFileContent(ctx context.Context, owner, repo, path, ref string) ([]byte, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/contents/%s?ref=%s", c.baseURL, owner, repo, path, ref)
	body, status, err := c.doAPI(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("get file content: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("get content (%d): %s", status, truncateBody(body))
	}

	var result struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		Size     int    `json:"size"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("get file content unmarshal: %w", err)
	}

	if result.Encoding != "base64" {
		return []byte(result.Content), nil
	}

	// GitHub base64 content includes line breaks
	cleaned := strings.ReplaceAll(result.Content, "\n", "")
	decoded, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		return nil, fmt.Errorf("decode base64: %w", err)
	}
	return decoded, nil
}
