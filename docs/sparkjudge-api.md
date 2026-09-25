# Sparkjudge hosted API

`cmd/sparkjudge-api` is the server behind the hosted checks in the Sparkjudge
iOS app. It runs the ideacheck core (package `ideacheck`, served by package
`server`) with **Jev judging** and an **OpenRouter model writing**
(`z-ai/glm-5.3-flash` by default), with web research before scoring. It adds:

- **who may call it**: an app user id, proven by Apple App Attest;
- **how much they may run**: a monthly allowance per plan (free, or pro while
  the RevenueCat entitlement is active), a per-user rate limit and a daily
  spend cap for the whole service;
- **the routes the app needs** around a check: `/v1/me`, the attestation
  routes and RevenueCat's webhook.

Provider keys stay on the server. The app never sees one.

The code: `internal/sparkjudge` (auth, quotas, webhook, the accounts store),
`cmd/sparkjudge-api` (wiring), `configs/sparkjudge.yaml` (plans, limits,
models). The local `ideacheck serve` is unchanged.

## Routes

| Route | Auth | What it does |
|---|---|---|
| `GET /v1/healthz` | none | `{"status":"ok"}`; Fly's health check |
| `POST /v1/attest/challenge` | user id | a one-time challenge for attesting a key |
| `POST /v1/attest` | user id | verify a key's attestation and keep it for the user |
| `POST /v1/webhooks/revenuecat` | webhook secret | RevenueCat subscription events |
| `GET /v1/me` | signed | plan, checks used and allowed this month, when it resets |
| `POST /v1/check` | signed | run a check; `?async=1` answers `202 {id, events}` at once |
| `GET /v1/checks` | signed | the caller's checks, newest first (`?limit=`, default 50) |
| `GET /v1/checks/{id}` | signed | one of the caller's checks |
| `GET /v1/checks/{id}/events` | signed | Server-Sent Events: `progress` …, then `result` or `error` |
| `GET /v1/fields` | signed | what an intake can carry (`fields.yaml`), for the idea form |
| `GET /v1/rubrics` | signed | the rubrics and their questions |

`POST /v1/check` takes the same intake JSON as `ideacheck serve`
(`{"idea": "...", "fields": {...}}`) and answers the same result
(`schemas/check_result.schema.json`). `?rubric=` forces a rubric; `?strict=1`
stops at missing facts instead of scoring around them.

Every user sees only their own checks: another user's check id is a 404, in the
list, by id and on the event stream alike.

### Errors

Always JSON, with a matching status:

```json
{"status": "error", "error": "quota_exceeded", "message": "no hosted checks left this month; upgrade to Pro for more"}
```

| Status | `error` | Meaning |
|---|---|---|
| 400 | `bad_request` | the body is not an intake, or not base64 where it must be |
| 401 | `missing_user` | no `X-App-User-Id`, or not a UUID |
| 401 | `attestation_required` | an unsigned request while attest is required |
| 401 | `unknown_key` | the key was never attested for this user |
| 401 | `bad_assertion` | the signature does not verify for this request |
| 401 | `stale_assertion` | the assertion was used already or a newer one arrived first: sign again and retry |
| 401 | `bad_challenge` / `bad_attestation` | an attestation that cannot be accepted |
| 402 | `quota_exceeded` | no checks left this month |
| 409 | `key_exists` | that key is already attested |
| 413 | `too_large` | body over `limits.max_body_bytes` |
| 429 | `rate_limited` | too many requests this minute, or checks this hour |
| 503 | `daily_cap` | the service has spent today's budget; checks resume at 00:00 UTC |
| 503 | `not_configured` | (webhook) `REVENUECAT_WEBHOOK_AUTH` is not set |

## Identity and App Attest

The app generates a **UUID once** (kept in the Keychain) and uses it as both
`X-App-User-Id` and RevenueCat's `appUserID`, so a purchase and a check name the
same person.

With `attest.mode: required` (production), that id is only believed from a
genuine copy of the app, following Apple's
[Validating apps that connect to your server](https://developer.apple.com/documentation/devicecheck/validating-apps-that-connect-to-your-server).

**Once per install** (and again whenever the key is lost):

1. `DCAppAttestService.shared.generateKey()` → `keyId` (base64 string).
2. `POST /v1/attest/challenge` with `X-App-User-Id` → `{"challenge": "<base64>", "expires_at": ...}`.
   A challenge lives `attest.challenge_ttl` (5 minutes) and works once.
3. `attestKey(keyId, clientDataHash: SHA256(challenge bytes))` → attestation.
4. `POST /v1/attest` with `X-App-User-Id` and
   `{"key_id": keyId, "attestation": "<base64>", "challenge": "<the base64 from step 2>"}` → `201`.

The server checks the certificate chain up to Apple's App Attestation Root CA
(embedded, pinned by fingerprint in a test), the nonce
`SHA256(authData ‖ SHA256(challenge))` in the credential certificate, that the key
id is the SHA-256 of the certificate's public key, the RP ID hash
`SHA256("<team id>.com.morethancoder.sparkjudge")`, a counter of 0 and the
`appattest` production aaguid (`appattestdevelop` only with
`attest.development: true`). It keeps the public key and Apple's receipt per user.
The checks are [github.com/takimoto3/app-attest](https://github.com/takimoto3/app-attest)'s
(v1.3.0, read before it was chosen; the root is injectable, so tests run on a
test CA).

**Every other request** is signed:

```
clientData = METHOD + "\n" + PATH_WITH_QUERY + "\n" + hex(SHA256(body))
```

e.g. `POST\n/v1/check?async=1\n5f2b…` (an empty body hashes to
`e3b0c442…b855`). The app calls
`generateAssertion(keyId, clientDataHash: SHA256(clientData))` and sends:

| Header | Value |
|---|---|
| `X-App-User-Id` | the UUID |
| `X-App-Attest-Key` | `keyId` |
| `X-App-Attest-Assertion` | base64 of the assertion |

The server verifies the signature over `SHA256(authenticatorData ‖ SHA256(clientData))`
with the stored key and the RP ID hash, then stores the assertion's counter **in
the same write that checks it is higher** than the stored one. An assertion
therefore works once, and cannot be moved onto another path or body.

Because counters must rise in the order requests *arrive*, two requests signed
back to back can reach the server swapped; the later-signed one wins and the
other gets `401 stale_assertion`. The app should send its API requests one at a
time, or re-sign and retry once on `stale_assertion`. The SSE stream is one
request: signed once when it opens.

`attest.mode: off` (development only) skips all of this: `X-App-User-Id` alone
identifies the caller. The simulator cannot attest, so a debug build talks to
a server running with `-attest off`. The server logs a warning at start.

## Plans and quotas

All numbers live in `configs/sparkjudge.yaml`:

| Setting | Default | |
|---|---|---|
| `plans.free.checks_per_month` | 3 | a taste of pro |
| `plans.pro.checks_per_month` | 100 | |
| `entitlement` | `pro` | the RevenueCat entitlement id that means pro |
| `limits.requests_per_minute` | 60 | per user, every signed route and the attest routes |
| `limits.checks_per_hour` | 10 | per user, on top of the monthly allowance |
| `limits.daily_spend_usd` | 25 | the whole service, per UTC day |

- The **ledger** counts checks per user per calendar month (UTC). A check takes
  one **before any model runs**, in a single conditional write, so racing
  checks can never overspend. `resets_at` is the first of next month, 00:00 UTC.
- A check that produced **no score** (an error before a result, or a result with
  status `error`) gives its check back. The cost of whatever models did run still
  counts toward the day's spend.
- The **spend cap** adds every check's `cost.usd` to the day's total and refuses
  checks with `503 daily_cap` once the total reaches the cap. Costs come from
  `pricing:` in `config.yaml` (Jev and `glm-5.3-flash` are priced there).
- Rate limits are token buckets in memory: they hold for the one machine and reset
  on restart. The ledger and the spend cap are in the database.

`GET /v1/me`:

```json
{"user": "0f4c…", "plan": "pro", "used": 4, "limit": 100,
 "resets_at": "2026-10-01T00:00:00Z", "pro_until": "2026-10-25T19:18:16Z"}
```

`pro_until` is absent for a user who never subscribed; for a lapsed one it is in
the past.

## RevenueCat

In the RevenueCat dashboard: **Integrations → Webhooks**, URL
`https://<app>.fly.dev/v1/webhooks/revenuecat`, and an **Authorization header
value** of your choosing, e.g. `Bearer <long random string>`. The same exact string
goes into `REVENUECAT_WEBHOOK_AUTH`. The app configures the SDK with
`appUserID` = the UUID above.

The handler ([RevenueCat webhook docs](https://www.revenuecat.com/docs/integrations/webhooks/event-types-and-fields)):

| Event | Effect |
|---|---|
| `INITIAL_PURCHASE`, `RENEWAL`, `UNCANCELLATION` | pro until `expiration_at_ms` |
| `CANCELLATION` | access lasts until `expiration_at_ms` (a refund moves it to the refund time) |
| `EXPIRATION` | back to free |
| `TRANSFER` | the source ids' entitlement moves to `transferred_to` |
| `PRODUCT_CHANGE`, `TEST`, others | recorded only |

Only events whose `entitlement_ids` contain `entitlement` change anything. Every
event id is recorded, so RevenueCat's retries apply once; an event older than a
user's last change never overwrites it. Changes apply to `app_user_id`,
`original_app_user_id` and every alias. Sandbox purchases (TestFlight) count
like real ones.

## Configuration

`configs/sparkjudge.yaml` is embedded in the binary. To change it without a
rebuild, put a file of the same name in a directory and point
`SPARKJUDGE_CONFIG_DIR` (or `-c`) at it; rubrics and prompts there override too.
Its `engine:` section is a `config.yaml` layer: backends, models, research. Any
`IDEACHECK_*` variable overrides it, e.g. `IDEACHECK_BACKENDS__STRUCTURED__MODEL`.

Environment:

| Variable | Secret | |
|---|---|---|
| `TYPESAFE_API_KEY` | yes | Jev, the judge |
| `OPENROUTER_API_KEY` | yes | the writer |
| `REVENUECAT_WEBHOOK_AUTH` | yes | the webhook's Authorization header value |
| `TAVILY_API_KEY` or `BRAVE_API_KEY` | yes | web search for research; without one, the writer's OpenRouter web plugin searches (billed per search) |
| `SPARKJUDGE_TEAM_ID` | no | the Apple Developer team id (10 characters); required with attest on |
| `SPARKJUDGE_ATTEST` | no | `required` (fly.toml) or `off` |
| `SPARKJUDGE_DATA_DIR` | no | where `checks.db` and `accounts.db` live (`/data`) |
| `SPARKJUDGE_LISTEN` | no | `:8080` |
| `SPARKJUDGE_BACKEND` | no | one backend for both roles, e.g. `mock` |
| `SPARKJUDGE_CONFIG_DIR` | no | override directory, above |
| `IDEACHECK_RESEARCH__ENDPOINTS__SEARXNG` | no | a SearXNG you run, searched before Tavily/Brave |

Flags: `-b BACKEND`, `-attest required|off`, `-listen ADDR`, `-c DIR`.

The server refuses to start with attest required and no team id, or with a
missing provider key.

## Storage

Two SQLite files (pure Go, no cgo) on one Fly volume:

- `checks.db`: the check history (package `store`), every row with its owner,
  plus the research findings cache (shared: it holds what the public web says
  about an idea).
- `accounts.db`: challenges, attested keys, entitlements, webhook event ids, the
  monthly ledger and the daily spend.

`sparkjudge.Accounts` and `server.Store` are interfaces. Every write that
decides access or money is one conditional statement or one transaction, so a
Postgres implementation can replace SQLite, and the service can then run on more
than one machine. That is not built.

## Run it locally

```sh
make api                                   # mock judge, attest off, data in bin/sparkjudge-data
U=11111111-2222-4333-8444-555555555555
curl -s localhost:8787/v1/me -H "X-App-User-Id: $U"
curl -s -X POST 'localhost:8787/v1/check?rubric=business' -H "X-App-User-Id: $U" -d '{"idea":"A payroll tool for bakeries"}'
curl -s -X POST localhost:8787/v1/webhooks/revenuecat -H 'Authorization: Bearer local' \
  -d '{"event":{"id":"e1","type":"INITIAL_PURCHASE","app_user_id":"'$U'","entitlement_ids":["pro"],"expiration_at_ms":4102444800000,"event_timestamp_ms":1758800000000}}'
```

With the real models, export `TYPESAFE_API_KEY` and `OPENROUTER_API_KEY` and run
`SPARKJUDGE_DATA_DIR=bin/sparkjudge-data go run ./cmd/sparkjudge-api -attest off -listen 127.0.0.1:8787`.

## Deploy to Fly.io

`make deploy` runs `scripts/deploy.sh`: it checks the Fly login, that the app
exists and that its secrets are set, runs the API's tests, then `fly deploy`.
It creates nothing. The first time, you do these once:

1. **Accounts and keys**: a [Fly.io](https://fly.io) account (`fly auth login`),
   a TypeSafe API key, an OpenRouter key (with credit), a Tavily or Brave key, a
   RevenueCat project with a `pro` entitlement, and your Apple **team id**
   (developer.apple.com → Membership).
2. **The app and its volume** (pick the region you want; `fra` is in fly.toml):

   ```sh
   fly apps create sparkjudge-api
   fly volumes create sparkjudge_data --app sparkjudge-api --region fra --size 1
   ```

3. **Secrets** (fly stores them encrypted; the app gets them as environment
   variables):

   ```sh
   fly secrets set --app sparkjudge-api \
     TYPESAFE_API_KEY=... OPENROUTER_API_KEY=... TAVILY_API_KEY=... \
     REVENUECAT_WEBHOOK_AUTH='Bearer ...' SPARKJUDGE_TEAM_ID=ABCDE12345
   ```

4. `make deploy`, then `curl https://sparkjudge-api.fly.dev/v1/healthz`.
5. **RevenueCat**: point the webhook at `/v1/webhooks/revenuecat` with the same
   Authorization value, and send a test event.
6. **Xcode**: add the App Attest capability with
   `com.apple.developer.devicecheck.appattest-environment` = `production` for
   TestFlight and App Store builds.

Notes on `fly.toml`:

- **One machine.** The SQLite volume belongs to one machine; keep
  `fly scale count 1` until the accounts store moves to Postgres. Fly snapshots
  volumes daily; `fly volumes snapshots list` shows them.
- **auto_stop_machines = "off".** A stopped machine costs the next request a cold
  start, and stopping mid-check would lose an async check that outlived its
  request. `"suspend"` is cheaper and resumes in well under a second; it is a
  reasonable choice once most checks are synchronous.
- **SSE through Fly's proxy.** No proxy setting is needed: the stream sends a
  `: keep-alive` comment every 15 seconds while a check is quiet, which also keeps
  any older 60-second idle timeout from cutting it. The Go server sets no write
  timeout, since a check takes minutes.
- **Shutdown.** Fly sends SIGTERM; the server stops taking requests and gives
  checks in flight two minutes (`kill_timeout = "130s"`).
- The image is Alpine with the static binary; the entrypoint hands `/data` to the
  `sparkjudge` user (a fresh volume is root's) and runs the API as that user.

## Not built yet

- Postgres behind `Accounts` and `server.Store`, and more than one machine.
- Apple's fraud-risk metric: the receipt is kept per key for a later
  `fraud/` query (the library has a client).
- Deleting a user's data on request (an account-deletion route).
- Async checks that survive a restart (the live registry is in memory).
