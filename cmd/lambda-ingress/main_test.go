package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"
)

func TestVerifySignature(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	t.Run("valid signature", func(t *testing.T) {
		timestamp := "1700000000"
		body := `{"type":1}`
		msg := []byte(timestamp + body)
		sig := ed25519.Sign(priv, msg)

		if !verifySignature(pub, hex.EncodeToString(sig), timestamp, body) {
			t.Error("verifySignature() = false, want true")
		}
	})

	t.Run("tampered body", func(t *testing.T) {
		timestamp := "1700000000"
		body := `{"type":1}`
		msg := []byte(timestamp + body)
		sig := ed25519.Sign(priv, msg)

		if verifySignature(pub, hex.EncodeToString(sig), timestamp, `{"type":2}`) {
			t.Error("verifySignature() = true for tampered body, want false")
		}
	})

	t.Run("empty signature", func(t *testing.T) {
		if verifySignature(pub, "", "1700000000", "body") {
			t.Error("verifySignature() = true for empty signature, want false")
		}
	})

	t.Run("empty timestamp", func(t *testing.T) {
		if verifySignature(pub, "aabb", "", "body") {
			t.Error("verifySignature() = true for empty timestamp, want false")
		}
	})

	t.Run("invalid hex signature", func(t *testing.T) {
		if verifySignature(pub, "not-hex", "12345", "body") {
			t.Error("verifySignature() = true for invalid hex, want false")
		}
	})
}

func TestExtractPrompt(t *testing.T) {
	tests := []struct {
		name        string
		interaction discordInteraction
		want        string
	}{
		{
			name: "prompt option present",
			interaction: discordInteraction{
				Data: &discordCommandData{
					Name: "nemuri",
					Options: []discordCommandOption{
						{Name: "prompt", Value: "hello world"},
					},
				},
			},
			want: "hello world",
		},
		{
			name: "no options",
			interaction: discordInteraction{
				Data: &discordCommandData{Name: "agent"},
			},
			want: "",
		},
		{
			name:        "nil data",
			interaction: discordInteraction{},
			want:        "",
		},
		{
			name: "different option name",
			interaction: discordInteraction{
				Data: &discordCommandData{
					Options: []discordCommandOption{
						{Name: "other", Value: "value"},
					},
				},
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPrompt(tt.interaction)
			if got != tt.want {
				t.Errorf("extractPrompt() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetHeader(t *testing.T) {
	headers := map[string]string{
		"Content-Type":        "application/json",
		"x-signature-ed25519": "abc123",
	}

	tests := []struct {
		name string
		key  string
		want string
	}{
		{"exact match", "Content-Type", "application/json"},
		{"case-insensitive", "content-type", "application/json"},
		{"signature header", "x-signature-ed25519", "abc123"},
		{"missing header", "X-Missing", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getHeader(headers, tt.key)
			if got != tt.want {
				t.Errorf("getHeader(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}
