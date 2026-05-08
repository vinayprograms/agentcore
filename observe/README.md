# observe

Default `EventSink` implementations for agentcore packages — typed-event observers for logs, metrics, and custom side-effects. Each implementation is a standalone sink; combine what you need with `Tee`.

| Sink | Purpose | Dispatch |
|---|---|---|
| `observe.Logger(*slog.Logger)` | Structured logs, one slog record per event | synchronous |
| `observe.Counter(metric.Meter)` | One OTel counter per event name, tagged by level + errored | synchronous |
| `observe.NewHandlers(observe.Limits{})` | Typed-callback registry — register with `observe.On[E]` | **async, bounded queue, drop-on-overflow** |
| `observe.Tee(...)` | Fans an event out to several sinks in declared order | passthrough |

**Tracing is not in this package.** agentcore packages emit OTel spans directly via `otel.Tracer(...)` at their internal boundaries (see `workflow/tracing.go`). `observe` is the channel for typed domain events; OTel is the channel for spans. The two are complementary: spans give you the structural timeline of a run; events give you the lifecycle moments worth logging or counting.

## Usage

`observe` works against any value that satisfies `observe.Event`. The producer of the events doesn't matter — `observe` doesn't import any agentcore producer package. Define an event type, fire it through a sink, and observe.

```go
import (
    "context"
    "log/slog"

    "go.opentelemetry.io/otel"

    "github.com/vinayprograms/agentcore/observe"
)

// 1. Define an event type that satisfies observe.Event.
type RequestCompleted struct {
    RequestID string
    Failure   error
}

func (e RequestCompleted) Name() string { return "request.completed" }
func (e RequestCompleted) Level() slog.Level {
    if e.Failure != nil {
        return slog.LevelError
    }
    return slog.LevelInfo
}
func (e RequestCompleted) Attrs() []slog.Attr {
    return []slog.Attr{slog.String("request_id", e.RequestID)}
}
func (e RequestCompleted) Err() error { return e.Failure }

// 2. Build the sink. Each piece is independent; combine with Tee.
handlers := observe.NewHandlers(observe.Limits{})
defer handlers.Close()

observe.On(handlers, func(ctx context.Context, e RequestCompleted) {
    if e.Err() != nil {
        // custom side effect: metric, alert, downstream call, etc.
    }
})

sink := observe.Tee(
    observe.Logger(slog.Default()),       // structured logs
    observe.Counter(otel.Meter("svc")),   // OTel metric counters
    handlers,                             // typed callbacks
)

// 3. Fire events.
sink.Fire(context.Background(), RequestCompleted{RequestID: "r-42"})
```

Any producer that defines its events to satisfy this interface plugs into the same sinks. See the [Producers](#producers) section below for what's wired today.

## Producers

Any package that fires events through an `observe.EventSink` is a producer. The producer defines its event types (each implementing `observe.Event`) and exposes a sink-shaped field on its config. The same `observe` sinks consume them all without code changes.

The `workflow` package is one such producer. It fires `WorkflowStarted`, `GoalEnded`, etc. through `Runtime.Telemetry`:

```go
import (
    "github.com/vinayprograms/agentcore/observe"
    "github.com/vinayprograms/agentcore/workflow"
)

rt := &workflow.Runtime{
    Model:     model,
    Telemetry: observe.Tee(
        observe.Logger(slog.Default()),
        observe.Counter(otel.Meter("agent")),
    ),
}
```

## Counter

Every event becomes an OTel int64 counter under `agentcore.<event-name>`, tagged with `level` and `errored`:

```
agentcore.workflow.started{level=info,errored=false}
agentcore.goal.ended{level=info,errored=false}
agentcore.subagent.completed{level=error,errored=true}
agentcore.preflight.failed{level=error,errored=true}
...
```

That's enough to graph rates, failure ratios per event, and error budgets without wiring per-event rules:

```promql
rate(agentcore_goal_ended_total{errored="false"}[5m])
sum by (event) (rate(agentcore_*_total{errored="true"}[5m]))
```

A new event type added in any agentcore package automatically gets its own counter the first time it fires.

## drop-on-overflow design

A slow handler (network call, disk write, etc.) must not block the producer. `Handlers` enqueues onto a bounded channel (default capacity 1024); when the queue is full, `Fire` increments `Dropped()` and returns immediately.

Size the queue at construction via `observe.Limits`:

```go
// Size the queue to absorb up to 4096 in-flight events.
handlers := observe.NewHandlers(observe.Limits{QueueSize: 4096})
defer handlers.Close()

// Periodically check for drops — emit a metric, log a warning, or fail
// the build if the count is non-zero.
if dropped := handlers.Dropped(); dropped > 0 {
    slog.Warn("observe handlers dropped events", "count", dropped)
}
```

Pass `observe.Limits{}` (zero value) to use the default queue capacity of 1024. `Limits` is the home for any future per-handler resource cap (timeout, overflow action) — they'd be added as fields on the same struct, so consumer code never breaks when we grow the surface.

`Logger` and `Counter` are synchronous because slog and OTel batch downstream — the extra goroutine layer would only add latency and lose ordering guarantees.

## How dispatch works

`Logger` and `Counter` program against the `observe.Event` interface, not against specific event types. Every event from any agentcore package implements `Event` (`Name`, `Level`, `Attrs`, `Err`). Both sinks pull the data out of those methods, so a new event type added to a producer package flows through them automatically — no edits to this package required.

## Lifecycle

`Handlers.Close()` signals the dispatcher to stop, drains anything queued at the time, waits for in-flight handlers to return, then exits. Always pair `NewHandlers()` with a deferred `Close()` so events queued at the end of a run don't get dropped on process exit.

`Logger` and `Counter` are stateless beyond their counter cache — no `Close` needed.

## Local-only / CLI setup

`observe.Logger` works without any OTel infrastructure — it writes to whatever `*slog.Logger` you give it:

```go
slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

rt := &workflow.Runtime{
    Model:     model,
    Telemetry: observe.Logger(slog.Default()),
}
```

If you also want OTel spans (workflow's own per-step spans, agentkit's per-Chat spans) captured locally, install the stdout exporter — no collector required:

```go
import (
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
    "go.opentelemetry.io/otel/sdk/trace"
)

exp, _ := stdouttrace.New(stdouttrace.WithWriter(os.Stderr))
otel.SetTracerProvider(trace.NewTracerProvider(trace.WithBatcher(exp)))
```

After this, every span anywhere in the process — including agentcore's workflow spans and agentkit's LLM-call spans — emits to stderr as JSON. No extra wiring through `observe`.

## Panic safety

A handler that panics does not kill the dispatcher. Panics are recovered silently; the next handler for the same event still runs. The package deliberately doesn't pollute logs from arbitrary user code — if you need to know about handler panics, install your own recover-and-log inside the handler:

```go
observe.On(handlers, func(ctx context.Context, e workflow.GoalEnded) {
    defer func() {
        if r := recover(); r != nil {
            slog.Error("handler panic", "event", e.Name(), "panic", r)
        }
    }()
    riskyHandler(ctx, e)
})
```
