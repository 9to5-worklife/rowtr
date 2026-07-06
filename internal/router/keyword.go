package router

import (
	"context"
	"regexp"
	"strings"
)

// KeywordRouter is the slice-1 heuristic: free, instant, and good enough to
// prove the pipe. It looks for task verbs that signal complexity and falls back
// to a length threshold. When nothing is decisive it routes Local — frontier is
// the deliberate escalation, so the default must be the cheap tier.
type KeywordRouter struct {
	// LongPromptWords is the word count above which an otherwise-ambiguous
	// prompt is escalated to Frontier (longer prompts tend to carry more
	// context and intent). Zero uses the default.
	LongPromptWords int
}

// Verbs that signal genuine reasoning/synthesis — route Frontier.
var frontierVerbs = []string{
	"compare", "contrast", "analyze", "analyse", "draft", "write",
	"brainstorm", "design", "architect", "synthesize", "synthesise",
	"evaluate", "critique", "reason", "plan", "strategize", "debate",
	"explain why", "trade-off", "tradeoff", "tradeoffs", "pros and cons",
}

// Verbs that signal boilerplate/lookup — route Local.
var localVerbs = []string{
	"summarize", "summarise", "define", "classify", "categorize", "categorise",
	"list", "extract", "translate", "rephrase", "reword", "tag", "label",
	"what is", "who is", "when is", "where is", "spell", "count",
}

const defaultLongPromptWords = 60

var wordSplit = regexp.MustCompile(`\s+`)

// Route implements Router.
func (k KeywordRouter) Route(_ context.Context, prompt string) Decision {
	p := strings.ToLower(strings.TrimSpace(prompt))

	// Explicit signals win, checked most-specific first. Frontier verbs beat
	// local verbs: "summarize and then compare X to Y" needs the Expert.
	if hit, ok := firstMatch(p, frontierVerbs); ok {
		return Decision{Tier: Frontier, Score: 1, Reason: "frontier verb: " + hit}
	}
	if hit, ok := firstMatch(p, localVerbs); ok {
		return Decision{Tier: Local, Score: 0, Reason: "local verb: " + hit}
	}

	// No verb signal — fall back to length as a coarse complexity proxy.
	threshold := k.LongPromptWords
	if threshold == 0 {
		threshold = defaultLongPromptWords
	}
	if words := len(wordSplit.Split(p, -1)); words > threshold {
		return Decision{Tier: Frontier, Score: 0.6, Reason: "long prompt (heuristic)"}
	}

	return Decision{Tier: Local, Score: 0.2, Reason: "default: no complexity signal"}
}

// firstMatch returns the first phrase found as a substring of p. Substring
// (not word-boundary) matching is intentional for slice 1 — it's blunt but
// catches "summarization", "comparing", etc. The scoreboard will tell us where
// it's wrong.
func firstMatch(p string, phrases []string) (string, bool) {
	for _, phrase := range phrases {
		if strings.Contains(p, phrase) {
			return phrase, true
		}
	}
	return "", false
}
