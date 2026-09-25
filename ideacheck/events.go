package ideacheck

import (
	"github.com/morethancoder/ideacheck/judge"
)

const (
	StagePreflight = "preflight" // gaps + router
	StageScore     = "score"     // the routed rubric
)

// Event is a judge event enriched with what a UI needs to draw it: the question
// itself and, once answered, its normalized value.
type Event struct {
	Type     judge.EventType `json:"type"`
	Stage    string          `json:"stage"`
	Question judge.Question  `json:"question"`
	Answer   *judge.Answer   `json:"answer,omitempty"`
	Value    *float64        `json:"value,omitempty"` // normalized [0,1] before polarity; nil when not valued
}

// relay forwards judge events as pipeline events until in is closed.
func relay(in <-chan judge.Event, out chan<- Event, stage string, qs []judge.Question) {
	byID := make(map[string]judge.Question, len(qs))
	for _, q := range qs {
		byID[q.ID] = q
	}
	for je := range in {
		e := Event{Type: je.Type, Stage: stage, Question: byID[je.QuestionID], Answer: je.Answer}
		if je.Answer != nil {
			if v, ok := Normalize(e.Question, *je.Answer); ok {
				e.Value = &v
			}
		}
		out <- e
	}
}
