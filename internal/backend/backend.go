// Package backend defines the model-execution interface and its two
// implementations: Ollama (local Gatekeeper) and Anthropic (frontier Expert).
package backend

import (
	"context"

	"github.com/connorhoulihan/rowtr/internal/router"
)

// Response is the normalized result of a completion, regardless of provider.
// Token counts feed the cost estimate; local backends may report zero.
type Response struct {
	Text         string
	Model        string
	InputTokens  int
	OutputTokens int
}

// Backend executes a prompt against one model tier.
type Backend interface {
	// Name is a human-readable identifier for logs (e.g. "ollama", "anthropic").
	Name() string
	// Tier reports which router tier this backend serves.
	Tier() router.Tier
	// Available returns nil if the backend can be used right now, or an
	// actionable error (missing key, daemon down). Checked before dispatch so
	// failure is a clear error, never a silent reroute that corrupts metrics.
	Available(ctx context.Context) error
	// Complete runs the prompt and returns a normalized Response.
	Complete(ctx context.Context, prompt string) (Response, error)
}
