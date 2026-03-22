package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const (
	newRepoMaxRetries   = 10
	newRepoPollInterval = 1 * time.Second
	gitFileMode         = "100644" // standard file mode for git blobs
)

// PRInput holds parameters for creating a pull request.
type PRInput struct {
	Owner  string
	Repo   string
	Base   string // base branch (e.g. "main")
	Branch string // head branch
	Title  string
	Body   string
}

// PRResult holds the result of a created pull request.
type PRResult struct {
	URL    string `json:"html_url"`
	Number int    `json:"number"`
}

// FileEntry represents a file to commit.
type FileEntry struct {
	Path    string
	Content []byte
}

// CreateRepoInput holds parameters for creating a repository under the authenticated user.
type CreateRepoInput struct {
	Name        string
	Description string
	Private     bool
}

// CreateRepoResult holds the result of a created repository.
type CreateRepoResult struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	HTMLURL       string `json:"html_url"`
}

// CreateRepo creates a new private repository under the authenticated user.
func (c *Client) CreateRepo(ctx context.Context, input CreateRepoInput) (*CreateRepoResult, error) {
	url := fmt.Sprintf("%s/user/repos", c.baseURL)

	body, status, err := c.doAPI(ctx, http.MethodPost, url, map[string]any{
		"name":        input.Name,
		"description": input.Description,
		"private":     input.Private,
		"auto_init":   true, // create initial commit so we can branch from it
	})
	if err != nil {
		return nil, fmt.Errorf("create repo: %w", err)
	}
	if status != http.StatusCreated {
		return nil, fmt.Errorf("create repo (%d): %s", status, truncateBody(body))
	}

	var result CreateRepoResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("create repo unmarshal: %w", err)
	}
	return &result, nil
}

// DeleteRepo deletes a repository. Used for rollback when post-creation steps fail.
func (c *Client) DeleteRepo(ctx context.Context, owner, repo string) error {
	url := fmt.Sprintf("%s/repos/%s/%s", c.baseURL, owner, repo)
	body, status, err := c.doAPI(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("delete repo: %w", err)
	}
	if status != http.StatusNoContent {
		return fmt.Errorf("delete repo (%d): %s", status, truncateBody(body))
	}
	return nil
}

// WaitForRepoReady polls the default branch ref until it exists.
// This is needed after CreateRepo with auto_init, as the initial commit may not be immediately available.
func (c *Client) WaitForRepoReady(ctx context.Context, owner, repo, defaultBranch string) error {
	refURL := fmt.Sprintf("%s/repos/%s/%s/git/refs/heads/%s", c.baseURL, owner, repo, defaultBranch)

	for range newRepoMaxRetries {
		_, status, err := c.doAPI(ctx, http.MethodGet, refURL, nil)
		if err != nil {
			return fmt.Errorf("wait for repo ready: %w", err)
		}
		if status == http.StatusOK {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(newRepoPollInterval):
		}
	}
	return fmt.Errorf("repo %s/%s default branch not ready after retries", owner, repo)
}

// GetDefaultBranch returns the default branch name for a repo.
func (c *Client) GetDefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s", c.baseURL, owner, repo)
	body, status, err := c.doAPI(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("get default branch: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("get repo (%d): %s", status, truncateBody(body))
	}

	var result struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("get default branch unmarshal: %w", err)
	}
	return result.DefaultBranch, nil
}

// CreateBranch creates a new branch from the tip of the base branch.
func (c *Client) CreateBranch(ctx context.Context, owner, repo, baseBranch, newBranch string) error {
	// Get the SHA of the base branch
	refURL := fmt.Sprintf("%s/repos/%s/%s/git/refs/heads/%s", c.baseURL, owner, repo, baseBranch)
	body, status, err := c.doAPI(ctx, http.MethodGet, refURL, nil)
	if err != nil {
		return fmt.Errorf("create branch get ref: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("get ref (%d): %s", status, truncateBody(body))
	}

	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := json.Unmarshal(body, &ref); err != nil {
		return fmt.Errorf("create branch unmarshal ref: %w", err)
	}

	// Create new branch ref
	createURL := fmt.Sprintf("%s/repos/%s/%s/git/refs", c.baseURL, owner, repo)
	body, status, err = c.doAPI(ctx, http.MethodPost, createURL, map[string]string{
		"ref": "refs/heads/" + newBranch,
		"sha": ref.Object.SHA,
	})
	if err != nil {
		return fmt.Errorf("create branch ref: %w", err)
	}
	if status != http.StatusCreated {
		return fmt.Errorf("create branch ref (%d): %s", status, truncateBody(body))
	}
	return nil
}

// CommitFiles commits multiple files to a branch using the Git Data API.
func (c *Client) CommitFiles(ctx context.Context, owner, repo, branch, message string, files []FileEntry) error {
	// 1. Get the current commit SHA of the branch
	refURL := fmt.Sprintf("%s/repos/%s/%s/git/refs/heads/%s", c.baseURL, owner, repo, branch)
	body, status, err := c.doAPI(ctx, http.MethodGet, refURL, nil)
	if err != nil {
		return fmt.Errorf("commit get branch ref: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("get branch ref (%d): %s", status, truncateBody(body))
	}

	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := json.Unmarshal(body, &ref); err != nil {
		return fmt.Errorf("commit unmarshal branch ref: %w", err)
	}

	// 2. Get the tree SHA of that commit
	commitURL := fmt.Sprintf("%s/repos/%s/%s/git/commits/%s", c.baseURL, owner, repo, ref.Object.SHA)
	body, status, err = c.doAPI(ctx, http.MethodGet, commitURL, nil)
	if err != nil {
		return fmt.Errorf("commit get tree: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("get commit (%d): %s", status, truncateBody(body))
	}

	var commit struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := json.Unmarshal(body, &commit); err != nil {
		return fmt.Errorf("commit unmarshal tree: %w", err)
	}

	// 3. Create blobs for each file
	type treeEntry struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
		Type string `json:"type"`
		SHA  string `json:"sha"`
	}
	entries := make([]treeEntry, 0, len(files))

	for _, f := range files {
		if f.Path == "" {
			return fmt.Errorf("file entry has empty path")
		}
		blobURL := fmt.Sprintf("%s/repos/%s/%s/git/blobs", c.baseURL, owner, repo)
		body, status, err = c.doAPI(ctx, http.MethodPost, blobURL, map[string]string{
			"content":  base64.StdEncoding.EncodeToString(f.Content),
			"encoding": "base64",
		})
		if err != nil {
			return fmt.Errorf("create blob for %s: %w", f.Path, err)
		}
		if status != http.StatusCreated {
			return fmt.Errorf("create blob for %s (%d): %s", f.Path, status, truncateBody(body))
		}

		var blob struct {
			SHA string `json:"sha"`
		}
		if err := json.Unmarshal(body, &blob); err != nil {
			return fmt.Errorf("unmarshal blob for %s: %w", f.Path, err)
		}

		entries = append(entries, treeEntry{
			Path: f.Path,
			Mode: gitFileMode,
			Type: "blob",
			SHA:  blob.SHA,
		})
	}

	// 4. Create a new tree
	treeURL := fmt.Sprintf("%s/repos/%s/%s/git/trees", c.baseURL, owner, repo)
	body, status, err = c.doAPI(ctx, http.MethodPost, treeURL, map[string]any{
		"base_tree": commit.Tree.SHA,
		"tree":      entries,
	})
	if err != nil {
		return fmt.Errorf("create tree: %w", err)
	}
	if status != http.StatusCreated {
		return fmt.Errorf("create tree (%d): %s", status, truncateBody(body))
	}

	var tree struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(body, &tree); err != nil {
		return fmt.Errorf("unmarshal tree: %w", err)
	}

	// 5. Create a new commit (retry on 403 for newly created repos where permissions may not have propagated yet)
	newCommitURL := fmt.Sprintf("%s/repos/%s/%s/git/commits", c.baseURL, owner, repo)
	commitPayload := map[string]any{
		"message": message,
		"tree":    tree.SHA,
		"parents": []string{ref.Object.SHA},
	}

	for attempt := range newRepoMaxRetries {
		body, status, err = c.doAPI(ctx, http.MethodPost, newCommitURL, commitPayload)
		if err != nil {
			return fmt.Errorf("create commit: %w", err)
		}
		if status == http.StatusForbidden && attempt < newRepoMaxRetries-1 {
			time.Sleep(newRepoPollInterval)
			continue
		}
		if status != http.StatusCreated {
			return fmt.Errorf("create commit (%d): %s", status, truncateBody(body))
		}
		break
	}

	var newCommit struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(body, &newCommit); err != nil {
		return fmt.Errorf("unmarshal commit: %w", err)
	}

	// 6. Update the branch ref to point to the new commit (non-force to detect conflicts)
	body, status, err = c.doAPI(ctx, http.MethodPatch, refURL, map[string]any{
		"sha":   newCommit.SHA,
		"force": false,
	})
	if err != nil {
		return fmt.Errorf("update ref: %w", err)
	}
	if status == http.StatusUnprocessableEntity {
		return fmt.Errorf("branch %s was modified concurrently; commit aborted to prevent overwrite", branch)
	}
	if status != http.StatusOK {
		return fmt.Errorf("update ref (%d): %s", status, truncateBody(body))
	}

	return nil
}

// CreatePR creates a pull request.
func (c *Client) CreatePR(ctx context.Context, input PRInput) (*PRResult, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls", c.baseURL, input.Owner, input.Repo)
	body, status, err := c.doAPI(ctx, http.MethodPost, url, map[string]string{
		"title": input.Title,
		"body":  input.Body,
		"head":  input.Branch,
		"base":  input.Base,
	})
	if err != nil {
		return nil, fmt.Errorf("create PR: %w", err)
	}
	if status != http.StatusCreated {
		return nil, fmt.Errorf("create PR (%d): %s", status, truncateBody(body))
	}

	var result PRResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("create PR unmarshal: %w", err)
	}
	return &result, nil
}
