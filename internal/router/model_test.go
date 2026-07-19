package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeOllama answers /api/chat with a fixed verdict and captures the request.
func fakeOllama(t *testing.T, verdict string, got *ollamaRouteRequest) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got != nil {
			if err := json.NewDecoder(r.Body).Decode(got); err != nil {
				t.Errorf("decoding request: %v", err)
			}
		}
		json.NewEncoder(w).Encode(ollamaRouteResponse{
			Message: ollamaRouteMessage{Role: "assistant", Content: verdict},
		})
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestModelRouterVerdicts(t *testing.T) {
	cases := []struct {
		verdict string
		want    Tier
	}{
		{"LOCAL", Local},
		{"local", Local},
		{" Local.\n", Local},
		{"FRONTIER", Frontier},
		{"frontier", Frontier},
	}
	for _, c := range cases {
		var got ollamaRouteRequest
		ts := fakeOllama(t, c.verdict, &got)
		m := &ModelRouter{Host: ts.URL, Model: "tiny"}
		d := m.Route(context.Background(), "summarize this")
		if d.Tier != c.want {
			t.Errorf("verdict %q: got %v, want %v (reason %q)", c.verdict, d.Tier, c.want, d.Reason)
		}
		if !strings.Contains(d.Reason, "model tiny") {
			t.Errorf("reason should name the model: %q", d.Reason)
		}
		// The call must be pinned deterministic with a tiny output budget.
		if got.Options["temperature"] != float64(0) {
			t.Errorf("temperature = %v, want 0", got.Options["temperature"])
		}
		if got.KeepAlive != -1 {
			t.Errorf("keep_alive = %d, want -1 (model must stay resident)", got.KeepAlive)
		}
	}
}

func TestModelRouterUnparseableFallsBack(t *testing.T) {
	ts := fakeOllama(t, "hmm, either LOCAL or FRONTIER could work", nil)

	// With a fallback, the fallback answers and the reason is marked.
	m := &ModelRouter{Host: ts.URL, Model: "tiny", Fallback: KeywordRouter{}}
	d := m.Route(context.Background(), "summarize this paragraph")
	if d.Tier != Local {
		t.Fatalf("fallback should have routed Local, got %v", d.Tier)
	}
	if !strings.HasPrefix(d.Reason, "model-fallback: ") {
		t.Fatalf("fallback reason unmarked: %q", d.Reason)
	}

	// Without a fallback, uncertainty must land on the safe tier.
	m = &ModelRouter{Host: ts.URL, Model: "tiny"}
	if d := m.Route(context.Background(), "summarize this"); d.Tier != Frontier {
		t.Fatalf("no-fallback error should route Frontier, got %v (%q)", d.Tier, d.Reason)
	}

	// RouteChecked must surface the malfunction instead of absorbing it.
	if _, err := m.RouteChecked(context.Background(), "summarize this"); err == nil {
		t.Fatal("RouteChecked should error on an unparseable verdict")
	}
}

func TestModelRouterTimeoutFallsBack(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	t.Cleanup(ts.Close)

	m := &ModelRouter{Host: ts.URL, Model: "tiny", Timeout: 20 * time.Millisecond, Fallback: KeywordRouter{}}
	start := time.Now()
	d := m.Route(context.Background(), "compare X and Y with tradeoffs")
	if elapsed := time.Since(start); elapsed > 150*time.Millisecond {
		t.Fatalf("timeout not enforced: took %v", elapsed)
	}
	if d.Tier != Frontier || !strings.HasPrefix(d.Reason, "model-fallback: ") {
		t.Fatalf("expected keyword fallback decision, got %v %q", d.Tier, d.Reason)
	}
}

func TestModelRouterTruncatesLongPrompts(t *testing.T) {
	var got ollamaRouteRequest
	ts := fakeOllama(t, "FRONTIER", &got)
	m := &ModelRouter{Host: ts.URL, Model: "tiny"}

	m.Route(context.Background(), strings.Repeat("é", maxRoutePromptChars+500))
	sent := got.Messages[len(got.Messages)-1].Content
	if n := len([]rune(sent)); n != maxRoutePromptChars {
		t.Fatalf("prompt sent to classifier is %d runes, want %d", n, maxRoutePromptChars)
	}
}

func TestParseVerdict(t *testing.T) {
	if _, err := parseVerdict(""); err == nil {
		t.Fatal("empty verdict should error")
	}
	if _, err := parseVerdict("LOCAL FRONTIER"); err == nil {
		t.Fatal("ambiguous verdict should error")
	}
}
