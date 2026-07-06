package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/connorhoulihan/rowtr/internal/router"
)

// Ollama is the local Gatekeeper backend. It talks to the Ollama daemon over its
// HTTP API directly (via net/http) rather than pulling in the full Ollama Go
// module — slice 1 only needs a single /api/chat call.
type Ollama struct {
	Host  string // base URL, e.g. http://localhost:11434
	Model string // model name, e.g. llama3.2
	HTTP  *http.Client
}

// NewOllama constructs an Ollama backend with a default HTTP client.
func NewOllama(host, model string) *Ollama {
	return &Ollama{
		Host:  host,
		Model: model,
		HTTP:  &http.Client{Timeout: 120 * time.Second},
	}
}

func (o *Ollama) Name() string      { return "ollama" }
func (o *Ollama) Tier() router.Tier { return router.Local }

// Available pings the daemon's root endpoint. A connection error here almost
// always means Ollama isn't installed or `ollama serve` isn't running.
func (o *Ollama) Available(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.Host, nil)
	if err != nil {
		return err
	}
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("ollama unreachable at %s: %w\n"+
			"  fix: install and start it — `brew install ollama && ollama serve && ollama pull %s`",
			o.Host, err, o.Model)
	}
	resp.Body.Close()
	return nil
}

// ollama /api/chat request/response shapes (subset we use).
type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaChatResponse struct {
	Message         ollamaMessage `json:"message"`
	PromptEvalCount int           `json:"prompt_eval_count"` // input tokens
	EvalCount       int           `json:"eval_count"`        // output tokens
	Error           string        `json:"error"`
}

// ChatMessage is a role-tagged turn for a multi-message local completion.
type ChatMessage struct {
	Role    string
	Content string
}

// Complete runs a single-prompt completion through the local model.
func (o *Ollama) Complete(ctx context.Context, prompt string) (Response, error) {
	return o.chat(ctx, []ollamaMessage{{Role: "user", Content: prompt}})
}

// Chat runs a multi-turn completion: an optional system prompt plus the
// conversation so far. The proxy uses this to offload a whole Claude Code
// conversation to the local tier. Empty turns are dropped, and any role that
// isn't user/assistant/system is coerced to user (Ollama only knows those).
func (o *Ollama) Chat(ctx context.Context, system string, msgs []ChatMessage) (Response, error) {
	om := make([]ollamaMessage, 0, len(msgs)+1)
	if strings.TrimSpace(system) != "" {
		om = append(om, ollamaMessage{Role: "system", Content: system})
	}
	for _, m := range msgs {
		if strings.TrimSpace(m.Content) == "" {
			continue
		}
		role := m.Role
		if role != "user" && role != "assistant" && role != "system" {
			role = "user"
		}
		om = append(om, ollamaMessage{Role: role, Content: m.Content})
	}
	return o.chat(ctx, om)
}

// chat performs the non-streaming /api/chat call shared by Complete and Chat.
func (o *Ollama) chat(ctx context.Context, msgs []ollamaMessage) (Response, error) {
	body, err := json.Marshal(ollamaChatRequest{Model: o.Model, Messages: msgs, Stream: false})
	if err != nil {
		return Response{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		o.Host+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.HTTP.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("ollama request failed: %w", err)
	}
	defer resp.Body.Close()

	var out ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Response{}, fmt.Errorf("decoding ollama response: %w", err)
	}
	if out.Error != "" {
		return Response{}, fmt.Errorf("ollama error: %s (is model %q pulled? `ollama pull %s`)",
			out.Error, o.Model, o.Model)
	}

	return Response{
		Text:         out.Message.Content,
		Model:        o.Model,
		InputTokens:  out.PromptEvalCount,
		OutputTokens: out.EvalCount,
	}, nil
}
