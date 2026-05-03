package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// sequence_test.go covers sequence.go: construction, the Steps / Customize /
// Supervise / SuperviseByHuman builders, Validate (and recursion through
// every step), clone independence, and Execute (ordered iteration, context
// cancellation, supervision-context propagation).

// ---------------------------------------------------------------------------
// Validate / Execute self-validation
// ---------------------------------------------------------------------------

func TestSequence_ValidateRejectsEmptySteps(t *testing.T) {
	seq := Sequence("solo")
	err := seq.Execute(context.Background(), &Runtime{}, NewState(nil))
	if err == nil || !strings.Contains(err.Error(), "sequence solo: at least one step is required") {
		t.Errorf("got: %v", err)
	}
}

func TestSequence_ExecuteSelfValidatesSubtree(t *testing.T) {
	bad := Goal("", "")
	seq := Sequence("main").Steps(bad)
	err := seq.Execute(context.Background(), &Runtime{Model: stubModel{}}, NewState(nil))
	if err == nil || !strings.Contains(err.Error(), "goal: name is required") {
		t.Errorf("got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Setters
// ---------------------------------------------------------------------------

func TestSequence_CustomizeAndSuperviseByHuman(t *testing.T) {
	s := Sequence("main").
		Customize(Override{SystemContext: "extra"}).
		SuperviseByHuman()
	if s.override.SystemContext != "extra" {
		t.Errorf("Customize did not stick")
	}
	if s.supervision != byHuman {
		t.Errorf("SuperviseByHuman did not stick: %v", s.supervision)
	}
}

func TestSequence_SuperviseSetsLLM(t *testing.T) {
	s := Sequence("main").Supervise()
	if s.supervision != byLLM {
		t.Errorf("got %v", s.supervision)
	}
}

// ---------------------------------------------------------------------------
// Independence
// ---------------------------------------------------------------------------

func TestSequence_IndependentCopiesAcrossWorkflows(t *testing.T) {
	r := Sequence("main").Steps(Goal("g", "t"))
	w1 := New("w1").Add(r)
	w2 := New("w2").Add(r)

	r1 := w1.sequences[0]
	r2 := w2.sequences[0]

	if r1 == r || r2 == r {
		t.Fatal("workflow.Add stored caller's *sequence pointer")
	}
	if r1 == r2 {
		t.Fatal("two workflows share the same *sequence pointer")
	}
}

func TestSequence_MutationAfterComposeDoesNotLeak(t *testing.T) {
	g := Goal("g", "task")
	r := Sequence("main").Steps(g)
	w := New("w").Add(r)
	stored := w.sequences[0]

	r.Steps(Goal("extra", "added later"))
	r.Supervise()

	if len(stored.steps) != 1 {
		t.Errorf("Steps mutation leaked: stored has %d, want 1", len(stored.steps))
	}
	if stored.supervision != notSupervised {
		t.Errorf("Supervise mutation leaked: state=%d", stored.supervision)
	}
}

func TestSequence_CloneBackingArrayIsolated(t *testing.T) {
	r := Sequence("r").Steps(Goal("g", "t"))
	w := New("w").Add(r)

	for range 100 {
		r.Steps(Goal("late", "t"))
	}
	stored := w.sequences[0]
	if len(stored.steps) != 1 {
		t.Errorf("sequence.steps backing array shared: stored has %d steps", len(stored.steps))
	}
}

func TestSequence_ClonePreservesAllFields(t *testing.T) {
	r := Sequence("r").Steps(Goal("g", "t")).Supervise()
	cp := r.clone()
	if cp.name != r.name {
		t.Errorf("name not preserved")
	}
	if len(cp.steps) != 1 {
		t.Errorf("steps not preserved")
	}
	if cp.supervision != byLLM {
		t.Errorf("supervision state not preserved: got %d, want byLLM", cp.supervision)
	}
}

// ---------------------------------------------------------------------------
// Execute
// ---------------------------------------------------------------------------

func TestSequence_CancelledContextStopsBeforeStep(t *testing.T) {
	rt := &Runtime{Model: &countingModel{}}
	seq := Sequence("main").Steps(Goal("g", "do it"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := seq.Execute(ctx, rt, NewState(nil)); !errors.Is(err, context.Canceled) {
		t.Errorf("expected cancellation, got: %v", err)
	}
}

// Sequence-level supervision flows down through ctxSupervision so child
// goals see the inherited mode without setting their own.
func TestSequence_PropagatesSupervisionToChildren(t *testing.T) {
	var seenCtx context.Context
	rc := &capCtx{dest: &seenCtx}
	seq := Sequence("main").Supervise().Steps(rc)

	rt := &Runtime{Model: &countingModel{}, Supervisor: &fakeSupervisor{
		reconcile: &ReconcileResult{Escalate: false},
	}}
	if err := seq.Execute(context.Background(), rt, NewState(nil)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if seenCtx == nil {
		t.Fatal("step never ran")
	}
	v, _ := seenCtx.Value(ctxSupervision).(supervision)
	if v != byLLM {
		t.Errorf("child saw supervision %v, want byLLM", v)
	}
}

// capCtx is a Step that records the context it was called with via a shared
// destination pointer — used to verify ctxSupervision propagation.
type capCtx struct {
	dest *context.Context
}

func (c *capCtx) Execute(ctx context.Context, rt *Runtime, state *State) error {
	*c.dest = ctx
	return nil
}
func (c *capCtx) Children() []Node { return nil }
func (c *capCtx) Name() string     { return "capCtx" }
func (c *capCtx) Kind() Kind       { return kindOf(c) }
func (c *capCtx) clone() Step {
	cp := *c
	return &cp
}
