package proxy

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/9to5-worklife/rowtr/internal/router"
)

func TestUpgradeCacheTTL(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		wantChanged bool
		wantTTL     string // expected ttl on the first cache_control, if changed
	}{
		{"ephemeral without ttl", `{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}}]}`, true, "1h"},
		{"explicit 5m upgraded", `{"tools":[{"name":"t","cache_control":{"type":"ephemeral","ttl":"5m"}}]}`, true, "1h"},
		{"already 1h left alone", `{"system":[{"type":"text","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`, false, ""},
		{"no cache_control", `{"messages":[{"role":"user","content":"hi"}]}`, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			nb, changed := upgradeCacheTTL([]byte(c.body))
			if changed != c.wantChanged {
				t.Fatalf("changed = %v, want %v", changed, c.wantChanged)
			}
			if !changed {
				if string(nb) != c.body {
					t.Fatalf("unchanged body should be returned verbatim")
				}
				return
			}
			if !strings.Contains(string(nb), `"ttl":"`+c.wantTTL+`"`) {
				t.Fatalf("want ttl %q in %s", c.wantTTL, nb)
			}
		})
	}
}

func TestCache1hForwarding(t *testing.T) {
	const withBreakpoint = `{"model":"claude-opus-4-8","max_tokens":64,` +
		`"system":[{"type":"text","text":"sys","cache_control":{"type":"ephemeral"}}],` +
		`"messages":[{"role":"user","content":"compare postgres and dynamodb"}]}`

	forward := func(cache1h bool, body string) []byte {
		var hits atomic.Int64
		var lastBody []byte
		up := upstreamJSON(t, &hits, &lastBody)
		srv, err := New(Options{Router: router.KeywordRouter{}, Upstream: up.URL,
			Mode: ModeObserve, Log: log.New(io.Discard, "", 0), Cache1h: cache1h})
		if err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()
		resp, err := http.Post(ts.URL+"/v1/messages", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()
		return lastBody
	}

	// With the flag on, the forwarded system breakpoint carries ttl "1h".
	got := forward(true, withBreakpoint)
	var parsed struct {
		System []struct {
			CacheControl struct{ TTL string `json:"ttl"` } `json:"cache_control"`
		} `json:"system"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("forwarded body not JSON: %v", err)
	}
	if len(parsed.System) != 1 || parsed.System[0].CacheControl.TTL != "1h" {
		t.Fatalf("cache breakpoint not upgraded to 1h: %s", got)
	}

	// With the flag off, forwarding is byte-identical.
	if off := forward(false, withBreakpoint); string(off) != withBreakpoint {
		t.Fatalf("cache-1h off must forward byte-identical:\nsent %s\ngot  %s", withBreakpoint, off)
	}
}
