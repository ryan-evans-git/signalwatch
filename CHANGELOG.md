# Changelog

All notable changes to signalwatch are recorded here. Format adheres to [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Project versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html). The first published release is `v0.4.0` (2026-05-13); v0.2 / v0.3 scope landed on `main` but rolled into the v0.4 tag.

## [Unreleased]

### Added

- **Canonical OpenAPI 3.1 spec with MCP-ready serving.** New `docs/openapi.yaml` (symlinked to the embed source `internal/api/openapi.yaml`) describes every `/v1/*` operation with explicit `operationId`s, summaries, tags, request/response schemas, and security definitions. The running server publishes the spec at `GET /openapi.yaml` and `GET /openapi.json` — both unauthenticated, like `/healthz`, so MCP adapters and codegen tools can discover the schema without a credential. Internal route table refactor (`gatedRoutes()` / `authRoutes()` / `handlerFor()` in `internal/api/api.go`) provides the single source of truth for both `Mount` and the new `internal/api/openapi_test.go` drift checks: validates the document with `kin-openapi`, asserts unique `operationId`s + per-op summary/tags, and confirms the set of mounted routes exactly matches the set of documented operations. New `docs/MCP.md` walks through wiring an `openapi-mcp-server` style bridge with a scoped per-user token.
- **One-shot subscriptions.** New `one_shot` bool on `Subscription` (defaults to false). When true, the dispatcher delivers exactly one notification across the subscription's lifetime — no renotifies, no resolve ping, no refire on a new incident. Backed by a new `NotificationRepo.ExistsForSubscription` index-backed lookup added on all three drivers (migration `0003`). UI exposes it as a checkbox on the subscription form ("One-time notification") and surfaces the mode as a pill on the list. API uses the `one_shot` JSON field on the existing `subscriptionPayload`.

## [0.4.0] — 2026-05-13

First published release. Closes out Program Increment 3 ("Production hardening + cloud scale"). Repo is signed-commit-only on `main`, 17 required status checks, ≥90% coverage gate.

### Added (PI 3)

- **Per-user API tokens.** New `internal/auth` package + `api_tokens` table (sqlite/postgres/mysql migrations) backing DB-stored, scoped, expiring bearer tokens. Two coarse scopes (`admin`, `read`); read-scope callers get 403 on mutating verbs. New endpoints `POST /v1/auth/tokens` (issue; raw secret returned exactly once), `GET /v1/auth/tokens` (list metadata only — no secrets / hashes leaked), `DELETE /v1/auth/tokens/{id}` (revoke). Tokens stored as `sha256(raw)` so a DB dump can't be replayed. `last_used_at` updated in a 2s detached-context goroutine so an unresponsive DB can't slow auth. Legacy `SIGNALWATCH_API_TOKEN` shared-token path remains intact for back-compat (treated as admin scope). New `APITokenRepo` conformance tests across all three drivers. `SECURITY.md` documents the issuance / rotation / scope model.
- **OpenTelemetry tracing.** New `internal/observability` package wires the OTel SDK from the standard `OTEL_TRACES_EXPORTER` / `OTEL_EXPORTER_OTLP_*` / `OTEL_SERVICE_NAME` env vars; default is `none` (no exporter, no overhead). Supported exporters: `otlp` (http/protobuf or grpc) and `stdout` (debug). Spans emitted: `signalwatch.engine.submit` (library + HTTP event submit), `signalwatch.dispatcher.tick` (every evaluator tick), `signalwatch.dispatcher.deliver` (per-subscription delivery, parent of channel sends, with `sent_ok` / `sent_err` / `skipped` counters), `signalwatch.channel.send` (one per channel.Send, across all seven channel impls via a transparent wrapper). HTTP server wrapped with `otelhttp` for inbound spans + W3C trace-context propagation. New `docs/OBSERVABILITY.md` covers env-var config, span catalog, and Jaeger setup.
- **Alert-history retention + archival.** New `internal/retention` package periodically deletes resolved incidents (and cascades their notifications + incident_sub_states) older than a configured window. Optional archive sinks (`json` rotating-file or `webhook` POST) capture each deleted incident before the row goes away. New conformance methods `IncidentRepo.ListResolvedBefore` + `IncidentRepo.DeleteResolvedBefore` implemented on sqlite/postgres/mysql. Config under `retention:` in `cmd/signalwatch` YAML; defaults to off. `docs/RETENTION.md` walks the lifecycle, sink formats, and tuning knobs.
- **Google Cloud Pub/Sub stream input.** New `internal/input/stream/pubsub` package consuming from one or more Pub/Sub subscriptions via `cloud.google.com/go/pubsub` v1.50.2. Credentials follow Application Default Credentials (`GOOGLE_APPLICATION_CREDENTIALS`, workload identity, or GCE/GKE/Cloud Run metadata) — no service-account JSON in YAML. JSON-object payloads are Ack'd; non-JSON / non-object / empty payloads are Nack'd so Pub/Sub redelivers (or routes to a DLQ topic if one is bound). New `test (pubsub integration)` CI job uses the `gcloud-cli:emulators` image via testcontainers-go; branch protection now requires 17 checks.
- **AWS MSK (managed Kafka) auth.** The existing `internal/input/stream/kafka` package gains a `SASL` config option implementing AWS' `AWS_MSK_IAM` mechanism via `aws-msk-iam-sasl-signer-go`. When configured, the input dials brokers with TLS + an IAM-signed presigned-URL token; AWS credentials follow the SDK default chain (env / IRSA / shared config). Plain (no-SASL) dialing remains the default for on-prem clusters and the testcontainers integration job.

### Changed (PI 3)

- **Dependency bumps.** `modernc.org/sqlite` v1.36.1 → v1.50.1, `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp` v0.62.0 → v0.68.0, `google.golang.org/api` v0.274.0 → v0.279.0, `google.golang.org/grpc` v1.80.0 → v1.81.0 (Dependabot group). All minor/patch — no API churn for signalwatch consumers.

### Added (PI 2)

- **Per-rule incident drill-down (UI).** New hash route `#/rules/:id` reachable via a *view* link on each rule row. Renders the rule's full incident history with per-incident notification timelines (channel · address · timestamp). Top-right *Export incidents CSV* shortcut links to the new export endpoint. UI also fixes a display bug where unresolved incidents could render with Go's zero-time timestamp.
- **Alert-history export.** New `GET /v1/incidents/export` endpoint. Supports `?format=csv|json` (defaults to JSON), `?rule_id=...` to narrow to one rule, `?since=...` accepting either RFC3339 or a Go duration (`168h` = last week). CSV ships with the proper `Content-Type` and `Content-Disposition` so the bundled UI can hand it to a download.
- **`GET /v1/incidents?rule_id=...`** — optional filter on the incidents list, used by the drill-down UI.
- **Discord channel.** New `internal/channel/discord` package posting an embed-style JSON payload to a Discord webhook URL. Color-coded by (severity, kind); embed fields include rule / severity / value / incident; optional timestamp.
- **Twilio SMS channel.** New `internal/channel/sms` package using Twilio's Programmable Messaging API. Credentials sourced from `SIGNALWATCH_TWILIO_ACCOUNT_SID` / `SIGNALWATCH_TWILIO_AUTH_TOKEN` env vars — never persisted in config files. `from_number` (Twilio-provisioned sender) goes in YAML; subscriber binding `address` carries the destination phone number. Body composed as `[SEVERITY/KIND] rule — value — description — inc:id` and byte-capped at 480 (3 SMS segments). `SECURITY.md` now documents the env-var handling and rotation guidance.
- **PagerDuty channel.** New `internal/channel/pagerduty` package implementing the PagerDuty Events API v2. Notification kinds map to event actions (`firing`/`repeat` → `trigger`, `resolved` → `resolve`); `dedup_key` derives from the incident ID so a firing/resolve pair always collapses to one PD incident regardless of how many repeats happened in between. Config: channel-level `routing_key` with per-subscriber override via the binding's `address`.
- **MS Teams channel.** New `internal/channel/teams` package posting an Adaptive `MessageCard` envelope to an incoming-webhook URL. Color-coded by severity / kind; facts list shows rule, kind, severity, value, incident, triggered-at.
- **DuckDB datasource (opt-in).** New `internal/datasource/duckdb` package wraps [go-duckdb/v2](https://github.com/marcboeker/go-duckdb) behind a `duckdb` build tag. Default builds stay pure-Go single-binary and get a stub that returns `duckdb.ErrDisabled` from `Open()`; CGO-enabled builds with `-tags=duckdb` get a working `*sql.DB` that can be registered as a `sqlquery` datasource for `sql_returns_rows` rules. New `test (duckdb integration)` CI job covers the real driver path. `make test-duckdb` for local invocation.
- **Expression-language conditions.** New `expression` rule condition type via [expr-lang/expr](https://github.com/expr-lang/expr). Push- or scheduled-evaluable; sandboxed env (no `os.*`, no I/O); record field access in push mode plus the same window helpers as `window_aggregate` (`avg_over`, `sum_over`, `min_over`, `max_over`, `count_over`, `regex_match`); flexible duration parsing (`"30d"`, `"2w"` plus everything `time.ParseDuration` accepts). New `POST /v1/rules/validate` endpoint compiles a candidate rule without persisting so the UI can surface compile errors before submit; bundled UI's rule form gains a condition-type selector + textarea + Validate button.
- **UI deps refresh.** TypeScript 5 → 6, Vite 5 → 8, @vitejs/plugin-react 4 → 6, react / react-dom 18 → 19, tailwindcss 3 → 4 (CSS-first config; dropped tailwind.config.js + postcss.config.js + autoprefixer + postcss; added @tailwindcss/vite).
- **PI 2 plan.** Six sprints to `v0.3.0`: UI deps refresh + expression conditions + DuckDB datasource (this PR), then PagerDuty/Teams channels, Discord/Twilio SMS channels, per-rule incident drill-down UI + alert-history export, release.

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
