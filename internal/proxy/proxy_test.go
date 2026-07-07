package proxy

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/connorhoulihan/rowtr/internal/router"
)

// newTestServer wires a proxy at a fake upstream that records what reaches it.
func newTestServer(t *testing.T, token string, requireAuth bool) (*httptest.Server, *http.Request, *[]byte) {
	t.Helper()
	var gotReq http.Request
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = *r.Clone(r.Context())
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"msg_upstream","type":"message"}`))
	}))
	t.Cleanup(upstream.Close)

	srv, err := New(Options{
		Router:   router.KeywordRouter{},
		Upstream: upstream.URL,
		Mode:     ModeObserve,
		Log:      log.New(io.Discard, "", 0),
		Token:    token, RequireAuth: requireAuth,
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, &gotReq, &gotBody
}

func TestAuthRequired(t *testing.T) {
	ts, upstreamReq, _ := newTestServer(t, "sekrit", true)

	// No header → 401, and nothing may reach the upstream.
	resp, err := http.Post(ts.URL+"/v1/messages", "application/json",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no header: got %d, want 401", resp.StatusCode)
	}
	if !strings.Contains(string(body), "authentication_error") {
		t.Fatalf("401 body should be an Anthropic-shaped error, got %s", body)
	}
	if upstreamReq.URL != nil {
		t.Fatal("unauthenticated request reached the upstream")
	}

	// Wrong token → still 401.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/messages",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set(AuthHeader, "wrong")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d, want 401", resp.StatusCode)
	}

	// Right token → forwarded, and the header must be stripped before upstream.
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/v1/messages",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set(AuthHeader, "sekrit")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid token: got %d, want 200", resp.StatusCode)
	}
	if upstreamReq.URL == nil {
		t.Fatal("authenticated request never reached the upstream")
	}
	if got := upstreamReq.Header.Get(AuthHeader); got != "" {
		t.Fatalf("auth header leaked upstream: %q", got)
	}
}

func TestHealthProofAndModeGating(t *testing.T) {
	ts, _, _ := newTestServer(t, "sekrit", true)

	get := func(nonce, clientToken string) map[string]string {
		u := ts.URL + "/rowtr/health"
		if nonce != "" {
			u += "?nonce=" + url.QueryEscape(nonce)
		}
		req, _ := http.NewRequest(http.MethodGet, u, nil)
		if clientToken != "" {
			req.Header.Set(AuthHeader, clientToken)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]string
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// Unauthenticated: liveness only — no mode disclosure.
	out := get("", "")
	if out["service"] != "rowtr" {
		t.Fatalf("service = %q", out["service"])
	}
	if _, ok := out["mode"]; ok {
		t.Fatal("mode disclosed to unauthenticated caller")
	}

	// Nonce challenge: proof must be the HMAC over the nonce keyed by the token.
	out = get("abc123", "")
	if out["proof"] != HealthProof("sekrit", "abc123") {
		t.Fatalf("bad proof %q", out["proof"])
	}
	// A listener without the token can't fake it.
	if out["proof"] == HealthProof("other-token", "abc123") {
		t.Fatal("proof collision across tokens")
	}

	// Authenticated caller gets the mode.
	out = get("", "sekrit")
	if out["mode"] != string(ModeObserve) {
		t.Fatalf("mode = %q, want observe", out["mode"])
	}
}

func TestNoAuthPassthrough(t *testing.T) {
	ts, upstreamReq, gotBody := newTestServer(t, "sekrit", false)

	resp, err := http.Post(ts.URL+"/v1/messages", "application/json",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hello there"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200", resp.StatusCode)
	}
	if upstreamReq.URL == nil {
		t.Fatal("request never reached upstream")
	}
	if !strings.Contains(string(*gotBody), "hello there") {
		t.Fatalf("body not forwarded intact: %s", *gotBody)
	}
}

func TestTruncateRuneSafe(t *testing.T) {
	s := strings.Repeat("é", 100) // 2 bytes per rune
	got := truncate(s, 80)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected truncation marker, got %q", got)
	}
	if strings.ContainsRune(got, '�') {
		t.Fatal("truncate split a UTF-8 sequence")
	}
	if want := strings.Repeat("é", 80) + "…"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if short := truncate("plain", 80); short != "plain" {
		t.Fatalf("short string mangled: %q", short)
	}
}
