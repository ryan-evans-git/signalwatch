# Alert-history retention

signalwatch can periodically prune resolved incidents (and cascade their notifications + per-subscription state) so the store doesn't grow unboundedly. An optional **archive sink** receives each deleted incident before the row goes away, so cold storage keeps a copy for compliance or downstream pipelines.

Retention is **off by default**. Add the `retention:` block to your config to opt in.

## YAML

```yaml
retention:
  window: 90d           # keep resolved incidents for 90 days (accepts Xd, Xw, or any time.Duration)
  interval: 1h          # how often the pruner ticks; defaults to 1h
  archive:              # optional — omit to delete without archiving
    type: json          # "json" | "webhook"
    dir: /var/lib/signalwatch/archive   # required when type=json
    # url: https://archive.internal/alerts   # required when type=webhook
```

The pruner runs **once at startup** and then on the configured interval. Setting `window: 0s` (or omitting `window`) disables it.

## Lifecycle

1. **Tick.** Every `interval` (or on startup), the pruner computes `cutoff = now - window`.
2. **List.** It selects every incident where `resolved_at` is non-null and strictly before `cutoff`.
3. **Archive.** If an archiver is configured, each candidate's payload (`incident` + the full list of `notifications` for that incident) is handed to the sink.
4. **Delete.** All listed incidents are deleted from the store. The delete cascades to the `notifications` and `incident_sub_states` rows that reference them.

Steps 3 and 4 happen in that order, but archive failures **don't block the delete** — they log a warning and move on. The rationale is that retention enforcement is the headline guarantee; archive is best-effort secondary. Operators who need hard archival can wrap the configured archiver themselves and panic on persistent failures.

## Archive sinks

### `json`

Appends one JSON-encoded line per archived incident to `incidents-YYYY-MM-DD.jsonl` under the configured directory. New files roll daily (UTC). Files use `O_APPEND` so multiple processes won't collide on the same line.

Wire format per line:

```json
{
  "incident": { "id": "...", "rule_id": "...", "triggered_at": "...", "resolved_at": "...", "last_value": "..." },
  "notifications": [
    { "id": "...", "channel": "...", "address": "...", "kind": "firing", "sent_at": "...", "status": "ok" }
  ],
  "archived_at": "2026-05-13T12:00:00Z"
}
```

### `webhook`

POSTs the same JSON envelope as `application/json` to the configured URL. 2xx is success; non-2xx (and transport errors) return an error so the pruner logs and retries on the next tick. Default timeout is 10 seconds.

## Tuning

| Knob | Effect |
| --- | --- |
| `window` smaller | Less storage; less history available via the UI / `/v1/incidents/export`. |
| `interval` smaller | More frequent pruning; smaller per-tick batches but more DB churn. |
| `archive.type: json` | Cheapest archive; review file rotation if disk pressure matters. |
| `archive.type: webhook` | Streams to your pipeline; runs as a synchronous HTTP call per incident at archive time. |

## What's NOT in scope (for v0.4)

- **Per-rule retention overrides.** Today `window` is global. Per-rule policies (e.g. "keep critical-severity incidents for 1y, info for 30d") are a v0.5+ feature.
- **Live-state retention.** Live state rows are tied to rules — they live until the rule is deleted. The pruner only touches incidents + their children.
- **Configurable cascade.** The cascade is built into the delete itself (no policy knob); if you need different semantics, swap the store implementation.
- **S3 / GCS / Azure Blob archive sinks.** Plug them into `retention.Archiver` directly when embedding; the bundled binary ships with `json` and `webhook`.

## Testing locally

```bash
SIGNALWATCH_API_TOKEN=$(openssl rand -base64 32) \
  ./bin/signalwatch --config retention-test.yaml &

# trigger + resolve some events, then watch the logs:
#   {"msg":"retention.pruned","deleted":3,"cutoff":"..."}
```

For unit / integration coverage see `internal/retention/retention_test.go` and the conformance suite's `Incidents/ListResolvedBefore_*` + `Incidents/DeleteResolvedBefore_*` cases in `internal/store/storetest/storetest.go`.
