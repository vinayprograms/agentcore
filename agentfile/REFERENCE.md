# Agentfile Reference

The canonical reference for the Agentfile DSL. Describes the keyword set, field orderings, and edge cases that `agentfile.Parse` accepts and `agentfile.Compile` translates into `workflow` primitives.

---

## A complete example

A realistic Agentfile exercising the full keyword set: inputs with defaults, inline and file-backed agents, a skill bundle, multi-agent fan-out, CONVERGE with a runtime cap, structured outputs, supervision (workflow-level + per-step), and SECURITY scope.

```
# Threat-model a feature design document and produce a remediation plan.

NAME threat-model

INPUT design_doc                            # required
INPUT max_review_passes DEFAULT 3

SECURITY research "authorized internal architecture review"

SUPERVISED                                  # workflow-default supervision

# Inline persona — short, ad-hoc.
AGENT moderator "You coordinate analysis turns and synthesise the final report."

# File-backed persona — full prompt lives in agents/threat_analyst.md.
AGENT threat_analyst FROM agents/threat_analyst.md REQUIRES "reasoning-heavy"

# Skill bundle — frontmatter + body merged into the prompt at parse time;
# scripts/ and references/ inside the skill stay on disk for runtime use.
AGENT reviewer FROM skills/security-review

# Single-agent goal extracting structured fields.
GOAL extract_assets "Identify assets and trust boundaries in $design_doc." -> assets, boundaries USING threat_analyst

# Multi-agent fan-out goal; implicit synthesizer merges outputs.
GOAL enumerate_threats "Enumerate threats against assets: $assets." -> threats USING threat_analyst, reviewer

# Iterative refinement, capped at a runtime variable.
CONVERGE prioritise_threats "Rank $threats by severity until convergence." -> ranking USING reviewer WITHIN $max_review_passes

# Human-required gate: a moderator approves the final plan before it's emitted.
GOAL final_report "Compose the remediation plan from $ranking." -> report USING moderator SUPERVISED HUMAN

RUN main USING extract_assets, enumerate_threats, prioritise_threats, final_report
```

What each piece exercises:

- `INPUT topic` / `INPUT max_review_passes DEFAULT 3` — required vs. defaulted parameters.
- `SECURITY research "..."` — workflow-level content-guard mode with a scope; agentfile populates `Runtime.Guard` via `workflow/security.Build`.
- `SUPERVISED` — workflow default; inherited by every descendant unless overridden.
- `AGENT moderator "..."` — inline single-line prompt.
- `AGENT threat_analyst FROM ... REQUIRES "reasoning-heavy"` — file-backed prompt; `REQUIRES` resolves through the `profiles` map passed to `Compile`.
- `AGENT reviewer FROM skills/...` — SKILL.md frontmatter + body load at parse time; `scripts/` and `references/` inside the bundle stay on disk for the agent to read at task time via file tools.
- `-> assets, boundaries` — structured outputs flow back into the workflow's variable namespace as `$assets`, `$boundaries`.
- `USING threat_analyst, reviewer` — multi-agent fan-out with an implicit synthesizer that uses the workflow's default model.
- `CONVERGE ... WITHIN $max_review_passes` — runtime-resolved iteration cap (the `WithinVar` path on `workflow.Convergence`).
- `SUPERVISED HUMAN` on a single goal — escalates that goal to require human approval through `Runtime.HumanCh`, even though the workflow default is plain `SUPERVISED`.
- `RUN main USING ...` — ordered sequence of goals; outputs from earlier goals are visible to later ones.

---

## Top-level keyword set

The parser recognises exactly fifteen keywords plus the arrow modifier:

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

Anything else at the start of a non-comment, non-blank line is a parse error. (Some example files in the `agent` repo include stray `WORKFLOW` and `VERSION` lines — those files are stale and will not parse.)

Whitespace between keywords is required; identifiers and `$variable` references match `[A-Za-z_][A-Za-z0-9_]*`. Strings use `"..."` or triple-quoted `"""..."""` (multi-line). Comments begin with `#` and run to end-of-line.

---

## NAME — workflow identifier

**Form:** `NAME <identifier>`

**Happy case:** `NAME story-pipeline` declares the workflow's stable identifier. The runtime uses it as the workflow component of identity derivation, the metadata tag on every event, and the value carried in JSONL log records.

**Edge cases:**
- Missing NAME → preflight error.
- Whitespace-only or empty NAME → preflight error.
- Multiple NAME directives → undefined; the last one wins per the parser. Treat as a preflight warning.

---

## INPUT / DEFAULT — workflow parameters

**Form:** `INPUT <identifier>` (required) or `INPUT <identifier> DEFAULT <value>` (optional with fallback). The DEFAULT value may be a quoted string, a number, or a bare identifier.

**Happy case:** `INPUT topic` declares a required parameter; the value is supplied at run time and substitutes wherever `$topic` appears. `INPUT style DEFAULT "formal"` makes the parameter optional with the supplied fallback.

**Variable interpolation:** at goal-execution time, `$name` substring substitution applies to GOAL and CONVERGE descriptions and (per the executor) to AGENT prompt content loaded inline or from skills/files. Resolution order: declared inputs first, then upstream goal outputs.

**Edge cases:**
- Missing required INPUT at run time → preflight failure.
- Two INPUT directives with the same name → preflight error.
- Empty default (`DEFAULT ""`) is allowed; the empty string substitutes cleanly.
- An INPUT name colliding with an arrow output name (`-> topic`) → preflight error (variable namespace conflict).
- `$undeclared` in a description → preflight error.

---

## AGENT — sub-agent persona

**Form:**

```
AGENT <identifier> ( "<inline prompt>" | FROM <path> )
                  [ -> output1, output2, ... ]
                  [ REQUIRES "<profile>" ]
                  [ SUPERVISED [ HUMAN ] | UNSUPERVISED ]
```

Field order is fixed: prompt source → arrow outputs → REQUIRES → supervision modifier.

**Happy cases:**
- `AGENT critic FROM agents/critic.md` — markdown file at path relative to the Agentfile becomes the persona.
- `AGENT critic FROM skills/code-review` — directory containing `SKILL.md` resolves as a skill bundle (frontmatter eager, body on activation, scripts/references lazy). Loader rules: file ending in `.md` → prompt; directory with `SKILL.md` → skill; otherwise search paths from `agent.toml` `[skills] paths`.
- `AGENT brevity "You are concise."` — inline single-line string.
- `AGENT brevity """Multi-line\npersona\nprompt"""` — inline triple-quoted block.
- `AGENT critic FROM agents/critic.md REQUIRES "reasoning-heavy"` — pin to a capability profile defined under `[profiles.reasoning-heavy]` in `agent.toml`.
- `AGENT scanner "Scan for issues" -> vulnerabilities, severity` — the agent itself declares structured output fields. Used when the agent acts as a single-shot specialist and the workflow consumes its outputs directly.
- `AGENT validator "..." SUPERVISED HUMAN` — agent-level supervision override; every goal that uses this agent inherits the agent's flags.
- `AGENT scout "..." UNSUPERVISED` — opt this agent's invocations out of workflow-default supervision.

**Edge cases:**
- AGENT name reused → preflight error.
- AGENT inline prompt is an empty string → preflight error.
- `FROM <path>.agent` → preflight error (no nested `.agent` files; depth=1 at the source-format level).
- `REQUIRES "ghost"` where `ghost` is not declared in `agent.toml` profiles → preflight error.
- AGENT declared but never referenced in any USING → preflight warning.
- The same agent named in multiple goals is **stateless** — no shared memory across invocations.

---

## GOAL — agentic step

**Form:**

```
GOAL <identifier> ( "<description>" | FROM <path> )
                 [ -> output1, output2, ... ]
                 [ USING agent1, agent2, ... ]
                 [ SUPERVISED [ HUMAN ] | UNSUPERVISED ]
```

Field order is fixed: description → arrow outputs → USING → supervision.

**Happy cases:**
- `GOAL summarize "Summarize $topic in 200 words"` — single-shot agentic loop driven by the workflow's default LLM and tool registry.
- `GOAL outline FROM prompts/outline.md` — load description from a file resolved relative to the Agentfile.
- `GOAL summarize "..." -> headline, body, confidence` — declare structured output fields the LLM is instructed to return as a JSON object; values flatten into `$headline`, `$body`, `$confidence` in workflow scope.
- `GOAL evaluate "..." USING researcher, critic, writer` — fan out to three agents in parallel; outputs collected and synthesized.
- `GOAL deploy "..." SUPERVISED HUMAN` — escalate this goal to human-approval supervision regardless of workflow default.
- `GOAL format "..." UNSUPERVISED USING formatter` — opt out of workflow-default supervision for trusted/trivial operations. Field-order quirk: `UNSUPERVISED` precedes `USING` in some example files; both orderings parse because the parser is order-tolerant after the description.

**Edge cases:**
- GOAL name reused → preflight error.
- Description references `$undeclared` → preflight error.
- Empty description (`""`) → preflight error.
- `FROM <path>` not resolvable → preflight error reporting the attempted path.
- GOAL declared but never named in any RUN block → preflight warning (unused goal).
- All declared arrow outputs are *required* in v1 — the LLM must return all of them, or the goal fails (or, in supervised mode, escalates as drift).
- Field-name collisions: arrow outputs cannot collide with any INPUT name, prior GOAL/CONVERGE output name, or AGENT-declared output (preflight error).

---

## CONVERGE — iterative refinement

**Form:**

```
CONVERGE <identifier> ( "<description>" | FROM <path> )
                     [ -> output1, output2, ... ]
                     [ USING agent1, agent2, ... ]
                     WITHIN ( <number> | $variable )
                     [ SUPERVISED [ HUMAN ] | UNSUPERVISED ]
```

Field order is fixed: description → arrow outputs → USING → WITHIN → supervision. **WITHIN is mandatory.**

**Happy cases:**
- `CONVERGE refine "Polish the draft" WITHIN 5` — single-agent self-refinement; loop terminates when the agent emits the literal `CONVERGED` somewhere in its output, or 5 iterations elapse.
- `CONVERGE refine "..." USING writer, critic WITHIN 3` — multi-agent: agents take turns within each iteration; outputs across iteration *k* are visible to all agents in iteration *k+1*.
- `CONVERGE refine "..." -> final_text WITHIN 5` — structured output extracted from the final substantive iteration.
- `CONVERGE refine "..." WITHIN $max_iter` — WITHIN takes a numeric literal *or* a `$variable` reference. The variable is resolved against goal outputs first, then inputs.

**Behavior:**
- The substantive iteration *before* the iteration emitting `CONVERGED` is the goal's output.
- Each iteration receives all prior iterations' outputs as context (a `<convergence-history>`-style framing).
- Hitting the cap without convergence returns the last iteration's output with a non-convergence warning attached as metadata. The workflow continues — reaching the cap is *not* a failure.
- The numeric value of WITHIN is hidden from the LLM so the agent cannot trade convergence quality for budget.

**Edge cases:**
- Missing WITHIN → parse error (mandatory).
- `WITHIN 0` or negative → preflight error.
- `WITHIN $undeclared` → preflight error.
- All other CONVERGE edge cases mirror GOAL (reused name, undeclared `$var`, unresolved FROM, etc.).

---

## RUN — sequential execution block

**Form:**

```
RUN <identifier> USING goal1, goal2, ...
                [ SUPERVISED [ HUMAN ] | UNSUPERVISED ]
```

**Happy cases:**
- `RUN main USING gather, analyze, summarize` — runs the three named goals in declared order; outputs from `gather` are visible to `analyze`, etc.
- Multiple `RUN` blocks at the workflow level execute in declared file order.
- `RUN deploy_stage USING deploy SUPERVISED HUMAN` — block-level supervision applies to every goal in the block; per-goal escalations still allowed.

**Edge cases:**
- `RUN ... USING <name>` references an undeclared goal → preflight error reporting the unresolved name.
- `RUN step USING` with no goal names → parse error.
- The same step name reused across RUN blocks → preflight error.
- The same goal named in multiple RUN blocks is **allowed** and re-runs the goal (goals are stateless); intentional, not a warning.
- Variables are workflow-scoped — RUN boundaries do *not* introduce new scopes.

---

## USING — multi-agent fan-out and goal selection

`USING` appears in two distinct contexts:

**On GOAL or CONVERGE:** `USING <agent>, <agent>, ...` — names declared agents that handle this step. Multiple agents fan out in parallel; outputs are collected and an implicit synthesizer LLM produces the goal's final output (or fills the structured fields declared by `->`).

**On RUN:** `USING <goal>, <goal>, ...` — names declared goals to execute sequentially in declared order.

**Edge cases:**
- Single agent in `GOAL ... USING` degenerates to no-synthesis — the agent's output becomes the goal's output directly.
- Naming an undeclared agent in `GOAL ... USING` → preflight error.
- Naming an undeclared goal in `RUN ... USING` → preflight error.
- The same agent named twice in `USING` → preflight warning, treated as one occurrence.
- Sub-agents inside a USING fan-out cannot themselves use `spawn_agents` (depth=1 enforcement).

---

## WITHIN — convergence cap

**Form:** `WITHIN ( <number> | $variable )` — appears only on CONVERGE.

**Resolution:** when a `$variable` is used, the runtime resolves it from goal outputs first, then declared inputs.

**Edge cases:** zero, negative, or unresolved-variable values → preflight error. WITHIN is hidden from the LLM.

---

## FROM — resolved-content source

**Form:** `FROM <path>` — appears on AGENT (prompt or skill) and on GOAL/CONVERGE (description from file).

**Resolution rules** (per the parser's loader):

1. **File at relative path with `.md` extension.** Load the file content as the prompt or description. Path resolved relative to the Agentfile.
2. **Directory at relative path containing a `SKILL.md`.** Resolve as an `agentkit.Skill` bundle.
3. **Bare identifier (no slash, no `.md` extension).** Treated as a skill name; search the consumer's `[skills] paths` from `agent.toml` in declared order; first hit wins. Useful for centrally-installed skills (e.g., `~/.agent/skills/`).

**Edge cases:**
- Resolution failure → preflight error reporting all attempted paths (relative-from-Agentfile, then each configured `skills.paths` entry).
- `FROM <path>.agent` → preflight error (cannot nest `.agent` files; depth=1 enforcement at the source-format level).
- Symbolic links followed by default; path-traversal protection (escapes outside the workspace + configured roots) enforced by the loader.
- Two `skills.paths` entries containing skills of the same bare name → first match wins; later matches are silently shadowed (planned: warn on shadowing).

---

## REQUIRES — capability profile pin

**Form:** `REQUIRES "<profile-name>"` — appears only on AGENT. Mandatory **string literal** (not bare identifier).

**Happy case:** `AGENT critic FROM agents/critic.md REQUIRES "reasoning-heavy"` resolves at preflight to the model defined under `[profiles.reasoning-heavy]` in `agent.toml`.

**Edge cases:**
- Profile name not declared in `agent.toml` → preflight error.
- REQUIRES omitted on AGENT → uses the workflow-default profile from `agent.toml`.
- REQUIRES on GOAL / CONVERGE / RUN → parse error (the parser only accepts REQUIRES on AGENT).

---

## SUPERVISED / UNSUPERVISED / HUMAN — supervision modifiers

**Workflow level (top of file):**

```
SUPERVISED                # every goal supervised; cost-saving short-circuit allowed
SUPERVISED HUMAN          # every goal supervised + requires human approval
```

(Note: there is no workflow-level `UNSUPERVISED`; absence of `SUPERVISED` is the unsupervised default.)

**On AGENT, GOAL, CONVERGE, or RUN:**

```
SUPERVISED                # opt this node into supervision (overrides workflow default)
SUPERVISED HUMAN          # opt this node into supervision and require human approval
UNSUPERVISED              # opt this node out of workflow-default supervision
```

**Behavior:**
- Supervision drives the four-phase pipeline: COMMIT (intent declaration) → EXECUTE (work + post-checkpoint) → RECONCILE (deterministic drift triggers) → SUPERVISE (LLM verdict).
- Verdicts: `Continue` (proceed), `Reorient` (retry EXECUTE with the supervisor's correction; capped), `Pause` (halt; escalate to human if available).
- When the workflow has `SUPERVISED`, descendants inherit unless they declare `UNSUPERVISED`.
- `HUMAN` propagates downward but never upward: a goal `SUPERVISED HUMAN` inside a workflow with plain `SUPERVISED` requires human approval for that goal alone.
- A node `UNSUPERVISED` cannot exist inside a HUMAN-required scope at the same or higher level — pre-flight error if attempted.

**Edge cases:**
- No human channel wired up + any HUMAN-flagged node → preflight failure.
- `SUPERVISED` skips the SUPERVISE phase if RECONCILE produces zero drift triggers and no HUMAN is required (cost-saving short-circuit). HUMAN-required steps always invoke SUPERVISE so a verdict reaches the approver.
- Approval-channel timeout policy is consumer-configurable (escalate / fail / continue-with-warning); missing policy defaults to fail-closed.
- `PreflightFailed` event fires when preflight aborts.

---

## SECURITY — tier-review aggressiveness

**Form (workflow level only):**

```
SECURITY default
SECURITY paranoid
SECURITY research "<scope description>"
```

**Behaviors:**

- `SECURITY default` — Tier-1 deterministic checks (denylists, pattern matches, encoded-content detection) on every tool call. Tier-2/Tier-3 only on triggers. Lowest overhead.
- `SECURITY paranoid` — every tool call runs through Tier-2 + Tier-3.
- `SECURITY research "<scope>"` — Tier-3 receives the scope text and permits security-relevant actions within the declared boundary while blocking actions outside it.

**Constraints (per parser):**
- SECURITY is workflow-scoped only — there is no per-RUN or per-GOAL SECURITY override.
- `SECURITY research` *requires* a scope string literal — parse error otherwise.
- `SECURITY` modes other than `default | paranoid | research` are parse errors.
- Without a SECURITY directive, fallback to the value in `agent.toml`'s `[security]` section.

**Cross-cutting:**
- Encoded content (base64, hex, etc.) inside untrusted inputs always escalates regardless of mode (Tier-1 invariant).
- Tool-policy denials (from `policy.toml`) apply in *all* modes — security mode does not bypass policy.

---

## → (arrow) — structured output extraction

**Form:** `-> field1, field2, ...` — appears immediately after the description on AGENT, GOAL, and CONVERGE.

**Happy case:** the runtime instructs the LLM to return a JSON object containing the declared keys; parsed values flatten into the workflow variable namespace as `$field1`, `$field2`, ... visible to all subsequent steps.

**Combination semantics:**
- On a multi-agent GOAL/CONVERGE with `USING`, the implicit synthesizer LLM is responsible for producing the declared fields.
- On AGENT, the agent's own outputs become available when that agent is the sole `USING` participant for a goal that does not declare its own arrow fields.

**Edge cases:**
- All declared fields are *required* — missing fields cause the goal to fail (or, in supervised mode, a drift trigger).
- Field-name collisions with an INPUT or upstream output → preflight error.
- Fields scoped to the workflow run only — they don't persist across runs.

---

## Lexical details

- **Comments:** `#` to end-of-line. No block comments.
- **Strings:** `"..."` (single-line) or `"""..."""` (multi-line, preserves newlines literally). No string interpolation at the lexer level — `$var` substitution happens at execution time.
- **Numbers:** decimal integers. Used only in `WITHIN N`.
- **Paths:** unquoted dotted/slashed identifiers passed to `FROM` (e.g., `agents/critic.md`, `skills/code-review`). The lexer distinguishes `Path` from `Ident` based on context after `FROM`.
- **Identifiers:** `[A-Za-z_][A-Za-z0-9_]*`.
- **Variables:** `$<identifier>` inside string contexts and after `WITHIN`.
- **Whitespace:** insignificant beyond statement separation (newline). Indentation is not meaningful.

---

## Hook event taxonomy

For observability, the runtime fires these events through the in-process pub-sub bus — see `events` package for full struct definitions:

- `WorkflowStarted` / `WorkflowEnded`
- `GoalStarted` / `GoalEnded` (carries `Warning` for non-convergence)
- `CommitPhase` / `ExecutePhase` / `ReconcilePhase` / `SupervisePhase`
- `SubagentSpawned` / `SubagentCompleted`
- `PreflightFailed`
- `ApprovalRequested` / `ApprovalDecided`

The `logs` package serializes these as versioned JSONL lines.
