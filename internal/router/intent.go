package router

import (
	"context"
	"regexp"
	"strings"
)

// Outcome is the full routing decision for one request — the single source of
// truth shared by the proxy and the scoreboard so the two can't drift apart.
//
// Offloadable is true only when the request is eligible to be served locally:
// there are no tools and the request is either a real human turn the router
// scored Local, or an allowlisted piece of low-stakes agent housekeeping.
type Outcome struct {
	Tier        Tier
	Reason      string
	Intent      string // the human text we actually routed on ("" if internal/none)
	Internal    bool   // agent machinery (web content, transcript, search sub-call, …)
	Kind        string // internal payload kind ("" if not internal)
	Offloadable bool
}

// Decide extracts the human intent from a raw user message, detects agent-
// internal payloads, routes on the clean text, and applies the offload guard.
func Decide(r Router, rawUserText string, hasTools bool) Outcome {
	clean, internal, kind := classifyIntent(rawUserText)
	if internal {
		if allowlistedInternal[kind] && !hasTools {
			return Outcome{Tier: Local, Internal: true, Kind: kind,
				Reason: "internal:" + kind + " (allowlisted housekeeping)", Offloadable: true}
		}
		return Outcome{Tier: Frontier, Internal: true, Kind: kind, Reason: "agent-internal payload"}
	}
	if clean == "" {
		return Outcome{Tier: Frontier, Reason: "no user text"}
	}
	d := r.Route(context.Background(), clean)
	return Outcome{
		Tier:        d.Tier,
		Reason:      d.Reason,
		Intent:      clean,
		Offloadable: d.Tier == Local && !hasTools,
	}
}

// internalMarkers flag a request as Claude Code's own machinery rather than a
// human turn, tagged with what kind of machinery it is.
var internalMarkers = []struct{ marker, kind string }{
	{"web page content:", "web-content"},
	{"perform a web search for the query", "search"},
	{"<transcript>", "transcript"},
	{"<task-notification", "notification"},
	{"<command-name>", "command"},
	{"<local-command-stdout>", "command"},
	{"[suggestion mode", "suggestion"},
	{"adversarial claim verifier", "verifier"},
	{"source extractor", "extractor"},
	// Low-stakes housekeeping Claude Code fires constantly.
	{"indicates a new conversation topic", "topic"},
	{"word title for the following conversation", "title"},
	{"title for this conversation", "title"},
}

// allowlistedInternal are internal kinds safe to serve locally: frequent,
// tool-free, quality-tolerant UI housekeeping. Everything else internal —
// web/transcript extraction, search sub-calls, unknown machinery — must stay
// on the frontier; getting those wrong corrupts real work.
var allowlistedInternal = map[string]bool{
	"topic":      true,
	"title":      true,
	"suggestion": true,
}

// wrapperTags are containers Claude Code wraps around a real human turn; whole
// blocks are stripped so the router sees only what the user typed. RE2 has no
// backreferences, so one regex is compiled per tag.
var wrapperTags = []string{
	"system-reminder", "session",
	"command-name", "command-message", "command-args", "local-command-stdout",
}

var wrapperREs = func() []*regexp.Regexp {
	res := make([]*regexp.Regexp, len(wrapperTags))
	for i, t := range wrapperTags {
		res[i] = regexp.MustCompile(`(?is)<` + t + `\b[^>]*>.*?</\s*` + t + `\s*>`)
	}
	return res
}()

// ExtractIntent returns the human's actual prompt and whether the request is
// agent-internal. If the message is (or is dominated by) internal machinery,
// internal is true and clean is empty.
func ExtractIntent(raw string) (clean string, internal bool) {
	clean, internal, _ = classifyIntent(raw)
	return clean, internal
}

func classifyIntent(raw string) (clean string, internal bool, kind string) {
	if strings.TrimSpace(raw) == "" {
		return "", false, ""
	}
	low := strings.ToLower(raw)
	for _, m := range internalMarkers {
		if strings.Contains(low, m.marker) {
			return "", true, m.kind
		}
	}
	stripped := strings.TrimSpace(stripWrappers(raw))
	if stripped == "" {
		return "", true, "wrapper" // the message was nothing but wrappers → machinery
	}
	return stripped, false, ""
}

func stripWrappers(s string) string {
	for _, re := range wrapperREs {
		s = re.ReplaceAllString(s, " ")
	}
	return s
}
