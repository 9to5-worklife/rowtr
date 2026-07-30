package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/9to5-worklife/rowtr/internal/backend"
	"github.com/9to5-worklife/rowtr/internal/router"
)

type fakeBackend struct {
	name        string
	tier        router.Tier
	avail       error
	resp        backend.Response
	completeErr error
}

func (f fakeBackend) Name() string                                { return f.name }
func (f fakeBackend) Tier() router.Tier                           { return f.tier }
func (f fakeBackend) Available(context.Context) error             { return f.avail }
func (f fakeBackend) Complete(context.Context, string) (backend.Response, error) {
	return f.resp, f.completeErr
}

type fixedRouter struct{ tier router.Tier }

func (r fixedRouter) Route(context.Context, string) router.Decision {
	return router.Decision{Tier: r.tier, Reason: "fixed"}
}

func TestRoutesAndDispatches(t *testing.T) {
	local := fakeBackend{name: "local-fake", tier: router.Local,
		resp: backend.Response{Text: "loc", Model: "m-local", InputTokens: 4, OutputTokens: 2}}
	frontier := fakeBackend{name: "frontier-fake", tier: router.Frontier,
		resp: backend.Response{Text: "fro", Model: "claude-opus-4-8", InputTokens: 100, OutputTokens: 50}}

	p := New(fixedRouter{tier: router.Frontier}, local, frontier)
	res, err := p.Run(context.Background(), "anything", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Backend != "frontier-fake" || res.Text != "fro" || res.Model != "claude-opus-4-8" {
		t.Fatalf("routed to the wrong backend: %+v", res)
	}
	if res.EstCostUSD <= 0 {
		t.Fatalf("a frontier completion should carry a cost estimate, got %v", res.EstCostUSD)
	}
}

func TestForceTierOverridesRouter(t *testing.T) {
	local := fakeBackend{name: "local-fake", tier: router.Local, resp: backend.Response{Model: "m", Text: "x"}}
	frontier := fakeBackend{name: "frontier-fake", tier: router.Frontier}
	p := New(fixedRouter{tier: router.Frontier}, local, frontier)

	forced := router.Local
	res, err := p.Run(context.Background(), "prompt", &forced)
	if err != nil {
		t.Fatal(err)
	}
	if res.Backend != "local-fake" {
		t.Fatalf("--tier local should override the router, got backend %q", res.Backend)
	}
}

func TestUnavailableBackendErrorsNoSilentReroute(t *testing.T) {
	local := fakeBackend{name: "local-fake", tier: router.Local, avail: errors.New("ollama down")}
	p := New(fixedRouter{tier: router.Local}, local)
	if _, err := p.Run(context.Background(), "prompt", nil); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("an unavailable backend must error (never silently reroute), got %v", err)
	}
}

func TestNoBackendForTierErrors(t *testing.T) {
	local := fakeBackend{name: "local-fake", tier: router.Local}
	p := New(fixedRouter{tier: router.Frontier}, local) // no frontier backend registered
	if _, err := p.Run(context.Background(), "prompt", nil); err == nil || !strings.Contains(err.Error(), "no backend") {
		t.Fatalf("routing to a tier with no backend must error, got %v", err)
	}
}

func TestCompletionErrorPropagates(t *testing.T) {
	local := fakeBackend{name: "local-fake", tier: router.Local, completeErr: errors.New("boom")}
	p := New(fixedRouter{tier: router.Local}, local)
	if _, err := p.Run(context.Background(), "prompt", nil); err == nil || !strings.Contains(err.Error(), "completion failed") {
		t.Fatalf("a completion error must propagate, got %v", err)
	}
}
