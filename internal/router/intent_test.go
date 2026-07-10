package router

import "testing"

func TestDecide_Offloadable(t *testing.T) {
	cases := []struct {
		name         string
		prompt       string
		tools        bool
		wantOffload  bool
		wantInternal bool
	}{
		{"human local verb", "define recursion", false, true, false},
		{"human lookup", "what is a mutex", false, true, false},
		{"frontier verb stays frontier", "compare A and B", false, false, false},
		{"tool-bearing never offloads", "list the files", true, false, false},
		{"system-reminder wrapped human", "<system-reminder>ctx # claudeMd</system-reminder> define entropy", false, true, false},
		// Agent-internal payloads must stay frontier even though they
		// contain frontier/local verbs.
		{"web page content", " Web page content: --- compare LLM routers ...", false, false, true},
		{"web search sub-call", "Perform a web search for the query: RouteLLM cost", false, false, true},
		{"transcript sub-call", `<transcript>{"user":"x"}</transcript> tag this`, false, false, true},
		{"slash command", "<command-name>/usage</command-name>", false, false, true},
		// Allowlisted housekeeping: internal AND offloadable — low-stakes UI
		// calls a small local model can serve.
		{"suggestion mode allowlisted", "[SUGGESTION MODE: suggest next input] plan the work", false, true, true},
		{"topic detection allowlisted", "Analyze if this message indicates a new conversation topic.", false, true, true},
		{"title generation allowlisted", "Write a 5-10 word title for the following conversation: hi", false, true, true},
		{"allowlisted kind with tools stays frontier", "Analyze if this message indicates a new conversation topic.", true, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := Decide(KeywordRouter{}, c.prompt, c.tools)
			if out.Offloadable != c.wantOffload {
				t.Errorf("Offloadable=%v want %v (reason=%q)", out.Offloadable, c.wantOffload, out.Reason)
			}
			if out.Internal != c.wantInternal {
				t.Errorf("Internal=%v want %v", out.Internal, c.wantInternal)
			}
		})
	}
}
