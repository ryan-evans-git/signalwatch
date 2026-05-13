# PI Plan

This is the active Program Increment plan. Each sprint is two weeks. Cross-sprint context is in [`ROADMAP.md`](./ROADMAP.md). When work re-prioritizes, **edit this file** rather than letting the plan rot.

## How we work

- **TDD.** Every non-trivial change starts with a failing test. PRs without new or updated tests need an explicit reviewer waiver in the PR body.
- **One sprint goal.** Each sprint has a single headline goal. Side work that doesn't serve the goal goes in the backlog.
- **Definition of done.** Code merged to `main`, all CI gates green, coverage non-regressing, docs touched if behavior changed.
- **Slip rule.** If a sprint goal won't land in two weeks, cut scope before sliding the date. Then update this file with what was deferred and why.
- **Coverage gate is set at 90% from day 1.** CI will be red until coverage reaches 90%. No merges to `main` until then. This is intentional — it forces sprint 1–3 to focus on test coverage rather than letting feature work slip past untested code.

## PI 1 — "Test coverage + public-repo readiness" (≈ 12 weeks, 6 sprints) — **DELIVERED 2026-05-13**

**PI goal:** signalwatch reaches 90% branch coverage with a fully wired CICD pipeline and the foundational documents (LICENSE, SECURITY.md, README, CONTRIBUTING.md) live and ratifiable. By PI end, the repo is public, `main` is branch-protected, and the first merged PR has crossed the 90% gate.

**Outcome:** every PI 1 sprint goal landed; v0.2 feature scope shipped a sprint earlier than `v0.3.0` of the roadmap intended.

| | State at PI 1 close (commit `a8ebcf4`) |
| --- | --- |
| Coverage | 96.3% (1465/1522), gate enforced on every PR |
| CI gates | 15 required status checks |
| Stores | sqlite + postgres + mysql, one conformance suite |
| Stream inputs | kafka + sqs + rabbitmq (+ event/scrape/sqlquery from v0.1) |
| Auth | shared-token bearer + UI login gate |
| Branch protection | signed commits, linear history, no force-push, 15 checks, `enforce_admins: true` |
| Tag | `v0.2.0` deferred to PI 2 sprint 7; everything else in scope landed |

### Sprint 1 — Foundations + CICD scaffolding — **delivered**

**Goal:** every CI gate from the policy is wired up and runs on every PR; LICENSE, SECURITY.md, CONTRIBUTING.md, README are in place and reviewable. Coverage gate is set at 90% but is *expected to fail* — that's the point. No merges to `main` happen this sprint.

Scope:

- [x] [`docs/ROADMAP.md`](./ROADMAP.md), [`docs/PI-PLAN.md`](./PI-PLAN.md).
- [ ] `LICENSE` — Apache-2.0, owner Ryan Evans, year 2026 (already drafted, needs sign-off).
- [ ] `SECURITY.md` — vulnerability-reporting flow, supported versions, list of gates that block merge.
- [ ] `CONTRIBUTING.md` — TDD discipline, branch flow, PR template requirements, local dev setup.
- [ ] `README.md` — quickstart, status banner ("pre-alpha — under active development on a feature branch; no released versions yet"), link to ROADMAP and PI-PLAN.
- [ ] `.github/PULL_REQUEST_TEMPLATE.md`.
- [ ] `.github/workflows/ci.yml` — golangci-lint, `go test`, coverage gate via `vladopajic/go-test-coverage` (threshold 90%), gosec, govulncheck, `google/go-licenses` block-list, trivy fs scan, build sanity.
- [ ] `.github/workflows/codeql.yml` — Go CodeQL with `security-extended`, push + PR + Monday cron.
- [ ] `.github/workflows/gitleaks.yml` — full-history scan on every PR (`fetch-depth: 0`).
- [ ] `.github/dependabot.yml` — gomod + github-actions + npm (for `web/`) ecosystems.
- [ ] `.golangci.yml` — baseline linter set (revive, govet, ineffassign, errcheck, gocyclo, misspell).
- [ ] `.testcoverage.yml` — total threshold 90, package threshold 80, file threshold 70.
- [ ] `docs/BRANCH-PROTECTION.md` — required checks list + a `gh api` script that applies them.
- [ ] First public push of the repo. Branch-protect `main`.
- [ ] Sentinel PR (`feature/sprint-1-foundations`) demonstrates: every required check runs, coverage gate fails red as expected.

Acceptance:

- Every CI workflow is green on its own non-coverage assertions (lint, security, licenses, trivy, codeql, gitleaks).
- Coverage gate is configured at 90% and reports the actual repo number on each PR.
- `main` branch protection is active. The script in `BRANCH-PROTECTION.md` is idempotent.
- LICENSE / SECURITY.md / CONTRIBUTING.md / README all exist and are reviewable.

Out of scope: actually raising coverage to 90%. That's sprints 2–3.

### Sprint 2 — Coverage on `internal/rule`, `internal/eval`, `internal/subscriber` — **delivered**

**Goal:** the engine-core packages reach ≥ 90% branch coverage. Existing source code may be refactored where it's currently untestable, but **only as needed to enable testing** — no new features.

Scope (TDD: write the tests first, refactor production code only if testing exposes a seam that's missing):

- [ ] `internal/rule` — `Rule.Validate` table tests; condition compile + Eval table tests for threshold, window_aggregate, pattern_match (regex + contains + missing field + non-string field), sql_returns_rows; `state.Apply` transitions OK→FIRING, FIRING→OK, FIRING→FIRING, OK→OK, including IncidentID handling and edge cases.
- [ ] `internal/eval` — `WindowBuffers` Observe / prune / per-window helpers (Avg/Sum/Min/Max/Count) with fake clock; `Cache` Set/Delete/Get/ByInput/Scheduled; push and scheduled evaluator loops with channel-driven termination tests.
- [ ] `internal/subscriber` — `Subscription.Validate` cases.
- [ ] Coverage report posted in PR; only justified `// nocover:` annotations.

Acceptance:

- Coverage in `internal/rule`, `internal/eval`, `internal/subscriber` each ≥ 90% branch.
- Repo-wide coverage rises to ≥ 60%.
- `main` still unprotected from merges until sprint 4 (we let sprint 3 land first).

### Sprint 3 — Coverage on stores, channels, inputs — **delivered**

**Goal:** all I/O packages tested. Repo-wide coverage hits 90%.

Scope:

- [ ] `internal/store/sqlite` — repo round-trip tests for every repository method; migration idempotency; concurrent-write semantics.
- [ ] `internal/channel/{smtp,slack,webhook}` — `httptest`-based fake servers (and a tiny embedded SMTP server or an interface mock for SMTP) with success / 4xx / 5xx / timeout / malformed-response paths.
- [ ] `internal/input/{event,scrape,sqlquery}` — fake clocks, fake HTTP servers, registry round-trip.
- [ ] `internal/api` — handler tests via `httptest`, including malformed JSON, missing required fields, unknown fields, 404 paths, and event ingest under load.
- [ ] `internal/ui` — handler tests for index serve, asset serve, SPA fallback, missing-dist fallback, large-file limits.
- [ ] `internal/dispatcher` — close existing gap (currently 70%, target 90%).
- [ ] `engine` — close gap (currently 62%, target 90%).
- [ ] Extract testable inner functions out of `cmd/signalwatch/main.go` so the binary entry-point is the only uncovered code.

Acceptance:

- Repo-wide branch coverage ≥ 90%.
- `.testcoverage.yml` per-package threshold 90 enforced; CI green for the first time.
- Branch protection on `main` is updated to *require* the coverage check to pass.

### Sprint 4 — First merge + Postgres store — **delivered**

**Goal:** sprint-1-and-2-and-3 work consolidates into the first merge to `main`. New work begins: Postgres store as a drop-in `store.Store` implementation.

Scope:

- [ ] Squash + merge the long-running `feature/coverage-ramp` branch into `main`.
- [ ] Cut a `v0.2.0-alpha.1` pre-release tag.
- [ ] Postgres store: migrations, `pgx` driver, conformance suite parameterized over driver.
- [ ] `testcontainers-go` Postgres container in CI, behind `make test-pg` and a separate CI job.

### Sprint 5 — MySQL store + Kafka stream input — **delivered**

Scope:

- [ ] MySQL store driving the same conformance suite.
- [ ] `internal/input/stream` interface + Kafka implementation.
- [ ] Kafka integration test (testcontainers).
- [ ] CI matrix: MySQL + Kafka jobs.

### Sprint 6 — SQS + RabbitMQ + auth, v0.2 release — **delivered (tag deferred)**

Scope:

- [x] `internal/input/stream/sqs` (localstack in tests).
- [x] `internal/input/stream/rabbitmq` (testcontainers).
- [x] Token-based auth middleware on the HTTP API + UI login.
- [x] `CHANGELOG.md`; README screenshots refreshed.
- [ ] Cut `v0.2.0` tag; signed checksums. — **carried to PI 2 sprint 7 per direction.**

---

## PI 2 — "Ecosystem breadth → `v0.3.0`" (≈ 12 weeks, 6 sprints) — **DELIVERED 2026-05-13**

**PI goal:** signalwatch grows from "production-ready on three stores + four stream sources + three notification channels" to a usable v0.3.0 with the expression-language escape hatch live, two more datasources (DuckDB, expr), four more channels (PagerDuty / Teams / Discord / SMS), and per-rule incident drill-down in the UI. PI ends with a signed `v0.3.0` release.

The discipline that landed PI 1 carries forward unchanged: TDD-first, 90% gate enforced, branch-protected `main`, signed commits, linear history. The "How we work" section above is still the operating contract.

**Outcome:** every code-side goal landed. Both `v0.2.0` and `v0.3.0` release tags remain intentionally deferred per user direction.

| | State at PI 2 close (commit `824c6f8`) |
| --- | --- |
| Coverage | 95.9% (1825/1904); gate stable across the 6 sprints |
| CI gates | 16 required status checks (added `test (duckdb integration)`) |
| Rule conditions | 5 total (added `expression`) |
| Datasources | 4 (sqlite, postgres, mysql + opt-in DuckDB) |
| Channels | 7 (added pagerduty, teams, discord, sms) |
| UI | hash-routed drill-down + Validate-button rule form |
| Tags | `v0.2.0` + `v0.3.0` deferred |

### Sprint 7 — `v0.2.0` release + UI dependency refresh — **delivered (tag deferred)**

**Goal:** ship the deferred `v0.2.0` tag, clear the PI-1-deferred Dependabot bumps in one coordinated sweep. No new product features.

Scope:

- [ ] Cut signed `v0.2.0` annotated tag from `main`; publish a GitHub Release with the `CHANGELOG.md` body and SHA-256 checksums for `bin/signalwatch` + `bin/signalwatchctl` (linux-amd64, linux-arm64, darwin-arm64, darwin-amd64).
- [ ] **UI deps coordinated bump.** tailwindcss 3 → 4 (config-schema rewrite), react / react-dom 18 → 19 (hooks + concurrent-mode review), vite 5 → 8 (config + plugin compatibility), typescript 5 → 6 (tighter type-checking; fix or pin latent issues), `@vitejs/plugin-react` 4 → 6. One PR per dep where they don't pair; the vite + plugin-react pair lands together.
- [ ] Re-run `make screenshots` post-bump; commit refreshed PNGs if the visual diff is meaningful.
- [ ] Verify the 15-gate CI stays green throughout.

Acceptance:

- `v0.2.0` tag is signed, attached to a GitHub Release, and visible at `https://github.com/ryan-evans-git/signalwatch/releases`.
- All five deferred Dependabot PRs are closed (merged or superseded by hand-written PRs).
- `CHANGELOG.md`'s `[Unreleased]` section is renamed to `[0.2.0]` with a release date.

### Sprint 8 — Expression-language conditions — **delivered**

**Goal:** ship the `expr` rule condition — the original v0.1 plan's escape hatch from typed conditions, finally delivered.

Scope:

- [ ] New `Expression` condition type in `internal/rule/condition.go`. Delegates to `expr-lang/expr`. Compiles once at rule load. Push- and scheduled-evaluable.
- [ ] Wire the existing `ExprHelpers` interface (`AvgOver`, `SumOver`, `MinOver`, `MaxOver`, `CountOver`, `RecordRegexMatch`) into the expr environment so MPG-over-30-days-style rules work via expr without re-implementing the sugar.
- [ ] Sandbox: expr's `expr.AllowUndefinedVariables(false)` plus a whitelist of builtins. No `os.*`, no I/O.
- [ ] UI: a text-area expression input on the rule form, with inline validation by hitting `/v1/rules/validate` (new endpoint that compiles the expression without persisting).
- [ ] Docs: `docs/RULES.md` gets an Expression section with worked examples (the MPG one, log-pattern, threshold-over-window).
- [ ] 90%+ coverage on the new condition.

Acceptance:

- A rule with `{"type":"expression","spec":{"expr":"avg_over(\"mpg\",\"30d\") < 5"}}` round-trips through the API and evaluates correctly on push and scheduled paths.
- Lint + tests + coverage gate green.

### Sprint 9 — DuckDB datasource — **delivered**

**Goal:** the `sql_returns_rows` rule type accepts DuckDB registered datasources, opening analytical-SQL alerting without needing a separate Postgres.

Scope:

- [ ] DuckDB driver registered via `github.com/marcboeker/go-duckdb` in a new optional package (CGO-aware build constraint so the pure-Go default build stays single-file).
- [ ] `internal/input/sqlquery.Registry.Register` path documented for DuckDB.
- [ ] Integration test: a DuckDB file is created at test setup, populated, and queried via a `sql_returns_rows` rule.
- [ ] New `test (duckdb integration)` CI job. Branch protection updated to require it (16 checks).
- [ ] `docs/RULES.md` updated.

Acceptance:

- DuckDB-backed `sql_returns_rows` rule passes the same evaluator semantics already exercised by the SQLite-backed equivalent.
- DuckDB is opt-in: a build that doesn't enable the duckdb tag still compiles + ships as a static single binary.

### Sprint 10 — Channels A: PagerDuty + MS Teams — **delivered**

**Goal:** add the two most-asked-for incident-management channels, modeled exactly like Slack + webhook (testable, lint-clean, 90%+ coverage).

Scope:

- [ ] `internal/channel/pagerduty` — PagerDuty Events API v2 (trigger + acknowledge + resolve mappings; routing key configured per channel).
- [ ] `internal/channel/teams` — Microsoft Teams incoming-webhook channel using Adaptive Card payloads.
- [ ] httptest fakes for both; unit tests at 95%+.
- [ ] UI: channel-type dropdown gains both options; subscriber/channel form validates required fields per type.
- [ ] `cmd/signalwatch` config schema gains the new channel types.

Acceptance:

- Sending a FIRING / RESOLVED notification through each channel renders the expected wire payload (verified by httptest assertion).

### Sprint 11 — Channels B: Discord + Twilio SMS — **delivered**

**Goal:** add the consumer-facing notification channels. Twilio SMS introduces a paid-API integration so the CI story is different from Discord.

Scope:

- [ ] `internal/channel/discord` — webhook-based, same shape as Slack.
- [ ] `internal/channel/sms` — Twilio Messages API. Account SID + auth token come from env (`SIGNALWATCH_TWILIO_*`); never persisted in config files.
- [ ] httptest fake for Twilio (avoids real billing on every CI run). Document the optional `SW_TWILIO_LIVE_TEST=1` env-var path for maintainers who want to do a real send before tagging.
- [ ] UI: both channels added to the form.

Acceptance:

- Discord channel sends a working webhook payload (httptest-asserted).
- Twilio channel constructs the correct Messages API request (httptest-asserted); the integration test stays offline by default.
- `SECURITY.md` updated with the new Twilio-credentials handling guidance.

### Sprint 12 — UI per-rule drill-down, alert-history exports, `v0.3.0` release — **delivered (tag deferred)**

**Goal:** the headline UX upgrade for v0.3 plus the release.

Scope:

- [ ] New UI route `#/rules/:id` with a timeline view of incidents + notifications for that rule, scrollable across the last N days. Hits two existing endpoints: `GET /v1/incidents?rule_id=...` and `GET /v1/notifications?incident_id=...` (the latter already exists; the former needs the optional filter added).
- [ ] New `GET /v1/incidents/export?since=...&format=csv|json` endpoint. CSV for spreadsheet workflows, JSON for downstream pipelines.
- [ ] Re-run `make screenshots`; add a new `docs/screenshots/rule-detail.png`.
- [ ] CHANGELOG `[Unreleased]` → `[0.3.0]`; release notes; signed tag; GitHub Release.

Acceptance:

- A rule's detail page shows its open incident (if any), the incidents history, and per-incident notification timeline.
- Export endpoint produces stable CSV with the documented column order; round-trips through `csv.Reader` in tests.
- `v0.3.0` tag exists on `main` and is signed; the release page lists checksums.

---

## PI 3 — "Production hardening + cloud scale → `v0.4.0`" (≈ 12 weeks, 6 sprints) — **DELIVERED 2026-05-13**

**Outcome.** signalwatch now runs against MSK and Google Pub/Sub in addition to on-prem Kafka/SQS/RabbitMQ. Resolved incidents prune on a configurable window with optional JSON-file or webhook archival. The engine emits OpenTelemetry traces across every hot path (engine submit → dispatcher tick → channel send) wired up via standard `OTEL_*` env vars. The shared-token auth model is supplemented by DB-stored, scoped, expiring per-user tokens; the legacy shared token continues to work for back-compat. Repo coverage stayed above 95% throughout. Branch protection now requires 17 status checks (added pubsub-integration on top of the PI 2 baseline). `v0.4.0` tagged 2026-05-13.

| Sprint | Theme | Outcome |
| --- | --- | --- |
| 13 | MSK IAM-SASL | `SASL` config on `internal/input/stream/kafka`; AWS default-chain credentials; plain dialing remains default |
| 14 | Google Pub/Sub | `internal/input/stream/pubsub` via `cloud.google.com/go/pubsub` v1.50.2; ADC creds; new `test (pubsub integration)` CI job |
| 15 | Retention + archival | `internal/retention` pruner; JSON-file + webhook sinks; new `IncidentRepo.ListResolvedBefore` / `DeleteResolvedBefore` across all three drivers |
| 16 | OpenTelemetry | `internal/observability`; spans on engine/dispatcher/channels; otelhttp inbound; W3C propagation |
| 17 | Per-user API tokens | `internal/auth` + `api_tokens` table; `admin`/`read` scopes; `POST/GET/DELETE /v1/auth/tokens`; legacy shared token still accepted |
| 18 | `v0.4.0` release prep | Dependabot triage (`modernc.org/sqlite` v1.50.1, otelhttp v0.68.0, gRPC v1.81.0, api v0.279.0); CHANGELOG cut; tag |

**PI goal:** signalwatch becomes operationally credible at scale. Cloud-managed streams (the obvious "I run my alerts off MSK / Pub/Sub" deployments), retention so the store doesn't grow unboundedly, observability so operators can see what the engine is doing, and per-user API tokens replacing the shared-secret model from PI 1. PI ends with a signed `v0.4.0` release.

The operating contract carries forward unchanged: TDD-first, 90% gate enforced, branch-protected `main`, signed commits, linear history, 16 status checks (more added as new integration jobs land).

### Sprint 13 — MSK (AWS-managed Kafka) input — **delivered**

**Goal:** the existing `internal/input/stream/kafka` package gains an AWS-IAM-SASL auth path so it can connect to MSK without a static username/password. Most cloud-Kafka deployments use IAM, so this unblocks the biggest hosted-Kafka case.

Scope:

- [ ] Pull in `github.com/aws/aws-msk-iam-sasl-signer-go/signer` (or equivalent) and wire it into `kafka.Reader` via a TLS-aware `sasl.Mechanism`.
- [ ] Config: extend `kafka` channel config with `sasl: { mechanism: "AWS_MSK_IAM", region: "..." }`. The IAM credentials follow the AWS SDK default chain (env vars / IRSA / shared config). Plain SASL/SCRAM stays available for non-MSK clusters.
- [ ] Integration: testcontainers' Kafka module doesn't fake IAM, so the IAM path is unit-tested with a fake signer (the message-loop already has a `Reader` interface for this). A `SW_MSK_LIVE_TEST=1` opt-in path is documented for maintainers who want to do a real-MSK send before release; this lives outside CI.
- [ ] `docs/RULES.md` and the README config example gain MSK mentions.

Acceptance:

- A configured MSK-style channel signs its SASL/AUTHENTICATE frames against the AWS SDK credential chain. Unit tests fake the signer and assert the kafka-go Reader is constructed with the correct mechanism.
- Existing `test (kafka integration)` job stays green (no regression on plain Kafka).

### Sprint 14 — Google Pub/Sub input — **delivered**

**Goal:** new `internal/input/stream/pubsub` package consuming from GCP Pub/Sub subscriptions. Symmetric with the Kafka / SQS / RabbitMQ shape: per-subscription goroutine, JSON-object decoding, ack on success, dead-letter on bad messages.

Scope:

- [ ] `cloud.google.com/go/pubsub` client wired through a small `Subscriber` interface so unit tests can fake it.
- [ ] Authentication via Application Default Credentials (`GOOGLE_APPLICATION_CREDENTIALS` or workload identity); no service-account JSON in YAML.
- [ ] Integration test via Pub/Sub emulator (testcontainers `gcr.io/google.com/cloudsdktool/google-cloud-cli` with the `gcloud beta emulators pubsub start` mode), tagged `integration`.
- [ ] New `test (pubsub integration)` CI job. Branch protection updated to require it (17 checks).

Acceptance:

- A configured `pubsub` channel polls a real emulator-backed subscription, decodes JSON-object messages, emits `EvaluationRecord`s, and acks. Bad messages are nacked-with-no-redelivery (or dead-lettered via the GCP standard).

### Sprint 15 — Alert-history retention + archival — **delivered**

**Goal:** the store doesn't grow unboundedly. Operators can configure a retention window; the engine periodically prunes resolved-and-aged incidents (and their notifications + sub-states) and optionally streams them to an archive sink for cold storage.

Scope:

- [ ] New `internal/retention` package with a tick-based pruner. Config: `retention: { window: "90d", archive_sink: null|"json"|"webhook" }`.
- [ ] Store-level `DeleteIncidentsResolvedBefore(t time.Time) ([]Incident, error)` added to the conformance suite — all three drivers implement it.
- [ ] Optional archive sinks: JSON file (rotated daily) or HTTP POST to a configured webhook URL. Pluggable so a future S3 sink is trivial.
- [ ] `docs/RETENTION.md` describing the lifecycle + tuning knobs.

Acceptance:

- A rule that fires + resolves + ages past the retention window has its incident + notifications + sub-state removed from the store on the next prune tick.
- Archive sink (when configured) receives the deleted incident payload before the row goes away. Tests fake both sinks via httptest / tempdir.

### Sprint 16 — OpenTelemetry tracing — **delivered**

**Goal:** operators can see what the engine is doing. The hot paths — rule evaluation, channel send, store query — emit OpenTelemetry spans. An optional OTLP exporter ships traces to any OTel-compatible backend (Jaeger, Honeycomb, Tempo, etc.).

Scope:

- [ ] `go.opentelemetry.io/otel` + `go.opentelemetry.io/otel/sdk` deps. Wired through a small `engine.Options.Tracer` field; default is a no-op tracer (no overhead when disabled).
- [ ] Spans: `eval.RuleEval` (per evaluation, attrs: rule_id, input_ref, mode, triggered), `dispatcher.Notify` (per notification, attrs: channel, kind, status), `channel.Send` (per send, attrs: channel name, address-hash). HTTP API + store queries instrumented via existing OpenTelemetry middleware.
- [ ] OTLP exporter wired in `cmd/signalwatch` behind `SIGNALWATCH_OTEL_EXPORTER=otlp` + standard OTel env vars (`OTEL_EXPORTER_OTLP_ENDPOINT`, etc.).
- [ ] `docs/OBSERVABILITY.md` with the trace-attribute catalog + an example Jaeger screenshot.

Acceptance:

- With OTLP enabled, a rule-firing event produces a parent span (eval) with children for dispatch + each notification + each store call. Span attributes include the rule ID + incident ID so an operator can trace a single alert end-to-end.
- Coverage stays ≥ 90% on the new instrumentation code.

### Sprint 17 — Per-user API tokens (replacing shared-token) — **delivered**

**Goal:** the shared-token auth from PI 1 sprint 6 is adequate for v0.2 / v0.3 but not for multi-operator deployments. Replace it with per-user tokens stored in the database, with named scopes (read-only / full) and optional expiry. Sets up for v1.0's full RBAC.

Scope:

- [ ] New `api_tokens` table on all three stores (conformance suite extended). Columns: `id`, `name`, `hash` (bcrypt), `scope`, `created_at`, `expires_at`, `last_used_at`.
- [ ] New CRUD endpoints `GET/POST/DELETE /v1/api-tokens`. Creating a token returns the plaintext exactly once.
- [ ] `internal/api/auth.go`'s middleware switches to comparing against hashed tokens. The legacy `SIGNALWATCH_API_TOKEN` env var stays supported as a bootstrap admin token for first-run; a warning logs to encourage rotating to a DB-stored token.
- [ ] UI: new *API tokens* tab. Create → show plaintext once with a copy button + rotation guidance.
- [ ] `SECURITY.md` updated.

Acceptance:

- A token issued via the API authenticates for `/v1/*` requests. Revoking it via DELETE invalidates it on the next request. Bootstrap-env-var fallback continues to work for first-run + recovery.
- Token plaintext is never logged or persisted to disk.

### Sprint 18 — `v0.4.0` release prep + maintainer slack — **delivered**

**Goal:** cut `v0.4.0` (if user direction permits), refresh deferred Dependabot bumps + minor cleanups, regenerate screenshots that changed during PI 3 (likely OTel + API-tokens UI).

Scope:

- [ ] Triage open Dependabot PRs accumulated during PI 3; coordinated bump where useful.
- [ ] `make screenshots` regenerates the README PNGs; add `api-tokens.png` if the new UI tab is visually distinct.
- [ ] `CHANGELOG.md` rolls Unreleased → `[0.4.0]` with release date.
- [ ] Cut signed `v0.4.0` annotated tag (gated by user direction, like v0.2.0 / v0.3.0). If tags remain deferred, the prep work still lands and the tag waits.
- [ ] Slack space: room to absorb a slipped scope item from sprints 13–17 if one needs it.

Acceptance:

- `main` is publishable: docs current, tests green, gate stable, no half-landed feature in the diff between the last sprint-17 merge and the would-be tag commit.

---

## Backlog (post PI 3)

Items deferred to PI 4+:

- **Azure Service Bus** input — cloud stream parity gap from the original v0.4 roadmap; pushed because two cloud streams (MSK + Pub/Sub) is the high-impact pair for PI 3.
- **AWS EventBridge** input — same rationale as Service Bus.
- **Cloud-store adapters** (Aurora, Cloud SQL, Azure DB) — currently a Postgres connection-string works for all three; native adapters would mostly be auth + IAM glue.
- **Multi-node / leader election** (etcd or DB-row lock). Hard requirement for the v0.5 milestone; PI 4 lands it.
- **Sharded evaluators consuming from shared queues** — the partner workstream for multi-node.
- **Full RBAC + SSO** (OIDC/SAML). Per-user tokens (sprint 17) is the half-step; full RBAC is post-v0.4.
- **Audit log** — every state change persisted to a write-only, queryable timeline.
- **DuckDB write paths / persistence** — currently we register an existing `*sql.DB`; spawning + lifecycle would be its own work.
- **MariaDB compatibility test job** — the MySQL driver works against MariaDB in practice; a separate integration job would prove it.
- **Webhook channel templating** — a small expr-style template language for the webhook body, so operators don't have to write a downstream translator. Low priority.

---

## Updates log

- **2026-05-13 (afternoon)** — PI 2 closed (every sprint's code merged; both `v0.2.0` and `v0.3.0` tags remain deferred per user). PI 3 ("Production hardening + cloud scale → v0.4.0") drafted with six sprints: MSK (sprint 13), Pub/Sub (sprint 14), retention + archival (sprint 15), OpenTelemetry tracing (sprint 16), per-user API tokens (sprint 17), v0.4.0 release prep (sprint 18). The original v0.4 roadmap entry had four cloud streams (MSK, Pub/Sub, Service Bus, EventBridge); pulled two into PI 3 and deferred Service Bus + EventBridge to PI 4 to keep the integration-test surface manageable. Added per-user API tokens to PI 3 as a stepping stone for v1.0's full RBAC. Existing 16 required CI gates expected to grow to 17 (pubsub integration) during PI 3.
- **2026-05-13** — PI 1 closed; PI 2 ("Ecosystem breadth → v0.3.0") drafted. PI 1 outcome: 96.3% coverage, 15 required CI gates, three stores + six inputs + three channels, shared-token auth. The only scope item that slipped is the `v0.2.0` tag, intentionally carried into PI 2 sprint 7 so it bundles with the deferred UI dep refresh. PI 2 absorbs the v0.3 roadmap entries: expression-language conditions, DuckDB datasource, PagerDuty / Teams / Discord / SMS channels, per-rule incident drill-down UI. Multi-node, cloud-managed streams, RBAC/SSO, and per-user API tokens remain in the backlog.
- **2026-05-08 v2** — User redirected: 90% gate from day 1, no merges to main until 90%. Sprint 1 narrowed to foundations + CICD scaffolding only. Sprint 2 + 3 focus entirely on coverage of existing code. v0.2 feature work (Postgres / MySQL / streams) deferred to sprints 4–6.
- **2026-05-08 v1** — PI 1 created. v0.1 is code-complete but not release-ready (33% coverage, no CICD).
