package proxy

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/9to5-worklife/rowtr/internal/router"
)

// Shadow mode runs a second, experimental router alongside the authoritative
// one on every real human turn. The shadow decision never affects traffic —
// it's compared against the keyword decision and the comparison is written to
// a JSONL file. The disagreements ARE the experiment: they're the cases the
// eval set doesn't cover, and hand-labeling them is how the model router earns
// (or loses) the right to route for real.

// shadowEntry is one comparison. Intent is included ONLY on disagreements —
// that's the labeling corpus — which is why the shadow log is opt-in and 0600,
// mirroring the ROWTR_DEBUG_INTENT precedent for prompt text on disk.
type shadowEntry struct {
	Time          time.Time `json:"time"`
	Agree         bool      `json:"agree"`
	Error         string    `json:"error,omitempty"`
	KeywordTier   string    `json:"keyword_tier"`
	ShadowTier    string    `json:"shadow_tier,omitempty"`
	KeywordReason string    `json:"keyword_reason"`
	ShadowReason  string    `json:"shadow_reason,omitempty"`
	Tools         bool      `json:"tools"`
	LatencyMS     int64     `json:"latency_ms"`
	Intent        string    `json:"intent,omitempty"`
}

// maxConcurrentShadows bounds in-flight classifier calls so a slow shadow
// host (a busy Pi) can only ever drop comparisons, never queue up goroutines.
const maxConcurrentShadows = 2

// runShadow compares the shadow router against the authoritative outcome,
// asynchronously — the live request must never wait on the experiment.
func (s *Server) runShadow(out router.Outcome, hasTools bool) {
	select {
	case s.shadowSem <- struct{}{}:
	default:
		s.log.Printf("shadow: classifier busy — comparison skipped")
		return
	}
	go func() {
		defer func() { <-s.shadowSem }()

		// The request context dies with the response; the shadow call runs on
		// its own clock (the router's timeout bounds it).
		start := time.Now()
		var d router.Decision
		var err error
		if cr, ok := s.shadow.(router.CheckedRouter); ok {
			d, err = cr.RouteChecked(context.Background(), out.Intent)
		} else {
			d = s.shadow.Route(context.Background(), out.Intent)
		}

		e := shadowEntry{
			Time:          time.Now(),
			KeywordTier:   out.Tier.String(),
			KeywordReason: out.Reason,
			Tools:         hasTools,
			LatencyMS:     time.Since(start).Milliseconds(),
		}
		if err != nil {
			e.Error = err.Error()
			s.log.Printf("shadow: classifier error (%dms): %v", e.LatencyMS, err)
		} else {
			e.Agree = d.Tier == out.Tier
			e.ShadowTier = d.Tier.String()
			e.ShadowReason = d.Reason
			if !e.Agree {
				e.Intent = out.Intent
			}
			s.log.Printf("shadow: agree=%v keyword=%s model=%s latency=%dms",
				e.Agree, e.KeywordTier, e.ShadowTier, e.LatencyMS)
		}
		s.writeShadow(e)
	}()
}

func (s *Server) writeShadow(e shadowEntry) {
	if s.shadowLog == nil {
		return
	}
	s.shadowMu.Lock()
	defer s.shadowMu.Unlock()
	if err := json.NewEncoder(s.shadowLog).Encode(e); err != nil {
		s.log.Printf("shadow: write failed: %v", err)
	}
}

// shadowState is embedded in Server; split out so proxy.go stays about proxying.
type shadowState struct {
	shadow    router.Router
	shadowLog io.Writer
	shadowMu  sync.Mutex
	shadowSem chan struct{}
}
