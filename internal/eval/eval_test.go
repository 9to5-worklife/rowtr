package eval

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/9to5-worklife/rowtr/internal/router"
)

// stubRouter routes Local when the prompt contains a marker, else Frontier —
// enough to drive every quadrant of the confusion matrix deterministically.
type stubRouter struct{ localMarker string }

func (s stubRouter) Route(_ context.Context, prompt string) router.Decision {
	if len(s.localMarker) > 0 && containsMarker(prompt, s.localMarker) {
		return router.Decision{Tier: router.Local, Reason: "stub-local"}
	}
	return router.Decision{Tier: router.Frontier, Reason: "stub-frontier"}
}

func containsMarker(s, m string) bool {
	for i := 0; i+len(m) <= len(s); i++ {
		if s[i:i+len(m)] == m {
			return true
		}
	}
	return false
}

func TestLoadSkipsCommentsAndBlanks(t *testing.T) {
	p := filepath.Join(t.TempDir(), "e.jsonl")
	content := "// a comment line\n\n" +
		`{"prompt":"summarize x","tools":false,"offload":true}` + "\n" +
		`{"prompt":"do reasoning","offload":false,"note":"hard"}` + "\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cases, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 2 {
		t.Fatalf("want 2 cases (comment + blank skipped), got %d", len(cases))
	}
	if cases[0].Prompt != "summarize x" || !cases[0].Offload {
		t.Fatalf("case 0 parsed wrong: %+v", cases[0])
	}
	if cases[1].Note != "hard" {
		t.Fatalf("case 1 note = %q", cases[1].Note)
	}
}

func TestRunClassifiesEveryQuadrant(t *testing.T) {
	r := stubRouter{localMarker: "LOCAL"}
	cases := []Case{
		{Prompt: "LOCAL easy", Offload: true},                      // routed local, labeled offload → TP
		{Prompt: "hard reasoning", Offload: false},                 // routed frontier, labeled not     → TN
		{Prompt: "LOCAL but risky", Offload: false},                // routed local, labeled not        → FP
		{Prompt: "please do something", Offload: true},             // routed frontier, labeled offload → FN
		{Prompt: "LOCAL with tools", Tools: true, Offload: false},  // tools force non-offloadable      → TN
	}
	rep := Run(r, cases)
	if rep.TP != 1 || rep.FP != 1 || rep.FN != 1 || rep.TN != 2 {
		t.Fatalf("confusion matrix: TP=%d FP=%d FN=%d TN=%d", rep.TP, rep.FP, rep.FN, rep.TN)
	}
	if rep.Total() != 5 || rep.Correct() != 3 {
		t.Fatalf("total=%d correct=%d, want 5/3", rep.Total(), rep.Correct())
	}
	if a := rep.Accuracy(); a < 0.59 || a > 0.61 {
		t.Fatalf("accuracy=%v, want 0.6", a)
	}
}

func TestEmptyReportAccuracy(t *testing.T) {
	if a := (Report{}).Accuracy(); a != 0 {
		t.Fatalf("empty report accuracy = %v, want 0 (no divide-by-zero)", a)
	}
}

// TestRegressionSetStillPasses guards the shipped router against silent
// routing regressions, and reports the current offload numbers.
func TestRegressionSetStillPasses(t *testing.T) {
	const path = "../../evals/router.jsonl"
	cases, err := Load(path)
	if err != nil {
		t.Skipf("regression set not found at %s: %v", path, err)
	}
	rep := Run(router.KeywordRouter{}, cases)
	offloadRate := 100 * float64(rep.TP+rep.FP) / float64(rep.Total())
	t.Logf("regression set: %d cases · accuracy %.1f%% · TP=%d FP=%d FN=%d TN=%d · offload rate %.0f%%",
		rep.Total(), rep.Accuracy()*100, rep.TP, rep.FP, rep.FN, rep.TN, offloadRate)
	if rep.Accuracy() < 0.95 {
		t.Fatalf("keyword router regressed on its own eval set: %.1f%%\n%s", rep.Accuracy()*100, rep.String())
	}
	if rep.TP == 0 {
		t.Fatal("regression set has no true-positive offloads — it isn't guarding routing")
	}
}
