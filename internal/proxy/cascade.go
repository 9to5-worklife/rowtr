package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/9to5-worklife/rowtr/internal/router"
)

const (
	cascadeThreshold    = 7                // judge score (0-10) required to serve the local answer
	maxCascadeBody      = 32 << 10         // larger contexts overwhelm a small local model
	cascadeJudgeTimeout = 20 * time.Second // bounds the frontier-judge call
)

// cascadeState is embedded in Server: the judge model (empty = free local
// self-judge) and the optional outcome-feedback log.
type cascadeState struct {
	cascadeJudgeModel string
	cascadeLog        io.Writer
	cascadeMu         sync.Mutex
}

// cascadeOutcome is one judged cascade decision — the feedback corpus. A served
// outcome is a proven "local was good enough"; an escalation is a proven "not".
type cascadeOutcome struct {
	Time       time.Time `json:"time"`
	Category   string    `json:"category"`
	Score      int       `json:"score"`
	Served     bool      `json:"served"`
	JudgeTier  string    `json:"judge_tier"`
	Model      string    `json:"model"`
	LatencyMS  int64     `json:"latency_ms"`
	IntentHash string    `json:"intent_hash"`
	Intent     string    `json:"intent,omitempty"`
}

// serveCascade is the FrugalGPT-style path for tool-free real prompts the
// router sent to the frontier: complete locally, judge that answer, and serve
// it only if the judge is confident — otherwise nothing has been written and
// the caller falls through to the frontier. Every judged outcome is logged as
// feedback (served or escalated).
func (s *Server) serveCascade(w http.ResponseWriter, r *http.Request, req anthropicRequest, out router.Outcome, category string) bool {
	resp, latencyMS, ok := s.completeLocal(r, req)
	if !ok {
		return false // no local answer to judge; escalate silently
	}

	var score int
	judgeTier := "local"
	if s.cascadeJudgeModel != "" {
		judgeTier = "frontier"
		score, ok = s.judgeFrontier(r, out.Intent, resp.Text, s.cascadeJudgeModel)
	} else {
		score, ok = s.judgeLocal(r.Context(), out.Intent, resp.Text)
	}
	served := ok && score >= cascadeThreshold

	s.logCascade(cascadeOutcome{
		Time: time.Now(), Category: category, Score: score, Served: served,
		JudgeTier: judgeTier, Model: resp.Model, LatencyMS: latencyMS,
		IntentHash: hashIntent(out.Intent), Intent: out.Intent,
	})

	if !served {
		s.log.Printf("cascade: local answer judged %d/10 (%s judge) — escalating to frontier", score, judgeTier)
		return false
	}
	s.log.Printf("cascade: local answer judged %d/10 (%s judge) — serving locally", score, judgeTier)
	s.emitLocal(w, req, resp, category, latencyMS, score, false, [32]byte{})
	return true
}

// judgeLocal scores a local answer with a second local pass. Cheap and private,
// but the generator grades itself — errors are correlated, so it tends to bless
// its own mistakes. Any failure reports not-ok so the caller escalates.
func (s *Server) judgeLocal(ctx context.Context, question, answer string) (int, bool) {
	resp, err := s.local.Complete(ctx, judgePrompt(question, answer))
	if err != nil {
		s.log.Printf("cascade judge (local) failed: %v", err)
		return 0, false
	}
	return firstInt(resp.Text)
}

// judgeFrontier grades a local answer with a cheap Claude model — a far better
// grader than the model that produced the answer. It reuses the client's own
// Anthropic credentials from the incoming request; the proxy holds no creds of
// its own. Any failure reports not-ok so the caller escalates cleanly.
func (s *Server) judgeFrontier(r *http.Request, question, answer, model string) (int, bool) {
	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": 8,
		"messages":   []map[string]string{{"role": "user", "content": judgePrompt(question, answer)}},
	})
	u := *s.upstream
	u.Path = "/v1/messages"
	hreq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		s.log.Printf("cascade judge (frontier) build failed: %v", err)
		return 0, false
	}
	// Carry the client's Anthropic auth so the judge call authenticates exactly
	// as the real request would — never the local rowtr token.
	for _, h := range []string{"Authorization", "X-Api-Key", "Anthropic-Version", "Anthropic-Beta"} {
		if v := r.Header.Get(h); v != "" {
			hreq.Header.Set(h, v)
		}
	}
	hreq.Header.Set("Content-Type", "application/json")
	if hreq.Header.Get("Anthropic-Version") == "" {
		hreq.Header.Set("Anthropic-Version", "2023-06-01")
	}
	resp, err := (&http.Client{Timeout: cascadeJudgeTimeout}).Do(hreq)
	if err != nil {
		s.log.Printf("cascade judge (frontier) call failed: %v", err)
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		s.log.Printf("cascade judge (frontier) returned status %d", resp.StatusCode)
		return 0, false
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return 0, false
	}
	var b strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return firstInt(b.String())
}

func judgePrompt(question, answer string) string {
	return fmt.Sprintf(
		"Question:\n%s\n\nAnswer:\n%s\n\nOn a scale of 0 to 10, how completely and correctly does the answer address the question? Respond with only the integer.",
		question, answer)
}

func hashIntent(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func (s *Server) logCascade(o cascadeOutcome) {
	if s.cascadeLog == nil {
		return
	}
	s.cascadeMu.Lock()
	defer s.cascadeMu.Unlock()
	if err := json.NewEncoder(s.cascadeLog).Encode(o); err != nil {
		s.log.Printf("cascade: outcome write failed: %v", err)
	}
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
