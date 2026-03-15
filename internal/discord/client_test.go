package discord

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestClient creates a client pointing at a custom base URL (for testing).
func newTestClient(botToken, baseURL string) *Client {
	return &Client{
		botToken:   botToken,
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
}

func TestSendFollowUp_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/webhooks/app123/token456") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		// Webhook follow-ups should NOT have Bot auth
		if r.Header.Get("Authorization") != "" {
			t.Errorf("follow-up should not have Authorization header, got: %s", r.Header.Get("Authorization"))
		}
		var payload messagePayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload.Content != "hello" {
			t.Errorf("unexpected content: %s", payload.Content)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient("bot-token", srv.URL)
	err := c.SendFollowUp(context.Background(), "app123", "token456", "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSendChannelMessage_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/channels/ch789/messages") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bot bot-token" {
			t.Errorf("expected Bot auth header, got: %s", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient("bot-token", srv.URL)
	err := c.SendChannelMessage(context.Background(), "ch789", "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSendChannelMessage_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Missing Access"}`))
	}))
	defer srv.Close()

	c := newTestClient("bot-token", srv.URL)
	err := c.SendChannelMessage(context.Background(), "ch789", "hello")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("expected 403 in error, got: %v", err)
	}
}

func TestSendResult_FollowUpSuccess(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient("bot-token", srv.URL)
	err := c.SendResult(context.Background(), "app", "token", "ch", "content")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call (follow-up only), got %d", calls)
	}
}

func TestSendResult_FallbackToChannel(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			// Follow-up fails
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Unknown Webhook"}`))
			return
		}
		// Channel message succeeds
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient("bot-token", srv.URL)
	err := c.SendResult(context.Background(), "app", "token", "ch", "content")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls (follow-up + fallback), got %d", calls)
	}
}

func TestCreateThread_Success(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			// Thread creation
			if !strings.Contains(r.URL.Path, "/channels/ch/threads") {
				t.Errorf("unexpected path: %s", r.URL.Path)
			}
			var payload createThreadPayload
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			if payload.Name != "Test Thread" {
				t.Errorf("unexpected thread name: %s", payload.Name)
			}
			if payload.Type != 11 {
				t.Errorf("expected type 11, got %d", payload.Type)
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(threadResponse{ID: "thread-123"})
			return
		}
		// Send initial message
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient("bot-token", srv.URL)
	threadID, err := c.CreateThread(context.Background(), "ch", "Test Thread", "Hello thread!")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if threadID != "thread-123" {
		t.Errorf("expected thread-123, got %s", threadID)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls (create + send message), got %d", calls)
	}
}

func TestCreateThread_NoInitialMessage(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(threadResponse{ID: "thread-456"})
	}))
	defer srv.Close()

	c := newTestClient("bot-token", srv.URL)
	threadID, err := c.CreateThread(context.Background(), "ch", "Thread", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if threadID != "thread-456" {
		t.Errorf("expected thread-456, got %s", threadID)
	}
	if calls != 1 {
		t.Errorf("expected 1 call (create only), got %d", calls)
	}
}

func TestCreateThread_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Missing Access"}`))
	}))
	defer srv.Close()

	c := newTestClient("bot-token", srv.URL)
	_, err := c.CreateThread(context.Background(), "ch", "Thread", "msg")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		max    int
		suffix string
		want   string
	}{
		{"no truncation", "hello", 10, "", "hello"},
		{"exact length", "hello", 5, "", "hello"},
		{"truncate default suffix", "hello world", 8, "", "hello..."},
		{"truncate custom suffix", "hello world", 8, "…", "hello w…"},
		{"unicode", "こんにちは世界", 5, "", "こん..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Truncate(tt.input, tt.max, tt.suffix)
			if got != tt.want {
				t.Errorf("Truncate(%q, %d, %q) = %q, want %q", tt.input, tt.max, tt.suffix, got, tt.want)
			}
		})
	}
}
