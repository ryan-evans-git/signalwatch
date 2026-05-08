# Subscribers and subscriptions

A `Subscriber` is a person or system that wants to be notified. They have a name and a list of `ChannelBinding`s — each one names a configured channel (e.g. `ops-email`) and an address (e.g. `oncall@example.com`).

A `Subscription` connects a subscriber to one or more rules with delivery preferences:

| Field | Meaning |
|---|---|
| `subscriber_id` | which subscriber |
| `rule_id` *or* `label_selector` | which rule(s); exactly one must be set |
| `dwell_seconds` | the rule must be continuously firing for this long before the subscriber hears anything (default 0) |
| `repeat_interval_seconds` | renotify at this cadence while the rule is still firing (default 0 = never) |
| `notify_on_resolve` | whether to send a resolution notification when the rule clears (default true) |
| `channel_filter` | optional subset of the subscriber's channels to use for this subscription |

## Why these live on the subscription, not the rule

Different humans want different things from the same alert:

- The on-call engineer wants to be paged after 30 seconds of failure with a 5-minute renotify cadence.
- The product manager wants a daily summary email.
- The director only wants to know if it's been broken for an hour.

Putting dwell/repeat/resolve on the subscription means the rule itself stays a clean predicate. Three subscriptions, three preferences, one rule.

## Concrete examples

**Page me fast, renotify every 5 min:**

```json
{
  "subscriber_id": "<eng-oncall>",
  "rule_id": "<rule>",
  "dwell_seconds": 30,
  "repeat_interval_seconds": 300,
  "notify_on_resolve": true
}
```

**Only tell me about persistent issues:**

```json
{
  "subscriber_id": "<director>",
  "rule_id": "<rule>",
  "dwell_seconds": 3600,
  "repeat_interval_seconds": 0,
  "notify_on_resolve": false
}
```

**Catch everything via labels:**

```json
{
  "subscriber_id": "<sre-team>",
  "label_selector": { "team": "platform", "env": "prod" },
  "dwell_seconds": 0,
  "repeat_interval_seconds": 600,
  "notify_on_resolve": true
}
```

## Notification kinds

Each notification carries a `kind`:

- `firing` — the first notification within an incident.
- `repeat` — a renotify because the incident is still open and `repeat_interval` elapsed.
- `resolved` — sent when the rule transitions back to OK, but only if a `firing` was already sent for this subscription. (You don't want a resolution notice for a transient blip you never heard about.)
