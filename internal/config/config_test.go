package config

import (
	"os"
	"path/filepath"
	"testing"
)

func approxEq(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}

func TestEstimateCostUSD(t *testing.T) {
	// opus is $5/M input, $25/M output.
	if got := EstimateCostUSD("claude-opus-4-8", 1_000_000, 1_000_000); !approxEq(got, 30.0) {
		t.Fatalf("opus 1M in + 1M out = %v, want 30.0", got)
	}
	// haiku is $1/M input.
	if got := EstimateCostUSD("claude-haiku-4-5", 2_000_000, 0); !approxEq(got, 2.0) {
		t.Fatalf("haiku 2M in = %v, want 2.0", got)
	}
	// Unknown / local models cost nothing.
	if got := EstimateCostUSD("some-local-model", 1_000_000, 1_000_000); got != 0 {
		t.Fatalf("unknown model should cost 0, got %v", got)
	}
}

func TestCachePricing(t *testing.T) {
	// 5-minute write: 1M × 1.25 × $5/M = 6.25.
	if got := EstimateCostUSDCached1h("claude-opus-4-8", 0, 0, 1_000_000, 0, 0); !approxEq(got, 6.25) {
		t.Fatalf("5m write = %v, want 6.25", got)
	}
	// 1-hour write: 1M × 2.0 × $5/M = 10.00 — the whole point of the feature is
	// that a 1-hour write costs more than a 5-minute one.
	if got := EstimateCostUSDCached1h("claude-opus-4-8", 0, 0, 0, 1_000_000, 0); !approxEq(got, 10.0) {
		t.Fatalf("1h write = %v, want 10.0", got)
	}
	// Cache read: 1M × 0.1 × $5/M = 0.50.
	if got := EstimateCostUSDCached1h("claude-opus-4-8", 0, 1_000_000, 0, 0, 0); !approxEq(got, 0.5) {
		t.Fatalf("read = %v, want 0.5", got)
	}
	// EstimateCostUSDCached must equal the 1h form with zero 1h-writes.
	plain := EstimateCostUSDCached("claude-opus-4-8", 10, 20, 30, 40)
	as1h := EstimateCostUSDCached1h("claude-opus-4-8", 10, 20, 30, 0, 40)
	if !approxEq(plain, as1h) {
		t.Fatalf("EstimateCostUSDCached (%v) must equal the 1h form with 0 1h-writes (%v)", plain, as1h)
	}
	// Same write volume is strictly pricier at the 1-hour TTL.
	w5 := EstimateCostUSDCached1h("claude-opus-4-8", 0, 0, 1000, 0, 0)
	w1 := EstimateCostUSDCached1h("claude-opus-4-8", 0, 0, 0, 1000, 0)
	if w1 <= w5 {
		t.Fatalf("1h write (%v) should cost more than the same 5m write (%v)", w1, w5)
	}
}

func TestMergeOverlaysNonEmptyOnly(t *testing.T) {
	dst := Defaults()
	frontierBefore := dst.FrontierModel
	merge(&dst, Config{LocalModel: "custom-local", Cache1h: true, CascadeJudge: "haiku"})

	if dst.LocalModel != "custom-local" {
		t.Fatalf("LocalModel not overlaid: %q", dst.LocalModel)
	}
	if !dst.Cache1h {
		t.Fatal("Cache1h not overlaid")
	}
	if dst.CascadeJudge != "haiku" {
		t.Fatalf("CascadeJudge not overlaid: %q", dst.CascadeJudge)
	}
	// An empty field in src must not clobber the destination.
	if dst.FrontierModel != frontierBefore {
		t.Fatalf("empty src field overwrote FrontierModel: %q", dst.FrontierModel)
	}
}

func TestEnvOverride(t *testing.T) {
	t.Setenv("ROWTR_TEST_KEY_X", "from-env")
	if got := env("ROWTR_TEST_KEY_X", "fallback"); got != "from-env" {
		t.Fatalf("env override = %q, want from-env", got)
	}
	if got := env("ROWTR_TEST_MISSING_KEY_ZZZ", "fallback"); got != "fallback" {
		t.Fatalf("missing env = %q, want fallback", got)
	}
}

func TestDefaults(t *testing.T) {
	d := Defaults()
	if d.FrontierModel != DefaultFrontierModel || d.LocalModel != DefaultLocalModel {
		t.Fatalf("defaults not wired: %+v", d)
	}
	if d.DownshiftModel != DefaultDownshiftModel || d.RouterModel != DefaultRouterModel {
		t.Fatalf("model defaults not wired: %+v", d)
	}
}

func TestRouterHost(t *testing.T) {
	c := Config{OllamaHost: "http://localhost:11434", RouterOllamaHost: "http://pi.local:11434"}
	if c.RouterHost() != "http://pi.local:11434" {
		t.Fatalf("a dedicated router host should win: %q", c.RouterHost())
	}
	c.RouterOllamaHost = ""
	if c.RouterHost() != "http://localhost:11434" {
		t.Fatalf("an empty router host should fall back to OllamaHost: %q", c.RouterHost())
	}
}

func TestLoadFileRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	data := `{"local_model":"gemma3n:e2b","cache_1h":true,"cascade_judge":"haiku","proxy_mode":"route"}`
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := loadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.LocalModel != "gemma3n:e2b" || !c.Cache1h || c.CascadeJudge != "haiku" || c.ProxyMode != "route" {
		t.Fatalf("loadFile parsed wrong: %+v", c)
	}
}
