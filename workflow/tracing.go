package workflow

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	otrace "go.opentelemetry.io/otel/trace"
)

// tracerName is the OTel scope used for every span produced by this
// package. Consumers don't need to pass a tracer in — they configure their
// global TracerProvider; without one, OTel's no-op default discards every
// span.
const tracerName = "github.com/vinayprograms/agentcore/workflow"

// trace starts a span scoped to this package and returns a derived ctx
// plus a cleanup function. Callers wire it as:
//
//	ctx, end := trace(ctx, "goal.execute", attribute.String("goal", g.name))
//	defer end(&err)
//
// On end, if *errp is non-nil the span records the error and sets
// status=Error; otherwise it ends cleanly. The returned ctx carries the
// new span, so any further calls (LLM via agentkit, tool calls, child
// span starts) become children automatically.
func trace(ctx context.Context, op string, attrs ...attribute.KeyValue) (context.Context, func(*error)) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, op,
		otrace.WithAttributes(attrs...),
	)
	return ctx, func(errp *error) {
		if errp != nil && *errp != nil {
			span.RecordError(*errp)
			span.SetStatus(codes.Error, (*errp).Error())
		}
		span.End()
	}
}
