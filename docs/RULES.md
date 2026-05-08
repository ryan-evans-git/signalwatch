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

## Choosing push vs scheduled

| Want to detect... | Use... |
|---|---|
| Per-event conditions (this single record looks bad) | `threshold` or `pattern_match` |
| Trends over time (avg over a window crossed a line) | `window_aggregate` |
| Operational-data conditions (a row exists that shouldn't) | `sql_returns_rows` |
| Queue/log message containing a specific phrase | `pattern_match` on a stream-input record |

## Subscriptions and delivery

Rules say *what* triggered; subscriptions say *who hears about it and when*. See [SUBSCRIPTIONS.md](./SUBSCRIPTIONS.md).
