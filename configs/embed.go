// Package configs holds the embedded default configuration, rubrics and prompts.
// Users override any file by placing one with the same relative path in their
// config directory; see internal/config.
package configs

import "embed"

// FS contains the defaults. The `all:` prefix is required: a plain directory
// pattern skips files beginning with "_" (rubrics/_gaps.yaml, rubrics/_router.yaml).
//
//go:embed config.yaml fields.yaml research.yaml all:rubrics all:prompts
var FS embed.FS
