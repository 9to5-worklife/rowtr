package router

import (
	"context"
	"strings"
	"testing"
)

func TestKeywordRouter_Route(t *testing.T) {
	r := KeywordRouter{}
	ctx := context.Background()

	cases := []struct {
		name   string
		prompt string
		want   Tier
	}{
		{"local verb: define", "Define entropy in one sentence.", Local},
		{"local verb: summarize", "Summarize this paragraph for me.", Local},
		{"local verb inflected", "Summarization of the quarterly report, please.", Local},
		{"local lookup", "What is the capital of France?", Local},
		{"frontier verb: compare", "Compare Postgres and DynamoDB with tradeoffs.", Frontier},
		{"frontier verb: draft", "Draft an email to the board about the outage.", Frontier},
		{"frontier beats local", "Summarize this doc and then compare it to last year's.", Frontier},
		{"short ambiguous defaults local", "The weather today.", Local},
		{"long ambiguous escalates", strings.Repeat("context ", 80), Frontier},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := r.Route(ctx, tc.prompt)
			if got.Tier != tc.want {
				t.Errorf("Route(%q) = %v (%s); want %v",
					tc.prompt, got.Tier, got.Reason, tc.want)
			}
		})
	}
}

func TestKeywordRouter_DecisionHasReason(t *testing.T) {
	got := KeywordRouter{}.Route(context.Background(), "hello")
	if got.Reason == "" {
		t.Error("Decision.Reason must never be empty — routing must be auditable")
	}
}
