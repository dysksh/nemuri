package fixture

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nemuri/nemuri/internal/github"
)

// DirMock implements github.API by serving files from a local directory.
type DirMock struct {
	root string
}

// NewMockFromDirectory creates a GitHub API mock that serves files from a directory.
func NewMockFromDirectory(dir string) github.API {
	return &DirMock{root: dir}
}

func (m *DirMock) GetDefaultBranch(_ context.Context, _, _ string) (string, error) {
	return "main", nil
}

func (m *DirMock) GetTree(_ context.Context, _, _, _ string) ([]github.TreeEntry, error) {
	var entries []github.TreeEntry
	err := filepath.WalkDir(m.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(m.root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// Skip hidden directories and files
		if strings.HasPrefix(filepath.Base(rel), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		entry := github.TreeEntry{
			Path: rel,
		}
		if d.IsDir() {
			entry.Type = "tree"
		} else {
			entry.Type = "blob"
			info, infoErr := d.Info()
			if infoErr == nil {
				entry.Size = int(info.Size())
			}
		}
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk directory %s: %w", m.root, err)
	}
	return entries, nil
}

func (m *DirMock) GetFileContent(_ context.Context, _, _, path, _ string) ([]byte, error) {
	fullPath := filepath.Join(m.root, path)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file not found: %s", path)
		}
		return nil, fmt.Errorf("read file %s: %w", path, err)
	}
	return data, nil
}

// Unsupported operations — eval only needs read operations.

func (m *DirMock) CreateBranch(_ context.Context, _, _, _, _ string) error {
	return fmt.Errorf("CreateBranch not supported in eval mock")
}

func (m *DirMock) CommitFiles(_ context.Context, _, _, _, _ string, _ []github.FileEntry) error {
	return fmt.Errorf("CommitFiles not supported in eval mock")
}

func (m *DirMock) CreatePR(_ context.Context, _ github.PRInput) (*github.PRResult, error) {
	return nil, fmt.Errorf("CreatePR not supported in eval mock")
}

func (m *DirMock) CreateRepo(_ context.Context, _ github.CreateRepoInput) (*github.CreateRepoResult, error) {
	return nil, fmt.Errorf("CreateRepo not supported in eval mock")
}

func (m *DirMock) DeleteRepo(_ context.Context, _, _ string) error {
	return fmt.Errorf("DeleteRepo not supported in eval mock")
}

func (m *DirMock) WaitForRepoReady(_ context.Context, _, _, _ string) error {
	return nil
}
