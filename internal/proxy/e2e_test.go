package proxy

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/9to5-worklife/rowtr/internal/backend"
	"github.com/9to5-worklife/rowtr/internal/router"
	"github.com/9to5-worklife/rowtr/internal/usage"
)

// localPrompt routes Local (translate is a local verb) and carries no tools, so
// it is eligible for offload in route mode.
const localPrompt = `{"model":"claude-opus-4-8","max_tokens":64,` +
	`"messages":[{"role":"user","content":"translate this sentence to french"}]}`

// TestRouteModeLocalOffload is the core value path end-to-end: in route mode an
// eligible prompt is answered by the local model, the frontier is never touched,
// the response is disclosed as local, and a usage event is recorded.
func TestRouteModeLocalOffload(t *testing.T) {
	var hits atomic.Int64
	var lastBody []byte
	up := upstreamJSON(t, &hits, &lastBody)
	stub := ollamaStub(t, "unused")
	store, err := usage.Open(filepath.Join(t.TempDir(), "u.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	srv, err := New(Options{Router: router.KeywordRouter{}, Upstream: up.URL,
		Mode: ModeRoute, Log: log.New(io.Discard, "", 0), Store: store,
		Local: backend.NewOllama(stub.URL, "stub-model")})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/messages", "application/json", strings.NewReader(localPrompt))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if hits.Load() != 0 {
		t.Fatalf("an offloaded prompt must not reach the frontier (hits=%d)", hits.Load())
	}
	if resp.Header.Get("X-Rowtr-Tier") != "local" {
		t.Fatalf("offloaded response should disclose X-Rowtr-Tier: local, got %q", resp.Header.Get("X-Rowtr-Tier"))
	}
	if resp.Header.Get("X-Rowtr-Model") == "" {
		t.Fatal("offloaded response should disclose the local model")
	}
	if !strings.Contains(string(body), `"type":"message"`) {
		t.Fatalf("local response should be an Anthropic-shaped message, got %s", body)
	}
	sum, err := store.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if sum.Local != 1 || sum.Frontier != 0 {
		t.Fatalf("want 1 local event, 0 frontier, got local=%d frontier=%d", sum.Local, sum.Frontier)
	}
}

// TestLocalFailureFallsBackToFrontier is the never-break-the-client invariant:
// when the local model can't serve an eligible prompt, the request forwards to
// Claude cleanly instead of erroring.
func TestLocalFailureFallsBackToFrontier(t *testing.T) {
	var hits atomic.Int64
	var lastBody []byte
	up := upstreamJSON(t, &hits, &lastBody)

	srv, err := New(Options{Router: router.KeywordRouter{}, Upstream: up.URL,
		Mode: ModeRoute, Log: log.New(io.Discard, "", 0),
		Local: backend.NewOllama("http://127.0.0.1:1", "none")}) // unreachable local
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/messages", "application/json", strings.NewReader(localPrompt))
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	if hits.Load() != 1 {
		t.Fatalf("a local failure must fall back to the frontier exactly once (hits=%d)", hits.Load())
	}
	if resp.Header.Get("X-Rowtr-Tier") == "local" {
		t.Fatal("a fallback response must not claim to be local")
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("client must still get a clean response, got status %d", resp.StatusCode)
	}
}

// TestObserveModeNeverOffloads confirms the safe default: observe mode forwards
// everything, even a prompt that would be eligible for local offload.
func TestObserveModeNeverOffloads(t *testing.T) {
	var hits atomic.Int64
	var lastBody []byte
	up := upstreamJSON(t, &hits, &lastBody)
	stub := ollamaStub(t, "unused")

	srv, err := New(Options{Router: router.KeywordRouter{}, Upstream: up.URL,
		Mode: ModeObserve, Log: log.New(io.Discard, "", 0),
		Local: backend.NewOllama(stub.URL, "stub-model")})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/messages", "application/json", strings.NewReader(localPrompt))
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	if hits.Load() != 1 {
		t.Fatalf("observe mode must forward everything to the frontier (hits=%d)", hits.Load())
	}
	if resp.Header.Get("X-Rowtr-Tier") == "local" {
		t.Fatal("observe mode must never serve locally")
	}
}
