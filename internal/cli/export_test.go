package cli

import "github.com/morethancoder/ideacheck/internal/pipeline"

func ParseForTest(s string) (pipeline.Intake, error) { return pipeline.ParseIntake([]byte(s)) }
