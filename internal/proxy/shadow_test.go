package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/9to5-worklife/rowtr/internal/router"
)

// stubRouter always answers with a fixed decision.
type stubRouter struct{ d router.Decision }

func (s stubRouter) Route(context.Context, string) router.Decision { return s.d }

// failingRouter is a CheckedRouter whose checked path always errors.
type failingRouter struct{}

func (failingRouter) Route(context.Context, string) router.Decision {
	return router.Decision{Tier: router.Frontier, Reason: "unreachable"}
}
func (failingRouter) RouteChecked(context.Context, string) (router.Decision, error) {
	return router.Decision{}, errors.New("classifier down")
}

// chanWriter delivers each Write to a channel so tests can await the async
// shadow goroutine without sleeping.
type chanWriter struct{ ch chan []byte }

func (w chanWriter) Write(p []byte) (int, error) {
	w.ch <- append([]byte(nil), p...)
	return len(p), nil
}

func newShadowServer(t *testing.T, shadow router.Router) (*httptest.Server, chan []byte) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"msg_upstream","type":"message"}`))
	}))
	t.Cleanup(upstream.Close)

	lines := make(chan []byte, 8)
	srv, err := New(Options{
		Router:    router.KeywordRouter{},
		Upstream:  upstream.URL,
		Mode:      ModeObserve,
		Log:       log.New(io.Discard, "", 0),
		Shadow:    shadow,
		ShadowLog: chanWriter{lines},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, lines
}

func awaitEntry(t *testing.T, lines chan []byte) shadowEntry {
	t.Helper()
	select {
	case b := <-lines:
		var e shadowEntry
		if err := json.Unmarshal(b, &e); err != nil {
			t.Fatalf("bad shadow line %q: %v", b, err)
		}
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("no shadow entry written")
		return shadowEntry{}
	}
}

func post(t *testing.T, url, userText string) {
	t.Helper()
	resp, err := http.Post(url+"/v1/messages", "application/json",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":`+
			string(mustJSON(userText))+`}]}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200", resp.StatusCode)
	}
}

func mustJSON(s string) []byte {
	b, _ := json.Marshal(s)
	return b
}

func TestShadowDisagreementCapturesIntent(t *testing.T) {
	// Keyword routes "summarize …" Local; the shadow insists Frontier.
	ts, lines := newShadowServer(t, stubRouter{router.Decision{
		Tier: router.Frontier, Reason: "model tiny: FRONTIER"}})

	post(t, ts.URL, "summarize this paragraph for me")
	e := awaitEntry(t, lines)

	if e.Agree {
		t.Fatal("expected disagreement")
	}
	if e.KeywordTier != "local" || e.ShadowTier != "frontier" {
		t.Fatalf("tiers: keyword=%s shadow=%s", e.KeywordTier, e.ShadowTier)
	}
	if e.Intent != "summarize this paragraph for me" {
		t.Fatalf("disagreement must carry the intent for labeling, got %q", e.Intent)
	}
}

func TestShadowAgreementOmitsIntent(t *testing.T) {
	ts, lines := newShadowServer(t, stubRouter{router.Decision{
		Tier: router.Local, Reason: "model tiny: LOCAL"}})

	post(t, ts.URL, "summarize this paragraph for me")
	e := awaitEntry(t, lines)

	if !e.Agree {
		t.Fatalf("expected agreement, got keyword=%s shadow=%s", e.KeywordTier, e.ShadowTier)
	}
	if e.Intent != "" {
		t.Fatalf("agreement lines must not persist prompt text, got %q", e.Intent)
	}
}

func TestShadowErrorIsNotADisagreement(t *testing.T) {
	ts, lines := newShadowServer(t, failingRouter{})

	post(t, ts.URL, "summarize this paragraph for me")
	e := awaitEntry(t, lines)

	if e.Error == "" {
		t.Fatal("classifier failure should be recorded as an error")
	}
	if e.ShadowTier != "" || e.Intent != "" {
		t.Fatalf("error entries must carry no verdict or intent: %+v", e)
	}
}

func TestShadowSkipsInternalPayloads(t *testing.T) {
	ts, lines := newShadowServer(t, stubRouter{router.Decision{Tier: router.Local}})

	post(t, ts.URL, "Web page content: summarize and compare everything")
	select {
	case b := <-lines:
		t.Fatalf("internal payload reached the shadow router: %s", b)
	case <-time.After(300 * time.Millisecond):
	}
}
