# CLAUDE.md — map of this repository

`ideacheck` is one Go binary that checks an idea: it asks a judge model a set of
independent **typed** questions concurrently, combines the answers with weights we
control, and returns per-dimension scores plus a verdict (`build`, `explore`,
`park`, `kill`) — plus what information is missing. Before scoring it can look the
idea up on the web, so questions about the world read findings, not the pitch. It
is not a success predictor.

`README.md` is the user-facing manual (install, backends, config, API, bench).
This file is the map for working **on** the code. It replaces the old `HANDOFF.md`
build plan (removed; `git log -- HANDOFF.md` still has it).

## The rule that shapes everything

**Anything that is a prompt, rubric, question, weight, threshold, gate, model name
or price lives in a config file, never in Go source.** Go owns the loop, the
arithmetic, the aggregation and the verdict gates. The model is asked only narrow
judgments it can answer in one token or one small JSON object; it is never asked
for a final score.

**Two roles.** The *judge* (`backend:`, `Engine.Judge`) answers every typed
question — anything whose answer is one of a known set of options, including how a
research finding relates to the idea. The *writer* (`writer:`, `Engine.Writer`)
does what has no option list: read a document, search the web, write a paragraph.
One backend may hold both roles; Jev and Laya can only hold the first. When you add a
model call, decide which role it is: if the answer can be typed, it is a question
in a rubric file for the judge, not a prompt for the writer.

Corollary: to change behaviour, first ask whether the change belongs in
`configs/` (embedded defaults, user-overridable) rather than in a `.go` file.

## Three modes, one core

| Mode | Entry | Output |
|---|---|---|
| Human | `ideacheck` / `ideacheck "idea"` | bubbletea TUI (`internal/tui`) |
| Agent | `ideacheck --agent --answer problem=… ` or `ideacheck idea.md --agent` | JSON on stdout, logs on stderr, exit code carries the status |
| Server | `ideacheck serve` | same JSON over HTTP (`server`) |
| Hosted | `sparkjudge-api` (Fly.io) | the same HTTP API for the Sparkjudge iOS app, per user, behind App Attest and quotas (`docs/sparkjudge-api.md`) |

All three call `ideacheck.Engine.Check`. Nothing in package `ideacheck` knows
about a terminal, a process or a config file — see "Using the core from another
host" below.

## Pipeline (`ideacheck/`, package `ideacheck`)

```
intake → extract → gaps + router (one batch) → research + sift → score (fan-out) → aggregate → verdict → explain → persist
          writer     judge                        writer   judge   judge                                       writer
```

| Stage | File | What it does |
|---|---|---|
| intake | `intake.go` | text / file / stdin JSON / YAML frontmatter / `--answer` → `Intake{Idea, Context, Fields, Profile}`; `State()` labels it for the model. Fields alone are a complete check — no document needed |
| fields | `fields.go` + `configs/fields.yaml` | the catalogue of what a caller can send and what each field means: one source for `ideacheck fields`, the JSON form, and the extraction prompt |
| extract | `extract.go` | one optional model call that pulls idea fields out of free prose, filling only fields the user left empty (`extract: true`) |
| gaps | `engine.go` + `rubrics/_gaps.yaml` | nouls "does the description state X"; below `threshold` and the field is still empty → `missing[]` |
| route | `engine.go` + `rubrics/_router.yaml` | one `choice` over idea types → picks `rubrics/<type>.yaml` (`other` → the router's `fallback:`) |
| research | `research.go`, `lookup.go` + `research.yaml` + `prompts/research*` | the rubric is picked first, so only the `research.yaml` topics its questions read (`uses: [evidence.<topic>]`) are looked up. Findings (with URLs) land in `result.research` and, grouped by topic, in `evidence` state. `lookup.go` is the default path and has no model in the loop: writer **plans** queries (`Planner`; else the topic's own `queries:`) → Go **searches** (`Engine.Search`: SearXNG / Tavily / Brave) → Go **reads** pages to text (`Engine.Pages`) → writer **digests** (`Digester`; else results are the findings; a finding whose URL was not among the results is dropped). `research.search: llm`, or nothing to search with, falls back to the writer's own web tool (`Researcher`). Cached per idea-as-given (`Engine.Cache`, the history DB). Every step degrades, none fails the check |
| sift | `research.go` + `rubrics/_evidence.yaml` | the judge types each finding (one `choice` over `idea` + `finding`); options under `drop:` remove it from `evidence`. `research.sift: auto` = only when judge ≠ writer. The cache keeps each finding's relation and who typed it, so the same judge never sifts a cached search twice |
| score | `engine.go`, `../judge/fanout.go` | every rubric question at once, one goroutine each |
| aggregate | `aggregate.go` | normalize to [0,1], apply polarity, weight, divide by the weights **answered** |
| verdict | `verdict.go` + rubric `verdict:` block | ordered gates, then composite thresholds. No model call |
| explain | `explain.go` + `prompts/explain.tmpl` | optional paragraph saying why, after scoring. Never changes a score. The bands that turn a value into "strength" or "probably true" are in the template |
| persist | `../store/` | SQLite history (`history`, `last`, `show`); the host saves, not the engine |

Invariants worth keeping:

- One failed question never fails the batch. It is excluded and renormalized, and
  it shows up in `warnings[]` and in that dimension's `error`.
- A question only sees the state it declares in `uses:` (context-rot rule,
  `judge.State.Sub`).
- Research supplies facts, never numbers. A question that merely benefits from
  `evidence` lists it in `uses`; one that cannot be judged without it also sets
  `requires: [evidence]` and is skipped when nothing was searched — so a run
  without research scores exactly the questions it always did.
- A search that could not be run is never "searched, found none": an empty
  list is evidence (and is cached for a week), so a SearXNG whose engines turned
  it away is an error (`search.EnginesError`). All searches failing = not
  researched; some failing = a warning beside the findings.
- A gap whose field a research topic `covers:` is never asked about while research
  will run, and leaves `missing[]` once findings come back. The TUI asks at most
  `ask_limit` follow-ups (`Engine.FollowUps`), then re-runs with the needs_input
  result as `CheckOptions.Earlier`: its gap and router answers stand, and a gap whose
  field is now filled is never asked (nor is any gap on a filled field).
- Missing facts do not block a verdict outside the TUI: agent and server runs
  score what is known and report `missing[]` with `partial: true`. `--strict`
  restores the old "stop and ask" behaviour.
- A question that declares `requires: [profile]` is skipped — not guessed —
  when that state is absent, so a missing profile costs a dimension, not the run.
  The profile is settable without a terminal (`ideacheck profile set field=value`),
  because the caller who most needs it has none.

## Layout

The public packages are the core any host imports; `internal/` is the desktop
CLI around it. `make core` fails when a public package reaches `os/exec`,
`internal/`, cobra, bubbletea, zerolog or koanf, and builds them for iOS and
Android.

```
cmd/ideacheck/         main; cmd/schemagen writes schemas/
cmd/sparkjudge-api/    the hosted API for the iOS app: server + internal/sparkjudge,
                       engine built from sparkjudge.yaml's engine: layer
ideacheck/             THE CORE (package ideacheck): Engine, New(Options), Check(ctx,
                       Intake, CheckOptions), Settings, Result, Event — the stages above
judge/                 the ONE interface all model access goes through + fanout,
                       and judge.WithLogger (log/slog on the context)
  jev/ logprob/        backends that run anywhere: plain HTTP, no processes
  structured/ openai/  (structured: Anthropic SDK / OpenAI-compatible)
  mock/                the offline, fixture-driven backend
rubric/                YAML load, validate, hash; verdict gates
prompt/                text/template loading of configs/prompts/*
search/                the web lookup Go runs itself: SearXNG/Tavily/Brave clients, and a
                       page reader (public addresses only) that boils HTML down to text
store/                 SQLite history (modernc.org/sqlite, pure Go), each check with an
                       owner (store.Local = this machine; a migration adds the column),
                       and FindingsCache, the research cache over it
server/                HTTP API + SSE; takes any Store (owner-scoped), logs through slog.
                       Hooks for a hosted API: Auth (names the tenant that owns every
                       check, registry run and list), Meter (may refuse a check before it
                       runs; told how it ended), Mount (extra routes behind Auth).
                       Errors are {"status","error":code,"message"}; *server.Error sets both
mobile/                package sparkcore: the core as a gomobile library (make mobile →
                       build/Sparkcore.xcframework) for the Sparkjudge iOS app
  swift/               FoundationJudge/FoundationWriter (Apple's on-device model) and
                       the throwing Swift overloads; the app compiles these in
  swiftcheck/          xcodegen test bundle: the proof it runs in an iOS process
configs/               EMBEDDED DEFAULTS (go:embed) + Files, the user-dir-over-embedded
                       reader. Kept at this path (not defaults/): it is what CLAUDE.md,
                       README and every "lives in configs/" rule name
  config.yaml          backends, pricing, setup wizard choices
  sparkjudge.yaml      the hosted API: attest mode, plans, limits, spend cap, and an
                       engine: section layered over config.yaml (Jev + OpenRouter writer)
  trends.yaml          the hosted API's GET /v1/trends: sources, refresh, sparks on or off
  fields.yaml          what a caller can send about an idea, and what each field means
  research.yaml        what the writer looks up on the web, and which gap each topic covers
  searxng/settings.yml the SearXNG `ideacheck search up` runs in Docker: the defaults + JSON output
  rubrics/             _gaps, _router, _evidence, business, side_project, content, research, creative
  prompts/             judge_system.md, question_*.tmpl, explain*, extract*, research*
internal/              desktop only
  cli/                 cobra commands and flags. check.go is the default action
  config/              koanf: flags > env IDEACHECK_* > LoadOptions.Layers > user dir >
                       embedded; Config.Settings() is what the core runs with
  backends/            the registry: config → judge, for -b and setup
  judge/               backends that start processes: laya (Jev's wire format over an
                       embedded Python worker running laya-mlx locally; one process per
                       checkpoint, shared by every engine in the binary), claudecli, codexcli
  searchlocal/         Docker SearXNG for `ideacheck search up|down|status`
  tui/                 bubbletea app: menu, idea form, live check, result, setup
  ui/                  the shared step column — install.sh in Go: rows, pending
                       lines with a spinner, the download bar, warnings, errors
  bench/               backend comparison on bench/ideas.jsonl
  selfupdate/          daily release check, verified self-replace
  logging/             zerolog to stderr, always; Slog bridges log/slog into it
  schema/              the JSON schema of Result
  sparkjudge/          the hosted API's own part: App Attest (Verifier, over
                       takimoto3/app-attest; Apple's root embedded, injectable in tests),
                       Accounts (SQLite now, an interface Postgres can take over): keys,
                       challenges, RevenueCat entitlements, the monthly ledger, daily
                       spend; Meter, rate limits, /v1/me and the RevenueCat webhook; the Lab
                       routes (lab.go, trends.go): /v1/mix (writer) and /v1/trends (judge types
                       each item with the router question, cached per refresh)
docs/                  sparkjudge-api.md: routes, auth flow, quotas, env, deploy
Dockerfile, fly.toml   the hosted API's image and Fly app (make deploy; deploy/sparkjudge/)
schemas/               check_result.schema.json, generated from the Go types
scripts/               bash for every non-trivial make target
```

User overrides live in `$XDG_CONFIG_HOME/ideacheck/` (or `-c DIR`) with the same
relative paths; any file present there replaces the embedded one.

## Core interface (`judge`)

```go
type Judge interface {
    Name() string
    Evaluate(ctx, State, Question) (Answer, error)   // ONE question, called concurrently
    Capabilities() Capabilities
}
```

Optional, type-asserted where used: `BatchJudge` (all questions in one request),
and the writer's three — `Narrator` (the explain paragraph), `Extractor` (the
extract stage), `Planner` + `Digester` (name the searches, then turn fetched
results into findings — no web access needed, so a local model can), `Researcher`
(the writer's own web tool; `CanResearch()` says whether this configuration can
reach the web). A backend that implements none of them still
works. For the `structured` family, searching is a provider capability:
`structured.Searcher` (claude-cli, codex-cli, Anthropic, OpenRouter).

Question kinds: `noul` (probability a statement is true, with optional
`criteria: {yes, no}` for the boundary), `score` (ordered levels), `choice` (named
options). Rubric-only metadata on a question: `weight`, `polarity` (+1
good-when-high, −1 bad-when-high, 0 informational), `uses`, `requires`, `derive`
(below), and for `_gaps.yaml` only `ask`/`fills`. A rubric's `verdict.backends.<name>` overrides
gates/thresholds/min_confidence for one backend: cuts are tuned per backend on
`bench`, never carried across. Composite confidence averages choice and score
answers only — a noul is a probability, not a doubt.

## Using the core from another host

A host builds `ideacheck.Settings` from its own configuration (the CLI:
`config.Config.Settings()`; anyone else: `ideacheck.DefaultSettings`, the
embedded config.yaml's values), picks a judge from `judge/…`, and calls `New`:

```go
settings, err := ideacheck.DefaultSettings("jev", "") // judge jev, no separate writer
if err != nil {
	return err
}
engine, err := ideacheck.New(ideacheck.Options{
	Settings: settings, // Files nil = the embedded rubrics, prompts and research.yaml
	Judge:    &jev.Judge{BaseURL: "https://api.typesafe.ai", APIKey: key, Model: settings.Judge.Model},
})
if err != nil {
	return err
}
res, err := engine.Check(ctx, ideacheck.Intake{Idea: "a to-do app for dentists"}, ideacheck.CheckOptions{
	Proceed: true,                                 // score what is known, report missing[]
	OnEvent: func(e ideacheck.Event) { /* … */ }, // live progress, one event at a time
})
```

The rest is optional ports on `Options`: `Writer` (a backend that can write —
extract, research, explain), `Search` + `Pages` (`search.New`,
`search.NewReader`), `Cache` (`store.FindingsCache`). Saving is the host's job
(`store.Open(…).Save(ctx, store.Local, …)`: every check has an owner, `""` on
this machine). `server.Server` is the HTTP face of the same engine. A
result's `config_hash` is `Settings.Hash`, or a hash of the Settings when the
host leaves it empty; the CLI's is `Config.Hash()`, pinned by a test in
`internal/config` because history and bench baselines compare it.

### Mobile (`mobile/`, package `sparkcore`)

The iOS app runs the whole check on the phone: `sparkcore` wraps `ideacheck` in
what gomobile can carry — strings (JSON), numbers, errors and interfaces of
those. The app implements `Judge` (and optionally `Writer`) in Swift; Go renders
the prompts from `configs/` and sends them in each request, so no prompt lives
in Swift. `Engine.Prepare(intake, options)` → `Check.Run(listener)` /
`Check.Cancel()`; `Fields()` and `Rubrics()` feed the app's forms. No research,
no store: evidence questions are skipped, the app keeps its own history.

- Every exported call recovers panics into errors: a Go panic kills the app. A
  Swift call cannot be interrupted, so on cancel or timeout Go stops waiting
  and drops its answer.
- A gomobile `(string, error)` is not `throws` in Swift (non-null NSString +
  NSError pointer): protocol methods take `error: NSErrorPointer`
  (`sparkcoreCatching` adapts), and `mobile/swift/Sparkcore+Throws.swift` gives
  the calls a throwing shape.
- The judge's `Name()` picks the verdict cuts (`verdict.backends.<name>`); the
  on-device judge is `foundation` and has none yet — tune them on bench, do not
  guess. It answers with one greedy pick, so run it with `max_concurrent: 1`.
- The bind runs from a throwaway module in `build/bind` that requires
  `golang.org/x/mobile`: in go.mod it would drag x/net and x/tools forward for
  the CLI, and gomobile cannot bind inside a go.work.

## Writing rubric questions (`rubric.Validate` enforces most of it)

One judgment per question; "yes" is the natural positive reading; every `choice`
includes `other`; `score` has 2–10 concrete levels, low → high; weights ≥ 0 and
every weighted question sets polarity; `uses` lists only the state the question
needs, and a question about the person sets `requires: [profile]`; state names are
`idea`, `profile`, `evidence` (research findings by topic; an empty list means
"searched, found none") and `finding` (`_evidence.yaml` only). Prefer
`evidence.<topic>` to the whole of `evidence`: the question sees only that topic,
and a topic no question of the chosen rubric reads is neither searched nor
sifted (`requires: [evidence]` is met by any topic read); name the state
the question reads (`idea`, `profile`, `evidence.competitors`) — Jev reads literally; gate expressions may
reference real question ids only, and see normalized values in [0,1]. After
changing a question, `make bench ARGS='-b jev'` and compare (`bench --compare`).

Only what simple code cannot decide goes to the judge. A noul may carry one
`derive:` rule that answers it without a model call when it can, and asks the
judge as usual when it cannot:

- `present: [profile.skills, …]` — 1 when any of these intake fields is filled, else 0
  (the only rule a `_gaps.yaml` question may use: nothing else is known yet);
- `same_as: <gap id>` — the answer the judge gave that gap question in this check;
- `evidence: {topic, relation}` — 1 when a finding in the topic was sifted with that
  relation, 0 when every finding in it was sifted otherwise (or none was found);
  the question must read that topic;
- `zero_when_unstated: <field>` — 0 when the field is empty and its gap question
  found it unstated; for a question whose `no` criteria say "states none".

`requires:` is checked first, so a derived question is still skipped without its
state. A derived answer has method `derived`, its dimension names the rule
(`derived`), and it counts toward the composite like any other. Names a rule
points at in other files are checked when the rubric loads (`derivable`).

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
  matches the installer that put it there. On top, the app adds the terminal's
  own cyan as its accent (`accent`, `heading`, `pill` in `styles.go`): where you
  are, what has focus, which key does what. Scores are colored by good news
  (`scoreStyle`: polarity-aware green/yellow/red), never by raw height.
- **One highlight.** "Here" is always the same cyan bar: `pill` for the app's
  name, the open tab and the menu line, `tableStyles` for the history row,
  `formTheme` for a form's focused button. A bubbles or huh default (pink row,
  black button) is a second program showing through. The bar is never bold:
  lipgloss draws padding without it, and a bold-as-bright terminal then fills
  one bar in two shades.
- **One model line.** Who judges, who writes (`modelLine`, `who` in `app.go`)
  sits under every page's title in one format; in Settings the same line shows
  the choices being made (`rolesLine`) instead of a second panel.

## Flags

`internal/cli/check.go` is the authority for the default action's flags, and each
flag carries its rationale in a comment beside it. Rules when adding one: the long
form is a real word, the short form is one letter checked against the conventions
people know from `git`/`kubectl`/`curl`/`make`/`ssh`, lowercase enables and
uppercase negates (`-a`/`-A`, `-e`/`-E`), and letters with a strong conflicting
convention elsewhere (`-j` jobs, `-p` port) are not reused for something else.
Document the flag in `README.md` too.

Agent contract: `--agent` = JSON on stdout + never ask + score with gaps. A field
added to `ideacheck.ideaFields`/`profileFields` must be described in
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
make up         # = ideacheck search up: a SearXNG in Docker on 127.0.0.1, so research searches for real; make down removes it
make api        # the Sparkjudge hosted API on 127.0.0.1:8787: mock judge, attest off, X-App-User-Id alone
make deploy     # fly deploy of the hosted API; checks login, app and secrets first, creates nothing
```

- The hosted API's tests (`internal/sparkjudge`) play the device: a test root CA,
  intermediate and credential certificate, hand-encoded CBOR attestation and
  assertion objects (`device_test.go`). No test reaches Apple, RevenueCat or Fly.

- `-b mock` researches only when its fixtures dir holds a `research.json` (a list
  of findings), so offline runs and old tests are unchanged; `bench` never researches.
- Setup asks for the two roles by name: **Judge** (provider, key, model, effort),
  then **Writer** — the judge itself when it can write (`sameWriter`), another
  provider that is ready, or nobody beside a judge that only judges (`noWriter`).
  The Writer step is skipped when there is only one possible answer. `modelSteps`
  (list, typed id, effort) runs once per role; each provider keeps its own `pick`,
  so no step may assume "the" model. A provider that only judges is `needs_writer`
  in `config.yaml`; nothing in Go names Jev or Laya as the classifier.
- `laya` tests run the test binary as the worker (`TestMain` + `Judge.Command`),
  speaking worker.py's JSON lines, so no test needs Python. `laya.Downloaded`
  reads the Hugging Face cache the way huggingface_hub lays it out, so setup can
  say "not downloaded" without starting an interpreter; the readiness check does
  the same before a check, because the worker would otherwise download 850 MB
  inside a question's timeout.
- Setup's last step (`Research`, `internal/tui/pages.go`) offers `search up` through
  `Host.Search` / `Host.StartSearch`. A wizard default belongs in the step's
  `build`, never in the draft's constructor: a skipped step must not answer yes
  (that is how a first run once saved Jev as judge without a key). `wizard.fit`
  re-lays a form out after sizing it: huh draws what it laid out last, so text
  kept the old width (cut off at the edge) and the form measured too short —
  huh then squeezed the list into a window starting at the cursor, often just
  the chosen option. Settings builds selects with `choose` (options before value).
- `searchlocal.Local` talks to Docker through the `searchlocal.Docker` func (`app.docker` in
  the CLI), so its tests fake the docker CLI and no test needs Docker. Its errors
  are the product: `DockerError` is problem + what to do, per OS.
- Core tests replace `Engine.Search` / `Engine.Pages` with fakes; `search`
  tests use `httptest` per provider. No test touches the network.
- An agent loop is the expensive way to do anything here: each turn re-sends all
  earlier tool output. Before adding a tool to a model call, ask whether Go can
  fetch the material and hand it over in one call instead.
- Setup's model lists are read live (`discover:` per provider, `internal/cli/models.go`):
  a model id or effort level written into Go or a preset list goes stale with the
  next release, so `models:` in `config.yaml` only lends labels and is the fallback
  when a list cannot be reached. `claude` has no list command; its aliases come from
  `--help` (verified on 2.1.282).
- The search flags were verified against the installed CLIs (`claude` 2.1.278,
  `codex` 0.153.4) — re-verify them, as the comments beside them say, when bumping.
- `-b mock` is the offline backend: use it for anything that is not about a real
  provider's wire format.
- Prompt output is pinned by golden files: `go test ./prompt -update`.
- Backend tests use `net/http/httptest` fixtures per wire format — never a live API.
- Fan-out tests assert real concurrency (12 questions × 200 ms must finish well
  under their sum) and partial failure, timeout and 429 `Retry-After` handling.
- Never log API keys; full prompts only at trace level. stdout stays clean JSON.
