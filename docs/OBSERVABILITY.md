# Observability — OpenTelemetry tracing

signalwatch emits OpenTelemetry traces across the engine, dispatcher, HTTP server, and every channel send. Tracing is **off by default**: when `OTEL_TRACES_EXPORTER` is unset or `none`, the SDK stays on its no-op tracer and span creation is essentially free.

## Enabling

We honor the [OTel SDK environment-variable spec](https://opentelemetry.io/docs/specs/otel/configuration/sdk-environment-variables/) verbatim — no signalwatch-specific knobs — so the same env vars you already set for the rest of your stack work here.

| Variable | Values | Default |
| --- | --- | --- |
| `OTEL_TRACES_EXPORTER` | `none` / `otlp` / `stdout` | `none` (no exporter) |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `http/protobuf` / `grpc` | `http/protobuf` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | URL of your collector | `http://localhost:4318` (http) / `localhost:4317` (grpc) |
| `OTEL_SERVICE_NAME` | service name in traces | `signalwatch` |
| `OTEL_RESOURCE_ATTRIBUTES` | `key=val,key=val` extra resource attrs | unset |

Quick start with an OTLP collector on localhost:

```bash
OTEL_TRACES_EXPORTER=otlp \
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 \
OTEL_SERVICE_NAME=signalwatch-prod \
./bin/signalwatch --config /etc/signalwatch/config.yaml
```

Local debugging — print spans to stdout:

```bash
OTEL_TRACES_EXPORTER=stdout ./bin/signalwatch --config dev.yaml
```

## Spans emitted

Every span uses the instrumentation library name `github.com/ryan-evans-git/signalwatch`.

| Span name | Where | Attributes |
| --- | --- | --- |
| `<METHOD> <path>` | every `/v1/*` and UI request | standard `http.*` attributes from `otelhttp` |
| `signalwatch.engine.submit` | `engine.Submit` (library `Submit` + HTTP `POST /v1/events`) | `signalwatch.input.ref` |
| `signalwatch.dispatcher.tick` | every rule-evaluator dispatcher tick | `signalwatch.rule.id`, `signalwatch.rule.name`, `signalwatch.severity`, `signalwatch.rule.triggered` |
| `signalwatch.dispatcher.deliver` | per-subscription delivery (parent of channel sends) | `signalwatch.rule.id`, `signalwatch.incident.id`, `signalwatch.subscription.id`, `signalwatch.notification.kind`, `signalwatch.deliver.sent_ok`, `signalwatch.deliver.sent_err`, `signalwatch.deliver.skipped` |
| `signalwatch.channel.send` | every channel.Channel.Send (smtp / slack / webhook / pagerduty / teams / discord / sms) | `signalwatch.channel.name`, `signalwatch.incident.id`, `signalwatch.rule.id`, `signalwatch.rule.name`, `signalwatch.notification.kind`, `signalwatch.severity` |

Span status is set to `Error` (with `RecordError`) on any failure — channel-send failures, dispatcher errors, engine-submit errors. The `signalwatch.dispatcher.deliver` span also marks `Error` whenever `sent_err > 0`, so a dashboard filtered on span status surfaces partial delivery.

## Trace propagation

W3C `traceparent` + `tracestate` headers are honored on inbound HTTP requests (via `otelhttp`). When tracing is disabled (no exporter), the propagator is still installed, so any client trace context is preserved through context — useful for downstream services that DO export.

Outbound HTTP requests from channel implementations (Slack webhook, generic webhook, PagerDuty, etc.) do not yet inject `traceparent`. That's tracked as a v0.5 enhancement; the per-channel spans are still emitted so you can correlate by `signalwatch.incident.id`.

## Local development with Jaeger

```bash
docker run -d --name jaeger \
  -p 16686:16686 -p 4317:4317 -p 4318:4318 \
  jaegertracing/all-in-one:latest

OTEL_TRACES_EXPORTER=otlp \
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 \
./bin/signalwatch --config dev.yaml
```

Then open `http://localhost:16686` and look for the `signalwatch` service.

## What's NOT in scope (for v0.4)

- **Metrics + logs.** This is a tracing-only release. The OTel meter and log SDKs aren't wired up.
- **Outbound trace-context injection** on channel HTTP clients (Slack/webhook/PagerDuty/etc.). Channels emit local spans but downstream services see fresh root spans, not children.
- **OTel SDK auto-configured TLS or auth headers** beyond what the SDK reads from `OTEL_EXPORTER_OTLP_HEADERS` etc. — that's the SDK's contract, we don't add to it.
- **Library-embedding override.** If you embed `engine` directly, call `observability.Setup` yourself from your host program; the service binary handles that itself.
