package cli

import "github.com/morethancoder/ideacheck/ideacheck"

func ParseForTest(s string) (ideacheck.Intake, error) { return ideacheck.ParseIntake([]byte(s)) }
