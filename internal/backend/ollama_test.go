package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/9to5-worklife/rowtr/internal/router"
)

// ollamaServer fakes the daemon: liveness on any non-chat path, a canned
// completion on /api/chat, and (optionally) captures the request it received.
func ollamaServer(t *testing.T, capture *ollamaChatRequest) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if capture != nil {
			json.NewDecoder(r.Body).Decode(capture)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"message":           map[string]string{"role": "assistant", "content": "hi there"},
			"prompt_eval_count": 12, "eval_count": 7,
		})
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestOllamaComplete(t *testing.T) {
	ts := ollamaServer(t, nil)
	o := NewOllama(ts.URL, "test-model")
	resp, err := o.Complete(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "hi there" || resp.Model != "test-model" ||
		resp.InputTokens != 12 || resp.OutputTokens != 7 {
		t.Fatalf("normalized response wrong: %+v", resp)
	}
}

func TestOllamaChatCoercesRolesAndDropsEmpty(t *testing.T) {
	var got ollamaChatRequest
	ts := ollamaServer(t, &got)
	o := NewOllama(ts.URL, "m")
	_, err := o.Chat(context.Background(), "be brief", []ChatMessage{
		{Role: "user", Content: "q1"},
		{Role: "tool", Content: "weird role"}, // unknown role → coerced to user
		{Role: "assistant", Content: "   "},   // whitespace-only → dropped
	})
	if err != nil {
		t.Fatal(err)
	}
	// system + q1 + coerced weird = 3 (the empty assistant turn is dropped).
	if len(got.Messages) != 3 {
		t.Fatalf("want 3 messages, got %d: %+v", len(got.Messages), got.Messages)
	}
	if got.Messages[0].Role != "system" {
		t.Fatalf("first message should be the system prompt, got %q", got.Messages[0].Role)
	}
	if got.Messages[2].Role != "user" {
		t.Fatalf("unknown role should coerce to user, got %q", got.Messages[2].Role)
	}
}

func TestOllamaErrorFieldSurfaces(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"error": "model not found"})
	}))
	defer ts.Close()
	o := NewOllama(ts.URL, "missing")
	_, err := o.Complete(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("daemon error should surface, got %v", err)
	}
}

func TestOllamaAvailabilityAndMetadata(t *testing.T) {
	ts := ollamaServer(t, nil)
	o := NewOllama(ts.URL, "m")
	if err := o.Available(context.Background()); err != nil {
		t.Fatalf("live daemon should be available: %v", err)
	}
	if dead := NewOllama("http://127.0.0.1:1", "m"); dead.Available(context.Background()) == nil {
		t.Fatal("unreachable daemon should return an error")
	}
	if o.Name() != "ollama" || o.Tier() != router.Local {
		t.Fatalf("metadata wrong: name=%q tier=%v", o.Name(), o.Tier())
	}
}
