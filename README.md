# ideacheck

Check an idea from the terminal. `ideacheck` asks a judge model a set of independent
typed questions about your idea — all at once — combines the answers with weights
you control, and reports per-dimension scores plus a verdict: **build**, **explore**,
**park** or **kill**. Before judging, it tells you what information is missing.

It structures thinking and flags gaps. **It is not a success predictor.**

The model is only asked narrow judgments ("is this a tarpit idea?"). The loop, the
arithmetic, the gates and the verdict are ordinary code. The model is never asked
for a final score.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/morethancoder/ideacheck/main/install.sh | sh
```

One self-contained binary, no runtime, no Go toolchain. The script picks the build
for your OS and CPU (macOS and Linux, Intel and ARM), **verifies it against the
release's published checksums**, and installs it into `~/.local/bin` — it tells you
if that is not on your `PATH`. Put it somewhere else with
`IDEACHECK_INSTALL_DIR=/usr/local/bin`, or pin a version with `IDEACHECK_VERSION=0.1.0`.

If you have Go and would rather build it yourself:

```sh
go install github.com/morethancoder/ideacheck/cmd/ideacheck@latest
```

Or from a clone: `make install` (into `GOBIN`), or `make build` for `bin/ideacheck`.
Binaries and checksums for every release are at
[github.com/morethancoder/ideacheck/releases](https://github.com/morethancoder/ideacheck/releases).

### Upgrading

```sh
ideacheck upgrade          # install the latest release
ideacheck upgrade --check  # just tell me whether there is one
```

`upgrade` is the install script again, from inside the binary — the same column of
steps, the same live download bar:

```
  ideacheck  ›  upgrade

  ✓ current    0.1.0
  ✓ latest     0.2.0
  ✓ download   6.0 MB
  ✓ verify     sha256 matches the release
  ✓ install    ~/.local/bin/ideacheck

  ideacheck 0.2.0 is ready
```

It downloads the release build for your machine, checks it against the published
checksums, and swaps it in with an atomic rename — an interrupted upgrade leaves
the working binary in place. An ideacheck installed by Homebrew or `go install` is
left alone, and the command prints the right way to upgrade it instead.

Piped or in CI there is no spinner and no bar: one plain line per finished step, in
the same order, so the output stays greppable.

You do not have to remember to check. Once a day, in the background, ideacheck asks
GitHub whether there is a newer release and remembers the answer; when there is one
it adds a single line after your check:

```
ideacheck 0.2.0 is available (you have 0.1.0) — run `ideacheck upgrade`.
```

That is all it ever sends: one unauthenticated GET to the public releases API, at
most once a day. It never blocks a check, never runs when output is piped or in CI,
and never runs on a build you compiled yourself. Turn it off for good with
`export IDEACHECK_NO_UPDATE_CHECK=1`.

## Quick start

Installed it? Just run `ideacheck`. Working on the code instead:

```sh
make setup            # check tools, download modules
make dev              # build and open the app — the first run walks you through setup
```

The first time you run `ideacheck` it asks how to reach a model. The default,
preselected choice is a **local Ollama model: free, no key, no login**
(`ollama pull qwen3:8b`, or any model you already have):

| Choice | Needs |
|---|---|
| Ollama (default) | a local Ollama server with at least one model pulled |
| Claude CLI | the `claude` command, logged in |
| Codex CLI | the `codex` command, logged in |
| Anthropic / OpenAI / OpenRouter | an API key (typed once, or exported in your shell) |
| Ollama Cloud | an `OLLAMA_API_KEY` from https://ollama.com/settings/keys |

Setup then offers a model list and an effort level. For the Claude and Codex CLIs
the first choice is **Off — no thinking**, the default: every question is one
narrow judgment, and thinking multiplies the wait (Haiku spent 128 output tokens
to answer "no" with it on, 4 with it off). A local Ollama model never thinks
before answering either: the probability is read from the first token's logits,
so there would be nothing to read. Codex and Ollama
list the models your login or server actually has (and default to one that is
installed); the others list common models with their API price. "Other…" takes any
model id.

Before every check ideacheck makes sure a local Ollama is running and has the model.
If not, it says so in one line (start Ollama / `ollama pull <model>` / the models you
do have) and, in the app, opens Settings and then carries on with your check. No
questions are sent until the model can answer them.

Change it any time with `ideacheck setup` (alias `settings`) or **Settings** inside the
app. The choice is saved to `config.yaml`; a typed API key goes to `credentials.yaml`
(mode 0600) in the same directory — never into `config.yaml`. An exported environment
variable always wins over the saved key.

The app is paged: **menu → idea, step by step → live check → follow-up questions →
result** (tabs: Overview · All scores · Gaps · Details), plus History, Profile and
Settings pages. `esc` goes back a step; the last result stays in your scrollback on exit.
Details you left blank in the step-by-step form count as answered ("I don't know"), so
the follow-up page only asks about what you were never shown.

Every result opens with a short plain-language paragraph on why the idea got its
verdict (one extra model call after scoring; it explains the scores, never changes
them — `-E` or `explain: false` skips it), and a cost line: the dollar total, model,
tokens in/cached/out, and the per-million-token price used. The Claude CLI reports
its own cost; other backends are priced from `pricing:` in config; local Ollama is
free. On a CLI subscription the figure is the API list-price equivalent.

```
ideacheck                          # open the app (menu)
ideacheck "an app that ..."        # check it: live view, then the result page
ideacheck "..." -i                 # edit the idea step by step first
ideacheck idea.md -r side_project  # read a file, force a rubric
ideacheck "..." -b logprob -m qwen3:8b
ideacheck "..." -o json | jq .verdict
cat idea.json | ideacheck --agent         # agent mode: never asks, JSON out, logs on stderr
ideacheck -- "serve"               # `--` forces the next arg to be idea text
ideacheck serve -p 9000            # local HTTP API, same JSON contract
ideacheck fields | rubrics | setup | history | last | show 12 | profile | config path
ideacheck bench -b structured,logprob -n 3
```

## Agents and scripts

`ideacheck --help-agent` prints this whole contract at any time.

Two ways to call it, best first:

```sh
# Send the facts themselves — nothing has to be inferred, and one call less
ideacheck --agent --answer problem="..." --answer audience="..." --answer why_now="..."

# Send a document — one call reads the fields it already states; --answer fills the rest
ideacheck PRODUCT.md --agent --answer why_now="agents read repos now"
```

`ideacheck fields` prints every field and what it means, with an example;
`ideacheck fields -o json` is the same as data, each entry carrying the flag that
sets it. That catalogue (`fields.yaml`) is also what the document reader is told
each field means, so the two cannot drift apart.

`--agent` means JSON on stdout, logs on stderr, no question is ever asked, and the
idea is **scored even when facts are missing**: what nobody stated comes back in
`missing[]`, `partial` is true, and the confidence is discounted by how much is
unknown. Nothing is withheld unless you ask for that with `--strict`.

Supply the facts instead of being asked, in whichever form suits the caller:

| | |
|---|---|
| `--answer field=value` | an idea field, repeatable — enough on its own, no file needed |
| `--answer profile.background=…` | anything about you |
| `--profile-text "…"` | shorthand for `profile.background` |
| `--context "…"` | supporting material that is not the pitch |
| YAML frontmatter | `context:`, `fields:`, `profile:` at the top of the document |
| stdin JSON | `{"idea": "...", "context": "...", "fields": {...}, "profile": {...}}` |

Fields: `title problem audience solution why_now monetization competitors_known differentiation`.
Profile: `skills domains network would_use_myself time_horizon background` — saved to
`$XDG_CONFIG_HOME/ideacheck/profile.yaml`; `-P NAME|FILE` picks another. Set it
without a terminal with `ideacheck profile set background="..."` (merges; an empty
value clears a field) and read it back with `ideacheck profile show`. Without a
profile, founder-fit questions are left unscored rather than guessed, so this one
command is worth it.

Before looking for gaps, one call reads the fields your document already states and
fills the ones you left empty (`extracted[]` says which; `extract: false` turns it
off). So a fact written in prose stops being asked about, and a question that has
no basis at all — founder fit with no profile — is left unscored with its weight
moved to the others, rather than guessed.

Exit codes: **0** scored · **2** `needs_input`, nothing scored (only under
`--strict`) · **3** the check ran but no question could be scored · **1** the command
itself failed. With nothing set up it uses the local Ollama default; if that model
cannot be reached it exits 1 with a one-line reason and the fix (start Ollama,
`ollama pull`, `ideacheck setup`, or `-b` / `-m`).

When stdout is not a terminal, output is JSON without being asked. The contract is
published as [`schemas/check_result.schema.json`](schemas/check_result.schema.json)
(generated from the Go types; a test fails if it drifts).

## Backends

| `-b` | Probabilities come from | Needs | Notes |
|---|---|---|---|
| `structured` | k-sample vote frequencies, or model-stated | an Anthropic, OpenAI, OpenRouter or Ollama Cloud key (`provider:` in config) | Anthropic goes through the official Go SDK (`output_config.format` JSON schema, state block prompt-cached, no temperature — current Claude models reject it). OpenAI/OpenRouter use the OpenAI-compatible API with a strict `json_schema` response format. Ollama Cloud (`provider: ollama`, `base_url: https://ollama.com/v1`) has no structured outputs, so the schema goes in the prompt and the JSON is cut out of the reply. |
| `claude-cli` | model-stated | `claude` CLI, logged in | Human use only: spends your Claude subscription's limits (that policy may change). ~11k tokens of CLI overhead per call, so it batches by default. |
| `codex-cli` | model-stated | `codex` CLI, logged in | Human use only, same trade-offs (~12k tokens overhead per call). Runs `codex exec` read-only and ephemeral with `--output-schema`. |
| `logprob` | token logits over answer labels | Ollama ≥ 0.12, llama.cpp, vLLM, or an OpenRouter provider that returns logprobs | Local and free. **Raw logits, not calibrated.** Runs each choice/score question `shuffle_runs` times in shuffled order and averages, to damp position bias. Ollama Cloud returns no logprobs — use `structured` with `provider: ollama` for it. |
| `jev` | model-native, calibrated | `TYPESAFE_API_KEY` ([typesafe.ai](https://typesafe.ai)) | TypeSafe's System One model: typed noul/score/choice answers with probabilities, no sampling. Wire format checked against docs.typesafe.ai/api (2026-09-21); one request per state group. Pinned to `jev-1.13.0` (`jev-latest` moves). Offered in `ideacheck setup`. |
| `mock` | seeded / fixtures | nothing | Tests and `bench --dry-run`. |

`serve` refuses the two CLI-login backends unless you pass `--allow-cli-backend`.

API keys come from the environment, or from `credentials.yaml` written by `ideacheck setup` — never from `config.yaml`.
In a checkout, `make run/dev/serve/bench` also load a gitignored `.env` (copy `.env.example`); a blank line there leaves your shell's value alone.

## Configuration

Everything that is a prompt, rubric, question, weight, threshold or model name lives
in config, not in Go. Defaults are embedded in the binary; any file you place in
`$XDG_CONFIG_HOME/ideacheck/` (or `-c DIR`) with the same relative path replaces it.

```sh
ideacheck config path                 # which directory is in use
ideacheck config dump                 # print every effective file
ideacheck config dump ./my-rubrics    # write them somewhere to start editing
ideacheck config set backends.claude-cli.model opus
```

`config dump DIR` never overwrites a file you have edited, and it refuses to write
into the directory ideacheck reads unless you pass `--force`: a copy there shadows
the built-in default from then on, upgrades included. `config set` keeps a choice
that `-b` / `-m` would otherwise make for one run, and undoes itself if the value
does not load.

Precedence: flags > env (`IDEACHECK_BACKEND=jev`, nest with `__`:
`IDEACHECK_BACKENDS__JEV__MODEL=…`) > your config dir > embedded defaults.

Rubrics (`rubrics/*.yaml`) are validated on load: one judgment per question, every
`choice` has `other`, 2–10 score levels, weighted questions declare polarity, gate
expressions may only reference real question ids. Gates see **normalized** values in
[0,1] and are skipped when a question they read went unanswered.

- A `noul` may carry `criteria: {yes: …, no: …}` saying where the line between yes
  and no falls. Every backend sees it (Jev as its native `criteria`).
- `verdict.backends.<name>` replaces the gates, `thresholds` or `min_confidence`
  for one backend. Probabilities from different backends are not on one scale:
  Jev's are calibrated, while vote counts come in steps of 1/k. Tune a cut on
  `bench` for the backend it applies to.
- The composite's confidence averages the **choice and score** answers only. A noul
  is already a probability: 0.25 means "probably not", which is a clear answer, not a
  doubtful one.

## HTTP API

`POST /v1/check` (body = intake JSON; `?rubric=`, `?strict=1`, `?async=1`),
`GET /v1/checks`, `GET /v1/checks/{id}`, `GET /v1/checks/{id}/events` (SSE:
`progress` events, then `result`), `GET /v1/rubrics`, `GET /v1/healthz`.
CORS is allowed for localhost origins only. Binds to 127.0.0.1 by default.

## Benchmarking

`ideacheck bench -b structured,logprob -n 3` runs `bench/ideas.jsonl` (25 labelled
ideas) and reports latency p50/p95, cost, agreement with labels (accuracy, score MAE,
Brier), self-consistency across repeats, logprob order bias, gap-detection recall,
and cross-backend Cohen's kappa / Spearman. Results land in `bench/results/` as JSON
and CSV; `bench --compare a.json b.json` diffs two runs.

## Development

`make` lists targets: `dev build install run serve test race lint fmt tidy bench dry dump schema release doctor setup clean`.
Prompt changes are pinned by golden files: `go test ./internal/prompt -update`.

### Cutting a release

```sh
make release            # asks for the version, suggesting the next patch
make release ARGS=0.2.0
```

It refuses a dirty tree or a branch other than `main`, runs the tests, then tags
`vX.Y.Z` and pushes it. The tag is the trigger: `.github/workflows/release.yml`
runs [GoReleaser](https://goreleaser.com) (`.goreleaser.yaml`), which cross-compiles
darwin/linux × amd64/arm64, publishes the archives with a `checksums.txt`, and
writes the changelog from the commits since the last tag. Within a day every
installed copy offers the new version; `ideacheck upgrade` takes it immediately.

The archive names (`ideacheck_<version>_<os>_<arch>.tar.gz`) and `checksums.txt`
are a contract with every installed binary — `internal/selfupdate` looks for
exactly those. Renaming them breaks upgrades for everyone who already has the tool.
