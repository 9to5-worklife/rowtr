package router

import (
	"context"
	"regexp"
	"strings"
)

// Outcome is Rowtr's full routing decision for one request, independent of proxy
// mode. It's the single source of truth shared by the proxy and the scoreboard
// so the two can't drift apart.
//
// Offloadable is true only when the request is genuinely eligible to be served
// locally: the router picked Local, there are no tools, and it's a real human
// turn rather than Claude Code's internal machinery.
type Outcome struct {
	Tier        Tier
	Reason      string
	Intent      string // the human text we actually routed on ("" if internal/none)
	Internal    bool   // agent machinery (web content, transcript, search sub-call, …)
	Offloadable bool
}

// Decide extracts the human intent from a raw user message, detects agent-
// internal payloads, routes on the clean text, and applies the offload guard.
func Decide(r Router, rawUserText string, hasTools bool) Outcome {
	clean, internal := ExtractIntent(rawUserText)
	if internal {
		return Outcome{Tier: Frontier, Internal: true, Reason: "agent-internal payload"}
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
// human turn. These are the tool-free sub-calls that leaked to local in the
// wild (see the routing log): web-page/transcript processing, search sub-calls,
// slash-command expansions, suggestion mode, workflow sub-agent prompts.
var internalMarkers = []string{
	"web page content:",
	"perform a web search for the query",
	"<transcript>",
	"<task-notification",
	"<command-name>",
	"<local-command-stdout>",
	"[suggestion mode",
	"adversarial claim verifier",
	"source extractor",
}

// wrapperTags are containers Claude Code wraps around (or alongside) a real
// human turn. We strip the whole block so the router sees only what the user
// typed. RE2 has no backreferences, so we compile one regex per tag.
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
// internal is true and clean is empty — such requests must never be offloaded.
func ExtractIntent(raw string) (clean string, internal bool) {
	if strings.TrimSpace(raw) == "" {
		return "", false
	}
	low := strings.ToLower(raw)
	for _, m := range internalMarkers {
		if strings.Contains(low, m) {
			return "", true
		}
	}
	stripped := strings.TrimSpace(stripWrappers(raw))
	if stripped == "" {
		return "", true // the message was nothing but wrappers → machinery
	}
	return stripped, false
}

func stripWrappers(s string) string {
	for _, re := range wrapperREs {
		s = re.ReplaceAllString(s, " ")
	}
	return s
}
