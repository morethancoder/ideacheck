# CLAUDE.md — map of this repository

`ideacheck` is one Go binary that checks an idea: it asks a judge model a set of
independent **typed** questions concurrently, combines the answers with weights we
control, and returns per-dimension scores plus a verdict (`build`, `explore`,
`park`, `kill`) — plus what information is missing. It is not a success predictor.

`README.md` is the user-facing manual (install, backends, config, API, bench).
This file is the map for working **on** the code. It replaces the old `HANDOFF.md`
build plan (removed; `git log -- HANDOFF.md` still has it).

## The rule that shapes everything

**Anything that is a prompt, rubric, question, weight, threshold, gate, model name
or price lives in a config file, never in Go source.** Go owns the loop, the
arithmetic, the aggregation and the verdict gates. The model is asked only narrow
judgments it can answer in one token or one small JSON object; it is never asked
for a final score.

Corollary: to change behaviour, first ask whether the change belongs in
`configs/` (embedded defaults, user-overridable) rather than in a `.go` file.

## Three modes, one core

| Mode | Entry | Output |
|---|---|---|
| Human | `ideacheck` / `ideacheck "idea"` | bubbletea TUI (`internal/tui`) |
| Agent | `ideacheck --agent --answer problem=… ` or `ideacheck idea.md --agent` | JSON on stdout, logs on stderr, exit code carries the status |
| Server | `ideacheck serve` | same JSON over HTTP (`internal/server`) |

All three call `pipeline.Engine.Check`. Nothing in `pipeline` knows about a
terminal.

## Pipeline (`internal/pipeline`)

```
intake → extract → gaps + router (one batch) → score (fan-out) → aggregate → verdict → explain → persist
```

| Stage | File | What it does |
|---|---|---|
| intake | `intake.go` | text / file / stdin JSON / YAML frontmatter / `--answer` → `Intake{Idea, Context, Fields, Profile}`; `State()` labels it for the model. Fields alone are a complete check — no document needed |
| fields | `fields.go` + `configs/fields.yaml` | the catalogue of what a caller can send and what each field means: one source for `ideacheck fields`, the JSON form, and the extraction prompt |
| extract | `extract.go` | one optional model call that pulls idea fields out of free prose, filling only fields the user left empty (`extract: true`) |
| gaps | `engine.go` + `rubrics/_gaps.yaml` | nouls "does the description state X"; below `threshold` and the field is still empty → `missing[]` |
| route | `engine.go` + `rubrics/_router.yaml` | one `choice` over idea types → picks `rubrics/<type>.yaml` (`other` → `business`) |
| score | `engine.go`, `judge/fanout.go` | every rubric question at once, one goroutine each |
| aggregate | `aggregate.go` | normalize to [0,1], apply polarity, weight, divide by the weights **answered** |
| verdict | `verdict.go` + rubric `verdict:` block | ordered gates, then composite thresholds. No model call |
| explain | `explain.go` | optional paragraph saying why, after scoring. Never changes a score |
| persist | `store/` | SQLite history (`history`, `last`, `show`) |

Invariants worth keeping:

- One failed question never fails the batch. It is excluded and renormalized, and
  it shows up in `warnings[]` and in that dimension's `error`.
- A question only sees the state it declares in `uses:` (context-rot rule,
  `judge.State.Sub`).
- Missing facts do not block a verdict outside the TUI: agent and server runs
  score what is known and report `missing[]` with `partial: true`. `--strict`
  restores the old "stop and ask" behaviour.
- A question that declares `requires: [profile]` is skipped — not guessed —
  when that state is absent, so a missing profile costs a dimension, not the run.
  The profile is settable without a terminal (`ideacheck profile set field=value`),
  because the caller who most needs it has none.

## Layout

```
cmd/ideacheck/         main; everything else is internal
internal/
  cli/                 cobra commands and flags. check.go is the default action
  pipeline/            the stages above; pure functions except the judge call
  judge/               the ONE interface all model access goes through + fanout
    backends/          jev, logprob (OpenAI-compatible + logprobs), structured
                       (Anthropic SDK / OpenAI-compatible), claudecli, codexcli, mock
  rubric/              YAML load, validate, hash; verdict gates
  prompt/              text/template loading of configs/prompts/*
  config/              koanf: flags > env IDEACHECK_* > user dir > embedded
  store/               SQLite history (modernc.org/sqlite, pure Go)
  tui/                 bubbletea app: menu, idea form, live check, result, setup
  ui/                  the shared step column — install.sh in Go: rows, pending
                       lines with a spinner, the download bar, warnings, errors
  server/              HTTP API + SSE
  bench/               backend comparison on bench/ideas.jsonl
  selfupdate/          daily release check, verified self-replace
  logging/             zerolog to stderr, always
configs/               EMBEDDED DEFAULTS (go:embed), user-overridable
  config.yaml          backends, pricing, setup wizard choices
  fields.yaml          what a caller can send about an idea, and what each field means
  rubrics/             _gaps, _router, business, side_project, content, research, creative
  prompts/             judge_system.md, question_*.tmpl, explain*, extract*
schemas/               check_result.schema.json, generated from the Go types
scripts/               bash for every non-trivial make target
```

User overrides live in `$XDG_CONFIG_HOME/ideacheck/` (or `-c DIR`) with the same
relative paths; any file present there replaces the embedded one.

## Core interface (`internal/judge`)

```go
type Judge interface {
    Name() string
    Evaluate(ctx, State, Question) (Answer, error)   // ONE question, called concurrently
    Capabilities() Capabilities
}
```

Optional, type-asserted where used: `BatchJudge` (all questions in one request),
`Narrator` (the explain paragraph), `Extractor` (the extract stage). A backend
that implements none of them still works.

Question kinds: `noul` (probability a statement is true, with optional
`criteria: {yes, no}` for the boundary), `score` (ordered levels), `choice` (named
options). Rubric-only metadata on a question: `weight`, `polarity` (+1
good-when-high, −1 bad-when-high, 0 informational), `uses`, `requires`, and for
`_gaps.yaml` only `ask`/`fills`. A rubric's `verdict.backends.<name>` overrides
gates/thresholds/min_confidence for one backend: cuts are tuned per backend on
`bench`, never carried across. Composite confidence averages choice and score
answers only — a noul is a probability, not a doubt.

## Writing rubric questions (`rubric.Validate` enforces most of it)

One judgment per question; "yes" is the natural positive reading; every `choice`
includes `other`; `score` has 2–10 concrete levels, low → high; weights ≥ 0 and
every weighted question sets polarity; `uses` lists only the state the question
needs, and a question about the person sets `requires: [profile]`; name the state
the question reads (`idea`, `profile`) — Jev reads literally; gate expressions may
reference real question ids only, and see normalized values in [0,1]. After
changing a question, `make bench ARGS='-b jev'` and compare (`bench --compare`).

## Look and feel

Everything ideacheck prints outside the bubbletea app goes through `internal/ui`:
a two-space column of `✓ label  value` rows, a live pending line while a step
runs, and the same progress bar `install.sh` draws. The rules that keep them the
same tool:

- **install.sh is the reference.** Its glyphs (`✓ ✗ ↓ · ━ ─ ›`), its ten-character
  label column, its dim/green/yellow/red, its ASCII fallback for a non-UTF-8
  locale. Change one side and change the other.
- **Live only on a terminal.** Off one, a step prints exactly one plain line with
  no escape codes, in the same order — agents and CI read that output.
- **stdout stays the artifact.** Steps that are progress, not result, go to
  stderr (`bench` writes its table to stdout and its steps to stderr).
- The app (`internal/tui`) uses the same palette: faint rather than a grey that
  guesses at the theme, and the terminal's own green/red/yellow, so the window
  matches the installer that put it there.

## Flags

`internal/cli/check.go` is the authority for the default action's flags, and each
flag carries its rationale in a comment beside it. Rules when adding one: the long
form is a real word, the short form is one letter checked against the conventions
people know from `git`/`kubectl`/`curl`/`make`/`ssh`, lowercase enables and
uppercase negates (`-a`/`-A`, `-e`/`-E`), and letters with a strong conflicting
convention elsewhere (`-j` jobs, `-p` port) are not reused for something else.
Document the flag in `README.md` too.

Agent contract: `--agent` = JSON on stdout + never ask + score with gaps. A field
added to `pipeline.ideaFields`/`profileFields` must be described in
`configs/fields.yaml` — a test fails otherwise, because that catalogue is what
callers and the extraction prompt both read.
Exit codes: `0` scored, `2` `needs_input` (only under `--strict`), `3` the check
ran but produced no score, `1` the command itself failed. `ideacheck --help-agent`
prints the recipe.

## Working on it

```sh
make            # list targets
make test       # go test ./...
make race lint  # what CI runs, plus gofmt and golangci-lint when installed
make run ARGS='"an idea" -b mock'      # no model needed
make dev        # build + open the TUI
make schema     # regenerate schemas/check_result.schema.json from the Go types
```

- `-b mock` is the offline backend: use it for anything that is not about a real
  provider's wire format.
- Prompt output is pinned by golden files: `go test ./internal/prompt -update`.
- Backend tests use `net/http/httptest` fixtures per wire format — never a live API.
- Fan-out tests assert real concurrency (12 questions × 200 ms must finish well
  under their sum) and partial failure, timeout and 429 `Retry-After` handling.
- Never log API keys; full prompts only at trace level. stdout stays clean JSON.
