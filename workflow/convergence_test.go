package workflow

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vinayprograms/agentkit/llm"
)

// convergence_test.go covers convergence.go: construction, the Using /
// WithOutputs / Customize / Supervise / SuperviseByHuman builders, Validate,
// clone independence, and the iteration loop in both single-agent and
// fan-out modes (CONVERGED marker, cap-reached, errors, structured outputs).

// ---------------------------------------------------------------------------
// Validate
// ---------------------------------------------------------------------------

func TestConvergence_ValidateEmptyName(t *testing.T) {
	c := Convergence("", "polish", 2)
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Errorf("expected name-required, got: %v", err)
	}
}

func TestConvergence_ExecuteRejectsEmptyDescription(t *testing.T) {
	c := Convergence("blank", "", 3)
	err := c.Execute(context.Background(), &Runtime{}, NewState(nil))
	if err == nil || !strings.Contains(err.Error(), "description is required") {
		t.Errorf("got: %v", err)
	}
}

func TestConvergence_ExecuteRejectsZeroWithin(t *testing.T) {
	c := Convergence("c", "polish", 0)
	err := c.Execute(context.Background(), &Runtime{}, NewState(nil))
	if err == nil || !strings.Contains(err.Error(), "'within' must be > 0") {
		t.Errorf("got: %v", err)
	}
}

func TestConvergence_ExecuteSelfValidatesSubtree(t *testing.T) {
	bad := Agent("", "")
	c := Convergence("c", "polish", 3).Using(bad)
	err := c.Execute(context.Background(), &Runtime{Model: stubModel{}}, NewState(nil))
	if err == nil || !strings.Contains(err.Error(), "agent: name is required") {
		t.Errorf("got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Setters
// ---------------------------------------------------------------------------

func TestConvergence_CustomizeAndSupervise(t *testing.T) {
	c := Convergence("c", "polish", 2).
		Customize(Override{SystemContext: "extra"}).
		Supervise()
	if c.override.SystemContext != "extra" {
		t.Errorf("Customize did not stick")
	}
	if c.supervision != byLLM {
		t.Errorf("Supervise did not stick: %v", c.supervision)
	}
}

func TestConvergence_SuperviseByHumanSetsByHuman(t *testing.T) {
	c := Convergence("c", "polish", 2).SuperviseByHuman()
	if c.supervision != byHuman {
		t.Errorf("got %v", c.supervision)
	}
}

// ---------------------------------------------------------------------------
// Independence
// ---------------------------------------------------------------------------

func TestConvergence_IndependentCopiesAcrossSequences(t *testing.T) {
	c := Convergence("refine", "polish", 5)
	r1 := Sequence("r1").Steps(c)
	r2 := Sequence("r2").Steps(c)

	c1 := mustConvergence(t, r1.steps[0])
	c2 := mustConvergence(t, r2.steps[0])

	if c1 == c || c2 == c || c1 == c2 {
		t.Fatal("Convergence cloning failed across sequences")
	}
}

func TestConvergence_MutationAfterComposeDoesNotLeak(t *testing.T) {
	c := Convergence("refine", "polish", 5).WithOutputs("final")
	r := Sequence("r").Steps(c)
	stored := mustConvergence(t, r.steps[0])

	c.Using(Agent("late", "joiner"))
	c.WithOutputs("extra")
	c.within = 999

	if len(stored.using) != 0 {
		t.Errorf("Using mutation leaked: stored has %d, want 0", len(stored.using))
	}
	if slices.Contains(stored.outputs, "extra") {
		t.Errorf("WithOutputs mutation leaked: %v", stored.outputs)
	}
	if stored.within != 5 {
		t.Errorf("within mutation leaked: stored=%d, want 5", stored.within)
	}
}

func TestConvergence_CloneBackingArrayIsolated(t *testing.T) {
	c := Convergence("c", "t", 3).Using(Agent("a", "p"))
	r := Sequence("r").Steps(c)

	for range 100 {
		c.Using(Agent("late", "p"))
	}
	stored := mustConvergence(t, r.steps[0])
	if len(stored.using) != 1 {
		t.Errorf("convergence.using backing array shared: stored has %d", len(stored.using))
	}
}

func TestConvergence_ClonePreservesAllFields(t *testing.T) {
	c := Convergence("c", "desc", 7).WithOutputs("x").SuperviseByHuman()
	cp := c.clone().(*convergence)
	if cp.name != c.name || cp.description != c.description {
		t.Errorf("name/description not preserved")
	}
	if cp.within != 7 {
		t.Errorf("within not preserved: %d", cp.within)
	}
	if !slices.Equal(cp.outputs, c.outputs) {
		t.Errorf("outputs not preserved")
	}
	if cp.supervision != byHuman {
		t.Errorf("supervision state not preserved: got %d, want byHuman", cp.supervision)
	}
}

// ---------------------------------------------------------------------------
// Execute: single-agent iteration loop
// ---------------------------------------------------------------------------

type convergedAfterTwoModel struct{ calls atomic.Int32 }

func (m *convergedAfterTwoModel) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	if m.calls.Add(1) == 1 {
		return &llm.ChatResponse{Content: "draft 1"}, nil
	}
	return &llm.ChatResponse{Content: "draft 2 CONVERGED"}, nil
}

func TestConvergence_StopsAtMarker(t *testing.T) {
	c := Convergence("c", "polish", 5)
	rt := &Runtime{Model: &convergedAfterTwoModel{}}
	state := NewState(nil)
	if err := c.Execute(context.Background(), rt, state); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Output is the last substantive iteration before CONVERGED.
	if state.Outputs["c"] != "draft 1" {
		t.Errorf("got %q", state.Outputs["c"])
	}
}

func TestConvergence_HitsCap(t *testing.T) {
	rt := &Runtime{Model: &countingModel{reply: "still drafting"}}
	c := Convergence("c", "polish", 2)
	state := NewState(nil)
	if err := c.Execute(context.Background(), rt, state); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if state.Failures["c"] != 2 {
		t.Errorf("expected cap-reached failure record, got %d", state.Failures["c"])
	}
}

func TestConvergence_IterateError(t *testing.T) {
	rt := &Runtime{Model: loopErrModel{}}
	c := Convergence("c", "polish", 2)
	err := c.Execute(context.Background(), rt, NewState(nil))
	if err == nil || !strings.Contains(err.Error(), "model down") {
		t.Errorf("got: %v", err)
	}
}

type convergeToolModel struct{ calls atomic.Int32 }

func (m *convergeToolModel) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	if m.calls.Add(1) == 1 {
		return &llm.ChatResponse{
			ToolCalls: []llm.ToolCallResponse{
				{ID: "1", Name: "echo", Args: map[string]any{"text": "x"}},
			},
		}, nil
	}
	return &llm.ChatResponse{Content: "CONVERGED"}, nil
}

func TestConvergence_IterateSingleToolCallBranch(t *testing.T) {
	rt := &Runtime{Model: &convergeToolModel{}, Tools: makeRegistry(t)}
	c := Convergence("c", "polish", 2)
	if err := c.Execute(context.Background(), rt, NewState(nil)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

type convergeToolErrModel struct{}

func (convergeToolErrModel) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{
		ToolCalls: []llm.ToolCallResponse{{ID: "1", Name: "missing"}},
	}, nil
}

func TestConvergence_IterateSingleToolError(t *testing.T) {
	rt := &Runtime{Model: convergeToolErrModel{}, Tools: makeRegistry(t)}
	c := Convergence("c", "polish", 2)
	if err := c.Execute(context.Background(), rt, NewState(nil)); err == nil {
		t.Fatal("expected tool error")
	}
}

// ---------------------------------------------------------------------------
// Execute: fan-out iteration
// ---------------------------------------------------------------------------

type convergeFanOutModel struct{ calls atomic.Int32 }

func (m *convergeFanOutModel) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	switch m.calls.Add(1) {
	case 1, 2:
		return &llm.ChatResponse{Content: "iter-1-agent"}, nil
	case 3:
		return &llm.ChatResponse{Content: "iter-1-synth"}, nil
	default:
		return &llm.ChatResponse{Content: "CONVERGED"}, nil
	}
}

func TestConvergence_FanOutSynthesisAndConverge(t *testing.T) {
	rt := &Runtime{Model: &convergeFanOutModel{}}
	c := Convergence("c", "polish", 5).Using(Agent("a1", "p1"), Agent("a2", "p2"))
	state := NewState(nil)
	if err := c.Execute(context.Background(), rt, state); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(state.Outputs["c"], "iter-1-synth") {
		t.Errorf("expected synthesised first iteration as final, got: %q", state.Outputs["c"])
	}
}

type convergeFanOutSingleModel struct{ calls atomic.Int32 }

func (m *convergeFanOutSingleModel) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	if m.calls.Add(1) == 1 {
		return &llm.ChatResponse{Content: "first"}, nil
	}
	return &llm.ChatResponse{Content: "CONVERGED"}, nil
}

func TestConvergence_FanOutSingleAgentUnwrapsLabel(t *testing.T) {
	rt := &Runtime{Model: &convergeFanOutSingleModel{}}
	c := Convergence("c", "polish", 5).Using(Agent("solo", "p"))
	state := NewState(nil)
	if err := c.Execute(context.Background(), rt, state); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if state.Outputs["c"] != "first" {
		t.Errorf("expected unwrapped first iteration, got: %q", state.Outputs["c"])
	}
}

func TestConvergence_FanOutChildError(t *testing.T) {
	rt := &Runtime{Model: fanOutAgentErrModel{}}
	c := Convergence("c", "polish", 3).Using(Agent("a", "p"))
	err := c.Execute(context.Background(), rt, NewState(nil))
	if err == nil || !strings.Contains(err.Error(), "agent-fail") {
		t.Errorf("got: %v", err)
	}
}

type convergeFanOutSynErrModel struct{ calls atomic.Int32 }

func (m *convergeFanOutSynErrModel) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	if m.calls.Add(1) <= 2 {
		return &llm.ChatResponse{Content: "ok"}, nil
	}
	return nil, errors.New("conv-syn-fail")
}

func TestConvergence_FanOutSynthesisError(t *testing.T) {
	rt := &Runtime{Model: &convergeFanOutSynErrModel{}}
	c := Convergence("c", "polish", 3).Using(Agent("a1", "p1"), Agent("a2", "p2"))
	err := c.Execute(context.Background(), rt, NewState(nil))
	if err == nil || !strings.Contains(err.Error(), "conv-syn-fail") {
		t.Errorf("got: %v", err)
	}
}

func TestConvergence_StructuredOutputsParsedAtEnd(t *testing.T) {
	rt := &Runtime{Model: &countingModel{reply: `{"verdict":"ok","score":7}`}}
	c := Convergence("c", "polish", 1).WithOutputs("verdict")
	state := NewState(nil)
	if err := c.Execute(context.Background(), rt, state); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if state.Outputs["verdict"] != "ok" {
		t.Errorf("verdict=%q", state.Outputs["verdict"])
	}
}
