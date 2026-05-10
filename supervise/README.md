# supervise

Default `workflow.Supervisor` implementation for agentcore's supervision pipeline. Handles the two pluggable phases: deterministic drift detection (`Reconcile`) and LLM-driven verdict rendering (`Supervise`). The other three phases — COMMIT, EXECUTE, and POST — run inside the workflow package against the agent's model; only RECONCILE and SUPERVISE are swappable.

## Usage

Wire into `workflow.Runtime` to supervise any node marked `Supervise()` or `SuperviseByHuman()`:

```go
import (
    "github.com/vinayprograms/agentcore/supervise"
    "github.com/vinayprograms/agentcore/workflow"
)

// Dedicated supervisor model — optional. When nil, the supervisor
// falls back to the agent's main model carried by SuperviseRequest.
sup := supervise.New(supervise.Config{
    Model: supervisorModel,
})

rt := &workflow.Runtime{
    Model:      agentModel,
    Supervisor: sup,
}

wf := workflow.New("pipeline").
    Add(workflow.Sequence("main").Steps(
        workflow.Goal("draft", "Write a proposal").
            Supervise(), // runs through the full 5-phase pipeline
        workflow.Convergence("refine", "Polish the text", 3).
            SuperviseByHuman(), // requires human approval
    ))

state, err := wf.Execute(ctx, rt, inputs)
```

Without a supervisor, unsupervised steps execute directly; supervised steps fail at Validate.

## Reconcile

Deterministic, no LLM. Checks four signals from the agent's post-execution self-assessment:

| Signal | Trigger produced |
|---|---|
| `MetCommitment == false` | `commitment_not_met` |
| Non-empty `Deviations` | `deviation:<text>` per entry |
| Non-empty `Concerns` | `concern:<text>` per entry |
| Non-empty `Unexpected` | `unexpected:<text>` per entry |

Any trigger sets `Escalate = true` and the pipeline proceeds to SUPERVISE (for byLLM steps) or AskHuman (for byHuman steps). Clean execution short-circuits — no costly LLM call.

## Supervise

LLM call against `Config.Model` (if set) or the agent's model (fallback). Receives the original goal, the pre/post checkpoints, and reconcile triggers. Renders one of four verdicts:

| Verdict | Behavior |
|---|---|
| `Continue` | Accept output as-is |
| `Reorient` | Re-run EXECUTE with correction (bounded by `MaxReorientAttempts`) |
| `AskHuman` | Dispatch question on `HumanCh`; human response becomes correction |
| `Halt` | Terminate with the supervisor's reason |

On parse failure (malformed JSON from the LLM), the verdict degrades to `AskHuman` so a human can resolve the ambiguous output.

## Lifecycle

Stateless beyond the `Config` struct — no `Close` needed. The reorient counter and human-handshake orchestration live in the workflow package; this package only renders verdicts when called.
