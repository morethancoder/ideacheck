// Command ideacheck checks an idea. The command surface lives in internal/cli.
package main

import (
	"os"

	"github.com/morethancoder/ideacheck/internal/cli"
)

func main() { os.Exit(cli.Execute()) }
