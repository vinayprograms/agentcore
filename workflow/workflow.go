package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel/attribute"

	"github.com/vinayprograms/agentcore/workflow/security"
)

// workflow is the root of the execution tree. It owns the declared inputs and
// the ordered list of sequences. Implements Node but not Step.
//
// Constructed via workflow.New — never directly.
type workflow struct {
	name       string
	parameters []Parameter
	sequences  []*sequence

	supervision supervision

	securityMode     security.Mode
	securityScope    string
	securityRequired bool // true once Security() has been called
}

// New constructs a workflow with the given name. This is the package's primary
// type, hence the New() prefix per Go convention.
func New(name string) *workflow {
	return &workflow{name: name}
}

// Input declares the workflow's parameter set in a single call. Each entry
// is a workflow.Parameter struct; an empty Default makes the parameter
// required, a non-empty Default makes it optional with that fallback.
//
//	wf.Input(
//	    workflow.Parameter{Name: "topic"},
//	    workflow.Parameter{Name: "style", Default: "concise"},
//	)
//
// May be called multiple times to append additional parameters; each call
// adds to the workflow's parameter set.
func (w *workflow) Input(parameters ...Parameter) *workflow {
	w.parameters = append(w.parameters, parameters...)
	return w
}

// Add appends one or more sequences to this workflow. Each argument is
// deep-copied — passing the same sequence to multiple workflows is safe.
func (w *workflow) Add(sequences ...*sequence) *workflow {
	for _, seq := range sequences {
		w.sequences = append(w.sequences, seq.clone())
	}
	return w
}

// Supervise marks the workflow for LLM-driven supervision.
func (w *workflow) Supervise() *workflow {
	w.supervision = byLLM
	return w
}

// SuperviseByHuman marks the workflow for supervision requiring human approval.
func (w *workflow) SuperviseByHuman() *workflow {
	w.supervision = byHuman
	return w
}

// Security declares the workflow-level content-guard mode. Calling this opts
// the workflow in to building a *contentguard.Guard at Execute time using
// Runtime.Model. For security.Research, pair this call with .Scope.
func (w *workflow) Security(mode security.Mode) *workflow {
	w.securityMode = mode
	w.securityRequired = true
	return w
}

// Scope declares the free-text scope for security.Research mode. It is
// embedded in the reviewer stage's system prompt so the supervisor knows what
// is permitted within the declared engagement.
func (w *workflow) Scope(scope string) *workflow {
	w.securityScope = scope
	return w
}

// Name implements Node.
func (w *workflow) Name() string { return w.name }

// Kind implements Node.
func (w *workflow) Kind() Kind { return kindOf(w) }

// Children implements Node. Workflow children are runs only — inputs are
// arguments to the workflow, not navigable nodes.
func (w *workflow) Children() []Node {
	nodes := make([]Node, 0, len(w.sequences))
	for _, r := range w.sequences {
		nodes = append(nodes, r)
	}
	return nodes
}

// Execute binds inputs, validates the workflow, then executes each sequence
// in declared order. State accumulates across sequences.
//
// If the workflow declared a security mode and rt.Guard is nil, Execute
// builds a content guard from the declared mode/scope using rt.Model. The
// caller's *env is never mutated — execution uses a derived shallow copy.
func (w *workflow) Execute(ctx context.Context, rt *Runtime, inputs map[string]string) (state *State, err error) {
	state, err = w.bind(inputs)
	if err != nil {
		return nil, err
	}

	if err := Validate(w, rt); err != nil {
		rt.fire(ctx, PreflightFailed{Workflow: w.name, Failure: err})
		return nil, fmt.Errorf("preflight: %w", err)
	}

	ctx, end := trace(ctx, "workflow.run", attribute.String("workflow", w.name))
	defer end(&err)

	runEnv := *rt
	if w.securityRequired && runEnv.Guard == nil {
		guard, buildErr := security.Build(w.securityMode, w.securityScope, runEnv.Model)
		if buildErr != nil {
			err = fmt.Errorf("build content guard: %w", buildErr)
			return nil, err
		}
		runEnv.Guard = guard
		defer guard.Close()
	}

	runEnv.fire(ctx, WorkflowStarted{Workflow: w.name})

	// Seed the supervision context with the workflow-level mode. Sequences,
	// goals, and convergences read this and either inherit or override.
	ctx = context.WithValue(ctx, ctxSupervision, w.supervision)

	var execErr error
	for _, r := range w.sequences {
		if ctxErr := ctx.Err(); ctxErr != nil {
			execErr = ctxErr
			break
		}
		if stepErr := r.Execute(ctx, &runEnv, state); stepErr != nil {
			execErr = stepErr
			break
		}
	}

	runEnv.fire(ctx, WorkflowEnded{Workflow: w.name, Failure: execErr})
	if execErr != nil {
		err = execErr
		return nil, err
	}
	return state, nil
}

// bind resolves declared parameters against the provided values. An empty
// Default on a parameter means "no default" — the caller must supply a value.
func (w *workflow) bind(provided map[string]string) (*State, error) {
	seen := make(map[string]bool, len(w.parameters))
	resolved := make(map[string]string, len(w.parameters))

	for _, p := range w.parameters {
		if seen[p.Name] {
			return nil, fmt.Errorf("duplicate parameter declaration: %s", p.Name)
		}
		seen[p.Name] = true

		if v, ok := provided[p.Name]; ok {
			resolved[p.Name] = v
		} else if p.Default != "" {
			resolved[p.Name] = p.Default
		} else {
			return nil, fmt.Errorf("required parameter missing: %s", p.Name)
		}
	}
	return NewState(resolved), nil
}

// Validate performs preflight checks on w and rt. It composes each subtree's
// Validate() with workflow-level and cross-tree checks (parameter conflicts,
// output-name collisions, sequence-name uniqueness, runtime requirements).
//
// Workflow.Execute calls Validate before doing anything; consumers may also
// call it directly for ahead-of-time checks.
func Validate(w *workflow, rt *Runtime) error {
	var errs []error

	// Workflow-level checks
	if strings.TrimSpace(w.name) == "" {
		errs = append(errs, fmt.Errorf("workflow: name is required"))
	}
	if rt == nil || rt.Model == nil {
		errs = append(errs, fmt.Errorf("workflow: Runtime.Model is required"))
	}
	if w.securityRequired && w.securityMode == security.Research && strings.TrimSpace(w.securityScope) == "" {
		errs = append(errs, fmt.Errorf("workflow: security.Research requires a non-empty scope (set via .Scope)"))
	}
	if len(w.sequences) == 0 {
		errs = append(errs, fmt.Errorf("workflow: at least one sequence is required (use .Add to attach a sequence)"))
	}

	// Parameter checks (workflow-level cross-cutting)
	declared := make(map[string]string)
	paramNames := make(map[string]bool, len(w.parameters))
	for _, p := range w.parameters {
		if strings.TrimSpace(p.Name) == "" {
			errs = append(errs, fmt.Errorf("workflow: parameter has empty name"))
			continue
		}
		if paramNames[p.Name] {
			errs = append(errs, fmt.Errorf("parameter %s: declared more than once", p.Name))
			continue
		}
		paramNames[p.Name] = true
		declared[p.Name] = "parameter"
	}

	// Sequences: name uniqueness + per-sequence Validate (recursive into its
	// subtree) + cross-tree output-name collision check.
	seqNames := make(map[string]bool, len(w.sequences))
	for _, seq := range w.sequences {
		if seqNames[seq.name] {
			errs = append(errs, fmt.Errorf("sequence %s: declared more than once", seq.name))
		}
		seqNames[seq.name] = true

		if err := seq.Validate(); err != nil {
			errs = append(errs, err)
		}
		for _, step := range seq.steps {
			errs = append(errs, registerOutputs(step, declared)...)
		}
	}

	// Variable-reference check: every $var inside a node's description /
	// prompt / task must resolve to something declared (parameter or any
	// step's output anywhere in the workflow). Caught at preflight rather
	// than silently passing through interpolation as literal text.
	for _, seq := range w.sequences {
		for _, step := range seq.steps {
			errs = append(errs, checkVarRefs(step, declared)...)
		}
	}

	// Supervision wiring: if any node in the tree (or the workflow itself,
	// or any sequence) requested supervision, Runtime.Supervisor MUST be
	// non-nil. If any node requested human-required supervision, both
	// Runtime.Supervisor and Runtime.HumanCh must be wired.
	anyMode, anyHuman := scanSupervision(w)
	if anyMode && rt != nil && rt.Supervisor == nil {
		errs = append(errs, fmt.Errorf("workflow: Runtime.Supervisor is required when any node sets Supervise() or SuperviseByHuman()"))
	}
	if anyHuman && rt != nil && rt.HumanCh == nil {
		errs = append(errs, fmt.Errorf("workflow: Runtime.HumanCh is required when any node sets SuperviseByHuman()"))
	}

	return errors.Join(errs...)
}

// scanSupervision walks the workflow tree and reports whether any node
// requested supervision (anyMode) and whether any specifically requested
// human-required supervision (anyHuman).
func scanSupervision(w *workflow) (anyMode, anyHuman bool) {
	var sup func(s supervision)
	sup = func(s supervision) {
		switch s {
		case byLLM:
			anyMode = true
		case byHuman:
			anyMode = true
			anyHuman = true
		}
	}
	sup(w.supervision)
	for _, seq := range w.sequences {
		sup(seq.supervision)
		for _, step := range seq.steps {
			scanStepSupervision(step, &anyMode, &anyHuman)
		}
	}
	return anyMode, anyHuman
}

func scanStepSupervision(step Step, anyMode, anyHuman *bool) {
	mark := func(s supervision) {
		switch s {
		case byLLM:
			*anyMode = true
		case byHuman:
			*anyMode = true
			*anyHuman = true
		}
	}
	switch s := step.(type) {
	case *goal:
		mark(s.supervision)
		for _, c := range s.using {
			scanStepSupervision(c, anyMode, anyHuman)
		}
	case *convergence:
		mark(s.supervision)
		for _, c := range s.using {
			scanStepSupervision(c, anyMode, anyHuman)
		}
	}
}

// checkVarRefs walks a step subtree and reports any $var reference that
// doesn't resolve to a declared parameter or output name.
func checkVarRefs(step Step, declared map[string]string) []error {
	var errs []error
	switch s := step.(type) {
	case *goal:
		errs = append(errs, undeclaredVars("goal", s.name, s.description, declared)...)
		for _, child := range s.using {
			errs = append(errs, checkVarRefs(child, declared)...)
		}
	case *convergence:
		errs = append(errs, undeclaredVars("convergence", s.name, s.description, declared)...)
		for _, child := range s.using {
			errs = append(errs, checkVarRefs(child, declared)...)
		}
	case *agent:
		errs = append(errs, undeclaredVars("agent", s.name+" prompt", s.prompt, declared)...)
		errs = append(errs, undeclaredVars("agent", s.name+" task", s.task, declared)...)
	}
	return errs
}

func undeclaredVars(kind, owner, text string, declared map[string]string) []error {
	var errs []error
	for _, name := range extractVars(text) {
		if _, ok := declared[name]; !ok {
			errs = append(errs, fmt.Errorf("%s %s: $%s is not declared as a parameter or upstream output", kind, owner, name))
		}
	}
	return errs
}

// registerOutputs walks a step subtree and reports any output-name collision
// against the running 'declared' map (shared across the whole workflow).
// Used only by the package-level Validate; per-type Validate methods do
// structural checks but don't have a workflow-wide map to consult.
func registerOutputs(step Step, declared map[string]string) []error {
	var errs []error
	switch s := step.(type) {
	case *goal:
		errs = append(errs, recordOutputs("goal", s.name, s.outputs, declared)...)
		for _, child := range s.using {
			errs = append(errs, registerOutputs(child, declared)...)
		}
	case *convergence:
		errs = append(errs, recordOutputs("convergence", s.name, s.outputs, declared)...)
		for _, child := range s.using {
			errs = append(errs, registerOutputs(child, declared)...)
		}
	case *agent:
		errs = append(errs, recordOutputs("agent", s.name, s.outputs, declared)...)
	}
	return errs
}

func recordOutputs(kind, owner string, outputs []string, declared map[string]string) []error {
	var errs []error
	for _, out := range outputs {
		if strings.TrimSpace(out) == "" {
			// Empty-name detection lives in the per-type Validate; skip here
			// to avoid double-reporting.
			continue
		}
		if prev, conflict := declared[out]; conflict {
			errs = append(errs, fmt.Errorf("%s %s: output %q conflicts with %s", kind, owner, out, prev))
			continue
		}
		declared[out] = fmt.Sprintf("%s:%s", kind, owner)
	}
	return errs
}
