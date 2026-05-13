package observability_test

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/ryan-evans-git/signalwatch/internal/channel"
	"github.com/ryan-evans-git/signalwatch/internal/observability"
)

// withInMemoryTracer swaps the global tracer for an in-memory recorder and
// returns the exporter + a restore func. Run synchronous-export so tests
// can assert immediately after a span ends.
func withInMemoryTracer(t *testing.T) (*tracetest.InMemoryExporter, func()) {
	t.Helper()
	prev := otel.GetTracerProvider()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	otel.SetTracerProvider(tp)
	return exp, func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
	}
}

func TestSetup_NoExporter_IsNoop(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "")
	shutdown, err := observability.Setup(context.Background(), observability.SetupOptions{})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if shutdown == nil {
		t.Fatal("expected non-nil shutdown")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestSetup_NoneExplicit(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	shutdown, err := observability.Setup(context.Background(), observability.SetupOptions{})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestSetup_StdoutExporter(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "stdout")
	t.Setenv("OTEL_SERVICE_NAME", "test-svc")
	shutdown, err := observability.Setup(context.Background(), observability.SetupOptions{Version: "1.2.3"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()
	// emit one span so the exporter actually flushes
	_, span := observability.Tracer().Start(context.Background(), "test.span")
	span.End()
}

func TestSetup_OTLP_HTTPDefault(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "")
	shutdown, err := observability.Setup(context.Background(), observability.SetupOptions{})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestSetup_OTLP_HTTPExplicit(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf")
	shutdown, err := observability.Setup(context.Background(), observability.SetupOptions{})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	_ = shutdown(context.Background())
}

func TestSetup_OTLP_GRPC(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")
	shutdown, err := observability.Setup(context.Background(), observability.SetupOptions{})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	_ = shutdown(context.Background())
}

func TestSetup_OTLP_TracesProtocolFallback(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "grpc")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")
	shutdown, err := observability.Setup(context.Background(), observability.SetupOptions{})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	_ = shutdown(context.Background())
}

func TestSetup_OTLP_BadProtocol(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "bogus")
	_, err := observability.Setup(context.Background(), observability.SetupOptions{})
	if err == nil {
		t.Fatal("expected error for bogus protocol")
	}
}

func TestSetup_UnknownExporter(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "kafka") // not a thing in our setup
	_, err := observability.Setup(context.Background(), observability.SetupOptions{})
	if err == nil {
		t.Fatal("expected error for unknown exporter")
	}
}

// fakeChannel is a minimal channel.Channel implementation used to assert
// the TracedChannel wrapper around Send.
type fakeChannel struct {
	name    string
	calls   int
	err     error
	gotName string
}

func (f *fakeChannel) Name() string { return f.name }
func (f *fakeChannel) Send(_ context.Context, n channel.Notification) error {
	f.calls++
	f.gotName = n.RuleName
	return f.err
}

func TestTracedChannel_RecordsSpan(t *testing.T) {
	exp, restore := withInMemoryTracer(t)
	defer restore()

	inner := &fakeChannel{name: "fake"}
	tc := observability.TracedChannel(inner)
	if tc.Name() != "fake" {
		t.Fatalf("Name() = %q, want fake", tc.Name())
	}
	err := tc.Send(context.Background(), channel.Notification{
		RuleName: "cpu-high",
		RuleID:   "r-1",
		Kind:     "firing",
		Severity: "warn",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("inner not called: %d", inner.calls)
	}
	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d", len(spans))
	}
	if spans[0].Name != "signalwatch.channel.send" {
		t.Fatalf("bad span name: %q", spans[0].Name)
	}
	if spans[0].Status.Code == codes.Error {
		t.Fatalf("unexpected error status: %+v", spans[0].Status)
	}
	want := map[attribute.Key]string{
		"signalwatch.channel.name":      "fake",
		"signalwatch.rule.id":           "r-1",
		"signalwatch.rule.name":         "cpu-high",
		"signalwatch.notification.kind": "firing",
		"signalwatch.severity":          "warn",
	}
	got := map[attribute.Key]string{}
	for _, kv := range spans[0].Attributes {
		got[kv.Key] = kv.Value.AsString()
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("attr %s: got %q want %q", k, got[k], v)
		}
	}
}

func TestTracedChannel_RecordsError(t *testing.T) {
	exp, restore := withInMemoryTracer(t)
	defer restore()

	sentinel := errors.New("boom")
	inner := &fakeChannel{name: "fake", err: sentinel}
	tc := observability.TracedChannel(inner)
	if err := tc.Send(context.Background(), channel.Notification{}); !errors.Is(err, sentinel) {
		t.Fatalf("Send err = %v", err)
	}
	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d", len(spans))
	}
	if spans[0].Status.Code != codes.Error {
		t.Fatalf("want error status, got %v", spans[0].Status)
	}
	if len(spans[0].Events) == 0 {
		t.Fatal("want at least one event for the recorded error")
	}
}

func TestTracedChannel_NilInner(t *testing.T) {
	if observability.TracedChannel(nil) != nil {
		t.Fatal("TracedChannel(nil) should return nil")
	}
}

func TestTraceChannels(t *testing.T) {
	in := map[string]channel.Channel{
		"a": &fakeChannel{name: "a"},
		"b": &fakeChannel{name: "b"},
	}
	out := observability.TraceChannels(in)
	if len(out) != 2 {
		t.Fatalf("len = %d", len(out))
	}
	for k, v := range out {
		if v.Name() != k {
			t.Errorf("key %s: Name()=%s", k, v.Name())
		}
	}
}

func TestTraceChannels_Empty(t *testing.T) {
	if got := observability.TraceChannels(nil); len(got) != 0 {
		t.Fatalf("expected nil/empty, got %v", got)
	}
	empty := map[string]channel.Channel{}
	if got := observability.TraceChannels(empty); len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}

func TestTracer_Returns_NonNil(t *testing.T) {
	if observability.Tracer() == nil {
		t.Fatal("Tracer() returned nil")
	}
}
