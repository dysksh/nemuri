package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestClient creates a client pointing at a custom base URL (for testing).
func newTestClient(token, baseURL string) *Client {
	return &Client{
		token:      token,
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
}

func TestGetDefaultBranch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing or wrong auth header: %s", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
	}))
	defer srv.Close()

	c := newTestClient("test-token", srv.URL)
	branch, err := c.GetDefaultBranch(context.Background(), "owner", "repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if branch != "main" {
		t.Errorf("expected main, got %s", branch)
	}
}

func TestGetDefaultBranch_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()

	c := newTestClient("test-token", srv.URL)
	_, err := c.GetDefaultBranch(context.Background(), "owner", "repo")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCreateBranch(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch r.Method {
		case http.MethodGet: // get ref
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": map[string]string{"sha": "abc123"},
			})
		case http.MethodPost: // create ref
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"ref": "refs/heads/new-branch"})
		default:
			t.Errorf("unexpected method: %s", r.Method)
		}
	}))
	defer srv.Close()

	c := newTestClient("test-token", srv.URL)
	err := c.CreateBranch(context.Background(), "owner", "repo", "main", "new-branch")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount != 2 {
		t.Errorf("expected 2 API calls, got %d", callCount)
	}
}

func TestCreatePR(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["title"] != "Test PR" {
			t.Errorf("unexpected title: %s", body["title"])
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(PRResult{URL: "https://github.com/pr/1", Number: 1})
	}))
	defer srv.Close()

	c := newTestClient("test-token", srv.URL)
	pr, err := c.CreatePR(context.Background(), PRInput{
		Owner:  "owner",
		Repo:   "repo",
		Base:   "main",
		Branch: "feature",
		Title:  "Test PR",
		Body:   "body",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pr.Number != 1 {
		t.Errorf("expected PR number 1, got %d", pr.Number)
	}
}

func TestCreateRepo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(CreateRepoResult{
			FullName:      "user/test-repo",
			DefaultBranch: "main",
			HTMLURL:       "https://github.com/user/test-repo",
		})
	}))
	defer srv.Close()

	c := newTestClient("test-token", srv.URL)
	result, err := c.CreateRepo(context.Background(), CreateRepoInput{
		Name:    "test-repo",
		Private: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FullName != "user/test-repo" {
		t.Errorf("unexpected full_name: %s", result.FullName)
	}
}

func TestDeleteRepo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newTestClient("test-token", srv.URL)
	err := c.DeleteRepo(context.Background(), "owner", "repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCommitFiles(t *testing.T) {
	step := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step++
		switch step {
		case 1: // get branch ref
			_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": "ref-sha"}})
		case 2: // get commit
			_ = json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree-sha"}})
		case 3: // create blob
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": "blob-sha"})
		case 4: // create tree
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": "new-tree-sha"})
		case 5: // create commit
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": "new-commit-sha"})
		case 6: // update ref
			_ = json.NewEncoder(w).Encode(map[string]string{"ref": "refs/heads/branch"})
		}
	}))
	defer srv.Close()

	c := newTestClient("test-token", srv.URL)
	err := c.CommitFiles(context.Background(), "owner", "repo", "branch", "commit msg", []FileEntry{
		{Path: "file.go", Content: []byte("package main")},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if step != 6 {
		t.Errorf("expected 6 API calls, got %d", step)
	}
}

func TestCommitFiles_EmptyPath(t *testing.T) {
	// Should not even make API calls; fail immediately
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": map[string]string{"sha": "sha"},
			"tree":   map[string]string{"sha": "sha"},
		})
	}))
	defer srv.Close()

	c := newTestClient("test-token", srv.URL)
	err := c.CommitFiles(context.Background(), "owner", "repo", "branch", "msg", []FileEntry{
		{Path: "", Content: []byte("data")},
	})
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestGetTree(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tree": []map[string]any{
				{"path": "main.go", "type": "blob", "size": 100},
				{"path": "pkg", "type": "tree"},
			},
			"truncated": false,
		})
	}))
	defer srv.Close()

	c := newTestClient("test-token", srv.URL)
	entries, err := c.GetTree(context.Background(), "owner", "repo", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(entries))
	}
}

func TestGetFileContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content":  "cGFja2FnZSBtYWlu", // base64 of "package main"
			"encoding": "base64",
			"size":     12,
		})
	}))
	defer srv.Close()

	c := newTestClient("test-token", srv.URL)
	content, err := c.GetFileContent(context.Background(), "owner", "repo", "main.go", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(content) != "package main" {
		t.Errorf("unexpected content: %s", string(content))
	}
}
