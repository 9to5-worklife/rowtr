package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ModelRouter asks a small language model (via Ollama) which tier should
// handle a prompt. It exists to test the hypothesis that a learned router
// generalizes past the keyword heuristic; run it in shadow mode (proxy) or on
// the scoreboard (`rowtr score --router model`) before ever trusting it live.
//
// The classifier model is meant to be tiny (e.g. gemma3:270m) and may live on
// another machine on the LAN — a Raspberry Pi works.
type ModelRouter struct {
	Host  string // Ollama base URL, e.g. http://raspberrypi.local:11434
	Model string // classifier model, e.g. gemma3:270m
	// Timeout bounds one routing call end to end. Zero uses the default —
	// generous enough for a cold model load; tune down for authoritative use.
	Timeout time.Duration
	// Fallback answers when the model errors or is unparseable. Nil falls back
	// to Frontier: routing uncertainty must never send work to the weak tier.
	Fallback Router
	HTTP     *http.Client
}

const defaultRouteTimeout = 10 * time.Second

// maxRoutePromptChars caps how much of the prompt the classifier sees. The
// routing signal lives in the head of the prompt, and prefill is the whole
// cost of the call on small hardware.
const maxRoutePromptChars = 2000

// routeSystemPrompt is deliberately short and constant so Ollama's prefix
// cache absorbs it across calls.
const routeSystemPrompt = `You classify requests for an AI model router.
Answer LOCAL for simple self-contained tasks: summarizing, defining, translating, rephrasing, classifying, extracting, counting, short factual questions.
Answer FRONTIER for anything needing deep reasoning or high quality: analysis, comparison, design, planning, debugging, writing code or prose, multi-step or ambiguous requests.
Answer with exactly one word: LOCAL or FRONTIER.`

// Route implements Router. Errors are absorbed: the Fallback (or Frontier)
// answers, with the failure preserved in Reason so misroutes stay auditable.
func (m *ModelRouter) Route(ctx context.Context, prompt string) Decision {
	d, err := m.RouteChecked(ctx, prompt)
	if err == nil {
		return d
	}
	if m.Fallback != nil {
		fb := m.Fallback.Route(ctx, prompt)
		fb.Reason = "model-fallback: " + fb.Reason
		return fb
	}
	return Decision{Tier: Frontier, Score: 1, Reason: "model error → frontier: " + err.Error()}
}

// RouteChecked is Route with the error surfaced instead of absorbed — shadow
// mode and evals need to tell "the model said Frontier" from "the model broke".
func (m *ModelRouter) RouteChecked(ctx context.Context, prompt string) (Decision, error) {
	timeout := m.Timeout
	if timeout == 0 {
		timeout = defaultRouteTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	verdict, err := m.classify(ctx, truncateRunes(prompt, maxRoutePromptChars))
	if err != nil {
		return Decision{}, err
	}
	tier, err := parseVerdict(verdict)
	if err != nil {
		return Decision{}, err
	}
	d := Decision{Tier: tier, Score: 0.9, Reason: fmt.Sprintf("model %s: FRONTIER", m.Model)}
	if tier == Local {
		d = Decision{Tier: tier, Score: 0.1, Reason: fmt.Sprintf("model %s: LOCAL", m.Model)}
	}
	return d, nil
}

// CheckedRouter is a Router whose decisions can fail visibly. The proxy's
// shadow mode prefers this so a broken classifier is recorded as an error, not
// mistaken for a real disagreement.
type CheckedRouter interface {
	Router
	RouteChecked(ctx context.Context, prompt string) (Decision, error)
}

// Ollama /api/chat shapes (the subset the classifier call needs). The router
// keeps its own client rather than reusing the backend package: backend
// imports router, and the classifier call needs sampler options (temperature
// 0, capped output) the completion path must not inherit.
type ollamaRouteRequest struct {
	Model     string               `json:"model"`
	Messages  []ollamaRouteMessage `json:"messages"`
	Stream    bool                 `json:"stream"`
	KeepAlive int                  `json:"keep_alive"` // -1: keep the model resident
	Options   map[string]any       `json:"options"`
}

type ollamaRouteMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaRouteResponse struct {
	Message ollamaRouteMessage `json:"message"`
	Error   string             `json:"error"`
}

func (m *ModelRouter) classify(ctx context.Context, prompt string) (string, error) {
	body, err := json.Marshal(ollamaRouteRequest{
		Model: m.Model,
		Messages: []ollamaRouteMessage{
			{Role: "system", Content: routeSystemPrompt},
			{Role: "user", Content: prompt},
		},
		Stream:    false,
		KeepAlive: -1,
		Options: map[string]any{
			"temperature": 0, // a router must be deterministic
			"num_predict": 8, // one word — anything longer is a malfunction
		},
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		m.Host+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	client := m.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("router model request failed: %w", err)
	}
	defer resp.Body.Close()

	var out ollamaRouteResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decoding router model response: %w", err)
	}
	if out.Error != "" {
		return "", fmt.Errorf("router model error: %s (is %q pulled on %s?)", out.Error, m.Model, m.Host)
	}
	return out.Message.Content, nil
}

// parseVerdict accepts exactly one of the two labels; an answer containing
// both or neither is a malfunction and must not silently pick a tier.
func parseVerdict(s string) (Tier, error) {
	u := strings.ToUpper(s)
	local := strings.Contains(u, "LOCAL")
	frontier := strings.Contains(u, "FRONTIER")
	switch {
	case local && !frontier:
		return Local, nil
	case frontier && !local:
		return Frontier, nil
	}
	return Frontier, fmt.Errorf("unparseable verdict %q", truncateRunes(s, 60))
}

func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
