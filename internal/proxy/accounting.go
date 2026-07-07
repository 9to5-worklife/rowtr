package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// frontierTap wraps an upstream response body and extracts token usage as
// bytes stream through to the client. Anthropic reports usage in two shapes:
// SSE (`message_start` carries model + input_tokens, `message_delta` the final
// output_tokens) or one JSON object with `model` and `usage`. On EOF or Close,
// done() fires exactly once with whatever was found.
type frontierTap struct {
	inner io.ReadCloser
	isSSE bool
	done  func(model string, inTok, outTok int)

	line   bytes.Buffer // current SSE line
	body   bytes.Buffer // whole body (JSON mode only)
	model  string
	inTok  int
	outTok int
	fired  bool
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

// sseEvent covers both usage-bearing SSE event shapes.
type sseEvent struct {
	Type    string `json:"type"`
	Message struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens int `json:"input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
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
		t.inTok = ev.Message.Usage.InputTokens
	case "message_delta":
		// output_tokens is the cumulative final count; input may be restated.
		if ev.Usage.OutputTokens > 0 {
			t.outTok = ev.Usage.OutputTokens
		}
		if ev.Usage.InputTokens > 0 {
			t.inTok = ev.Usage.InputTokens
		}
	}
}

func (t *frontierTap) fire() {
	if t.fired {
		return
	}
	t.fired = true

	if !t.isSSE {
		var msg struct {
			Model string `json:"model"`
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(t.body.Bytes(), &msg) == nil {
			t.model = msg.Model
			t.inTok = msg.Usage.InputTokens
			t.outTok = msg.Usage.OutputTokens
		}
	}
	if t.done != nil {
		t.done(t.model, t.inTok, t.outTok)
	}
}
