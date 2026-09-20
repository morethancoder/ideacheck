package tui

import (
	"strings"

	"github.com/morethancoder/ideacheck/internal/pipeline"
)

var fieldTitles = map[string]string{
	"title": "Title", "problem": "Problem", "audience": "Audience", "solution": "Solution",
	"why_now": "Why now", "monetization": "Monetization", "competitors_known": "Known competitors",
	"differentiation": "What is different",
	"skills":          "Your skills", "domains": "Domains you know", "network": "Who you can reach",
	"would_use_myself": "Would you use it yourself?", "time_horizon": "Time you can give it",
	"background": "Your background relative to ideas like this",
}

var fieldHints = map[string]string{
	"problem":           "What problem does this solve, and what do people do about it today?",
	"audience":          "Who exactly is the first user? Be narrow.",
	"solution":          "What is the thing you would build or make, concretely?",
	"why_now":           "What changed recently that makes this possible or urgent?",
	"competitors_known": "What do people use instead? Name alternatives if you know any.",
	"differentiation":   "The one thing this does that existing options do not.",
	"monetization":      "Who pays, and for what?",
}

// ApplyReplies stores non-blank replies in the fields their gaps fill.
func ApplyReplies(in pipeline.Intake, missing []pipeline.Missing, replies []string) pipeline.Intake {
	for i, m := range missing {
		if reply := strings.TrimSpace(replies[i]); reply != "" {
			in = in.With(m.Fills, reply)
		}
	}
	return in
}
