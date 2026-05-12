# agentcore

Opinionated agent components for Go. A library of reusable parts every real
agent ends up needing — layered on top of
[`agentkit`](https://github.com/vinayprograms/agentkit) (single-agent
primitives) and [`swarmkit`](https://github.com/vinayprograms/swarmkit)
(multi-agent NATS coordination).

> **Status: early development.** APIs are not yet stable. Not ready for
> production use.

## What it is

`agentcore` is the opinionated middle tier:

- `agentkit` gives you the **pieces** — LLM clients, tools, memory, MCP,
  shell/content guards, policy, credentials, shutdown coordination.
- `swarmkit` gives you the **radio** — messaging, task dispatch,
  heartbeats, agent registry.
- `agentcore` tells you **how the pieces typically fit together** — a
  composition-tree workflow runtime, supervision pipeline, content-guard
  wiring, telemetry, and packaging.

Pragmatic, not dogmatic. Agents are free to mix `agentcore`, `agentkit`,
and `swarmkit` directly; `agentcore` never forces indirection.

## Packages

| Package | Status |
|---|---|
| [`workflow`](workflow/README.md) | landed |
| [`workflow/security`](workflow/security) | landed |
| [`observe`](observe/README.md) | landed |
| [`supervise`](supervise/README.md) | landed |
| [`packaging`](packaging/README.md) | landed |
| `identity`, `config` | planned |

## Quickstart

The `workflow` package is the entry point today. A minimal workflow:

```go
import (
    "github.com/vinayprograms/agentcore/workflow"
    "github.com/vinayprograms/agentcore/workflow/security"
    "github.com/vinayprograms/agentkit/llm"
)

model, _ := llm.New(llm.Config{Service: "anthropic", APIKey: apiKey})

wf := workflow.New("daily-summary").
    Input(workflow.Parameter{Name: "topic"}).
    Add(workflow.Sequence("main").Steps(
        workflow.Goal("draft", "Write a brief on $topic").WithOutputs("brief"),
        workflow.Goal("review", "Check $brief for accuracy"),
    )).
    Security(security.Default)

state, err := wf.Execute(ctx, &workflow.Runtime{Model: model},
    map[string]string{"topic": "memory consistency models"})
```

To wire telemetry, add a sink from the `observe` package:

```go
rt := &workflow.Runtime{
    Model:     model,
    Telemetry: observe.Logger(slog.Default()), // logs every event to slog
}
```

OTel tracing is emitted automatically by the workflow package whenever a `TracerProvider` is wired. No sink configuration needed for spans — just set up your OTel provider as you would for any other Go program.

To supervise a goal, mark it with `.Supervise()` (or `.SuperviseByHuman()`), wire a `Runtime.Supervisor` (e.g., `supervise.New(supervise.Config{})`), and optionally set `Runtime.MaxReorientAttempts`. See [`workflow/README.md`](workflow/README.md) for the full surface.

## Install

```
go get github.com/vinayprograms/agentcore
```

## Design principles

1. **Interface-driven at every seam.** Consumers program to interfaces;
   concrete types are returned from constructors and otherwise unexported.
2. **Independence by construction.** Composition deep-copies. State
   from one execution cannot bleed into another.
3. **Testable by design.** Dependencies (LLM, tools, MCP, supervisor,
   human channel) are injected via `Runtime`. Target:
   100% statement coverage on testable code.
4. **Small, focused packages.** Each package has one purpose.
5. **No re-implementations.** If `agentkit` or `swarmkit` owns a concept,
   `agentcore` depends on it.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Apache 2.0 — see [LICENSE](LICENSE).
