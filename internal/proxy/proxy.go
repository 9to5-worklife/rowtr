// Package proxy is an Anthropic-API-compatible endpoint clients point at via
// ANTHROPIC_BASE_URL. In observe mode (default) everything is forwarded and
// only logged; in route mode, Local + tool-free requests are diverted to Ollama
// and translated back into Anthropic's response shape. The local completion
// runs fully BEFORE any bytes are written, so failures fall back to the
// frontier cleanly — the client never breaks.
package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/connorhoulihan/rowtr/internal/backend"
	"github.com/connorhoulihan/rowtr/internal/config"
	"github.com/connorhoulihan/rowtr/internal/router"
	"github.com/connorhoulihan/rowtr/internal/usage"
)

// Mode controls whether the proxy diverts traffic or only observes.
type Mode string

const (
	ModeObserve Mode = "observe" // forward everything, log only (safe default)
	ModeRoute   Mode = "route"   // divert Local + tool-free requests to Ollama
)

// Server is the reverse proxy plus the local-offload path.
type Server struct {
	router   router.Router
	upstream *url.URL
	local    *backend.Ollama
	mode     Mode
	store    *usage.Store // may be nil (recording disabled)
	log      *log.Logger
	rp       *httputil.ReverseProxy
	counter  atomic.Uint64
}

// New builds a proxy. upstream is the frontier base URL; local serves route-mode
// offloads; store may be nil.
func New(r router.Router, upstream string, local *backend.Ollama, mode Mode, store *usage.Store, logger *log.Logger) (*Server, error) {
	u, err := url.Parse(upstream)
	if err != nil {
		return nil, err
	}
	s := &Server{router: r, upstream: u, local: local, mode: mode, store: store, log: logger}
	s.rp = s.reverseProxy()
	return s, nil
}

// record persists a usage event; a nil store or write error is tolerated.
func (s *Server) record(e usage.Event) {
	if s.store == nil {
		return
	}
	if err := s.store.Record(e); err != nil {
		s.log.Printf("usage record failed: %v", err)
	}
}

func (s *Server) reverseProxy() *httputil.ReverseProxy {
	rp := httputil.NewSingleHostReverseProxy(s.upstream)
	rp.FlushInterval = -1 // flush every write immediately — required for SSE

	base := rp.Director
	rp.Director = func(req *http.Request) {
		base(req)
		// Host must be the upstream's — TLS routes on Host/SNI.
		req.Host = s.upstream.Host
		// Strip Accept-Encoding so Go's transport handles gzip itself and the
		// accounting tap (ModifyResponse) sees plaintext.
		req.Header.Del("Accept-Encoding")
	}
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		s.log.Printf("upstream error: %v", err)
		http.Error(w, "rowtr: upstream error: "+err.Error(), http.StatusBadGateway)
	}

	// Frontier token accounting: tap successful /v1/messages responses and
	// record the real usage Anthropic reports (SSE or JSON).
	rp.ModifyResponse = func(resp *http.Response) error {
		if s.store == nil || resp.Request == nil ||
			resp.Request.Method != http.MethodPost ||
			resp.Request.URL.Path != "/v1/messages" ||
			resp.StatusCode != http.StatusOK ||
			resp.Header.Get("Content-Encoding") != "" { // still compressed → can't parse
			return nil
		}
		isSSE := strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")
		resp.Body = &frontierTap{inner: resp.Body, isSSE: isSSE,
			done: func(model string, inTok, outTok int) {
				s.record(usage.Event{
					Time:         time.Now(),
					Tier:         "frontier",
					Model:        model,
					InputTokens:  inTok,
					OutputTokens: outTok,
					CostUSD:      config.EstimateCostUSD(model, inTok, outTok),
				})
			}}
		return nil
	}
	return rp
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// Served by Rowtr itself (never proxied) — lets `rowtr claude` tell a
	// Rowtr proxy from something else on the port.
	mux.HandleFunc("/rowtr/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"service":"rowtr","mode":%q}`, s.mode)
	})
	mux.HandleFunc("/v1/messages", s.handleMessages)
	mux.Handle("/", s.rp) // models, count_tokens, files, … pass straight through
	return mux
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	body := s.readAndRestore(r)
	req, _ := parseRequest(body)
	rawUser := lastUserText(req)

	if strings.TrimSpace(rawUser) == "" {
		s.rp.ServeHTTP(w, r)
		return
	}

	hasTools := len(req.Tools) > 0

	// Decide routes on the extracted human intent, not raw payload text.
	// Tool-bearing requests never offload — they need the frontier.
	out := router.Decide(s.router, rawUser, hasTools)
	offload := s.mode == ModeRoute && s.local != nil && out.Offloadable

	s.log.Printf("route=%s reason=%q tools=%v internal=%v stream=%v offload=%v model=%s intent=%q",
		out.Tier, out.Reason, hasTools, out.Internal, req.Stream, offload, req.Model, truncate(out.Intent, 80))

	if offload && s.serveLocal(w, r, req) {
		return
	}
	if offload {
		s.log.Printf("local offload failed — forwarding to frontier instead")
	}
	// Frontier usage is recorded by the ModifyResponse tap, not here.
	s.rp.ServeHTTP(w, r)
}

// serveLocal runs the local completion and, on success, writes an Anthropic-
// shaped response. On failure it returns false having written nothing, so the
// caller can fall back to the frontier.
func (s *Server) serveLocal(w http.ResponseWriter, r *http.Request, req anthropicRequest) bool {
	if err := s.local.Available(r.Context()); err != nil {
		s.log.Printf("ollama unavailable: %v", err)
		return false
	}

	system := extractText(req.System)
	msgs := make([]backend.ChatMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, backend.ChatMessage{Role: m.Role, Content: extractText(m.Content)})
	}

	// Complete fully before writing anything — this keeps the frontier
	// fallback clean.
	start := time.Now()
	resp, err := s.local.Chat(r.Context(), system, msgs)
	if err != nil {
		s.log.Printf("ollama completion failed: %v", err)
		return false
	}
	latency := time.Since(start)

	// SavedUSD estimates what the requested frontier model would have charged
	// for these token counts.
	s.record(usage.Event{
		Time:         time.Now(),
		Tier:         "local",
		Model:        resp.Model,
		InputTokens:  resp.InputTokens,
		OutputTokens: resp.OutputTokens,
		LatencyMS:    latency.Milliseconds(),
		Offloaded:    true,
		SavedUSD:     config.EstimateCostUSD(req.Model, resp.InputTokens, resp.OutputTokens),
	})

	w.Header().Set("X-Rowtr-Tier", "local")
	w.Header().Set("X-Rowtr-Model", resp.Model)

	if req.Stream {
		s.writeSSE(w, req.Model, resp)
	} else {
		s.writeJSON(w, req.Model, resp)
	}
	return true
}

// writeJSON emits a non-streaming Anthropic Message, echoing the requested
// model string; the real backend is disclosed via the X-Rowtr-* headers.
func (s *Server) writeJSON(w http.ResponseWriter, model string, resp backend.Response) {
	out := map[string]any{
		"id":            s.msgID(),
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       []map[string]any{{"type": "text", "text": resp.Text}},
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage": map[string]int{
			"input_tokens":  resp.InputTokens,
			"output_tokens": resp.OutputTokens,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(out)
}

// writeSSE emits the Anthropic streaming event sequence for a single text
// block; the full local completion is delivered as one text_delta.
func (s *Server) writeSSE(w http.ResponseWriter, model string, resp backend.Response) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	id := s.msgID()
	send := func(event string, data any) {
		b, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}

	send("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            id,
			"type":          "message",
			"role":          "assistant",
			"model":         model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]int{"input_tokens": resp.InputTokens, "output_tokens": 0},
		},
	})
	send("content_block_start", map[string]any{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
	send("content_block_delta", map[string]any{
		"type": "content_block_delta", "index": 0,
		"delta": map[string]any{"type": "text_delta", "text": resp.Text},
	})
	send("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	send("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]int{"output_tokens": resp.OutputTokens},
	})
	send("message_stop", map[string]any{"type": "message_stop"})
}

func (s *Server) msgID() string {
	return fmt.Sprintf("msg_rowtr_local_%d", s.counter.Add(1))
}

// readAndRestore reads the request body and restores it so the request can
// still be forwarded intact.
func (s *Server) readAndRestore(r *http.Request) []byte {
	if r.Body == nil {
		return nil
	}
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		s.log.Printf("read body: %v", err)
		r.Body = io.NopCloser(bytes.NewReader(nil))
		return nil
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	return body
}

type anthropicRequest struct {
	Model    string            `json:"model"`
	System   json.RawMessage   `json:"system"`
	Messages []anthropicMsg    `json:"messages"`
	Tools    []json.RawMessage `json:"tools"`
	Stream   bool              `json:"stream"`
}

type anthropicMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func parseRequest(body []byte) (anthropicRequest, error) {
	var req anthropicRequest
	err := json.Unmarshal(body, &req)
	return req, err
}

func lastUserText(req anthropicRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			return extractText(req.Messages[i].Content)
		}
	}
	return ""
}

// extractText handles both content shapes: a bare string, or an array of
// blocks (text blocks concatenated; images, tool_use, etc. ignored).
func extractText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var b strings.Builder
		for _, bl := range blocks {
			if bl.Type == "text" {
				b.WriteString(bl.Text)
			}
		}
		return b.String()
	}
	return ""
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
