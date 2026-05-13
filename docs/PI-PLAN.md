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

## PI 2 — "Ecosystem breadth → `v0.3.0`" (≈ 12 weeks, 6 sprints)

**PI goal:** signalwatch grows from "production-ready on three stores + four stream sources + three notification channels" to a usable v0.3.0 with the expression-language escape hatch live, two more datasources (DuckDB, expr), four more channels (PagerDuty / Teams / Discord / SMS), and per-rule incident drill-down in the UI. PI ends with a signed `v0.3.0` release.

The discipline that landed PI 1 carries forward unchanged: TDD-first, 90% gate enforced, branch-protected `main`, signed commits, linear history. The "How we work" section above is still the operating contract.

### Sprint 7 — `v0.2.0` release + UI dependency refresh

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

### Sprint 8 — Expression-language conditions

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

### Sprint 9 — DuckDB datasource

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

### Sprint 10 — Channels A: PagerDuty + MS Teams

**Goal:** add the two most-asked-for incident-management channels, modeled exactly like Slack + webhook (testable, lint-clean, 90%+ coverage).

Scope:

- [ ] `internal/channel/pagerduty` — PagerDuty Events API v2 (trigger + acknowledge + resolve mappings; routing key configured per channel).
- [ ] `internal/channel/teams` — Microsoft Teams incoming-webhook channel using Adaptive Card payloads.
- [ ] httptest fakes for both; unit tests at 95%+.
- [ ] UI: channel-type dropdown gains both options; subscriber/channel form validates required fields per type.
- [ ] `cmd/signalwatch` config schema gains the new channel types.

Acceptance:

- Sending a FIRING / RESOLVED notification through each channel renders the expected wire payload (verified by httptest assertion).

### Sprint 11 — Channels B: Discord + Twilio SMS

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

### Sprint 12 — UI per-rule drill-down, alert-history exports, `v0.3.0` release

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

## Backlog (post PI 2)

Items deferred to PI 3+:

- Multi-node / leader election (etcd or DB-row lock).
- Cloud-managed stream variants (MSK, Pub/Sub, Service Bus, EventBridge).
- Full RBAC + SSO + audit log.
- Per-user API tokens with named scopes + expiry (the shared-token shape currently shipping is intentionally minimal for v0.2/v0.3).
- Alert history retention / archival (`docs/RETENTION.md`).
- OpenTelemetry traces on rule evaluation + dispatch.

---

## Updates log

- **2026-05-13** — PI 1 closed; PI 2 ("Ecosystem breadth → v0.3.0") drafted. PI 1 outcome: 96.3% coverage, 15 required CI gates, three stores + six inputs + three channels, shared-token auth. The only scope item that slipped is the `v0.2.0` tag, intentionally carried into PI 2 sprint 7 so it bundles with the deferred UI dep refresh. PI 2 absorbs the v0.3 roadmap entries: expression-language conditions, DuckDB datasource, PagerDuty / Teams / Discord / SMS channels, per-rule incident drill-down UI. Multi-node, cloud-managed streams, RBAC/SSO, and per-user API tokens remain in the backlog.
- **2026-05-08 v2** — User redirected: 90% gate from day 1, no merges to main until 90%. Sprint 1 narrowed to foundations + CICD scaffolding only. Sprint 2 + 3 focus entirely on coverage of existing code. v0.2 feature work (Postgres / MySQL / streams) deferred to sprints 4–6.
- **2026-05-08 v1** — PI 1 created. v0.1 is code-complete but not release-ready (33% coverage, no CICD).
