# workflow

Execution model for agentcore workflows. A composition tree of typed nodes where each primitive owns its execution loop. Constructors return values of unexported types — direct struct construction is impossible. Every composition method deep-copies, so a node placed into one parent cannot alias into another.

## Usage

```go
// Use default model to process open JIRA and TODO list tasks
jira := workflow.Agent("jira", "Pull outstanding tasks from the JIRA queue")
todo := workflow.Agent("todo", "Pull outstanding tasks from the TODO list")

// Use a heavy model for processing historical data — pinned via Customize.
heavy, _ := llm.New(llm.Config{Service: "anthropic", Model: "claude-opus-4-7", APIKey: apiKey})
past := workflow.Agent("past-actions", "Pull recurring or postponed past actions").
    Customize(workflow.Override{Model: heavy})

prepare := workflow.Goal("prepare", "Prepare task data from all sources").
    Using(jira, todo, past)

// Use structured outputs to capture response into variables. Supervised
// for alignment with stated goal.
analyze := workflow.Goal("analyze",
    "Analyze tasks across sources, choose the most important next actions that fit $hours_available hours").
    WithOutputs("chosen_tasks").
    Supervise()

writeDaily := workflow.Goal("write_daily",
    "Write $chosen_tasks to a daily-tasks file dated today, including context for each task")

main := workflow.Sequence("main").Steps(prepare, analyze, writeDaily)

// Build and execute as a single chained expression. `Input(...)` takes a
// variadic list of `workflow.Parameter` values — each parameter declares
// its name and an optional default (empty default ⇒ required at execute time).
// `.Security(...)` opts the workflow into content-guard tier checks at every
// tool call — important here because the JIRA agent fetches external content.
state, err := workflow.New("gtd-next-action").
    Input(
        workflow.Parameter{Name: "hours_available"},
        workflow.Parameter{Name: "style", Default: "concise"},
    ).
    Security(security.Default). // tier-1 always; LLM stages on escalation
    Add(main).
    Execute(ctx, &workflow.Runtime{Model: heavy}, map[string]string{
        "hours_available": "4",
    })
```

Stricter postures swap one line:

```go
.Security(security.Paranoid)                                  // tier-2 + tier-3 on every tool call
.Security(security.Research).Scope("auth-token-handling")     // permissive within a declared scope
```

## Constructors

`New()` is reserved for the workflow root (the package's primary type); secondary types use bare names per Go convention.

| Constructor | Returns | Required args |
|---|---|---|
| `workflow.New(name)` | workflow root | name |
| `workflow.Agent(name, prompt)` | agent | name, persona prompt |
| `workflow.Goal(name, description)` | goal | name, description |
| `workflow.Convergence(name, description, within)` | convergence | name, description, iteration cap |
| `workflow.Sequence(name)` | sequence | name |

## Methods

Optional configuration is set via chainable methods. The naming convention:

- **`With*` prefix** signals an *additive* feature: `WithOutputs(...)`.
- **Bare names** are *structural* and central to what the node is: `Using(...)`, `Steps(...)`, `Task(...)`.
- **Verb methods** are stateful flags: `Supervise()`, `SuperviseByHuman()`, `Customize(...)`.

| Method | On | Purpose |
|---|---|---|
| `.Input(...parameters)` | workflow | declare the parameter set in one call; each entry is a `workflow.Parameter{Name, Default}` value (variadic) |
| `.Add(...sequences)` | workflow | append one or more sequences (variadic) |
| `.Execute(ctx, rt, inputs)` | workflow | bind, validate, and run; terminal in the chain |
| `.Steps(...steps)` | sequence | append one or more steps in order (variadic) |
| `.Using(...steps)` | goal, convergence | declare participating agents (parallel fan-out, variadic) |
| `.Customize(workflow.Override{...})` | every node + sequence | per-node runtime override; layers on top of parent's Runtime, propagates to children unless they override in turn |
| `.Task(text)` | agent | set the per-invocation instruction; required before `.Execute()` (set automatically by parent fan-out) |
| `.WithOutputs(...fields)` | agent, goal, convergence | declare structured output fields (variadic) |
| `.Supervise()` | workflow, sequence, goal, convergence | LLM-driven supervision (agents inherit their parent's mode) |
| `.SuperviseByHuman()` | workflow, sequence, goal, convergence | supervision that requires human approval (agents inherit) |
| `.Security(mode)` | workflow | declare workflow-level content-guard mode (see `workflow/security`) |
| `.Scope(text)` | workflow | declare scope text for `security.Research` |

The supervision state is a single internal value with three valid modes (unsupervised, by LLM, by human). Methods set it directly — there are no orthogonal flags whose combinations could produce undefined states.

Every method on `workflow`, `agent`, `goal`, `convergence`, and `sequence` returns the receiver, so the entire definition is chainable from `workflow.New(...)` through `.Execute(...)`.

Parameter declaration uses a single variadic call: `wf.Input(workflow.Parameter{...}, workflow.Parameter{...}, ...)`. There is no chained `Input(name).Input(name)` pattern, no `WithDefault` method, and no `*input` builder type. Each `Parameter` is a struct value with `Name` and `Default` fields — an empty `Default` means required, a non-empty `Default` makes the parameter optional with that fallback.

## Parameters are arguments, not nodes

`Parameter` values declared via `Input(...)` are workflow arguments — they do not implement `Node` and do not appear in `Workflow.Children()`. The only children of a workflow are its sequences. An empty `Default` field is the universal "no default" signal: pass any non-empty string in `Default` to make the parameter optional.

## Independence by construction

Every composition method deep-copies. The same agent variable can be passed to multiple goals; each goal stores its own independent copy.

```go
critic := workflow.Agent("critic", "You are a critic")

review := workflow.Goal("review", "Review the draft").Using(critic)
debate := workflow.Goal("debate", "Debate alternatives").Using(critic)
// review and debate hold independent copies of the agent.
// Mutating critic afterward affects neither.
```

This eliminates state-bleed by construction: there is no exported pointer field, no shared backing array, no slice header reused across nodes. The `clone_test.go` suite proves this for every type and every reference-typed field.

## Interfaces

```go
type Node interface {
    Name() string
    Kind() node.Kind
    Children() []Node
}

type Step interface {
    Execute(ctx, rt, state) error
    clone() Step
}
```

`Node` carries enough metadata (`Name()` and `Kind()`) to make navigation useful — a tree walk can identify and act on each node without type assertions. `Kind` is derived from the concrete Go type name via reflection, so kinds stay in sync with the types automatically: rename a type and its kind follows. Today's values are `"workflow"`, `"sequence"`, `"goal"`, `"convergence"`, `"agent"`.

```go
switch n.Kind() {
case "workflow", "sequence":
    // structural
case "goal", "convergence":
    // execution units
case "agent":
    // leaf
}
```

`Node` and `Step` are independent interfaces. Concrete types that need both implement each separately:

| Type | Node | Step |
|---|---|---|
| workflow | ✓ | |
| sequence | ✓ | |
| goal | ✓ | ✓ |
| convergence | ✓ | ✓ |
| agent | ✓ | ✓ |
| input | (not a node) | |

The unexported `clone()` on `Step` closes the interface to this package — only types defined here can satisfy it.

## Security

Content-guard tier modes live in the `workflow/security` subpackage, which owns both the constants AND the policy that maps each mode to a configured `*contentguard.Guard`:

```go
import "github.com/vinayprograms/agentcore/workflow/security"

wf.Security(security.Default)                                // tier-1 only; LLM stages run on escalation
wf.Security(security.Paranoid)                               // all stages run on every tool call with untrusted context
wf.Security(security.Research).Scope("auth-token-handling")  // research mode + scope for the reviewer's prompt
```

**How modes change behavior:**

- **Default** wires the contentguard `Escalatory()` workflow with `Screener` + `Reviewer` stages. The deterministic tier-1 short-circuits Allow when there's no untrusted content; otherwise the screener runs first and the reviewer runs only if the screener escalates.
- **Paranoid** wires the contentguard `Paranoid()` workflow with the same stages. All stages run on every escalation; any Deny short-circuits.
- **Research** is Paranoid plus a `cfg.Context["scope"]` value that flows into the reviewer's research-permissive system prompt, allowing security-relevant actions inside the declared scope.

`workflow.Execute` builds the guard on demand: if the workflow declared a mode and `Runtime.Guard` is nil, it calls `security.Build(mode, scope, rt.Model)` and uses the result for the duration of the run. The caller's `*Runtime` is never mutated — execution runs against a derived shallow copy. If the caller pre-supplies their own `Runtime.Guard`, that takes precedence.

The runtime invokes `Guard.Check` before every tool call; a Deny or Modify verdict short-circuits the call with an error. Outputs from MCP tools are ingested as untrusted via `Guard.Ingest`, propagating taint into subsequent guard checks.

`Validate` rejects any workflow that declares `security.Research` without setting a scope.

## Components

Each component owns its execution behavior. The sections below cover what each type does at run time, what state it touches, and what events it fires.

### Parameter

A `Parameter` is a workflow argument declaration. Two fields:

- `Name` — the parameter name; the value bound to it is reachable from `$<name>` interpolations inside goal/convergence descriptions.
- `Default` — the fallback value used when the caller omits this parameter at execute time. An empty `Default` means the parameter is required; preflight fails if a caller doesn't supply one.

`Parameter` is a value type containing only strings. Shallow copy is deep copy — there is no aliasing concern. It does not implement `Node` and does not appear in `Workflow.Children()`.

`$var` interpolation: at goal/convergence execution time, `$<name>` substrings inside the description are replaced from `State`. Resolution order is **outputs first, then inputs** — so a downstream goal can reference an upstream goal's output by name.

### Workflow

The root of the execution tree. Owns parameters and the ordered list of sequences. Implements `Node` but not `Step`.

When `Execute` is called:

1. **Bind inputs** against declared parameters. Required parameters with no caller-supplied value cause an error (`required parameter missing: <name>`); duplicates also error.
2. **Validate** the entire tree (see the Validate section).
3. **Build the content guard**, if `Security(...)` was declared and the caller didn't pre-supply one. Uses the workflow's declared mode + scope + `rt.Model`. The caller's `*Runtime` is *never* mutated; execution runs against a shallow copy that has the new guard wired in.
4. **Fire `WorkflowStarted`**.
5. **Execute each sequence** in declared order. Context cancellation is checked between sequences. The first sequence error stops the run.
6. **Fire `WorkflowEnded`** (with any error).
7. Return the final `*State` and error.

### Sequence

An ordered block of `Step`s. Implements `Node` but not `Step` — sequences are how the workflow's execution order is declared, not steps inside other steps.

When the workflow executes a sequence:

1. For each step in declared order:
   - Check `ctx.Err()`; bail with that error if cancelled.
   - Call `step.Execute(ctx, rt, state)`. Errors stop the sequence.

State accumulates across the steps; by the time step *N* runs, the outputs of steps 1..*N-1* are visible in `state.Outputs` and reachable via `$<name>` interpolation in their descriptions.

A sequence-level `Supervise()` or `SuperviseByHuman()` propagates to every goal/convergence inside the sequence (and to their `Using` children) unless they declare their own mode. The supervision pipeline (COMMIT → EXECUTE → POST → RECONCILE → SUPERVISE) actually fires at each goal/convergence — the sequence just carries the inherited mode.

### Goal

An agentic step. Implements both `Node` and `Step`. Two execution modes depending on whether `Using(...)` was set.

**The description is mandatory.** It is what the goal IS. Empty descriptions are rejected by `Validate` at preflight and by `Goal.Execute` at runtime — there is no inference, no skip, no implicit "do something." If a goal doesn't know what to do, it can't run.

**Single-agent mode (no `Using`):** runs the LLM loop directly against `rt.Model`:

1. Build the system prompt (defaults to "You are a helpful assistant executing a goal" + optional `rt.SystemContext` appended).
2. Build the user prompt: prior goals' outputs as `<prior-goals>` blocks, then the interpolated description as `<goal>`, plus a structured-output instruction if `WithOutputs(...)` declared any fields.
3. Loop:
   - Call `rt.Model.Chat` with the messages and the merged tool definitions (`rt.Tools` + `rt.MCP`).
   - If the response has no tool calls → exit with `resp.Content` as the goal's output.
   - Otherwise, append the assistant's tool-call message and execute each tool call in parallel via `runTools` (see Tool execution below). Append all tool-result messages and continue.

The orchestrator never decides termination — the model does, by returning a turn with no tool calls.

**Fan-out mode (`Using` non-empty):** runs each `Step` in `Using` concurrently:

1. Place the interpolated description into context (`ctxGoal`) so any tool calls inside the fan-out can read it as the originating goal.
2. For each child step, launch a goroutine that:
   - Forks the State so each goroutine has its own outputs map (no race).
   - Fires `SubagentSpawned`.
   - Calls `step.Execute(taskCtx, rt, childState)`.
   - Fires `SubagentCompleted` with the output and any error.
3. Wait for all goroutines, collect outputs.
4. If only one child ran, return its output directly. If multiple ran, call `rt.Model.Chat` once more to **synthesize** the agent outputs into a single coherent response.

**Output handling:**

- The goal's primary output (raw text) goes into `state.Outputs[<goal name>]`.
- If `WithOutputs(...)` declared structured fields, the model is instructed to return JSON; matching keys are extracted and flattened into `state.Outputs[<field name>]`. Failures to parse are silent (the structured fields just don't appear).

Events fired: `GoalStarted`, `GoalEnded`. In fan-out mode, `SubagentSpawned`/`SubagentCompleted` per child.

### Convergence

An iterative refinement step. Implements both `Node` and `Step`. Wraps a Goal-like loop in a hard iteration cap.

**Description and `Within > 0` are both mandatory.** Both are checked by `Validate` at preflight and by `Convergence.Execute` at runtime. A convergence with no description doesn't know what it's refining; a convergence with `Within ≤ 0` would never run a single iteration.

When executed:

1. For each iteration up to `Within`:
   - Build the prompt with `<convergence-history>` listing all prior iterations' outputs.
   - Run one iteration — single-agent (default model + tools) if no `Using`, or fan-out with synthesis if `Using` is set.
   - If the iteration's output contains the literal `"CONVERGED"`, **stop and return the *previous* substantive iteration's output** (not the one with the marker). The marker is a signal, not a result.
   - Otherwise, append the iteration to history and update `state.Outputs[<convergence name>]` so subsequent iterations and downstream goals see the latest.
2. If the loop completes without converging, fire `ConvergenceCapReached`, set `state.Failures[<convergence name>] = within`, and return the last substantive iteration. **Hitting the cap is NOT an error** — execution continues.

Structured outputs (`WithOutputs`) are extracted from the final substantive iteration, same as Goal.

The convergence cap is *hidden* from the model — the prompt never mentions it. This prevents the model from trading convergence quality for budget.

### Agent

A persona with a fixed prompt. Implements both `Node` and `Step`. Used inside `Goal.Using(...)` or `Convergence.Using(...)`, **or invoked standalone**. Each reference site holds an independent deep copy.

Two mandatory inputs to `Execute`, both fail-loud if missing:

- **`prompt`** — the agent's persona (set at construction). Empty prompt → error. There is no fallback persona, no generic substitution.
- **`task`** — the per-invocation instruction (set via `.Task(...)` before `.Execute()`). Empty task → error.

The persona prompt describes WHO the agent is. The task describes WHAT it should do this time. They are separate concerns: the prompt is set once when the agent is constructed; the task changes per invocation.

**Standalone invocation:**

```go
critic := workflow.Agent("critic", "You are a rigorous critic.")
err := critic.Task("Find weaknesses in this draft: ...").Execute(ctx, rt, state)
```

The `Task` builder method anchors the per-invocation instruction at the call site — no context magic, no hidden contract. Reads as a sentence: *"the critic's task is X; execute."*

**In-fan-out invocation** is the same mechanism: `Goal.fanOut` and `Convergence.iterateFanOut` clone each child once more and call `.Task(description)` on the clone before `.Execute()`. The original stored agent is never mutated, so a goal that runs twice produces two fresh per-invocation copies with no leakage between runs.

When executed:

1. Validate (structural — `name`, `prompt`, output names; recurses through any using-children).
2. Validate `task` is non-empty — fail-loud (per-invocation precondition, not declarative).
3. Merge `agent.override` (set via `.Customize(workflow.Override{...})`) on top of the parent runtime — pinning a specific `Model`, narrowing `Tools`, swapping `MCP`, tightening `Policy`, or appending `SystemContext`.
4. **Interpolate `$var` references in BOTH `prompt` and `task`** against `state` (outputs first, then inputs; unknown `$var` left literal). The interpolation is idempotent — when a parent fan-out has already resolved the task, re-interpolating it here is a no-op. (`Validate` rejects undeclared `$var` references at preflight, so a runtime miss only happens for variables that were declared but unset.)
5. Build messages: interpolated prompt as system, interpolated task as user.
6. Run an agentic loop identical to Goal's single-agent loop — repeat until the model returns a turn with no tool calls.
7. Write the final response to `state.Outputs[<agent name>]`.

When invoked from a parent fan-out, the parent collects and synthesizes the outputs of all participating agents.

**Interpolation example.** Both fields can reference parameters and upstream outputs by `$name`:

```go
critic := workflow.Agent("critic", "You are a critic for $domain content.")
err := critic.Task("Review $document for issues.").Execute(ctx, rt, state)
// state has Inputs{"domain":"security","document":"policy.md"} →
// system: "You are a critic for security content."
// user:   "Review policy.md for issues."
```

**Independence guarantee:** when the same agent variable is passed into two goals' `Using` lists, each `Using` call clones it. Setting `.Task` on the clone in one goal cannot affect the clone in the other, the user's original variable, or any other use site. The fan-out's per-invocation clone provides one more layer of isolation, so even repeat-runs of the same goal cannot trample each other.

### Tool execution

Every tool call from any LLM loop (Goal, Convergence, Agent) goes through `executeTool`:

1. **Content guard check** — if `rt.Guard` is non-nil, `Guard.Check(ctx, toolName, args, currentGoal)` runs first. The current goal description is threaded via context (`ctxGoal`, set by the goal/convergence at the start of its Execute). A `Deny` or `Modify` verdict short-circuits with an error; `Allow` proceeds.
2. **Dispatch:**
   - Tool names starting with `mcp_<server>_<tool>` route to `rt.MCP.CallTool` for that server. The result text is concatenated.
   - Other names route to `rt.Tools.Execute` (the built-in registry).
3. **Taint registration** — for MCP tools (external content), the result is ingested into the guard as **untrusted**, propagating the taint surface for subsequent guard checks within the same workflow run.

Multiple tool calls in a single LLM turn are executed **in parallel** via `runTools`. Tool-result messages are stitched back into the conversation in the original tool-call order.

### Workflow execution recap

```
Workflow.Execute
├── bind parameters from caller's input map
├── Validate(workflow, runtime)
├── build content guard (if Security declared)
├── fire WorkflowStarted
├── for each Sequence:
│   └── for each Step:
│       └── Step.Execute(ctx, rt, state)
│           ├── Goal/Convergence/Agent runs LLM loop
│           ├── tool calls flow through guard + dispatch
│           └── outputs land in state
└── fire WorkflowEnded
```

State accumulates across all steps in all sequences — every step sees every prior output.

## Runtime

`Runtime` is wired once at construction and immutable during execution. Only `Model` is required.

```go
rt := &workflow.Runtime{
    Model:           llmModel,        // required
    Tools:           toolRegistry,    // optional
    MCP:             mcpManager,      // optional — any workflow.MCPManager (e.g. *mcp.Manager)
    Policy:          policyLookup,    // optional
    Guard:           contentGuard,    // optional (auto-built when .Security() declared)
    Telemetry:       eventSink,       // optional — see agentcore/observe
    SystemContext:   "...",           // appended to system prompt on every call
    Supervisor:      mySupervisor,    // required when any node is Supervised
    CheckpointStore: myStore,         // optional persistence for the four checkpoint records
    HumanCh:         make(chan string), // required when any node is SuperviseByHuman
    Debug:           false,
}
```

### Per-step customization (`Customize` + `Override`)

A workflow-wide `Runtime` is the default for every step in the workflow, but any individual step (sequence, goal, convergence, agent) can override a curated subset of fields just for its own subtree:

```go
type Override struct {
    Model         llm.Model         // pin a specific LLM
    Tools         *tools.Registry   // narrow / replace the tool registry
    MCP           workflow.MCPManager // swap the MCP provider (any *mcp.Manager satisfies it)
    Policy        policy.Lookup     // tighten policy
    SystemContext string            // appended (NOT replaced) onto parent's
}
```

Apply via the uniform `Customize` method, available on every step type and on sequence:

```go
memReg := tools.NewRegistry().With(memoryRead, memoryWrite)

memUpdate := workflow.Goal("update_memory",
    "Persist relevant facts from this run into memory.").
    Customize(workflow.Override{
        Tools:         memReg,
        SystemContext: "You only have memory-update tools. Do not attempt other actions.",
    })
```

The `memUpdate` goal sees the workflow's `Model`, `MCP`, `Policy`, `Telemetry`, `Guard` etc. as inherited, but its `Tools` becomes `memReg` and its system prompt has the workflow's `SystemContext` plus the goal's restriction note appended.

**Inheritance model.** Each step's `Execute` merges its own `Override` on top of the parent runtime it was given, then passes the merged runtime to its children:

```
Workflow.Execute (rt = workflow.runtime)
└── Sequence.Execute (rt ⊕ seq.override)
    └── Goal.Execute (rt ⊕ goal.override)
        └── Agent.Execute (rt ⊕ agent.override)
```

At every level, fields the override leaves zero/nil inherit from above; non-zero fields replace. `SystemContext` is the only append-not-replace field — each level layers its content on top of what the parent already declared.

**What's NOT in `Override` (and why).** `Telemetry`, `Guard`, `Debug`, `Supervisor`, `CheckpointStore`, and `HumanCh` are workflow-wide concerns — a single event sink, a single taint surface across the run, a single debug flag, a single supervision policy and human-handoff channel. Allowing per-node overrides would split observability, security, and supervision guarantees, so those fields stay on `Runtime` only.

**Multiple `Customize` calls replace.** Calling `.Customize(...)` twice on the same step replaces the previous override — compose multiple customizations into a single `Override{...}` literal rather than chaining multiple calls.

## Supervision

Any node — workflow, sequence, goal, or convergence — may be marked `Supervise()` (LLM-driven) or `SuperviseByHuman()` (LLM-driven plus human approval on pause). A supervised step runs through a four-phase pipeline that wraps its underlying execution:

```
COMMIT     LLM declares intent (PreCheckpoint)        ─ uses rt.Model
EXECUTE    the step's actual work runs
POST       LLM self-assesses the work (PostCheckpoint) ─ uses rt.Model
RECONCILE  rt.Supervisor compares pre vs. post → triggers + escalate flag
SUPERVISE  rt.Supervisor renders a Verdict           ─ skipped when no triggers
                                                       AND not human-required
```

Verdicts:

- `VerdictContinue` — return the EXECUTE output as-is.
- `VerdictReorient` — re-run EXECUTE once with the supervisor's correction prepended to the instruction.
- `VerdictPause` — for human-required nodes, send the supervisor's question on `Runtime.HumanCh` and wait for a response. A non-empty response is treated as an approval (used as a correction, then EXECUTE re-runs). A whitespace-only response, a closed channel, or context cancellation aborts the step. For LLM-only nodes, Pause is reported as an error.

The pipeline integrates the consumer's policy through two interfaces, both wired on `Runtime`:

```go
type Supervisor interface {
    Reconcile(pre *PreCheckpoint, post *PostCheckpoint) *ReconcileResult
    Supervise(ctx context.Context, req SuperviseRequest) (*SuperviseResult, error)
}

type CheckpointStore interface {
    SavePre(*PreCheckpoint) error
    SavePost(*PostCheckpoint) error
    SaveReconcile(*ReconcileResult) error
    SaveSupervise(*SuperviseResult) error
}
```

`Validate` enforces wiring: any node with `Supervise()` requires `Runtime.Supervisor`; any node with `SuperviseByHuman()` additionally requires `Runtime.HumanCh`. `CheckpointStore` is optional — a nil store skips persistence.

A minimal `Supervisor` that always continues:

```go
type permissive struct{}

func (permissive) Reconcile(pre *workflow.PreCheckpoint, post *workflow.PostCheckpoint) *workflow.ReconcileResult {
    return &workflow.ReconcileResult{StepID: pre.StepID, Escalate: false}
}

func (permissive) Supervise(ctx context.Context, req workflow.SuperviseRequest) (*workflow.SuperviseResult, error) {
    return &workflow.SuperviseResult{Verdict: workflow.VerdictContinue}, nil
}

rt := &workflow.Runtime{
    Model:      m,
    Supervisor: permissive{},
}
```

A real `Supervisor` compares pre vs. post in `Reconcile` to decide whether drift occurred (set `Escalate: true` and populate `Triggers`), and uses an LLM (or a human) inside `Supervise` to render a `Verdict`. There is no default implementation today — write your own against this two-method interface.

Supervision propagates through the tree via context: a workflow- or sequence-level `Supervise()` flows into nested goals/convergences unless they declare a different mode of their own. Agents inherit their parent goal/convergence's supervision automatically (they have no `Supervise` setter — supervision belongs to the unit of declared work, not to a sub-agent).

The COMMIT and POST phases degrade gracefully on model errors or malformed JSON: the checkpoints are still produced (with low-confidence defaults and a recorded failure assumption) so RECONCILE always runs against meaningful inputs.

## Events

`Runtime.Telemetry` (if set) receives these events:

| Event | When |
|---|---|
| `WorkflowStarted` | `Workflow.Execute` begins |
| `WorkflowEnded` | `Workflow.Execute` returns (carries any error) |
| `GoalStarted` | Goal or Convergence begins |
| `GoalEnded` | Goal or Convergence completes |
| `SubagentSpawned` | Fan-out starts a child Step |
| `SubagentCompleted` | Child Step in fan-out finishes (carries err if any) |
| `ConvergenceCapReached` | Convergence hits its `within` without converging |
| `PreflightFailed` | `Validate` rejects the workflow |

Every event satisfies the `observe.Event` interface (`agentcore/observe`):

```go
type Event interface {
    Name() string       // stable identifier, e.g. "goal.started"
    Level() slog.Level  // slog.LevelInfo / Warn / Error
    Attrs() []slog.Attr // structured fields, type-safe at construction
    Err() error         // nil unless the event represents a failure
}
```

Sinks dispatch generically — a custom sink reads `Name`, `Level`, `Attrs`, and `Err` and never needs a type switch. New event types added here flow through every sink automatically.

The `agentcore/observe` package provides drop-in `EventSink` implementations today: `Logger` (slog), `Counter` (OTel metrics), and `NewHandlers` (typed-callback registry). Compose with `observe.Tee`.

## Tracing

Tracing is emitted automatically by this package via OpenTelemetry — no consumer wiring needed at the sink layer. Each `Workflow.Execute`, `Sequence.Execute`, `Goal.Execute`, `Convergence.Execute`, and `Agent.Execute` opens a span, records errors via `RecordError` / `SetStatus(Error)`, and ends it. The span hierarchy mirrors the composition tree.

Without an OTel TracerProvider configured, every span goes to OTel's no-op default and is silently discarded — zero overhead, zero noise.

To capture spans locally (no collector required):

```go
import (
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
    "go.opentelemetry.io/otel/sdk/trace"
)

exp, _ := stdouttrace.New(stdouttrace.WithWriter(os.Stderr))
otel.SetTracerProvider(trace.NewTracerProvider(trace.WithBatcher(exp)))
```

To wrap a workflow run in a parent span (for higher-level context like a user-request ID):

```go
ctx, span := otel.Tracer("agent").Start(ctx, "user.request",
    attribute.String("request_id", reqID))
defer span.End()

state, err := wf.Execute(ctx, rt, inputs)
```

The workflow's spans become children of the user's span automatically via OTel context propagation. agentkit's per-LLM-call spans (`llm.chat`) and contentguard checks become deeper children inside the goal span that triggered them. The full trace tree falls out for free.

## Validate

`Validate(w, rt)` is called by `Workflow.Execute` before any execution and may also be called directly. It enforces every mandatory-field rule the agentfile DSL spec requires, applied via `strings.TrimSpace` so whitespace-only values are rejected:

**Workflow-level**

- name is required (non-whitespace)
- `Runtime.Model` is non-nil
- `security.Research` mode has a non-empty scope (when declared)
- at least one sequence is attached

**Parameters**

- each parameter has a non-empty name
- parameter names are unique within the workflow
- parameter names don't collide with any node's output field
- every `$var` reference inside any goal/convergence description, agent prompt, or agent task resolves to a declared parameter or some node's output anywhere in the workflow

**Supervision wiring**

- `Runtime.Supervisor` is non-nil if any node has `Supervise()` or `SuperviseByHuman()` (workflow-level, sequence-level, or any goal/convergence in the tree)
- `Runtime.HumanCh` is non-nil if any node has `SuperviseByHuman()`

**Sequences**

- each sequence has a non-empty name
- sequence names are unique within the workflow
- each sequence has at least one step

**Goals / Convergences**

- name is required (non-whitespace)
- description is required (non-whitespace)
- `Convergence.within > 0`
- declared output field names are non-empty
- output field names don't collide with parameters or other nodes' outputs (across the entire tree, including nested fan-outs)
- nested goals, convergences, and agents in `Using(...)` are validated **recursively** — a malformed nested node is caught no matter how deep it sits

**Agents**

- name is required (non-whitespace)
- prompt is required (non-whitespace) — there is no fallback persona substituted at runtime
- declared output field names are non-empty and don't collide

**Self-validation at every entry point.** Validation is not just a workflow-level affair — every primitive that has an `Execute` method validates itself (and recurses into its subtree) before doing any work. The hierarchy:

| Type | Method | What it checks |
|---|---|---|
| `agent` | `Validate()` | name, prompt, output names |
| `goal` | `Validate()` | name, description, output names + recurses into `using` |
| `convergence` | `Validate()` | name, description, `within > 0`, output names + recurses into `using` |
| `sequence` | `Validate()` | name, has-steps, recurses into every step |
| package-level | `Validate(w, rt)` | composes every `seq.Validate()` + workflow-level checks (model, security scope, parameters, sequence-name uniqueness) + cross-tree output-name collisions |

Each `Execute` calls `Validate()` first as its initial step:

```go
func (g *goal) Execute(...) error {
    if err := g.Validate(); err != nil { return err }
    // ... actual goal logic
}
```

This means a consumer who skips the workflow and invokes a primitive directly — `goal.Execute(ctx, rt, state)` or `agent.Task(...).Execute(ctx, rt, state)` — still gets the same structural-validity guarantee for that subtree. A malformed agent inside a `Using` list is caught at the very start, not partway through the agentic loop.

The cross-tree concerns (output-name collisions across the entire workflow, sequence-name uniqueness, parameter conflicts) are detected only at the package-level `Validate(w, rt)` because they require a workflow-wide view. Standalone `Goal.Validate()` knows nothing about other goals; that's the workflow's job.

**Per-invocation preconditions.** Distinct from declarative-field validation, some checks are runtime-only:

- `Agent.Execute` requires `Task` to have been set (per-invocation, set by `.Task(text)` or by a parent fan-out) — this is checked AFTER `Validate()` because task isn't part of the agent's static declaration.
