package ideacheck

import (
	"context"
	"strings"

	"github.com/morethancoder/ideacheck/judge"
	"github.com/morethancoder/ideacheck/prompt"
)

// StageExtract is the single call that reads stated facts out of the document.
const StageExtract = "extract"

// extractQuestion stands in for the extraction in live events; it is not asked.
var extractQuestion = judge.Question{ID: "extract", Instructions: "Read the stated facts out of the document"}

// extract fills intake fields the user left empty with what the document
// already says, so a fact given in prose is not reported as missing and does
// not vary from run to run. It never overwrites a field the user set, and it
// never fails the check: without it the check simply knows less.
func (e *Engine) extract(ctx context.Context, in Intake, res *Result, o CheckOptions) (Intake, *judge.Answer) {
	extractor, ok := e.writer().(judge.Extractor)
	want := in.emptyFields()
	if !ok || !e.Settings.Extract || len(want) == 0 || (in.Idea == "" && in.Context == "") {
		return in, nil
	}
	prompts, err := prompt.Load(e.Files, e.Settings.PromptsDir)
	if err != nil {
		res.Warnings = append(res.Warnings, "document not read for stated facts: "+err.Error())
		return in, nil
	}
	described, err := describe(e.Files, want)
	if err != nil {
		res.Warnings = append(res.Warnings, "document not read for stated facts: "+err.Error())
		return in, nil
	}
	user, err := prompts.Extract(in.Idea, in.Context, described)
	if err != nil {
		res.Warnings = append(res.Warnings, "document not read for stated facts: "+err.Error())
		return in, nil
	}
	emit(o.OnEvent, Event{Type: judge.EventStarted, Stage: StageExtract, Question: extractQuestion})
	ctx, cancel := context.WithTimeout(ctx, e.Settings.Timeouts.Batch)
	defer cancel()
	start := e.now()
	out, err := extractor.Extract(ctx, prompts.ExtractSystem, user, want)
	a := judge.Answer{ID: extractQuestion.ID, Model: out.Model, LatencyMS: e.now().Sub(start).Milliseconds(),
		TokensIn: out.TokensIn, TokensOut: out.TokensOut, TokensCached: out.TokensCached, CostUSD: out.CostUSD}
	if err != nil {
		a.Err = err.Error()
		res.Warnings = append(res.Warnings, "document not read for stated facts: "+err.Error())
		emit(o.OnEvent, Event{Type: judge.EventFailed, Stage: StageExtract, Question: extractQuestion, Answer: &a})
		return in, &a
	}
	for _, field := range want {
		if v := strings.TrimSpace(out.Values[field]); v != "" {
			in, res.Extracted = in.With(field, v), append(res.Extracted, field)
		}
	}
	emit(o.OnEvent, Event{Type: judge.EventAnswered, Stage: StageExtract, Question: extractQuestion, Answer: &a})
	return in, &a
}

// describe pairs each wanted field with what it means, so the extraction call
// and `ideacheck fields` read from the same catalogue.
func describe(r interface{ Read(string) ([]byte, error) }, want []string) ([]prompt.Field, error) {
	catalogue, err := LoadFields(r)
	if err != nil {
		return nil, err
	}
	out := make([]prompt.Field, 0, len(want))
	for _, f := range catalogue.Idea {
		if contains(want, f.Name) {
			out = append(out, prompt.Field{Name: f.Name, Description: f.Description})
		}
	}
	return out, nil
}

// emptyFields are the idea fields still unfilled: the only ones worth reading
// out of the document.
func (in Intake) emptyFields() []string {
	out := []string{}
	for _, f := range ideaFields {
		if !in.Has(f) {
			out = append(out, f)
		}
	}
	return out
}
