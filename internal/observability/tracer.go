package observability

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

// TracerName is the instrumentation library name on every span signalwatch
// emits. Keeping this constant lets consumers filter signalwatch spans
// uniformly regardless of which subsystem produced them.
const TracerName = "github.com/ryan-evans-git/signalwatch"

// Tracer returns the package-scoped Tracer. When OTel isn't configured,
// the returned tracer is the global no-op.
func Tracer() trace.Tracer { return otel.Tracer(TracerName) }
