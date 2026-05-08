# Architecture

signalwatch is split into a few concerns. Each one is a small package with a small interface, and the engine wires them together.

```
                       ┌─────────────┐
inputs (push or pull)  │   Engine    │  channels (smtp/slack/webhook)
─────────────────────► │             │ ─────────────────────────────►
                       │  evaluator  │
                       │     │       │
                       │     ▼       │
                       │ dispatcher  │
                       │     │       │
                       │     ▼       │
                       │   store     │   (sqlite by default)
                       └─────────────┘
```

## Packages

| Package | Role |
|---|---|
| `engine` | Public library API. `engine.New + Start + Submit + Close`. The only stable surface for embedding. |
| `internal/rule` | Rule struct, typed conditions (`threshold`, `window_aggregate`, `pattern_match`, `sql_returns_rows`), and the OK↔FIRING state machine in [state.go](../internal/rule/state.go). |
| `internal/subscriber` | Subscriber, Subscription, Incident, Notification, and per-incident sub-state types. |
| `internal/store` | Store interface plus per-driver implementations. The default lives at `internal/store/sqlite`. |
| `internal/eval` | Push and scheduled evaluators plus the rolling-window buffer that backs `window_aggregate`. |
| `internal/dispatcher` | Applies dwell/dedup/repeat/notify-on-resolve and routes to channels. |
| `internal/channel` | Channel interface plus `smtp`, `slack`, `webhook` implementations. |
| `internal/input` | Input interface plus `event` (in-process push), `scrape` (HTTP JSON pull), `sqlquery` (named DB registry). |
| `internal/api` | HTTP handlers; mounted onto a `*http.ServeMux` in the service binary. |
| `internal/ui` | `embed.FS` wrapping the built React app. Falls back to a stub if dist is empty. |
| `cmd/signalwatch` | The standalone service binary that wires the above from a YAML config. |
| `cmd/signalwatchctl` | A small client CLI for the HTTP API. |

## Rule state machine

Each rule has a single piece of authoritative state in `live_states`: `state ∈ {ok, firing}` plus `triggered_at`. The dispatcher computes per-(incident × subscription) decisions on top:

```
on every Tick(rule, triggered, value):
  apply transition (ok→firing opens an incident; firing→ok closes one)
  for each subscription matching this rule:
    if rule is firing:
      triggered_for = now - rule.triggered_at
      if triggered_for < subscription.dwell:                  do nothing
      if first notification this cycle:                       SEND (firing)
      elif now - last_notified >= subscription.repeat:        SEND (repeat)
    if rule just resolved:
      if any firing notification was already sent for this subscription
         and notify_on_resolve:                               SEND (resolved)
```

## Why these separations

- **Engine vs service.** The engine knows nothing about HTTP; the service binary is a ~200 line wrapper that adds the HTTP API and the bundled UI. Anyone embedding the engine in their own Go app gets the same evaluation guarantees without running a server.
- **Dispatcher owns delivery decisions.** Dwell, dedup, and repeat are properties of how a *human* wants to be notified — not of the rule. Putting them on subscriptions lets two humans subscribe to the same rule with different urgency, without the rule needing to know.
- **Pluggable channels and inputs are tiny interfaces.** Adding a new channel (PagerDuty, Teams, SMS) is one file implementing two methods. Same for inputs.
- **SQLite by default, pure Go.** The binary ships as a single file with no CGO. Postgres / MySQL / DuckDB are slated for v0.2 and slot in via the same `store.Store` interface.
