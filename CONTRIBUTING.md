# Contributing to agentcore

Thanks for considering a contribution. `agentcore` is deliberately small
and opinionated — every addition needs to earn its place.

## Scope check

`agentcore` is the opinionated layer above
[`agentkit`](https://github.com/vinayprograms/agentkit) and
[`swarmkit`](https://github.com/vinayprograms/swarmkit). Before proposing
a new package or feature, confirm it belongs here rather than in one of
the siblings:

- Belongs in **`agentkit`** if it is a single-agent primitive (LLM,
  tools, memory, MCP, ACP, guards, policy, credentials, errors,
  shutdown, embedding).
- Belongs in **`swarmkit`** if it is a multi-agent coordination primitive
  over NATS (messaging, task dispatch, heartbeat, registry).
- Belongs in **`agentcore`** if it encodes an *opinion* about how agents
  are composed — a DSL, a runtime shape, a lifecycle pipeline, a
  packaging format, a shared execution state.

If in doubt, open an issue and ask before writing code.

## Design principles

1. **Interface-driven.** Every exported type at a package boundary sits
   behind an interface. Consumers program to the interface; constructors
   return concrete types.
2. **Testable by design.** If a piece of code is needed, it is testable.
   Inject dependencies (clocks, randomness, I/O). Untestable code is a
   design smell, not an acceptable trade-off.
3. **TDD / BDD is the house style.** Write the failing test first. Keep
   statement coverage at 100% unless a reason not to is recorded in
   `docs/` and justified in the PR.
4. **Small, focused packages.** A package with a purpose that cannot be
   summarized in six words is too broad.
5. **Composition over configuration.** Constructors take typed options,
   not free-form maps.
6. **No re-implementations.** If `agentkit` or `swarmkit` already owns a
   concept, depend on it.

## Development

Prerequisites: Go 1.25 or later.

```bash
git clone git@github.com:vinayprograms/agentcore.git
cd agentcore
go test ./...
```

## Submitting changes

1. For non-trivial changes, open an issue describing the change before
   you start. Align on scope before writing code.
2. Every change includes tests. New behavior in a PR without tests will
   be asked for tests before review.
3. Keep changes focused — one logical change per PR.
4. Update `CHANGELOG.md` under the `Unreleased` section.

## Code style

- `gofmt` + `goimports` clean.
- `go vet ./...` clean.
- Exported identifiers have Go doc comments explaining intent, not just
  restating the signature.
- Comments explain non-obvious *why*, never restate *what*.
- Follow the naming conventions established across
  `agentkit` / `swarmkit` / `agentcore`: defensive/gating components use
  the `*guard` suffix; primitives are simple nouns.

## Reporting bugs

Open a GitHub issue with:

- What you were doing.
- What you expected to happen.
- What actually happened.
- A minimal reproduction.
