// Package configs holds the embedded default configuration, rubrics and prompts,
// and Files, which reads them. Users override any file by placing one with the
// same relative path in their config directory (Over).
package configs

import "embed"

// FS contains the defaults. The `all:` prefix is required: a plain directory
// pattern skips files beginning with "_" (rubrics/_gaps.yaml, rubrics/_router.yaml).
//
//go:embed config.yaml fields.yaml research.yaml sparkjudge.yaml trends.yaml all:rubrics all:prompts searxng
var FS embed.FS
