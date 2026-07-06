// Package router decides which model tier should handle a given prompt.
//
// Slice 1 ships a single free heuristic (KeywordRouter). The Router interface
// exists so a smarter, costlier router can be swapped in later without touching
// the pipeline — but only once the scoreboard (slice 2) can prove it pays off.
package router

import (
	"context"
	"encoding/json"
)

// Tier identifies a model class. Local is the cheap/fast Gatekeeper; Frontier
// is the expensive/powerful Expert.
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

// MarshalJSON emits the tier as its name ("local"/"frontier") so JSON results
// are self-describing for downstream consumers (the slice-2 scoreboard).
func (t Tier) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.String())
}

// Decision is the router's output. Reason keeps every routing choice auditable —
// without it, tuning the router is guesswork.
type Decision struct {
	Tier   Tier   `json:"tier"`
	Reason string `json:"reason"`
	// Score is the frontier-leaning signal strength (higher = more confident
	// the task needs the Expert). Slice 1 uses a coarse scale; it becomes
	// meaningful once the scoreboard calibrates it.
	Score float64 `json:"score"`
}

// Router picks a tier for a prompt. Implementations must be cheap enough that
// the routing decision doesn't erode the savings it creates.
type Router interface {
	Route(ctx context.Context, prompt string) Decision
}
