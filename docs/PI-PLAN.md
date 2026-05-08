# PI Plan

This is the active Program Increment plan. Each sprint is two weeks. Cross-sprint context is in [`ROADMAP.md`](./ROADMAP.md). When work re-prioritizes, **edit this file** rather than letting the plan rot.

## How we work

- **TDD.** Every non-trivial change starts with a failing test. PRs without new or updated tests need an explicit reviewer waiver in the PR body.
- **One sprint goal.** Each sprint has a single headline goal. Side work that doesn't serve the goal goes in the backlog.
- **Definition of done.** Code merged to `main`, all CI gates green, coverage non-regressing, docs touched if behavior changed.
- **Slip rule.** If a sprint goal won't land in two weeks, cut scope before sliding the date. Then update this file with what was deferred and why.
- **Coverage gate is set at 90% from day 1.** CI will be red until coverage reaches 90%. No merges to `main` until then. This is intentional — it forces sprint 1–3 to focus on test coverage rather than letting feature work slip past untested code.

## PI 1 — "Test coverage + public-repo readiness" (≈ 12 weeks, 6 sprints)

**PI goal:** signalwatch reaches 90% branch coverage with a fully wired CICD pipeline and the foundational documents (LICENSE, SECURITY.md, README, CONTRIBUTING.md) live and ratifiable. By PI end, the repo is public, `main` is branch-protected, and the first merged PR has crossed the 90% gate.

PI 1 is **deliberately not** a feature PI. v0.2 features (Postgres, MySQL, streams, auth) are pulled into PI 2.

### Sprint 1 — Foundations + CICD scaffolding

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

### Sprint 2 — Coverage on `internal/rule`, `internal/eval`, `internal/subscriber`

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

### Sprint 3 — Coverage on stores, channels, inputs

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

### Sprint 4 — First merge + Postgres store

**Goal:** sprint-1-and-2-and-3 work consolidates into the first merge to `main`. New work begins: Postgres store as a drop-in `store.Store` implementation.

Scope:

- [ ] Squash + merge the long-running `feature/coverage-ramp` branch into `main`.
- [ ] Cut a `v0.2.0-alpha.1` pre-release tag.
- [ ] Postgres store: migrations, `pgx` driver, conformance suite parameterized over driver.
- [ ] `testcontainers-go` Postgres container in CI, behind `make test-pg` and a separate CI job.

### Sprint 5 — MySQL store + Kafka stream input

Scope:

- [ ] MySQL store driving the same conformance suite.
- [ ] `internal/input/stream` interface + Kafka implementation.
- [ ] Kafka integration test (testcontainers).
- [ ] CI matrix: MySQL + Kafka jobs.

### Sprint 6 — SQS + RabbitMQ + auth, v0.2 release

Scope:

- [ ] `internal/input/stream/sqs` (localstack in tests).
- [ ] `internal/input/stream/rabbitmq` (testcontainers).
- [ ] Token-based auth middleware on the HTTP API + UI login.
- [ ] `CHANGELOG.md`; README screenshots refreshed.
- [ ] Cut `v0.2.0` tag; signed checksums.

---

## Backlog (post PI 1)

Items deferred to PI 2+:

- DuckDB datasource for `sql_returns_rows`.
- PagerDuty / Teams / Discord / SMS channels.
- Expression-language conditions (`expr-lang/expr`).
- Multi-node / leader election.
- Cloud-managed stream variants (MSK, Pub/Sub, Service Bus, EventBridge).
- Full RBAC + SSO + audit log.

---

## Updates log

- **2026-05-08 v2** — User redirected: 90% gate from day 1, no merges to main until 90%. Sprint 1 narrowed to foundations + CICD scaffolding only. Sprint 2 + 3 focus entirely on coverage of existing code. v0.2 feature work (Postgres / MySQL / streams) deferred to sprints 4–6.
- **2026-05-08 v1** — PI 1 created. v0.1 is code-complete but not release-ready (33% coverage, no CICD).
