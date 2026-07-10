package proxy

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"github.com/connorhoulihan/rowtr/internal/router"
)

const (
	cascadeThreshold = 7       // judge score (0-10) required to serve the local answer
	maxCascadeBody   = 32 << 10 // larger contexts overwhelm a small local model
)

// serveCascade is the FrugalGPT-style path for tool-free real prompts the
// router sent to the frontier: complete locally, have the local model judge
// its own answer, and serve it only if the judge is confident — otherwise
// nothing has been written and the caller falls through to the frontier.
func (s *Server) serveCascade(w http.ResponseWriter, r *http.Request, req anthropicRequest, out router.Outcome, category string) bool {
	resp, latencyMS, ok := s.completeLocal(r, req)
	if !ok {
		return false
	}
	score, ok := s.judgeLocal(r.Context(), out.Intent, resp.Text)
	if !ok || score < cascadeThreshold {
		s.log.Printf("cascade: local answer judged %d/10 — escalating to frontier", score)
		return false
	}
	s.log.Printf("cascade: local answer judged %d/10 — serving locally", score)
	s.emitLocal(w, req, resp, category, latencyMS, score, false, [32]byte{})
	return true
}

// judgeLocal scores a local answer with a second local pass. Any failure
// reports not-ok so the caller escalates — the judge must never block the
// frontier fallback.
func (s *Server) judgeLocal(ctx context.Context, question, answer string) (int, bool) {
	prompt := fmt.Sprintf(
		"Question:\n%s\n\nAnswer:\n%s\n\nOn a scale of 0 to 10, how completely and correctly does the answer address the question? Respond with only the integer.",
		question, answer)
	resp, err := s.local.Complete(ctx, prompt)
	if err != nil {
		s.log.Printf("cascade judge failed: %v", err)
		return 0, false
	}
	return firstInt(resp.Text)
}

func firstInt(s string) (int, bool) {
	start := -1
	for i, r := range s {
		if unicode.IsDigit(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			break
		}
	}
	if start < 0 {
		return 0, false
	}
	end := start
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n := 0
	for _, c := range strings.TrimSpace(s[start:end]) {
		n = n*10 + int(c-'0')
	}
	return n, true
}
