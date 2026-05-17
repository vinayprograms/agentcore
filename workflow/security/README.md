# workflow/security

Maps content-guard mode names to fully-configured `*contentguard.Guard` instances. Consumed by `workflow.Workflow.Execute` when a workflow declares a security mode and the caller has not pre-supplied a guard.

## Modes

| Mode | Behaviour |
|---|---|
| `Default` | Tier-1 deterministic checks on every tool call; tier-2/3 fire only on escalation. Lowest overhead. |
| `Paranoid` | All tiers (deterministic + screener + reviewer) run on every tool call regardless of triggers. Highest assurance. |
| `Research` | Same staging as Paranoid; the reviewer receives a free-text `scope` string that declares what security-relevant operations are permitted within the engagement. |

## Usage

```go
guard, err := security.Build(security.Paranoid, "", model)
```

```go
guard, err := security.Build(security.Research, "authorized pentest of lab.example.internal", model)
```

`Build` takes an `llm.Model` used by both the screener (cheap triage) and reviewer (full evaluation) stages. Pass the same model for both or construct your own `*contentguard.Guard` directly when you need distinct models per tier.

## Wiring

The workflow builds a guard automatically when `Workflow.Security(mode)` is declared and `Runtime.Guard` is nil. Callers who need explicit control — or who want to reuse a guard across multiple workflows — can pre-supply `Runtime.Guard` directly; the auto-build path is skipped.

```go
wf := workflow.New("threat-model").Security(security.Research).Scope("authorized lab")
// guard is built from Runtime.Model at Execute time if rt.Guard == nil
state, err := wf.Execute(ctx, rt, inputs)
```
