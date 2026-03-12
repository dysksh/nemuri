package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const presignExpiry = 24 * time.Hour

// sanitizePath validates and cleans a path component to prevent path traversal.
// Returns an error if the result is empty or escapes the intended prefix.
func sanitizePath(name string) (string, error) {
	cleaned := path.Clean(name)
	if cleaned == "." || cleaned == "" {
		return "", fmt.Errorf("invalid path: %q", name)
	}
	if strings.HasPrefix(cleaned, "/") || strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, "/../") {
		return "", fmt.Errorf("path traversal detected: %q", name)
	}
	return cleaned, nil
}

// Client wraps S3 operations for artifacts and outputs.
type Client struct {
	s3Client *s3.Client
	presign  *s3.PresignClient
	bucket   string
}

// NewClient creates a new S3 storage client.
func NewClient(s3Client *s3.Client, bucket string) *Client {
	return &Client{
		s3Client: s3Client,
		presign:  s3.NewPresignClient(s3Client),
		bucket:   bucket,
	}
}

// UploadArtifact uploads a file to the artifacts prefix.
func (c *Client) UploadArtifact(ctx context.Context, jobID, filename string, data []byte) error {
	key, err := buildKey("artifacts", jobID, filename)
	if err != nil {
		return err
	}
	return c.upload(ctx, key, data)
}

// UploadOutput uploads a file to the outputs prefix.
func (c *Client) UploadOutput(ctx context.Context, jobID, filename string, data []byte) error {
	key, err := buildKey("outputs", jobID, filename)
	if err != nil {
		return err
	}
	return c.upload(ctx, key, data)
}

// GetOutputPresignedURL generates a presigned GET URL for an output file.
func (c *Client) GetOutputPresignedURL(ctx context.Context, jobID, filename string) (string, error) {
	key, err := buildKey("outputs", jobID, filename)
	if err != nil {
		return "", err
	}
	req, err := c.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(presignExpiry))
	if err != nil {
		return "", fmt.Errorf("presign %s: %w", key, err)
	}
	return req.URL, nil
}

// buildKey constructs a safe S3 key from prefix, jobID, and filename.
func buildKey(prefix, jobID, filename string) (string, error) {
	safeJobID, err := sanitizePath(jobID)
	if err != nil {
		return "", fmt.Errorf("invalid job ID: %w", err)
	}
	safeName, err := sanitizePath(filename)
	if err != nil {
		return "", fmt.Errorf("invalid filename: %w", err)
	}
	return fmt.Sprintf("%s/%s/%s", prefix, safeJobID, safeName), nil
}

// DownloadArtifact downloads a file from the artifacts prefix.
func (c *Client) DownloadArtifact(ctx context.Context, jobID, filename string) ([]byte, error) {
	key, err := buildKey("artifacts", jobID, filename)
	if err != nil {
		return nil, err
	}
	out, err := c.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", key, err)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", key, err)
	}
	return data, nil
}

func (c *Client) upload(ctx context.Context, key string, data []byte) error {
	_, err := c.s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return fmt.Errorf("upload %s: %w", key, err)
	}
	return nil
}
