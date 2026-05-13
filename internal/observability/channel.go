package observability

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/ryan-evans-git/signalwatch/internal/channel"
)

// TracedChannel returns a channel.Channel that wraps inner with a
// "signalwatch.channel.send" span around every Send call. The wrapper is
// transparent — its Name() forwards to inner — so callers can swap a
// traced channel in for an untraced one with no other code changes.
func TracedChannel(inner channel.Channel) channel.Channel {
	if inner == nil {
		return nil
	}
	return &tracedChannel{inner: inner}
}

type tracedChannel struct {
	inner channel.Channel
}

func (t *tracedChannel) Name() string { return t.inner.Name() }

func (t *tracedChannel) Send(ctx context.Context, n channel.Notification) error {
	ctx, span := Tracer().Start(ctx, "signalwatch.channel.send",
		trace.WithAttributes(
			attribute.String("signalwatch.channel.name", t.inner.Name()),
			attribute.String("signalwatch.incident.id", n.IncidentID),
			attribute.String("signalwatch.rule.id", n.RuleID),
			attribute.String("signalwatch.rule.name", n.RuleName),
			attribute.String("signalwatch.notification.kind", n.Kind),
			attribute.String("signalwatch.severity", n.Severity),
		),
	)
	defer span.End()

	err := t.inner.Send(ctx, n)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}

// TraceChannels wraps every channel in m and returns a fresh map with the
// same keys. The input map is not modified.
func TraceChannels(m map[string]channel.Channel) map[string]channel.Channel {
	if len(m) == 0 {
		return m
	}
	out := make(map[string]channel.Channel, len(m))
	for k, v := range m {
		out[k] = TracedChannel(v)
	}
	return out
}
