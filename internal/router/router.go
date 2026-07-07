// Package router decides which model tier should handle a given prompt.
package router

import (
	"context"
	"encoding/json"
)

// Tier identifies a model class: Local is the cheap/fast tier, Frontier the
// expensive/powerful one.
type Tier int

const (
	Local Tier = iota
	Frontier
)

func (t Tier) String() string {
	switch t {
	case Local:
		return "local"
	case Frontier:
		return "frontier"
	default:
		return "unknown"
	}
}

// MarshalJSON emits the tier as its name ("local"/"frontier") so JSON output
// is self-describing.
func (t Tier) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.String())
}

// Decision is the router's output. Reason must always be set: it keeps every
// routing choice auditable, and tuning the router without it is guesswork.
type Decision struct {
	Tier   Tier   `json:"tier"`
	Reason string `json:"reason"`
	// Score is the frontier-leaning signal strength (higher = more confident
	// the task needs the frontier tier).
	Score float64 `json:"score"`
}

// Router picks a tier for a prompt. Implementations must be cheap enough that
// the routing decision doesn't erode the savings it creates.
type Router interface {
	Route(ctx context.Context, prompt string) Decision
}
