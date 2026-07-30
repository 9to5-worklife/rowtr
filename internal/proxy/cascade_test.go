package proxy

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/9to5-worklife/rowtr/internal/backend"
	"github.com/9to5-worklife/rowtr/internal/router"
)

// TestCascadeFrontierJudge exercises the Haiku-judge path: the local answer is
// graded by a Claude call (not a second local pass) that reuses the client's
// own credentials, and only the grade — not the answer — decides escalation.
func TestCascadeFrontierJudge(t *testing.T) {
	const frontierPrompt = `{"model":"claude-opus-4-8","max_tokens":64,` +
		`"messages":[{"role":"user","content":"compare postgres and dynamodb with tradeoffs"}]}`

	run := func(judgeScore string) (*http.Response, int64, string) {
		var forwards atomic.Int64
		var judgeAuth string
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			if strings.Contains(string(b), "On a scale of 0 to 10") { // the judge call
				judgeAuth = r.Header.Get("Authorization")
				w.Write([]byte(`{"type":"message","role":"assistant","model":"claude-haiku-4-5",` +
					`"content":[{"type":"text","text":"` + judgeScore + `"}],` +
					`"usage":{"input_tokens":5,"output_tokens":1}}`))
				return
			}
			forwards.Add(1) // a real escalation to the frontier
			w.Write([]byte(`{"type":"message","role":"assistant","model":"claude-opus-4-8",` +
				`"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":10,"output_tokens":2}}`))
		}))
		t.Cleanup(up.Close)

		stub := ollamaStub(t, "unused") // serves the local completion only
		srv, err := New(Options{Router: router.KeywordRouter{}, Upstream: up.URL,
			Mode: ModeRoute, Log: log.New(io.Discard, "", 0),
			Local:             backend.NewOllama(stub.URL, "stub-model"),
			Cascade:           true,
			CascadeJudgeModel: "claude-haiku-4-5"})
		if err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewServer(srv.Handler())
		t.Cleanup(ts.Close)

		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/messages", strings.NewReader(frontierPrompt))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer test-key")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, forwards.Load(), judgeAuth
	}

	// Confident Haiku grade → served locally, no forward to the frontier.
	resp, forwards, auth := run("9")
	if forwards != 0 {
		t.Fatalf("a Haiku-approved answer must not forward to the frontier (forwards=%d)", forwards)
	}
	if resp.Header.Get("X-Rowtr-Tier") != "local" || resp.Header.Get("X-Rowtr-Cascade-Score") != "9" {
		t.Fatalf("cascade headers wrong: tier=%q score=%q",
			resp.Header.Get("X-Rowtr-Tier"), resp.Header.Get("X-Rowtr-Cascade-Score"))
	}
	if auth != "Bearer test-key" {
		t.Fatalf("judge call must reuse the client's Authorization, got %q", auth)
	}

	// Unconfident grade → escalate to the frontier.
	resp, forwards, _ = run("2")
	if forwards != 1 {
		t.Fatalf("a weak answer must escalate to the frontier (forwards=%d)", forwards)
	}
	if resp.Header.Get("X-Rowtr-Tier") == "local" {
		t.Fatal("escalated response must not claim to be local")
	}
}
