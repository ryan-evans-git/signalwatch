# Changelog

All notable changes to signalwatch are recorded here. Format adheres to [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Project versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html) once `v0.2.0` ships; before that, `main` is the only supported reference.

## [Unreleased]

### Added (PI 2)

- **Expression-language conditions.** New `expression` rule condition type via [expr-lang/expr](https://github.com/expr-lang/expr). Push- or scheduled-evaluable; sandboxed env (no `os.*`, no I/O); record field access in push mode plus the same window helpers as `window_aggregate` (`avg_over`, `sum_over`, `min_over`, `max_over`, `count_over`, `regex_match`); flexible duration parsing (`"30d"`, `"2w"` plus everything `time.ParseDuration` accepts). New `POST /v1/rules/validate` endpoint compiles a candidate rule without persisting so the UI can surface compile errors before submit; bundled UI's rule form gains a condition-type selector + textarea + Validate button.
- **UI deps refresh.** TypeScript 5 → 6, Vite 5 → 8, @vitejs/plugin-react 4 → 6, react / react-dom 18 → 19, tailwindcss 3 → 4 (CSS-first config; dropped tailwind.config.js + postcss.config.js + autoprefixer + postcss; added @tailwindcss/vite).
- **PI 2 plan.** Six sprints to `v0.3.0`: this expression work, then DuckDB datasource, PagerDuty/Teams channels, Discord/Twilio SMS channels, per-rule incident drill-down UI + alert-history export, release.

### Added

- **Postgres store** — `internal/store/postgres` via `jackc/pgx/v5`. Schema mirrors sqlite with `BIGINT` for ms-resolution timestamps. Drop-in replacement for sqlite via the `Store` interface.
- **MySQL store** — `internal/store/mysql` via `go-sql-driver/mysql`. Schema mirrors sqlite/postgres with MySQL-specific adjustments (`condition` backtick-quoted, `VARCHAR(255)` IDs, `ON DUPLICATE KEY UPDATE`, inline `KEY` clauses, no partial-predicate indexes). `Open()` injects `multiStatements=true` for the multi-statement migration.
- **Cross-driver store conformance suite** — `internal/store/storetest.RunConformance(t, factory)` runs the full Rules/Subscribers/Subscriptions/Incidents/Notifications/LiveStates/IncidentSubStates assertion matrix against any `store.Store`. Used by sqlite/postgres/mysql; new backends should adopt it.
- **Kafka streaming input** — `internal/input/stream/kafka` via `segmentio/kafka-go`. Per-topic consumer goroutines; JSON-object messages become `rule.Record`s; bad messages logged + dropped; transient errors tolerated.
- **SQS streaming input** — `internal/input/stream/sqs` via `aws-sdk-go-v2`. Long-polling per queue; bad messages left in queue for SQS redrive; successful deliveries `DeleteMessage`d; receive-error backoff.
- **RabbitMQ streaming input** — `internal/input/stream/rabbitmq` via `amqp091-go`. Per-queue consumer goroutines over a shared AMQP connection; bad messages `Reject(requeue=false)` for DLQ routing; successful deliveries ack'd.
- **Shared-token HTTP API auth** — `internal/api.WithAPIToken(token)` enables `Authorization: Bearer <token>` on every `/v1/*` route, with `crypto/subtle` constant-time comparison. Configured via the `SIGNALWATCH_API_TOKEN` env var. Empty value preserves the v0.1 open-by-default behavior. `/healthz` and `/v1/auth-status` stay open.
- **UI login gate** — full-screen sign-in card that renders when the server requires auth and no token is stored. Token is persisted to `localStorage`; 401 responses clear it and re-render the gate.
- **Integration test job per backend** — testcontainers-go drives `test (postgres integration)`, `test (mysql integration)`, `test (kafka integration)`, `test (sqs integration)`, `test (rabbitmq integration)`. All five are required-status-check gates in branch protection.
- **Documentation:** `CHANGELOG.md` (this file); README refresh with current status table + UI screenshots in `docs/screenshots/`.

### Changed

- **golangci-lint upgraded to v2** — `.golangci.yml` migrated to v2 schema via `golangci-lint migrate`; action pinned to v9.2.0 (`1e7e51e771db61008b38414a730f564565cf7c20`).
- **Go floor raised to 1.25.0** — `pgx/v5` and `testcontainers-go` both declare `go 1.25.0`; `go.mod` follows suit. CI matrix dropped `go1.24.x` since the auto-downloaded 1.25 toolchain lacks `covdata` and breaks `go test -cover`.
- **Coverage gate now uses `-count=1`** — `setup-go`'s restored test cache produced cross-PR coverage drift; explicit cache bypass keeps every gate run honest.
- **Branch protection** now requires 15 status checks (was 10 at v0.1). Added: matrix on 1.25 only (was 1.24 + 1.25), postgres / mysql / kafka / sqs / rabbitmq integration jobs.

### Internal

- **Coverage policy hit 90% per package** for the first time on 2026-05-12 via the sprint-2 + sprint-3 ramp (PR #13). Headline numbers as of `e039747` on 2026-05-13: 96.3% repo, every package above the 90% gate.
- Repo-wide `gofmt` sweep + comprehensive table-driven tests across `internal/{rule,subscriber,eval,channel/*,input/*,store/*,api,ui,dispatcher,engine}`.
- Small production refactors driven by testability: `internal/channel/smtp.Config.TLSConfig` for custom CAs/client certs; `internal/ui.handlerFromFS` extracted from `Handler()` to enable in-memory SPA tests.
- Dispatcher gained a fault-injection wrapper for tests covering its rarely-hit error-return branches.

## [0.1.0] — pre-release (2026-05-08)

The v0.1 reference state, captured for posterity. Never tagged; never released. Code-complete MVP: engine, dispatcher with dwell/dedup/repeat, sqlite store, three channels (smtp/slack/webhook), three inputs (event/sql/scrape), HTTP API, embedded React UI, CLI. Coverage: 33% statement, no CICD gates. Branch protection: none.

The next tag (`v0.2.0`) will be the first published release; until then `main` is the only supported reference.
