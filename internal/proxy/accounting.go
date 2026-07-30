package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// tapUsage is the token accounting extracted from one frontier response.
type tapUsage struct {
	InTok       int // uncached, full-price input
	CacheRead   int // input served from the prompt cache
	CacheWrite  int // total input written to the prompt cache (5m + 1h)
	CacheWrite1h int // the 1-hour-TTL portion of CacheWrite (priced 2× not 1.25×)
	OutTok      int
}

// frontierTap wraps an upstream response body and extracts token usage as
// bytes stream through to the client. Anthropic reports usage in two shapes:
// SSE (`message_start` carries model + input tokens, `message_delta` the final
// output_tokens) or one JSON object with `model` and `usage`. On EOF or Close,
// done() fires exactly once with whatever was found. For non-SSE bodies,
// onBody (optional) fires alongside done() with the complete response bytes.
type frontierTap struct {
	inner  io.ReadCloser
	isSSE  bool
	done   func(model string, u tapUsage)
	onBody func(body []byte)

	line  bytes.Buffer // current SSE line
	body  bytes.Buffer // whole body (JSON mode only)
	model string
	u     tapUsage
	fired bool
}

const (
	maxLineBuf = 2 << 20  // 2MB — usage lines are tiny; giant text deltas get dropped unparsed
	maxBodyBuf = 16 << 20 // 16MB cap for non-streaming bodies
)

func (t *frontierTap) Read(p []byte) (int, error) {
	n, err := t.inner.Read(p)
	if n > 0 {
		if t.isSSE {
			t.scanSSE(p[:n])
		} else if t.body.Len() < maxBodyBuf {
			t.body.Write(p[:n])
		}
	}
	if err == io.EOF {
		t.fire()
	}
	return n, err
}

func (t *frontierTap) Close() error {
	t.fire()
	return t.inner.Close()
}

func (t *frontierTap) scanSSE(chunk []byte) {
	for _, b := range chunk {
		if b == '\n' {
			t.handleLine(t.line.String())
			t.line.Reset()
			continue
		}
		if t.line.Len() < maxLineBuf {
			t.line.WriteByte(b)
		}
	}
}

type usageFields struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	// cache_creation splits the write by TTL; present when 1-hour caching is
	// used. cache_creation_input_tokens is the sum of the two.
	CacheCreation struct {
		Ephemeral5m int `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h int `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

// sseEvent covers both usage-bearing SSE event shapes.
type sseEvent struct {
	Type    string `json:"type"`
	Message struct {
		Model string      `json:"model"`
		Usage usageFields `json:"usage"`
	} `json:"message"`
	Usage usageFields `json:"usage"`
}

func (t *frontierTap) handleLine(line string) {
	if !strings.HasPrefix(line, "data:") {
		return
	}
	// Cheap filter before JSON-parsing: only two event types carry usage.
	if !strings.Contains(line, `"message_start"`) && !strings.Contains(line, `"message_delta"`) {
		return
	}
	var ev sseEvent
	if json.Unmarshal([]byte(strings.TrimSpace(line[len("data:"):])), &ev) != nil {
		return
	}
	switch ev.Type {
	case "message_start":
		t.model = ev.Message.Model
		t.applyUsage(ev.Message.Usage)
	case "message_delta":
		// output_tokens is the cumulative final count; input may be restated.
		t.applyUsage(ev.Usage)
	}
}

// applyUsage merges a usage report, keeping the largest value seen per field —
// message_delta restates finals, and zero never overwrites a real count.
func (t *frontierTap) applyUsage(u usageFields) {
	if u.InputTokens > t.u.InTok {
		t.u.InTok = u.InputTokens
	}
	if u.OutputTokens > t.u.OutTok {
		t.u.OutTok = u.OutputTokens
	}
	if u.CacheReadInputTokens > t.u.CacheRead {
		t.u.CacheRead = u.CacheReadInputTokens
	}
	if u.CacheCreationInputTokens > t.u.CacheWrite {
		t.u.CacheWrite = u.CacheCreationInputTokens
	}
	if u.CacheCreation.Ephemeral1h > t.u.CacheWrite1h {
		t.u.CacheWrite1h = u.CacheCreation.Ephemeral1h
	}
}

func (t *frontierTap) fire() {
	if t.fired {
		return
	}
	t.fired = true

	if !t.isSSE {
		var msg struct {
			Model string      `json:"model"`
			Usage usageFields `json:"usage"`
		}
		if json.Unmarshal(t.body.Bytes(), &msg) == nil {
			t.model = msg.Model
			t.applyUsage(msg.Usage)
		}
		if t.onBody != nil && t.body.Len() > 0 && t.body.Len() < maxBodyBuf {
			t.onBody(t.body.Bytes())
		}
	}
	if t.done != nil {
		t.done(t.model, t.u)
	}
}
