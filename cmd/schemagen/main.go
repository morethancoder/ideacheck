// Command schemagen prints the check-result JSON Schema (used by `make schema`).
package main

import (
	"fmt"
	"os"

	"github.com/morethancoder/ideacheck/internal/schema"
)

func main() {
	b, err := schema.CheckResult()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Stdout.Write(b)
}
