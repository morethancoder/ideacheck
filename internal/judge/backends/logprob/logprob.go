// Package logprob reads answer probabilities from the token logits of an
// OpenAI-compatible endpoint (Ollama >= 0.12, llama.cpp, vLLM, some OpenRouter
// providers). The numbers are raw softmax mass, not calibrated probabilities.
package logprob

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/morethancoder/ideacheck/internal/judge"
	"github.com/morethancoder/ideacheck/internal/judge/backends/openai"
	"github.com/morethancoder/ideacheck/internal/prompt"
)

const (
	Name   = "logprob"
	method = "logprob"
	yes    = "Y"
	no     = "N"
	// letters label choice options; more options than this cannot be single tokens.
	letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
)

type Judge struct {
	Client        *openai.Client
	Prompts       *prompt.Set
	Model         string
	TopLogprobs   int
	ShuffleRuns   int    // order-bias mitigation for choice/score; < 1 means 1
	Effort        string // reasoning_effort; "" = none, EffortUnset = leave the field out
	MaxConcurrent int
}

func (j *Judge) Name() string { return Name }
func (j *Judge) Capabilities() judge.Capabilities {
	return judge.Capabilities{RealLogprobs: true, MaxConcurrent: j.MaxConcurrent}
}

// layout is one presentation of a question: labels in display order, and the
// outcome (option key / level index / yes|no) each label stands for.
type layout struct {
	labels  []prompt.Label
	outcome map[string]string // label → outcome
}

func (j *Judge) Evaluate(ctx context.Context, state judge.State, q judge.Question) (judge.Answer, error) {
	runs := j.ShuffleRuns
	if runs < 1 || q.Kind == judge.Noul {
		runs = 1
	}
	total := map[string]float64{}
	var ans judge.Answer
	var first map[string]float64
	for run := 0; run < runs; run++ {
		lay, err := layoutFor(q, run)
		if err != nil {
			return judge.Answer{}, err
		}
		probs, res, err := j.ask(ctx, state, q, lay)
		if err != nil {
			return judge.Answer{}, err
		}
		for outcome, p := range probs {
			total[outcome] += p / float64(runs)
		}
		if run == 0 {
			first = probs
		} else {
			ans.OrderBias += meanAbsDiff(first, probs) / float64(runs-1)
		}
		ans.Model = res.Model
		ans.TokensIn += res.Usage.PromptTokens
		ans.TokensOut += res.Usage.CompletionTokens
	}
	return finish(q, ans, total), nil
}

// meanAbsDiff is the order-bias signal: how far a shuffled presentation moved
// the distribution from the natural-order one. 0 means position did not matter.
func meanAbsDiff(a, b map[string]float64) float64 {
	if len(a) == 0 {
		return 0
	}
	var sum float64
	for k, p := range a {
		sum += math.Abs(p - b[k])
	}
	return sum / float64(len(a))
}

func finish(q judge.Question, a judge.Answer, probs map[string]float64) judge.Answer {
	a.Method, a.Probabilities = method, probs
	dist := make([]float64, 0, len(probs))
	for _, p := range probs {
		dist = append(dist, p)
	}
	a.Confidence = judge.EntropyConfidence(dist)
	switch q.Kind {
	case judge.Noul:
		a.Noul = probs["yes"]
	case judge.Score:
		a.Score = judge.ExpectedLevel(probs)
	default:
		a.Choice = judge.Argmax(probs)
	}
	return a
}

// layoutFor builds run 0 in natural order and later runs in a shuffled order
// that is reproducible for (question, run).
func layoutFor(q judge.Question, run int) (layout, error) {
	if q.Kind == judge.Noul {
		return layout{outcome: map[string]string{yes: "yes", no: "no"}}, nil
	}
	items := outcomesOf(q)
	if q.Kind == judge.Choice && len(items) > len(letters) {
		return layout{}, fmt.Errorf("%d options exceed the %d single-token labels the logprob backend can use", len(items), len(letters))
	}
	if run > 0 {
		h := fnv.New64a()
		fmt.Fprintf(h, "%s/%d", q.ID, run)
		rand.New(rand.NewPCG(h.Sum64(), uint64(run))).Shuffle(len(items), func(a, b int) { items[a], items[b] = items[b], items[a] })
	}
	lay := layout{outcome: map[string]string{}}
	for pos, it := range items {
		label := it.key // score: the label is always the level index, only display order moves
		if q.Kind == judge.Choice {
			label = string(letters[pos])
		}
		lay.labels = append(lay.labels, prompt.Label{Label: label, Text: it.text})
		lay.outcome[label] = it.key
	}
	return lay, nil
}

type item struct{ key, text string }

func outcomesOf(q judge.Question) []item {
	var items []item
	if q.Kind == judge.Score {
		for i, l := range q.Levels {
			items = append(items, item{strconv.Itoa(i), l})
		}
		return items
	}
	for k, d := range q.Options {
		items = append(items, item{k, k + " — " + d})
	}
	sort.Slice(items, func(a, b int) bool { return items[a].key < items[b].key })
	return items
}

func (j *Judge) ask(ctx context.Context, state judge.State, q judge.Question, lay layout) (map[string]float64, *openai.Response, error) {
	user, err := j.Prompts.Logprob(state, q, lay.labels)
	if err != nil {
		return nil, nil, err
	}
	zero := 0.0
	res, err := j.Client.Chat(ctx, openai.Request{
		Model:           j.Model,
		Messages:        []openai.Message{{Role: "system", Content: j.Prompts.System}, {Role: "user", Content: user}},
		MaxTokens:       readoutTokens,
		Temperature:     &zero,
		Logprobs:        true,
		TopLogprobs:     j.TopLogprobs,
		ReasoningEffort: j.effort(),
	})
	if err != nil {
		return nil, nil, err
	}
	lp := res.Choices[0].Logprobs
	if lp == nil || len(lp.Content) == 0 {
		return nil, nil, fmt.Errorf("endpoint returned no logprobs for model %s (it must support logprobs + top_logprobs)", j.Model)
	}
	probs, err := distribution(lp.Content[0], lay.outcome)
	return probs, res, err
}

// EffortUnset omits reasoning_effort, for a server that rejects the field.
const EffortUnset = "default"

// effort defaults to "none": the readout needs the answer as the first token,
// so a model that thinks first leaves nothing to read. Models that cannot
// think ignore the field (verified on Ollama 0.13.5 with llama3.2).
func (j *Judge) effort() string {
	if j.Effort == EffortUnset {
		return ""
	}
	return cmp.Or(j.Effort, "none")
}

// readoutTokens is the generation budget. Only the first token is read, but
// Ollama (verified on 0.13.5 with qwen3) returns no logprobs at all for a
// thinking-capable model when max_tokens is 1: its thinking parser holds the
// only token back. The second token costs one decode step.
const readoutTokens = 2

// distribution keeps only tokens that are a label and renormalizes over them.
// Tokenizers emit several variants of one label (" N", "N", "n"): their mass is
// summed, so no variant is dropped.
func distribution(first openai.TokenLogprob, outcome map[string]string) (map[string]float64, error) {
	probs := make(map[string]float64, len(outcome))
	for _, o := range outcome {
		probs[o] = 0
	}
	var mass float64
	for _, t := range first.TopLogprobs {
		if o, ok := outcome[strings.ToUpper(strings.TrimSpace(t.Token))]; ok {
			p := math.Exp(t.Logprob)
			probs[o] += p
			mass += p
		}
	}
	if mass == 0 {
		return nil, fmt.Errorf("no answer label among the top tokens (first token %q); raise top_logprobs or use a stronger model", first.Token)
	}
	for o := range probs {
		probs[o] /= mass
	}
	return probs, nil
}

var thinkRE = regexp.MustCompile(`(?s)<think>.*?</think>`)

// Narrate writes the result summary as a plain chat completion: no logprobs,
// no schema (local servers vary in support), so the JSON is read leniently and
// a reply that is not JSON is taken as the paragraph itself.
func (j *Judge) Narrate(ctx context.Context, system, brief string) (judge.Narration, error) {
	res, err := j.Client.Chat(ctx, openai.Request{
		Model:           j.Model,
		MaxTokens:       narrateMaxTokens,
		Messages:        []openai.Message{{Role: "system", Content: system}, {Role: "user", Content: brief}},
		ReasoningEffort: j.effort(),
	})
	if err != nil {
		return judge.Narration{}, err
	}
	text := strings.TrimSpace(thinkRE.ReplaceAllString(res.Choices[0].Message.Content, ""))
	var out struct {
		Summary string `json:"summary"`
	}
	if start, end := strings.Index(text, "{"), strings.LastIndex(text, "}"); start >= 0 && end > start {
		if json.Unmarshal([]byte(text[start:end+1]), &out) == nil && out.Summary != "" {
			text = out.Summary
		}
	}
	if text == "" {
		return judge.Narration{}, fmt.Errorf("model returned an empty summary")
	}
	return judge.Narration{Text: text, Model: res.Model, TokensIn: res.Usage.PromptTokens, TokensOut: res.Usage.CompletionTokens}, nil
}

// narrateMaxTokens leaves room for a reasoning model's thinking before the paragraph.
const narrateMaxTokens = 2048
