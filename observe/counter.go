package observe

import (
	"context"
	"log/slog"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Counter returns an EventSink that emits one OTel metric counter per
// observed event, namespaced by the event's Name(). For an event with
// Name() == "goal.ended", the emitted counter is "agentcore.goal.ended".
//
// Each increment carries two standard attributes derived from the event:
//
//	level=info|warn|error   (from Event.Level())
//	errored=true|false      (true iff Event.Err() != nil)
//
// That lets operators graph rates and failure ratios per event type
// without configuring per-event rules:
//
//	rate(agentcore_goal_ended_total{errored="false"}[5m])
//	sum by (event) (rate(agentcore_*_total{errored="true"}[5m]))
//
// Counter caches one Int64Counter instrument per event name (sync.Map).
// Instruments that fail to register (e.g. invalid name) are skipped
// silently — observability must not break the producer.
//
// New event types in any agentcore package automatically get their own
// counter the first time they fire — no code changes here.
func Counter(meter metric.Meter) EventSink {
	if meter == nil {
		// A nil meter is degenerate but should not panic; return a sink
		// that does nothing.
		return tee(nil)
	}
	return &counter{meter: meter}
}

type counter struct {
	meter      metric.Meter
	instruments sync.Map // name → metric.Int64Counter
}

func (c *counter) Fire(ctx context.Context, event any) {
	e, ok := event.(Event)
	if !ok {
		return
	}
	inst := c.instrumentFor(e.Name())
	if inst == nil {
		return
	}
	inst.Add(ctx, 1, metric.WithAttributes(
		attribute.String("level", levelString(e)),
		attribute.Bool("errored", e.Err() != nil),
	))
}

// instrumentFor returns (and lazily creates) the Int64Counter for an
// event name. The metric name is "agentcore." + event-name, so a workflow
// event "goal.ended" becomes "agentcore.goal.ended" — namespaced under
// the project so it doesn't collide with consumer metrics.
func (c *counter) instrumentFor(eventName string) metric.Int64Counter {
	if v, ok := c.instruments.Load(eventName); ok {
		return v.(metric.Int64Counter)
	}
	name := "agentcore." + eventName
	inst, err := c.meter.Int64Counter(name)
	if err != nil {
		// Cache nothing on failure — a future call will retry. Failures
		// are silent; an SDK must not panic in observability code.
		return nil
	}
	actual, _ := c.instruments.LoadOrStore(eventName, inst)
	return actual.(metric.Int64Counter)
}

// levelString maps slog.Level to one of "debug" / "info" / "warn" /
// "error" — coarse enough to stay low-cardinality in metric labels.
// Anything outside slog's four named levels is reported as "error" so
// out-of-band values surface loudly rather than silently miscategorise.
func levelString(e Event) string {
	switch e.Level() {
	case slog.LevelDebug:
		return "debug"
	case slog.LevelInfo:
		return "info"
	case slog.LevelWarn:
		return "warn"
	case slog.LevelError:
		return "error"
	default:
		return "error"
	}
}
