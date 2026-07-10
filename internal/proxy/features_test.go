package proxy

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/connorhoulihan/rowtr/internal/backend"
	"github.com/connorhoulihan/rowtr/internal/router"
	"github.com/connorhoulihan/rowtr/internal/usage"
)

const titlePayload = `{"model":"claude-opus-4-8","max_tokens":512,"stream":false,` +
	`"thinking":{"type":"adaptive"},"output_config":{"effort":"low"},` +
	`"messages":[{"role":"user","content":"Please write a 5-10 word title for the following conversation: hi there"}]}`

// upstreamJSON is a fake Anthropic upstream returning a fixed Message and
// counting how many requests actually reached it.
func upstreamJSON(t *testing.T, hits *atomic.Int64, lastBody *[]byte) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		*lastBody = b
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"msg_up","type":"message","role":"assistant","model":"claude-haiku-4-5",` +
			`"content":[{"type":"text","text":"ok"}],` +
			`"usage":{"input_tokens":10,"output_tokens":2,"cache_read_input_tokens":100,"cache_creation_input_tokens":20}}`))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestByteIdenticalForwarding(t *testing.T) {
	var hits atomic.Int64
	var lastBody []byte
	up := upstreamJSON(t, &hits, &lastBody)
	srv, err := New(Options{Router: router.KeywordRouter{}, Upstream: up.URL,
		Mode: ModeObserve, Log: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	sent := `{"model":"claude-opus-4-8","max_tokens":64,"messages":[{"role":"user","content":"compare postgres and dynamodb with tradeoffs"}]}`
	resp, err := http.Post(ts.URL+"/v1/messages", "application/json", strings.NewReader(sent))
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	if string(lastBody) != sent {
		t.Fatalf("forwarded body not byte-identical:\nsent %s\ngot  %s", sent, lastBody)
	}
}

func TestDownshiftMutationScope(t *testing.T) {
	var hits atomic.Int64
	var lastBody []byte
	up := upstreamJSON(t, &hits, &lastBody)
	srv, err := New(Options{Router: router.KeywordRouter{}, Upstream: up.URL,
		Mode: ModeRoute, Log: log.New(io.Discard, "", 0),
		Local:          backend.NewOllama("http://127.0.0.1:1", "none"), // unreachable → downshift path
		DownshiftModel: "claude-haiku-4-5"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/messages", "application/json", strings.NewReader(titlePayload))
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	var sent, got map[string]any
	json.Unmarshal([]byte(titlePayload), &sent)
	if err := json.Unmarshal(lastBody, &got); err != nil {
		t.Fatalf("upstream body not JSON: %v", err)
	}
	if got["model"] != "claude-haiku-4-5" {
		t.Fatalf("model not downshifted: %v", got["model"])
	}
	if _, ok := got["thinking"]; ok {
		t.Fatal("thinking config should be stripped for the downshift tier")
	}
	if _, ok := got["output_config"]; ok {
		t.Fatal("effort-only output_config should be removed")
	}
	// Everything else — messages, system, max_tokens — must be untouched.
	for _, k := range []string{"model", "thinking", "output_config"} {
		delete(sent, k)
		delete(got, k)
	}
	if !reflect.DeepEqual(sent, got) {
		t.Fatalf("downshift touched fields beyond model/thinking/effort:\nsent %v\ngot  %v", sent, got)
	}
}

func TestDedupReplayAndCacheAccounting(t *testing.T) {
	var hits atomic.Int64
	var lastBody []byte
	up := upstreamJSON(t, &hits, &lastBody)
	store, err := usage.Open(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	srv, err := New(Options{Router: router.KeywordRouter{}, Upstream: up.URL,
		Mode: ModeRoute, Log: log.New(io.Discard, "", 0), Store: store,
		Local:          backend.NewOllama("http://127.0.0.1:1", "none"),
		DownshiftModel: "claude-haiku-4-5"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	post := func() (*http.Response, []byte) {
		resp, err := http.Post(ts.URL+"/v1/messages", "application/json", strings.NewReader(titlePayload))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, b
	}

	_, first := post()
	if hits.Load() != 1 {
		t.Fatalf("first request should reach upstream once, got %d", hits.Load())
	}
	resp2, second := post()
	if hits.Load() != 1 {
		t.Fatalf("identical request should be replayed, not re-sent (upstream hits: %d)", hits.Load())
	}
	if resp2.Header.Get("X-Rowtr-Tier") != "cache" {
		t.Fatalf("replay should disclose X-Rowtr-Tier: cache, got %q", resp2.Header.Get("X-Rowtr-Tier"))
	}
	if string(first) != string(second) {
		t.Fatalf("replayed body differs:\n%s\n%s", first, second)
	}

	sum, err := store.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if sum.Frontier != 1 || sum.Local != 1 {
		t.Fatalf("want 1 frontier + 1 replay event, got frontier=%d local=%d", sum.Frontier, sum.Local)
	}
	if sum.FrontierInput != 10 || sum.CacheReadTokens != 100 || sum.CacheWriteTokens != 20 {
		t.Fatalf("cache accounting wrong: in=%d read=%d write=%d", sum.FrontierInput, sum.CacheReadTokens, sum.CacheWriteTokens)
	}
	if sum.MaxPromptTokens != 130 {
		t.Fatalf("MaxPromptTokens = %d, want 130", sum.MaxPromptTokens)
	}
	if len(sum.ByCategory) != 1 || sum.ByCategory[0].Category != "internal" {
		t.Fatalf("category breakdown wrong: %+v", sum.ByCategory)
	}
	if sum.SavedUSD <= 0 {
		t.Fatal("downshift + replay should record positive savings")
	}
}

// ollamaStub fakes the local daemon: liveness on "/", canned chat completions
// on /api/chat, with the judge score controlled per test.
func ollamaStub(t *testing.T, judgeScore string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			w.WriteHeader(http.StatusOK)
			return
		}
		var req struct {
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		text := "The answer is 42."
		if n := len(req.Messages); n > 0 && strings.HasPrefix(req.Messages[n-1].Content, "Question:") {
			text = judgeScore
		}
		json.NewEncoder(w).Encode(map[string]any{
			"message":           map[string]string{"role": "assistant", "content": text},
			"prompt_eval_count": 10, "eval_count": 5,
		})
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestCascade(t *testing.T) {
	frontierPrompt := `{"model":"claude-opus-4-8","max_tokens":64,"messages":[{"role":"user","content":"compare postgres and dynamodb with tradeoffs"}]}`

	run := func(judgeScore string) (*http.Response, int64) {
		var hits atomic.Int64
		var lastBody []byte
		up := upstreamJSON(t, &hits, &lastBody)
		stub := ollamaStub(t, judgeScore)
		srv, err := New(Options{Router: router.KeywordRouter{}, Upstream: up.URL,
			Mode: ModeRoute, Log: log.New(io.Discard, "", 0),
			Local:   backend.NewOllama(stub.URL, "stub-model"),
			Cascade: true})
		if err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewServer(srv.Handler())
		t.Cleanup(ts.Close)
		resp, err := http.Post(ts.URL+"/v1/messages", "application/json", strings.NewReader(frontierPrompt))
		if err != nil {
			t.Fatal(err)
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, hits.Load()
	}

	// Confident judge → served locally, frontier untouched.
	resp, hits := run("9")
	if hits != 0 {
		t.Fatalf("good local answer should not reach the frontier (hits=%d)", hits)
	}
	if resp.Header.Get("X-Rowtr-Tier") != "local" || resp.Header.Get("X-Rowtr-Cascade-Score") != "9" {
		t.Fatalf("cascade headers wrong: tier=%q score=%q",
			resp.Header.Get("X-Rowtr-Tier"), resp.Header.Get("X-Rowtr-Cascade-Score"))
	}

	// Unconfident judge → escalates to the frontier.
	resp, hits = run("2")
	if hits != 1 {
		t.Fatalf("weak local answer should escalate to the frontier (hits=%d)", hits)
	}
	if resp.Header.Get("X-Rowtr-Tier") == "local" {
		t.Fatal("escalated response must not claim to be local")
	}
}

func TestTapParsesSSECacheFields(t *testing.T) {
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"model":"claude-opus-4-8","usage":{"input_tokens":7,"cache_read_input_tokens":900,"cache_creation_input_tokens":50}}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":33}}` + "\n\n"
	var gotModel string
	var got tapUsage
	tap := &frontierTap{inner: io.NopCloser(strings.NewReader(sse)), isSSE: true,
		done: func(model string, u tapUsage) { gotModel, got = model, u }}
	io.ReadAll(tap)
	tap.Close()
	if gotModel != "claude-opus-4-8" {
		t.Fatalf("model = %q", gotModel)
	}
	if got.InTok != 7 || got.CacheRead != 900 || got.CacheWrite != 50 || got.OutTok != 33 {
		t.Fatalf("usage = %+v", got)
	}
}
