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
- `agentcore` tells you **how the pieces typically fit together** — an
  Agentfile DSL, a goal-driven runtime, drift detection, sessions, skills,
  packaging.

Pragmatic, not dogmatic. Agents are free to mix `agentcore`, `agentkit`,
and `swarmkit` directly; `agentcore` never forces indirection.

## Planned packages

The first cut (in-progress) extracts and generalizes proven patterns from
an existing working agent. Intended surface:

| Package | Purpose |
|---|---|
| `agentfile` | Parser for the Dockerfile-style agent DSL (NAME / INPUT / AGENT / GOAL / RUN / LOOP / CONVERGE) |
| `runtime` | Goal-driven agentic loop — XML context, sub-agent dispatch, output parsing, convergence |
| `session` | Per-run execution state store |
| `driftguard` | Four-phase COMMIT → EXECUTE → RECONCILE → SUPERVISE pipeline for execution drift |
| `hooks` | Lifecycle event hooks |
| `identity` | Stable agent identity + advertised skills |
| `skills` | `SKILL.md` loader for reusable capability bundles |
| `packaging` | Signed agent bundle format (keygen / pack / verify / install) |
| `replay` | Event-log replay for debugging and regression testing |
| `config` | `agent.toml` loader (credentials + policy come from `agentkit`) |

## Install

```
go get github.com/vinayprograms/agentcore
```

*(Installation will become meaningful once the first package lands.)*

## Design principles

1. **Interface-driven at every seam.** Consumers program to interfaces;
   concrete types are returned from constructors.
2. **Testable by design.** If a piece of code is needed, it is testable.
   Dependencies (clocks, randomness, I/O) are injected. Target: 100%
   statement coverage.
3. **Small, focused packages.** Each package has one purpose.
4. **Composition over configuration.** Constructors take typed options,
   not free-form maps.
5. **No re-implementations.** If `agentkit` or `swarmkit` owns a concept,
   `agentcore` depends on it.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Changes are tracked in
[CHANGELOG.md](CHANGELOG.md).

## License

Apache 2.0 — see [LICENSE](LICENSE).
