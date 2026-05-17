# agentfile

Parser and compiler for the Agentfile DSL. Translates Agentfile source into the typed workflow primitives shipped by `agentcore/workflow`.

The package only translates Agentfile source into the workflow primitives; the runtime, supervisor, and content-guard implementations live in other agentcore packages. Consumers compose those onto the returned `Runtime` before executing.

## Example

Every wiring step is shown. `REPLACE:` markers point to the lines you change per project; the rest is the same in every use.

```go
package main

import (
    "context"
    "fmt"
    "log"
    "log/slog"
    "os"
    "path/filepath"

    "github.com/vinayprograms/agentcore/agentfile"
    "github.com/vinayprograms/agentcore/observe"
    "github.com/vinayprograms/agentcore/supervise"
    "github.com/vinayprograms/agentcore/workflow"

    "github.com/vinayprograms/agentkit/llm"
    "github.com/vinayprograms/agentkit/mcp"
    "github.com/vinayprograms/agentkit/tools"
)

func main() {
    ctx := context.Background()

    // REPLACE: load your own agent.toml or equivalent.
    cfg, err := loadAgentToml("agent.toml")
    if err != nil { log.Fatal(err) }

    // 1. Read the Agentfile and parse it. baseRoot is the directory FROM
    //    references resolve against; path-traversal escapes via "../" are
    //    blocked by the OS at the os.Root boundary.
    source, err := os.ReadFile("Agentfile")
    if err != nil { log.Fatal(err) }
    baseRoot, err := os.OpenRoot(filepath.Dir("Agentfile"))
    if err != nil { log.Fatal(err) }
    defer baseRoot.Close()
    spec, err := agentfile.Parse(string(source), baseRoot, agentfile.Config{Skills: cfg.SkillRoots})
    if err != nil { log.Fatalf("parse: %v", err) }

    // 2. Resolve every REQUIRES profile the Spec mentions.
    profiles := make(map[string]llm.Model, len(spec.Profiles()))
    for _, name := range spec.Profiles() {
        m, ok := cfg.Profiles[name]
        if !ok { log.Fatalf("REQUIRES %q: profile not defined in agent.toml", name) }
        profiles[name] = m
    }

    // 3. Compile the AST. tools must contain every name listed in any
    //    skill's allowed-tools; pass nil only when no skill uses allowed-tools.
    //    rt is always non-nil; Spec-derived fields are pre-populated.
    wf, rt, err := agentfile.Compile(spec, cfg.DefaultModel, profiles, cfg.Tools)
    if err != nil { log.Fatalf("compile: %v", err) }

    // 4. Layer the Runtime-only fields the Spec doesn't declare.
    rt.Model = cfg.DefaultModel
    mcpMgr, err := mcp.NewManager(cfg.MCPServers)
    if err != nil { log.Fatal(err) }
    rt.MCP = mcpMgr
    rt.Tools = cfg.Tools
    if spec.HasSupervision() {
        rt.Supervisor = supervise.New(supervise.Config{Model: cfg.SupervisorModel})
    }
    if spec.HasHumanSupervision() {
        rt.HumanCh = make(chan string)
        go handleHumanApprovals(ctx, rt.HumanCh) // REPLACE: your approval UI
    }
    rt.Telemetry = observe.Logger(slog.Default())

    // 5. Run.
    state, err := wf.Execute(ctx, rt, map[string]string{
        "topic": os.Args[1], // REPLACE: inputs from your CLI / HTTP handler
    })
    if err != nil { log.Fatalf("execute: %v", err) }
    fmt.Println("done:", state.Outputs)
}
```

`loadAgentToml` and `handleHumanApprovals` are caller-side helpers — they are **not** agentcore code. Different consumers (CLI, server, inspector, test harness) want different shapes for those, so agentfile exposes only primitives and leaves the wiring to the caller.

## Public API

| Function | Signature | Purpose |
|---|---|---|
| `Parse` | `Parse(text string, baseRoot *os.Root, cfg Config) (*Spec, error)` | Parse the source and resolve `FROM` references against `baseRoot`. The caller reads the source bytes themselves (file, embed.FS, HTTP, etc.) and opens an `*os.Root` on the directory FROM references are relative to. The OS blocks any FROM that tries to escape via `../`. Pass `nil` only when the source has no FROM references. |
| `Compile` | `Compile(spec *Spec, defaultModel llm.Model, profiles map[string]llm.Model, tools *tools.Registry) (*workflow.Workflow, *workflow.Runtime, error)` | Semantic validation plus AST → workflow translation. |
| `Spec.Profiles` | `() []string` | Unique REQUIRES names across all agents; iterate to build the `profiles` map. |
| `Spec.HasSupervision` | `() bool` | True if any node is SUPERVISED. Use it to decide whether to set `rt.Supervisor`. |
| `Spec.HasHumanSupervision` | `() bool` | True if any node ends up requiring human approval after propagation. Use it to decide whether to set `rt.HumanCh`. |

`Config` carries `Skills []*os.Root` today (skill bundle roots searched, in order, when an AGENT FROM is a bare identifier and is not present inside `baseRoot`). The caller opens each search root via `os.OpenRoot(path)` before passing them in. The struct is reserved for additive growth (shadow warnings, symlink policy, etc.) — bare-name `Config` matches the `tls.Config` / `image.Config` convention.

## Skill handling

Agentfile loads skills under the Anthropic Agent Skills [progressive disclosure model](https://www.anthropic.com/engineering/equipping-agents-for-the-real-world-with-agent-skills).

| Resource | When loaded | By whom |
|---|---|---|
| Frontmatter (`name`, `description`, `allowed-tools`) | At `Parse` time | the loader |
| SKILL.md body | At `Parse` time | the loader |
| `scripts/`, `references/`, `assets/` | At task time, on demand | the running agent via file-read tools |

Loading body at parse time is consistent with the standard's L2 (activation) level: in a declarative DSL, the `AGENT x FROM skills/...` clause **is** the activation event — the workflow author has decided the skill is in use for that agent in that workflow. Scripts and reference files stay on disk and are read by the agent at task time, which is the genuinely-progressive layer (L3).

Compile attaches a per-agent `Override.SystemContext` blurb identifying the skill bundle path and telling the agent it may consult `scripts/` and `references/` via file tools. If the SKILL.md declares `allowed-tools`, Compile produces `Override.Tools = registry.Subset(allowedTools)` and requires every named tool to exist in the registry argument; mismatches are a compile error.

### Variable interpolation in skills

Agentfile does **not** add its own substitution syntax for SKILL.md content. The workflow runtime already replaces `$<name>` references at Execute time using `State.Outputs` then `State.Inputs`, and the SKILL.md body is part of the agent's prompt — so a `$topic` written in a SKILL.md body resolves the same way as a `$topic` in a GOAL description. If you write Claude-Code-style placeholders like `$ARGUMENTS` or `${CLAUDE_SKILL_DIR}`, preflight fails with an "undeclared variable" error.

## Runtime returned by Compile

Compile always returns a non-nil `*workflow.Runtime`. Spec-derived fields are pre-populated; everything else is the zero value for the caller to fill in.

Compile pre-populates:

- `rt.Guard` — when the Spec declares `SECURITY default|paranoid|research`. `security.Build` runs with `defaultModel` and the declared scope.
- Per-agent `Override.Model` — from the resolved `REQUIRES` profile (when set) or `defaultModel`.
- Per-skill-agent `Override.SystemContext` and `Override.Tools` — see above.

Compile does **not** populate (consumer's responsibility):

- `rt.Model`, `rt.Tools`, `rt.MCP`, `rt.Telemetry`
- `rt.Supervisor`, `rt.HumanCh`
- `rt.MaxReorientAttempts`, `rt.SystemContext`

## Validations

Performed at `Compile` time (not `Parse`):

- Every agent name in `Goal.UsingAgent` must be declared.
- Every goal name in `Step.UsingGoals` must be declared.
- `Goal.WithinLimit` and `Goal.WithinVar` are mutually exclusive; on `CONVERGE` one of them is required and (when literal) must be `> 0`.
- `Goal.Outcome` and `Goal.FromPath` are not both set on a Parse-only Spec.
- `SECURITY research` requires a non-empty scope.
- Every name in `spec.Profiles()` is present in the `profiles` map.
- SKILL.md `allowed-tools` names are present in the tool registry (and the registry is non-nil).
- `UNSUPERVISED` is not declared inside a `SUPERVISED HUMAN` scope at any level.
- Agent/Goal duplicates are rejected.

Cross-cutting validations owned by `workflow.Validate` (variable resolution, output-name collisions, supervision wiring of `Supervisor` / `HumanCh`) run at Execute time, not at Compile.

## Grammar

The Agentfile keyword set is fixed at 15 keywords plus `->` and `$variable`:

```
NAME   INPUT   DEFAULT
AGENT   FROM   REQUIRES
GOAL   CONVERGE   USING   WITHIN
RUN
SUPERVISED   UNSUPERVISED   HUMAN
SECURITY
->
$variable
```

See [`REFERENCE.md`](REFERENCE.md) for field orderings, edge cases, and per-keyword behavior.
