# Roadmap

This roadmap is the source of truth for what gets built when. The active sprint lives in [`PI-PLAN.md`](./PI-PLAN.md). When you're picking up work, start there.

## Versioning intent

| Version | Theme | Status |
|---|---|---|
| **v0.1** | MVP — engine, dispatcher, SQLite, three channels, three inputs, HTTP API, embedded UI, CLI | code-complete reference state; never tagged |
| **v0.2** | Production-ready single-node — CICD gates green, ≥90% coverage, Postgres + MySQL stores, Kafka/SQS/RabbitMQ stream inputs, token auth | **scope landed on `main` (2026-05-13)**; tag deferred |
| **v0.3** | Ecosystem breadth — expression-language conditions, DuckDB datasource, PagerDuty/Teams/Discord/SMS channels, per-rule incident drill-down UI | **scope landed on `main` (2026-05-13)**; tag deferred |
| **v0.4** | Production hardening + cloud scale — MSK + Pub/Sub stream inputs, alert-history retention + archival, OpenTelemetry tracing, per-user API tokens | active in PI 3 |
| **v0.5** | Multi-node + remaining cloud streams — leader election, sharded evaluators, HA store; Service Bus + EventBridge inputs | |
| **v1.0** | Stable surfaces — engine API, HTTP API, on-disk schema all 1.0; full RBAC + SSO + audit log | |

## v0.1 (delivered, retroactively scoped down)

Already in `main`:

- Rule conditions: `threshold`, `window_aggregate`, `pattern_match`, `sql_returns_rows`.
- Rule state machine + dispatcher with dwell, dedup, repeat, notify-on-resolve.
- SQLite store (pure Go).
- Channels: SMTP, Slack incoming webhook, generic webhook.
- Inputs: HTTP event push + library `Submit`, scheduled SQL query, JSON metric scrape.
- Full CRUD HTTP API for rules, subscribers, subscriptions; read endpoints for incidents, notifications, live state.
- React UI (Vite + Tailwind) embedded via `go:embed`.
- `signalwatchctl` client CLI.

**Why v0.1 is not yet released:** coverage is 33%, no CICD, main is unprotected, no public repo. v0.2 is the first version that lands publicly; v0.1's code stays in the same module and is hardened during v0.2 work.

## v0.2 — production-ready single-node

**Acceptance criteria for the v0.2 tag:**

- ≥ 90% branch coverage on every package; ratchet enforced in CI.
- All CICD gates green and required for merge: lint, tests, coverage, gosec, govulncheck, license check, trivy, codeql, gitleaks.
- Postgres and MySQL stores implement `store.Store` with the same migrations test surface as SQLite.
- Stream inputs: Kafka, SQS, RabbitMQ.
- Token-based auth on the HTTP API + UI.
- `SECURITY.md` published; vulnerability-reporting flow live.
- Public GitHub repo with branch-protected `main`, signed commits, linear history.

## v0.3 — ecosystem breadth

- DuckDB as a query datasource for `sql_returns_rows`.
- Channels: PagerDuty, MS Teams, Discord, Twilio SMS.
- Expression-language conditions (escape hatch via `expr-lang/expr`).
- Per-rule incident drill-down view in the UI; alert history exports.

## v0.4 — production hardening + cloud scale

Active in [PI 3](./PI-PLAN.md). Cloud-stream-input coverage narrowed to the two highest-impact platforms; the other two move to v0.5.

- MSK (AWS-managed Kafka, IAM-SASL auth on the existing `internal/input/stream/kafka` package).
- Google Pub/Sub (new `internal/input/stream/pubsub` package).
- Alert-history retention + archival (configurable window, optional archive sinks).
- OpenTelemetry tracing on the rule-eval / dispatch / channel-send / store-query hot paths.
- Per-user API tokens (named scopes, optional expiry) replacing the v0.2/v0.3 shared-token model.

## v0.5 — multi-node + remaining cloud streams

- Leader election (etcd or DB-row lock) so the scheduler is single-writer.
- Horizontal evaluator workers consuming from shared queues.
- HA store guidance (Postgres replication topology, etc.).
- Azure Service Bus + AWS EventBridge stream inputs (deferred from v0.4 to keep PI 3 scoped).
- Cloud-store adapters as appropriate (Aurora, Cloud SQL, Azure DB).

## v1.0 — stable surfaces

- `engine` package, HTTP API, and on-disk schema all 1.0 with documented compatibility guarantees.
- Full RBAC, SSO (OIDC/SAML), audit log.
- Public release with semantic-versioning + deprecation policy.
