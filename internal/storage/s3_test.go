package storage

import (
	"testing"
)

func TestSanitizePath(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"simple", "file.txt", "file.txt", false},
		{"nested", "dir/file.txt", "dir/file.txt", false},
		{"dot prefix cleaned", "./file.txt", "file.txt", false},
		{"empty", "", "", true},
		{"dot only", ".", "", true},
		{"absolute path", "/etc/passwd", "", true},
		{"parent traversal", "../secret", "", true},
		{"mid traversal", "a/../../../etc/passwd", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sanitizePath(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("sanitizePath(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("sanitizePath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestBuildKey(t *testing.T) {
	tests := []struct {
		name     string
		prefix   string
		jobID    string
		filename string
		want     string
		wantErr  bool
	}{
		{"artifacts", "artifacts", "job-123", "result.json", "artifacts/job-123/result.json", false},
		{"outputs", "outputs", "job-456", "report.pdf", "outputs/job-456/report.pdf", false},
		{"nested file", "artifacts", "job-1", "dir/file.txt", "artifacts/job-1/dir/file.txt", false},
		{"invalid job ID", "artifacts", "..", "file.txt", "", true},
		{"invalid filename", "artifacts", "job-1", "../secret", "", true},
		{"empty job ID", "artifacts", "", "file.txt", "", true},
		{"empty filename", "artifacts", "job-1", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildKey(tt.prefix, tt.jobID, tt.filename)
			if (err != nil) != tt.wantErr {
				t.Errorf("buildKey(%q, %q, %q) error = %v, wantErr %v", tt.prefix, tt.jobID, tt.filename, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("buildKey(%q, %q, %q) = %q, want %q", tt.prefix, tt.jobID, tt.filename, got, tt.want)
			}
		})
	}
}
