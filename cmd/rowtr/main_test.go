package main

import (
	"testing"

	"github.com/9to5-worklife/rowtr/internal/router"
)

func TestEvalSetPath(t *testing.T) {
	if p, _ := evalSetPath("", "regression"); p != "evals/router.jsonl" {
		t.Fatalf("regression → %q", p)
	}
	if p, _ := evalSetPath("", "generalization"); p != "evals/generalization.jsonl" {
		t.Fatalf("generalization → %q", p)
	}
	if p, _ := evalSetPath("custom.jsonl", "regression"); p != "custom.jsonl" {
		t.Fatalf("explicit --evals must win → %q", p)
	}
	if _, err := evalSetPath("", "bogus"); err == nil {
		t.Fatal("unknown --set should error")
	}
}

func TestLoopbackHost(t *testing.T) {
	for _, h := range []string{"localhost", "127.0.0.1", "::1"} {
		if !loopbackHost(h) {
			t.Fatalf("%q should be loopback", h)
		}
	}
	for _, h := range []string{"0.0.0.0", "192.168.1.5", "", "example.com"} {
		if loopbackHost(h) {
			t.Fatalf("%q should NOT be loopback", h)
		}
	}
}

// TestCheckAddrSafety guards the security invariant: the proxy speaks cleartext
// and relays Claude credentials, so it must refuse to expose itself off-machine
// unless the user explicitly opts in.
func TestCheckAddrSafety(t *testing.T) {
	if err := checkAddrSafety("127.0.0.1:8787", "https://api.anthropic.com", false); err != nil {
		t.Fatalf("loopback bind + https upstream should be allowed: %v", err)
	}
	if err := checkAddrSafety("0.0.0.0:8787", "https://api.anthropic.com", false); err == nil {
		t.Fatal("non-loopback bind must be refused without --unsafe-remote")
	}
	if err := checkAddrSafety("0.0.0.0:8787", "https://api.anthropic.com", true); err != nil {
		t.Fatalf("--unsafe-remote should permit a non-loopback bind: %v", err)
	}
	if err := checkAddrSafety("127.0.0.1:8787", "http://evil.example.com", false); err == nil {
		t.Fatal("a cleartext remote upstream must be refused")
	}
	if err := checkAddrSafety("127.0.0.1:8787", "http://127.0.0.1:1234", false); err != nil {
		t.Fatalf("a cleartext loopback upstream should be allowed: %v", err)
	}
}

func TestParseTier(t *testing.T) {
	if tr, err := parseTier("auto"); err != nil || tr != nil {
		t.Fatalf("auto should be (nil, nil), got (%v, %v)", tr, err)
	}
	if tr, err := parseTier("local"); err != nil || tr == nil || *tr != router.Local {
		t.Fatalf("local parse wrong: (%v, %v)", tr, err)
	}
	if tr, err := parseTier("frontier"); err != nil || tr == nil || *tr != router.Frontier {
		t.Fatalf("frontier parse wrong: (%v, %v)", tr, err)
	}
	if _, err := parseTier("nonsense"); err == nil {
		t.Fatal("an invalid tier should error")
	}
}
