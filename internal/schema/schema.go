// Package schema generates the published JSON Schema for the output contract
// from the Go types, so the two cannot drift (see `make schema`).
package schema

import (
	"encoding/json"

	"github.com/invopop/jsonschema"

	"github.com/morethancoder/ideacheck/internal/pipeline"
)

const (
	// Path is where the schema is published, relative to the repository root.
	Path = "schemas/check_result.schema.json"
	id   = "https://ideacheck.dev/schemas/check_result.schema.json"
)

// CheckResult returns the JSON Schema of pipeline.Result, indented, newline-terminated.
func CheckResult() ([]byte, error) {
	r := jsonschema.Reflector{ExpandedStruct: true}
	s := r.Reflect(&pipeline.Result{})
	s.ID = id
	s.Title = "ideacheck check result"
	s.Description = "Output of `ideacheck -o json` and POST /v1/check. top_strengths/top_risks values are polarity-adjusted: 1 is always good."
	b, err := json.MarshalIndent(s, "", "  ")
	return append(b, '\n'), err
}
