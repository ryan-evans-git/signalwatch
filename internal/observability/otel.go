// Package observability wires the OpenTelemetry SDK from environment
// variables. We follow the OTEL SDK spec's standard env-var contract so
// operators can plug signalwatch into whatever collector their fleet
// already runs (Jaeger, Tempo, Honeycomb, Datadog, etc.) without
// signalwatch-specific knobs.
//
// Setup is opt-in: if OTEL_TRACES_EXPORTER is unset or "none" the global
// tracer provider stays the no-op default and every TracerProvider call in
// the codebase is a cheap nil-handler.
package observability

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// DefaultServiceName is used when OTEL_SERVICE_NAME is unset. Operators
// embedding signalwatch as a library should set OTEL_SERVICE_NAME (or
// override the resource directly) to surface their own service in traces.
const DefaultServiceName = "signalwatch"

// SetupOptions overrides individual SDK behaviors. The zero value reads
// every knob from the environment.
type SetupOptions struct {
	// Version is reported as service.version on the resource. Defaults to
	// the literal "dev" so traces from un-tagged builds are still tagged
	// recognizably.
	Version string
}

// Setup initializes the global OpenTelemetry tracer + propagator according
// to the environment variables described in docs/OBSERVABILITY.md.
//
// The returned shutdown function flushes the exporter and shuts the
// provider down; callers should defer it for the lifetime of the process.
// If tracing is disabled (the default), Setup returns a no-op shutdown.
func Setup(ctx context.Context, opts SetupOptions) (func(context.Context) error, error) {
	exporterName := strings.ToLower(strings.TrimSpace(os.Getenv("OTEL_TRACES_EXPORTER")))
	if exporterName == "" || exporterName == "none" {
		// Set up propagation so any incoming traceparent headers still
		// thread through, even when we aren't exporting.
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		))
		return func(context.Context) error { return nil }, nil
	}

	exporter, err := buildExporter(ctx, exporterName)
	if err != nil {
		return nil, fmt.Errorf("observability: build exporter: %w", err)
	}

	res, err := buildResource(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("observability: build resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return tp.Shutdown, nil
}

func buildExporter(ctx context.Context, name string) (sdktrace.SpanExporter, error) {
	switch name {
	case "otlp":
		// OTEL_EXPORTER_OTLP_PROTOCOL is the SDK-spec env var. We honor
		// both grpc and http/protobuf; default mirrors the spec.
		protocol := strings.ToLower(strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL")))
		if protocol == "" {
			protocol = strings.ToLower(strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL")))
		}
		switch protocol {
		case "", "http/protobuf":
			return otlptracehttp.New(ctx)
		case "grpc":
			return otlptracegrpc.New(ctx)
		default:
			return nil, fmt.Errorf("unsupported OTEL_EXPORTER_OTLP_PROTOCOL %q (want http/protobuf or grpc)", protocol)
		}
	case "stdout":
		return stdouttrace.New(stdouttrace.WithPrettyPrint())
	default:
		return nil, fmt.Errorf("unsupported OTEL_TRACES_EXPORTER %q (want none, otlp, or stdout)", name)
	}
}

func buildResource(ctx context.Context, opts SetupOptions) (*resource.Resource, error) {
	serviceName := strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME"))
	if serviceName == "" {
		serviceName = DefaultServiceName
	}
	version := strings.TrimSpace(opts.Version)
	if version == "" {
		version = "dev"
	}
	res, err := resource.New(ctx,
		resource.WithFromEnv(), // honors OTEL_RESOURCE_ATTRIBUTES
		resource.WithTelemetrySDK(),
		resource.WithProcess(),
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(version),
		),
	)
	// resource.New surfaces "partial detector" errors via errors.Is on
	// resource.ErrPartialResource; non-fatal — keep the partial resource
	// and move on.
	if err != nil && !errors.Is(err, resource.ErrPartialResource) {
		return nil, err
	}
	return res, nil
}
