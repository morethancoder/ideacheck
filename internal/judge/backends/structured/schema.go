// Package structured asks a chat model for typed JSON answers. Probabilities are
// either stated by the model ("verbalized") or the answer frequencies over k
// independent samples ("vote").
package structured

import (
	"sort"
	"strconv"

	"github.com/morethancoder/ideacheck/internal/judge"
)

const (
	ModeVote       = "vote"
	ModeVerbalized = "verbalized"
)

type object = map[string]any

func obj(props object) object {
	required := make([]string, 0, len(props))
	for k := range props {
		required = append(required, k)
	}
	sort.Strings(required)
	return object{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

var (
	number  = object{"type": "number"}
	boolean = object{"type": "boolean"}
)

// outcomes are the discrete answers a question allows: option keys (sorted), or
// level indexes "0".."n-1".
func outcomes(q judge.Question) []string {
	if q.Kind == judge.Score {
		out := make([]string, len(q.Levels))
		for i := range out {
			out[i] = strconv.Itoa(i)
		}
		return out
	}
	out := make([]string, 0, len(q.Options))
	for k := range q.Options {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SchemaFor is the JSON Schema of one question's answer. It sticks to the subset
// every structured-output provider accepts: type, enum, required, additionalProperties.
func SchemaFor(q judge.Question, mode string) object {
	if q.Kind == judge.Noul {
		if mode == ModeVote {
			return obj(object{"yes": boolean})
		}
		return obj(object{"probability": number})
	}
	keys := outcomes(q)
	props := object{}
	if q.Kind == judge.Choice {
		props["choice"] = object{"type": "string", "enum": keys}
	} else {
		levels := make([]int, len(keys))
		for i := range levels {
			levels[i] = i
		}
		props["level"] = object{"type": "integer", "enum": levels}
	}
	if mode == ModeVerbalized {
		dist := object{}
		for _, k := range keys {
			dist[k] = number
		}
		props["probabilities"] = obj(dist)
		props["confidence"] = number
	}
	return obj(props)
}

// BatchSchema wraps every question's schema under its id.
func BatchSchema(qs []judge.Question, mode string) object {
	props := object{}
	for _, q := range qs {
		props[q.ID] = SchemaFor(q, mode)
	}
	return obj(props)
}
