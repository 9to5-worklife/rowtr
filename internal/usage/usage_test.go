package usage

import (
	"path/filepath"
	"testing"
	"time"
)

func floatEq(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}

func openTemp(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestRecordAndSummary(t *testing.T) {
	st := openTemp(t)
	rec := func(e Event) {
		if err := st.Record(e); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	rec(Event{Time: time.Unix(1000, 0), Tier: "local", Model: "gemma", Category: "chat",
		InputTokens: 10, OutputTokens: 5, Offloaded: true, SavedUSD: 0.01})
	rec(Event{Time: time.Unix(2000, 0), Tier: "frontier", Model: "claude-opus-4-8", Category: "agent",
		InputTokens: 100, CacheReadTokens: 900, CacheWriteTokens: 50, OutputTokens: 33, CostUSD: 0.02})

	s, err := st.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if s.Total != 2 || s.Local != 1 || s.Frontier != 1 {
		t.Fatalf("counts: total=%d local=%d frontier=%d", s.Total, s.Local, s.Frontier)
	}
	if s.LocalTokens != 15 {
		t.Fatalf("LocalTokens=%d, want 15 (10 in + 5 out)", s.LocalTokens)
	}
	if s.FrontierTokens != 133 {
		t.Fatalf("FrontierTokens=%d, want 133 (100 in + 33 out)", s.FrontierTokens)
	}
	if s.FrontierInput != 100 || s.CacheReadTokens != 900 || s.CacheWriteTokens != 50 {
		t.Fatalf("frontier input/read/write = %d/%d/%d", s.FrontierInput, s.CacheReadTokens, s.CacheWriteTokens)
	}
	if s.MaxPromptTokens != 1050 {
		t.Fatalf("MaxPromptTokens=%d, want 1050 (100+900+50)", s.MaxPromptTokens)
	}
	if !floatEq(s.SavedUSD, 0.01) {
		t.Fatalf("SavedUSD=%v, want 0.01", s.SavedUSD)
	}
	if got := s.FrontierProcessed(); got != 1083 {
		t.Fatalf("FrontierProcessed=%d, want 1083 (133+900+50)", got)
	}
	if r := s.CacheHitRate(); r < 0.855 || r > 0.858 {
		t.Fatalf("CacheHitRate=%v, want ~0.857 (900/1050)", r)
	}
	if len(s.ByModel) != 2 {
		t.Fatalf("ByModel len=%d, want 2", len(s.ByModel))
	}
	if len(s.ByCategory) != 1 || s.ByCategory[0].Category != "agent" || s.ByCategory[0].Tokens != 1083 {
		t.Fatalf("ByCategory=%+v, want one 'agent' row of 1083 tokens", s.ByCategory)
	}
}

func TestSummarySinceFilters(t *testing.T) {
	st := openTemp(t)
	st.Record(Event{Time: time.Unix(1000, 0), Tier: "local", Model: "a", InputTokens: 1})
	st.Record(Event{Time: time.Unix(2000, 0), Tier: "frontier", Model: "b", InputTokens: 1})

	s, err := st.SummarySince(time.Unix(1500, 0))
	if err != nil {
		t.Fatal(err)
	}
	if s.Total != 1 || s.Frontier != 1 || s.Local != 0 {
		t.Fatalf("since 1500: total=%d local=%d frontier=%d, want only the frontier event", s.Total, s.Local, s.Frontier)
	}
}

func TestPromptTokens(t *testing.T) {
	e := Event{InputTokens: 10, CacheReadTokens: 20, CacheWriteTokens: 5}
	if e.PromptTokens() != 35 {
		t.Fatalf("PromptTokens=%d, want 35", e.PromptTokens())
	}
}

func TestEmptyStoreSummary(t *testing.T) {
	st := openTemp(t)
	s, err := st.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if s.Total != 0 || s.CacheHitRate() != 0 {
		t.Fatalf("empty store should be all-zero: %+v", s)
	}
}
