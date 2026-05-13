# Rule reference

Every rule has the shape:

```json
{
  "id": "uuid (auto-assigned if omitted)",
  "name": "descriptive name",
  "description": "free-form, shown in notifications",
  "enabled": true,
  "severity": "info | warning | critical",
  "labels": { "team": "ops", "env": "prod" },
  "input_ref": "events",
  "condition": { "type": "...", "spec": { ... } },
  "schedule_seconds": 30
}
```

`condition` is a discriminated union with four built-in types in v0.1. `schedule_seconds` only applies to scheduled-mode conditions (`window_aggregate`, `sql_returns_rows`).

## threshold

Triggers when a numeric field on an event satisfies a comparison against a constant. Push-evaluated.

```json
{ "type": "threshold", "spec": { "field": "total", "op": ">", "value": 1000 } }
```

Operators: `>`, `>=`, `<`, `<=`, `==`, `!=`.

## window_aggregate

Aggregates a numeric field over a rolling window and compares the result. Scheduled-evaluated; the engine maintains the rolling buffer in memory.

```json
{
  "type": "window_aggregate",
  "spec": {
    "field": "mpg",
    "agg": "avg",
    "window": 2592000000000000,
    "op": "<",
    "value": 5
  }
}
```

`window` is a Go `time.Duration` in nanoseconds. `2_592_000_000_000_000` = 30 days. `agg` ∈ `avg | sum | min | max | count`. Set `schedule_seconds` to control how often the aggregate is re-evaluated (typically the same as your sampling cadence or coarser).

## pattern_match

Triggers when a string field matches a substring or regex. Push-evaluated.

```json
{
  "type": "pattern_match",
  "spec": { "field": "level", "kind": "contains", "pattern": "ERROR" }
}
```

`kind` ∈ `contains | regex`.

## sql_returns_rows

Triggers when a SQL query against a registered datasource returns at least `min_rows` rows. Scheduled-evaluated.

```json
{
  "type": "sql_returns_rows",
  "spec": {
    "data_source": "default",
    "query": "SELECT 1 FROM orders WHERE status = 'stuck' AND age > 600",
    "min_rows": 1
  }
}
```

The `default` datasource is the engine's own state SQLite DB. Register your own datasources programmatically when constructing the engine via `engine.Options.SQLDatasources`.

**DuckDB.** signalwatch ships an opt-in DuckDB driver for analytical workloads. DuckDB is CGO-only, so the default `signalwatch` binary stays pure-Go and doesn't link it — to use DuckDB, rebuild with `-tags=duckdb` and `CGO_ENABLED=1` and register an opened `*sql.DB` against the engine's `SQLDatasources` registry:

```go
import (
    "github.com/ryan-evans-git/signalwatch/internal/datasource/duckdb"
    "github.com/ryan-evans-git/signalwatch/internal/input/sqlquery"
)

db, err := duckdb.Open("analytics.duckdb")   // "" for in-memory
if err != nil { return err }

reg := sqlquery.NewRegistry()
reg.Register("analytics", db)

engine.New(engine.Options{
    // ...
    SQLDatasources: reg,
})
```

A rule with `{"type":"sql_returns_rows","spec":{"data_source":"analytics", ...}}` then runs against that DuckDB. Callers can probe `duckdb.Enabled` to decide whether to attempt the open — the stub build returns `duckdb.ErrDisabled` from `Open()`.

### `expression`

The escape hatch for everything the four typed conditions don't cover. Evaluates an [expr-lang](https://github.com/expr-lang/expr) program against either the inbound record (push mode) or the rule's helper environment (scheduled mode). Triggers when the program returns `true`.

```json
{
  "type": "expression",
  "spec": {
    "expr": "record.level == \"ERROR\" && regex_match(\"host\", \"^web-\")",
    "mode": "push"
  }
}
```

Scheduled flavor — the same `30d avg MPG` rule expressed as expr:

```json
{
  "type": "expression",
  "spec": {
    "expr": "avg_over(\"mpg\", \"30d\") < 5",
    "mode": "scheduled"
  },
  "schedule_seconds": 3600
}
```

**Available bindings**

- `record` — only meaningful in push mode. A map keyed by the inbound record's field names; access fields as `record.level` or `record["level"]`.
- `avg_over(field, window)`, `sum_over(field, window)`, `min_over(field, window)`, `max_over(field, window)`, `count_over(field, window)` — window-aware aggregations over the rule's input. Available in scheduled mode (and in push mode if the engine is tracking a record buffer for that input). Return `(float64, bool)` — the bool is `false` when there's no data; comparison expressions against a literal will simply not trigger.
- `regex_match(field, pattern)` — in scheduled mode runs against the helper's record buffer; in push mode (or without helpers) runs against the current `record`. Returns `bool` and may error on malformed patterns.
- All of expr's [built-in functions](https://expr-lang.org/docs/language-definition) — `len`, `lower`, `upper`, `contains`, `startsWith`, `endsWith`, `matches`, ternary, set, array, map, etc.

**Window durations** accept anything `time.ParseDuration` accepts (e.g. `"5m"`, `"1h30m"`, `"1.5s"`) plus `"Xd"` (days) and `"Xw"` (weeks). Suffixes can't be mixed: `"30d"` is valid; for `"30d12h"` use `"732h"`.

**Mode and scheduling**

- `"push"` (default) runs on every record from the rule's `input_ref`. Omit or set `schedule_seconds` to `0`.
- `"scheduled"` runs on the rule's interval (`schedule_seconds` must be > 0).

**Validation** — the API exposes `POST /v1/rules/validate` which compiles the candidate rule and returns 400 with the error message if the expression fails to compile. The bundled UI's rule form has a *Validate* button that calls it.

**Sandbox** — expr programs run in a sealed env: no access to `os.*`, no filesystem, no network. Only the bindings listed above are reachable.

## Choosing push vs scheduled

| Want to detect... | Use... |
|---|---|
| Per-event conditions (this single record looks bad) | `threshold` or `pattern_match` |
| Trends over time (avg over a window crossed a line) | `window_aggregate` |
| Operational-data conditions (a row exists that shouldn't) | `sql_returns_rows` |
| Queue/log message containing a specific phrase | `pattern_match` on a stream-input record |
| Anything composing record fields with helpers or expr's built-ins | `expression` |

## Subscriptions and delivery

Rules say *what* triggered; subscriptions say *who hears about it and when*. See [SUBSCRIPTIONS.md](./SUBSCRIPTIONS.md).
