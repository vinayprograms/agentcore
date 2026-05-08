package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel/attribute"
)

// sequence is an ordered block of Steps. Implements Node but not Step — it
// is the workflow's unit of declared execution order, not a step itself.
//
// Constructed via workflow.Sequence — never directly.
type sequence struct {
	name  string
	steps []Step

	override Override

	supervision supervision
}

// Sequence constructs a sequence with the given name. Use .Steps(...) to
// populate it.
func Sequence(name string) *sequence {
	return &sequence{name: name}
}

// Steps appends Steps (Goals or Convergences) to this sequence in order.
// Each argument is deep-copied — passing the same step variable to multiple
// sequences is safe.
func (seq *sequence) Steps(steps ...Step) *sequence {
	for _, s := range steps {
		seq.steps = append(seq.steps, s.clone())
	}
	return seq
}

// Customize layers per-sequence runtime overrides on top of whatever the
// parent workflow provided. The override propagates to every step in the
// sequence, unless they declare their own Customize on top.
func (seq *sequence) Customize(o Override) *sequence {
	seq.override = o
	return seq
}

// Supervise marks this sequence for LLM-driven supervision (applies to all
// steps inside the sequence unless they override).
func (seq *sequence) Supervise() *sequence {
	seq.supervision = byLLM
	return seq
}

// SuperviseByHuman marks this sequence for supervision that requires human
// approval.
func (seq *sequence) SuperviseByHuman() *sequence {
	seq.supervision = byHuman
	return seq
}

// Name implements Node.
func (seq *sequence) Name() string { return seq.name }

// Kind implements Node.
func (seq *sequence) Kind() Kind { return kindOf(seq) }

// Children implements Node.
func (seq *sequence) Children() []Node {
	nodes := make([]Node, 0, len(seq.steps))
	for _, s := range seq.steps {
		if n, ok := s.(Node); ok {
			nodes = append(nodes, n)
		}
	}
	return nodes
}

// Validate checks the sequence's structural integrity and recurses into its
// steps. Cross-tree concerns (output-name collisions across the whole
// workflow) are not checked here — that's the package-level Validate's job.
func (seq *sequence) Validate() error {
	var errs []error
	if strings.TrimSpace(seq.name) == "" {
		errs = append(errs, fmt.Errorf("sequence: name is required"))
	}
	if len(seq.steps) == 0 {
		errs = append(errs, fmt.Errorf("sequence %s: at least one step is required", seq.name))
	}
	for _, step := range seq.steps {
		if v, ok := step.(interface{ Validate() error }); ok {
			if err := v.Validate(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// clone returns an independent copy, including deep-copies of children.
func (seq *sequence) clone() *sequence {
	cp := &sequence{
		name:        seq.name,
		override:    seq.override,
		supervision: seq.supervision,
	}
	for _, s := range seq.steps {
		cp.steps = append(cp.steps, s.clone())
	}
	return cp
}

// Execute runs each Step in declared order. Self-validates first (recursing
// into every step), so invoking a sequence standalone (without going through
// Workflow.Execute) still catches structural problems early.
//
// State binding (resolving inputs / defaults) is the workflow's job. When
// invoking a sequence standalone, the caller supplies whatever State the
// sequence's steps will read from.
func (seq *sequence) Execute(ctx context.Context, rt *Runtime, state *State) (err error) {
	if err = seq.Validate(); err != nil {
		return err
	}

	ctx, end := trace(ctx, "sequence.run", attribute.String("sequence", seq.name))
	defer end(&err)

	// Apply this sequence's overrides on top of the parent's runtime; each
	// step inside the sequence sees this merged runtime as its parent.
	rt = rt.merge(seq.override)

	// Propagate supervision: if the sequence sets its own mode, that flows
	// to children; otherwise children inherit whatever the workflow set.
	ctx = context.WithValue(ctx, ctxSupervision, effectiveSupervision(ctx, seq.supervision))

	for _, step := range seq.steps {
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = step.Execute(ctx, rt, state); err != nil {
			return err
		}
	}
	return nil
}
