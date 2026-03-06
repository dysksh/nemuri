package secrets

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// Client wraps AWS Secrets Manager for fetching secret values.
type Client struct {
	sm *secretsmanager.Client
}

// NewClient creates a new Secrets Manager client.
func NewClient(sm *secretsmanager.Client) *Client {
	return &Client{sm: sm}
}

// GetSecret retrieves a secret value by name.
func (c *Client) GetSecret(ctx context.Context, name string) (string, error) {
	out, err := c.sm.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(name),
	})
	if err != nil {
		return "", fmt.Errorf("get secret %s: %w", name, err)
	}
	if out.SecretString == nil {
		return "", fmt.Errorf("secret %s has no string value", name)
	}
	return *out.SecretString, nil
}
