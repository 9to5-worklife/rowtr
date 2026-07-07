// Package eval is Rowtr's scoreboard: it runs the router over a labeled set of
// prompts. It scores the *offload decision* (would this request be served
// locally?) because that's the choice that actually saves money.
package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/connorhoulihan/rowtr/internal/router"
)

// Case is one labeled example. Offload is the ground truth: should Rowtr serve
// this locally?
type Case struct {
	Prompt  string `json:"prompt"`
	Tools   bool   `json:"tools"`
	Offload bool   `json:"offload"`
	Note    string `json:"note,omitempty"`
}

// Result pairs a case with what the router actually decided.
type Result struct {
	Case       Case
	GotOffload bool
	Outcome    router.Outcome
}

// Report is the scored run. Offload is treated as the positive class:
//
//	FP = wrongly sent to local  → a quality risk (weak model answered)
//	FN = kept on the frontier   → a missed saving
type Report struct {
	Results        []Result
	TP, FP, FN, TN int
}

// Load reads eval cases from a JSONL file. Blank lines and //-comment lines are
// skipped; the buffer is large because internal-payload prompts can be huge.
func Load(path string) ([]Case, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var cases []Case
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		t := strings.TrimSpace(sc.Text())
		if t == "" || strings.HasPrefix(t, "//") {
			continue
		}
		var c Case
		if err := json.Unmarshal([]byte(t), &c); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		cases = append(cases, c)
	}
	return cases, sc.Err()
}

// Run scores the router against the cases.
func Run(r router.Router, cases []Case) Report {
	var rep Report
	for _, c := range cases {
		out := router.Decide(r, c.Prompt, c.Tools)
		got := out.Offloadable
		rep.Results = append(rep.Results, Result{Case: c, GotOffload: got, Outcome: out})
		switch {
		case got && c.Offload:
			rep.TP++
		case got && !c.Offload:
			rep.FP++
		case !got && c.Offload:
			rep.FN++
		default:
			rep.TN++
		}
	}
	return rep
}

func (r Report) Total() int   { return r.TP + r.FP + r.FN + r.TN }
func (r Report) Correct() int { return r.TP + r.TN }

func (r Report) Accuracy() float64 {
	if r.Total() == 0 {
		return 0
	}
	return float64(r.Correct()) / float64(r.Total())
}

// String renders the scoreboard, listing every mismatch.
func (r Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "cases: %d   accuracy: %.0f%% (%d/%d correct)\n",
		r.Total(), r.Accuracy()*100, r.Correct(), r.Total())
	fmt.Fprintf(&b, "offload decision — TP:%d  FP:%d (wrongly local)  FN:%d (missed saving)  TN:%d\n",
		r.TP, r.FP, r.FN, r.TN)

	var mism []Result
	for _, res := range r.Results {
		if res.GotOffload != res.Case.Offload {
			mism = append(mism, res)
		}
	}
	if len(mism) == 0 {
		fmt.Fprint(&b, "\nno mismatches — every case routed as labeled.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "\nmismatches (%d):\n", len(mism))
	for _, res := range mism {
		kind := "FN missed-local"
		if res.GotOffload {
			kind = "FP wrongly-local"
		}
		fmt.Fprintf(&b, "  [%-16s] want offload=%-5v got=%-5v reason=%q\n      %s\n",
			kind, res.Case.Offload, res.GotOffload, res.Outcome.Reason, truncate(res.Case.Prompt, 90))
	}
	return b.String()
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
