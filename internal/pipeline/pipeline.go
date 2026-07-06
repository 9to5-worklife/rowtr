// Package pipeline wires the router to the backends: it routes a prompt, selects
// the matching backend, dispatches, and assembles a Result with the metrics that
// make routing measurable (which tier, latency, token cost).
package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/connorhoulihan/rowtr/internal/backend"
	"github.com/connorhoulihan/rowtr/internal/config"
	"github.com/connorhoulihan/rowtr/internal/router"
)

// Result is everything Rowtr learned from handling one prompt. It's the unit the
// slice-2 scoreboard will consume, so it carries both the answer and the metrics.
type Result struct {
	Decision   router.Decision `json:"decision"`
	Backend    string          `json:"backend"`
	Model      string          `json:"model"`
	Text       string          `json:"text"`
	LatencyMS  int64           `json:"latency_ms"`
	InTokens   int             `json:"input_tokens"`
	OutTokens  int             `json:"output_tokens"`
	EstCostUSD float64         `json:"est_cost_usd"`
}

// Pipeline holds the router and the backends indexed by tier.
type Pipeline struct {
	router   router.Router
	backends map[router.Tier]backend.Backend
	cfg      config.Config
}

// New builds a pipeline from a router and a set of backends.
func New(r router.Router, cfg config.Config, backends ...backend.Backend) *Pipeline {
	m := make(map[router.Tier]backend.Backend, len(backends))
	for _, b := range backends {
		m[b.Tier()] = b
	}
	return &Pipeline{router: r, backends: m, cfg: cfg}
}

// Run routes and executes a prompt. forceTier, when non-nil, overrides the
// router (used for testing a specific tier from the CLI).
func (p *Pipeline) Run(ctx context.Context, prompt string, forceTier *router.Tier) (Result, error) {
	var decision router.Decision
	if forceTier != nil {
		decision = router.Decision{Tier: *forceTier, Reason: "forced via --tier", Score: -1}
	} else {
		decision = p.router.Route(ctx, prompt)
	}

	b, ok := p.backends[decision.Tier]
	if !ok {
		return Result{}, fmt.Errorf("no backend registered for tier %s", decision.Tier)
	}

	// Fail clearly if the chosen backend can't run — never silently reroute to
	// the other tier, which would make the metrics lie.
	if err := b.Available(ctx); err != nil {
		return Result{}, fmt.Errorf("%s backend (tier %s) unavailable: %w", b.Name(), decision.Tier, err)
	}

	start := time.Now()
	resp, err := b.Complete(ctx, prompt)
	elapsed := time.Since(start)
	if err != nil {
		return Result{}, fmt.Errorf("%s completion failed: %w", b.Name(), err)
	}

	return Result{
		Decision:   decision,
		Backend:    b.Name(),
		Model:      resp.Model,
		Text:       resp.Text,
		LatencyMS:  elapsed.Milliseconds(),
		InTokens:   resp.InputTokens,
		OutTokens:  resp.OutputTokens,
		EstCostUSD: config.EstimateCostUSD(resp.Model, resp.InputTokens, resp.OutputTokens),
	}, nil
}
