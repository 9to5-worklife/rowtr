// Package proxy is an Anthropic-API-compatible endpoint clients point at via
// ANTHROPIC_BASE_URL. In observe mode (default) everything is forwarded and
// only logged; in route mode, Local + tool-free requests are diverted to Ollama
// and translated back into Anthropic's response shape. The local completion
// runs fully BEFORE any bytes are written, so failures fall back to the
// frontier cleanly — the client never breaks.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/9to5-worklife/rowtr/internal/backend"
	"github.com/9to5-worklife/rowtr/internal/config"
	"github.com/9to5-worklife/rowtr/internal/router"
	"github.com/9to5-worklife/rowtr/internal/usage"
)

// Mode controls whether the proxy diverts traffic or only observes.
type Mode string

const (
	ModeObserve Mode = "observe" // forward everything, log only (safe default)
	ModeRoute   Mode = "route"   // divert Local + tool-free requests to Ollama
)

// Options configures a proxy Server.
type Options struct {
	Router   router.Router
	Upstream string // frontier base URL
	Local    *backend.Ollama
	Mode     Mode
	Store    *usage.Store // may be nil (recording disabled)
	Log      *log.Logger

	// Token is the shared secret (see LoadOrCreateToken). It keys the health
	// proof and, when RequireAuth is set, authenticates clients on /v1/messages.
	Token       string
	RequireAuth bool
	// DebugIntent includes the extracted prompt text in routing log lines.
	// Off by default: prompt fragments must not persist in logs.
	DebugIntent bool
	// DownshiftModel serves allowlisted-internal requests that can't go local
	// on a cheaper Claude model. Empty disables downshifting.
	DownshiftModel string
	// Cascade enables try-local-then-judge for tool-free frontier prompts.
	Cascade bool
	// Shadow runs an experimental second router on real human turns and logs
	// how it compares to the authoritative one (see shadow.go). Nil disables.
	Shadow router.Router
	// ShadowLog receives one JSON line per shadow comparison. Disagreement
	// lines include the prompt intent — callers must treat the destination as
	// user-private (0o600 file).
	ShadowLog io.Writer
}

// Server is the reverse proxy plus the local-offload path.
type Server struct {
	router         router.Router
	upstream       *url.URL
	local          *backend.Ollama
	mode           Mode
	store          *usage.Store
	log            *log.Logger
	token          string
	requireAuth    bool
	debugIntent    bool
	downshiftModel string
	cascade        bool
	dedup          *dedupCache
	rp             *httputil.ReverseProxy
	counter        atomic.Uint64
	shadowState
}

// New builds a proxy.
func New(opts Options) (*Server, error) {
	u, err := url.Parse(opts.Upstream)
	if err != nil {
		return nil, err
	}
	s := &Server{
		router: opts.Router, upstream: u, local: opts.Local, mode: opts.Mode,
		store: opts.Store, log: opts.Log,
		token: opts.Token, requireAuth: opts.RequireAuth, debugIntent: opts.DebugIntent,
		downshiftModel: opts.DownshiftModel, cascade: opts.Cascade,
		dedup: newDedupCache(),
		shadowState: shadowState{
			shadow: opts.Shadow, shadowLog: opts.ShadowLog,
			shadowSem: make(chan struct{}, maxConcurrentShadows),
		},
	}
	s.rp = s.reverseProxy()
	return s, nil
}

// Request-scoped values handed from handleMessages to the accounting tap.
type ctxKey int

const (
	ctxCategory ctxKey = iota
	ctxRequestedModel
	ctxDedupKey
)

// categorize buckets a request for the per-category spend breakdown.
func categorize(out router.Outcome, hasTools bool) string {
	switch {
	case out.Internal:
		return "internal"
	case hasTools:
		return "agent"
	default:
		return "chat"
	}
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
		// The auth token is local-only — it must never reach the upstream.
		req.Header.Del(AuthHeader)
	}
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		s.log.Printf("upstream error: %v", err)
		http.Error(w, "rowtr: upstream error: "+err.Error(), http.StatusBadGateway)
	}

	// Frontier token accounting: tap successful /v1/messages responses and
	// record the real usage Anthropic reports (SSE or JSON), including the
	// prompt-cache split. Dedup-eligible responses are also captured for replay.
	rp.ModifyResponse = func(resp *http.Response) error {
		if resp.Request == nil ||
			resp.Request.Method != http.MethodPost ||
			resp.Request.URL.Path != "/v1/messages" ||
			resp.StatusCode != http.StatusOK ||
			resp.Header.Get("Content-Encoding") != "" { // still compressed → can't parse
			return nil
		}
		ctx := resp.Request.Context()
		category, _ := ctx.Value(ctxCategory).(string)
		reqModel, _ := ctx.Value(ctxRequestedModel).(string)
		dkey, wantDedup := ctx.Value(ctxDedupKey).([32]byte)
		if s.store == nil && !wantDedup {
			return nil
		}

		isSSE := strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")
		var capturedBody []byte
		var onBody func([]byte)
		if wantDedup && !isSSE {
			onBody = func(b []byte) { capturedBody = append([]byte(nil), b...) }
		}
		resp.Body = &frontierTap{inner: resp.Body, isSSE: isSSE, onBody: onBody,
			done: func(model string, u tapUsage) {
				if capturedBody != nil {
					s.dedup.put(dkey, dedupEntry{body: capturedBody, model: model, inTok: u.InTok, outTok: u.OutTok})
				}
				if s.store == nil {
					return
				}
				cost := config.EstimateCostUSDCached(model, u.InTok, u.CacheRead, u.CacheWrite, u.OutTok)
				saved := 0.0
				if reqModel != "" && reqModel != model { // downshifted — savings vs the requested model
					if d := config.EstimateCostUSDCached(reqModel, u.InTok, u.CacheRead, u.CacheWrite, u.OutTok) - cost; d > 0 {
						saved = d
					}
				}
				s.record(usage.Event{
					Time:             time.Now(),
					Tier:             "frontier",
					Model:            model,
					Category:         category,
					InputTokens:      u.InTok,
					CacheReadTokens:  u.CacheRead,
					CacheWriteTokens: u.CacheWrite,
					OutputTokens:     u.OutTok,
					CostUSD:          cost,
					SavedUSD:         saved,
				})
			}}
		return nil
	}
	return rp
}

// forward hands the request to the reverse proxy with accounting context
// attached so the response tap can classify and attribute what comes back.
func (s *Server) forward(w http.ResponseWriter, r *http.Request, category, requestedModel string, dkey [32]byte, canDedup bool) {
	ctx := context.WithValue(r.Context(), ctxCategory, category)
	if requestedModel != "" {
		ctx = context.WithValue(ctx, ctxRequestedModel, requestedModel)
	}
	if canDedup {
		ctx = context.WithValue(ctx, ctxDedupKey, dkey)
	}
	s.rp.ServeHTTP(w, r.WithContext(ctx))
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/rowtr/health", s.handleHealth)
	mux.HandleFunc("/v1/messages", s.handleMessages)
	mux.Handle("/", s.rp) // models, count_tokens, files, … pass straight through
	return mux
}

// handleHealth is served by Rowtr itself (never proxied). Beyond liveness it
// carries a challenge-response: a caller supplying ?nonce= gets an HMAC proof
// that the listener holds the user-private token file, which is how
// `rowtr claude` tells a real Rowtr proxy from a squatter on the port. The
// mode is disclosed only to callers that themselves present the token.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]string{"service": "rowtr"}
	if nonce := r.URL.Query().Get("nonce"); nonce != "" && s.token != "" {
		resp["proof"] = HealthProof(s.token, nonce)
	}
	if s.token != "" && s.clientAuthed(r) {
		resp["mode"] = string(s.mode)
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) clientAuthed(r *http.Request) bool {
	return tokenEqual(r.Header.Get(AuthHeader), s.token)
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	if s.requireAuth && !s.clientAuthed(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"type":"error","error":{"type":"authentication_error","message":%q}}`,
			"rowtr: missing or invalid "+AuthHeader+" header — launch via `rowtr claude`, or copy the header printed by `rowtr serve` (opt out with --no-auth)")
		return
	}

	body := s.readAndRestore(r)
	req, _ := parseRequest(body)
	rawUser := lastUserText(req)

	if strings.TrimSpace(rawUser) == "" {
		s.forward(w, r, "other", "", [32]byte{}, false)
		return
	}

	hasTools := len(req.Tools) > 0

	// Decide routes on the extracted human intent, not raw payload text.
	// Tool-bearing requests never offload — they need the frontier.
	out := router.Decide(s.router, rawUser, hasTools)
	category := categorize(out, hasTools)
	offload := s.mode == ModeRoute && s.local != nil && out.Offloadable
	// Allowlisted housekeeping is the only traffic ever eligible for dedup
	// replay or model mutation — real turns are forwarded byte-identical.
	standalone := s.mode == ModeRoute && out.Internal && out.Offloadable

	// The log line carries routing signals only; prompt text stays out of the
	// log file unless the user explicitly opts into a debug session.
	line := fmt.Sprintf("route=%s reason=%q tools=%v internal=%v stream=%v offload=%v model=%s",
		out.Tier, out.Reason, hasTools, out.Internal, req.Stream, offload, req.Model)
	if s.debugIntent {
		line += fmt.Sprintf(" intent=%q", truncate(out.Intent, 80))
	}
	s.log.Print(line)

	// Shadow experiment: run the candidate router on real human turns only —
	// internal payloads are routed by policy, not classification, so there is
	// no decision for a model to second-guess.
	if s.shadow != nil && !out.Internal && out.Intent != "" {
		s.runShadow(out, hasTools)
	}

	var dkey [32]byte
	canDedup := standalone && !req.Stream && len(body) > 0 && len(body) <= dedupMaxBody
	if canDedup {
		dkey = dedupKey(body)
		if e, ok := s.dedup.get(dkey); ok {
			s.log.Printf("dedup: replaying cached response (model %s)", e.model)
			s.record(usage.Event{
				Time: time.Now(), Tier: "local", Model: "cache:" + e.model, Category: category,
				InputTokens: e.inTok, OutputTokens: e.outTok, Offloaded: true,
				SavedUSD: config.EstimateCostUSD(req.Model, e.inTok, e.outTok),
			})
			w.Header().Set("X-Rowtr-Tier", "cache")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(e.body)
			return
		}
	}

	if offload && s.serveLocal(w, r, req, category, dkey, canDedup) {
		return
	}
	if offload {
		s.log.Printf("local offload failed — falling back")
	}

	// Housekeeping that couldn't be served locally still doesn't deserve the
	// expensive model — rewrite it onto the downshift tier.
	if standalone && s.downshiftModel != "" && req.Model != s.downshiftModel {
		if nb, ok := downshiftBody(body, s.downshiftModel); ok {
			r.Body = io.NopCloser(bytes.NewReader(nb))
			r.ContentLength = int64(len(nb))
			s.log.Printf("downshift: %s → %s", req.Model, s.downshiftModel)
			s.forward(w, r, category, req.Model, dkey, canDedup)
			return
		}
	}

	// Cascade (opt-in): tool-free real prompts the router sent to the frontier
	// get one local attempt, judged locally; a weak answer escalates cleanly.
	if s.cascade && s.mode == ModeRoute && s.local != nil && !hasTools && !out.Internal &&
		out.Intent != "" && out.Tier == router.Frontier && len(body) <= maxCascadeBody {
		if s.serveCascade(w, r, req, out, category) {
			return
		}
	}

	// Frontier usage is recorded by the ModifyResponse tap, not here.
	s.forward(w, r, category, "", dkey, canDedup)
}

// serveLocal runs the local completion and, on success, writes an Anthropic-
// shaped response. On failure it returns false having written nothing, so the
// caller can fall back.
func (s *Server) serveLocal(w http.ResponseWriter, r *http.Request, req anthropicRequest, category string, dkey [32]byte, canDedup bool) bool {
	resp, latencyMS, ok := s.completeLocal(r, req)
	if !ok {
		return false
	}
	s.emitLocal(w, req, resp, category, latencyMS, -1, canDedup, dkey)
	return true
}

// completeLocal runs the full local completion BEFORE anything is written,
// so failures fall back to the frontier cleanly — the client never breaks.
func (s *Server) completeLocal(r *http.Request, req anthropicRequest) (backend.Response, int64, bool) {
	if err := s.local.Available(r.Context()); err != nil {
		s.log.Printf("ollama unavailable: %v", err)
		return backend.Response{}, 0, false
	}
	system := extractText(req.System)
	msgs := make([]backend.ChatMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, backend.ChatMessage{Role: m.Role, Content: extractText(m.Content)})
	}
	start := time.Now()
	resp, err := s.local.Chat(r.Context(), system, msgs)
	if err != nil {
		s.log.Printf("ollama completion failed: %v", err)
		return backend.Response{}, 0, false
	}
	return resp, time.Since(start).Milliseconds(), true
}

// emitLocal records and writes a locally-served response. score >= 0 marks a
// cascade-judged answer; canDedup stores non-streaming responses for replay.
func (s *Server) emitLocal(w http.ResponseWriter, req anthropicRequest, resp backend.Response, category string, latencyMS int64, score int, canDedup bool, dkey [32]byte) {
	// SavedUSD estimates what the requested frontier model would have charged
	// for these token counts.
	s.record(usage.Event{
		Time:         time.Now(),
		Tier:         "local",
		Model:        resp.Model,
		Category:     category,
		InputTokens:  resp.InputTokens,
		OutputTokens: resp.OutputTokens,
		LatencyMS:    latencyMS,
		Offloaded:    true,
		SavedUSD:     config.EstimateCostUSD(req.Model, resp.InputTokens, resp.OutputTokens),
	})

	w.Header().Set("X-Rowtr-Tier", "local")
	w.Header().Set("X-Rowtr-Model", resp.Model)
	if score >= 0 {
		w.Header().Set("X-Rowtr-Cascade-Score", strconv.Itoa(score))
	}

	if req.Stream {
		s.writeSSE(w, req.Model, resp)
		return
	}
	body := s.localMessageJSON(req.Model, resp)
	if canDedup {
		s.dedup.put(dkey, dedupEntry{body: body, model: resp.Model, inTok: resp.InputTokens, outTok: resp.OutputTokens})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// localMessageJSON builds a non-streaming Anthropic Message, echoing the
// requested model string; the real backend is disclosed via X-Rowtr-* headers.
func (s *Server) localMessageJSON(model string, resp backend.Response) []byte {
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
	b, _ := json.Marshal(out)
	return b
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

// maxInspectBytes caps how much of a request body is buffered for routing
// inspection — comfortably above Anthropic's request-size limit, so hitting it
// means the body isn't a routable prompt anyway.
const maxInspectBytes = 64 << 20

// readAndRestore reads the request body (up to maxInspectBytes) and restores
// it so the request can still be forwarded intact. An oversized body returns
// nil (skip inspection) but is forwarded untouched: what was read is stitched
// back in front of the unread remainder.
func (s *Server) readAndRestore(r *http.Request) []byte {
	if r.Body == nil {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxInspectBytes+1))
	if err != nil {
		s.log.Printf("read body: %v", err)
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(nil))
		return nil
	}
	if len(body) > maxInspectBytes {
		s.log.Printf("request body exceeds %dMB — forwarding without inspection", maxInspectBytes>>20)
		r.Body = readCloser{io.MultiReader(bytes.NewReader(body), r.Body), r.Body}
		return nil
	}
	r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	return body
}

type readCloser struct {
	io.Reader
	io.Closer
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

// truncate shortens s to at most n runes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
