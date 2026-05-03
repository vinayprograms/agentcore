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

// goal_test.go covers goal.go: construction, the Using / WithOutputs /
// Customize / Supervise / SuperviseByHuman builders, Validate (and recursive
// validation through Using), clone independence, and the full Execute
// surface — single-agent loop, fan-out with synthesis, structured outputs,
// error paths.

// ---------------------------------------------------------------------------
// Validate / Execute self-validation
// ---------------------------------------------------------------------------

func TestGoal_ExecuteRejectsEmptyDescription(t *testing.T) {
	g := Goal("blank", "")
	err := g.Execute(context.Background(), &Runtime{}, NewState(nil))
	if err == nil || !strings.Contains(err.Error(), "description is required") {
		t.Errorf("expected description-required, got: %v", err)
	}
}

func TestGoal_ExecuteSelfValidatesSubtree(t *testing.T) {
	bad := Agent("", "")
	g := Goal("g", "do work").Using(bad)

	err := g.Execute(context.Background(), &Runtime{Model: stubModel{}}, NewState(nil))
	if err == nil || !strings.Contains(err.Error(), "agent: name is required") {
		t.Errorf("standalone goal.Execute should validate its subtree; got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Supervision setters
// ---------------------------------------------------------------------------

func TestGoal_SuperviseSetsLLM(t *testing.T) {
	g := Goal("g", "x").Supervise()
	if g.supervision != byLLM {
		t.Errorf("got %v", g.supervision)
	}
}

func TestGoal_SuperviseByHumanSetsByHuman(t *testing.T) {
	g := Goal("g", "x").SuperviseByHuman()
	if g.supervision != byHuman {
		t.Errorf("got %v", g.supervision)
	}
}

// ---------------------------------------------------------------------------
// Independence: clone semantics
// ---------------------------------------------------------------------------

func TestGoal_IndependentCopiesAcrossSequences(t *testing.T) {
	g := Goal("g", "task").WithOutputs("answer")
	r1 := Sequence("r1").Steps(g)
	r2 := Sequence("r2").Steps(g)

	g1 := mustGoal(t, r1.steps[0])
	g2 := mustGoal(t, r2.steps[0])

	if g1 == g || g2 == g {
		t.Fatal("Sequence.Steps stored caller's *goal pointer")
	}
	if g1 == g2 {
		t.Fatal("two sequences share the same *goal pointer")
	}
}

func TestGoal_MutationAfterComposeDoesNotLeak(t *testing.T) {
	a := Agent("a", "do work")
	g := Goal("g", "task").Using(a)
	r := Sequence("r").Steps(g)
	stored := mustGoal(t, r.steps[0])

	g.Using(Agent("b", "extra"))
	g.WithOutputs("late")

	if len(stored.using) != 1 {
		t.Errorf("Using mutation leaked: stored has %d, want 1", len(stored.using))
	}
	if slices.Contains(stored.outputs, "late") {
		t.Errorf("WithOutputs mutation leaked: %v", stored.outputs)
	}
}

func TestGoal_CloneBackingArraysIsolated(t *testing.T) {
	t.Run("using", func(t *testing.T) {
		g := Goal("g", "t").Using(Agent("a", "p"))
		r := Sequence("r").Steps(g)

		for range 100 {
			g.Using(Agent("late", "p"))
		}
		stored := mustGoal(t, r.steps[0])
		if len(stored.using) != 1 {
			t.Errorf("goal.using backing array shared: stored has %d agents", len(stored.using))
		}
	})

	t.Run("outputs", func(t *testing.T) {
		g := Goal("g", "t").WithOutputs("a")
		r := Sequence("r").Steps(g)

		for range 100 {
			g.WithOutputs("leak")
		}
		stored := mustGoal(t, r.steps[0])
		if len(stored.outputs) != 1 {
			t.Errorf("goal.outputs backing array shared: stored=%v", stored.outputs)
		}
	})
}

func TestGoal_ClonePreservesAllFields(t *testing.T) {
	g := Goal("g", "desc").
		WithOutputs("a", "b").
		Using(Agent("inner", "p")).
		Supervise()
	cp := g.clone().(*goal)
	if cp.name != g.name || cp.description != g.description {
		t.Errorf("name/description not preserved")
	}
	if !slices.Equal(cp.outputs, g.outputs) {
		t.Errorf("outputs not preserved")
	}
	if len(cp.using) != 1 {
		t.Errorf("using count not preserved")
	}
	if cp.supervision != byLLM {
		t.Errorf("supervision state not preserved: got %d, want byLLM", cp.supervision)
	}
}

// ---------------------------------------------------------------------------
// Customize: SystemContext appends across levels
// ---------------------------------------------------------------------------

func TestGoal_CustomizeAppendsSystemContext(t *testing.T) {
	rec := &modelTag{id: "rec"}
	parent := &Runtime{Model: rec, SystemContext: "workflow-wide context"}

	g := Goal("g", "do something").
		Customize(Override{SystemContext: "goal-specific context"})

	if err := g.Execute(context.Background(), parent, NewState(nil)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(rec.captured) == 0 {
		t.Fatal("model was not called")
	}
	system := rec.captured[0].Content
	for _, want := range []string{"workflow-wide context", "goal-specific context"} {
		if !contains(system, want) {
			t.Errorf("system prompt missing %q\n got: %q", want, system)
		}
	}
}

func TestGoal_CustomizeSystemContextAppendsAcrossLevels(t *testing.T) {
	rec := &modelTag{id: "rec"}
	parent := &Runtime{Model: rec, SystemContext: "L0"}

	a := Agent("a", "You are an agent.").
		Customize(Override{SystemContext: "L2"}).
		Task("do work")
	g := Goal("g", "task").
		Customize(Override{SystemContext: "L1"}).
		Using(a)

	if err := g.Execute(context.Background(), parent, NewState(nil)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var agentSystem string
	for i := 0; i < len(rec.captured); i += 2 {
		if rec.captured[i].Role == "system" && contains(rec.captured[i].Content, "L2") {
			agentSystem = rec.captured[i].Content
			break
		}
	}
	if agentSystem == "" {
		t.Fatal("agent's system message not found")
	}
	for _, want := range []string{"L0", "L1", "L2"} {
		if !contains(agentSystem, want) {
			t.Errorf("agent system message missing layer %q\n got: %q", want, agentSystem)
		}
	}
}

func TestGoal_CustomizeToolsPropagatesIntoFanOut(t *testing.T) {
	parentRT := &Runtime{Model: &modelTag{id: "parent"}}
	reg := makeRegistry(t)

	var seen *Runtime
	rc := &runtimeCapture{dest: &seen}

	g := Goal("g", "task").
		Customize(Override{Tools: reg}).
		Using(rc)

	if err := g.Execute(context.Background(), parentRT, NewState(nil)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if seen == nil {
		t.Fatal("runtimeCapture never ran")
	}
	if seen.Tools != reg {
		t.Errorf("Tools override did not propagate into fan-out; want %p, got %p", reg, seen.Tools)
	}
}

// ---------------------------------------------------------------------------
// Execute: single-agent loop
// ---------------------------------------------------------------------------

func TestGoal_LoopWithToolCallThenFinalAnswer(t *testing.T) {
	rt := &Runtime{Model: &loopModel{}, Tools: makeRegistry(t)}
	g := Goal("g", "do work")
	state := NewState(nil)
	if err := g.Execute(context.Background(), rt, state); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if state.Outputs["g"] != "done" {
		t.Errorf("output: %q", state.Outputs["g"])
	}
}

func TestGoal_LoopReportsModelError(t *testing.T) {
	g := Goal("g", "do work")
	err := g.Execute(context.Background(), &Runtime{Model: loopErrModel{}}, NewState(nil))
	if err == nil || !strings.Contains(err.Error(), "model down") {
		t.Errorf("expected model-down error, got: %v", err)
	}
}

type goalToolErrModel struct{}

func (goalToolErrModel) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{
		ToolCalls: []llm.ToolCallResponse{{ID: "x", Name: "missing"}},
	}, nil
}

func TestGoal_LoopReportsToolError(t *testing.T) {
	rt := &Runtime{Model: goalToolErrModel{}, Tools: makeRegistry(t)}
	err := Goal("g", "do work").Execute(context.Background(), rt, NewState(nil))
	if err == nil {
		t.Fatal("expected tool error")
	}
}

// ---------------------------------------------------------------------------
// Execute: fan-out
// ---------------------------------------------------------------------------

func TestGoal_FanOutSynthesizesMultipleAgents(t *testing.T) {
	model := &countingModel{reply: "synthesized"}
	rt := &Runtime{Model: model}
	g := Goal("g", "task").Using(Agent("a1", "p1"), Agent("a2", "p2"))
	state := NewState(nil)
	if err := g.Execute(context.Background(), rt, state); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if state.Outputs["g"] != "synthesized" {
		t.Errorf("got %q", state.Outputs["g"])
	}
}

type fanOutSynErrModel struct{ calls atomic.Int32 }

func (m *fanOutSynErrModel) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	if m.calls.Add(1) <= 2 {
		return &llm.ChatResponse{Content: "ok"}, nil
	}
	return nil, errors.New("syn-fail")
}

func TestGoal_FanOutSynthesisError(t *testing.T) {
	rt := &Runtime{Model: &fanOutSynErrModel{}}
	g := Goal("g", "task").Using(Agent("a1", "p1"), Agent("a2", "p2"))
	err := g.Execute(context.Background(), rt, NewState(nil))
	if err == nil || !strings.Contains(err.Error(), "syn-fail") {
		t.Errorf("expected syn-fail, got: %v", err)
	}
}

type fanOutAgentErrModel struct{}

func (fanOutAgentErrModel) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	return nil, errors.New("agent-fail")
}

func TestGoal_FanOutChildAgentError(t *testing.T) {
	rt := &Runtime{Model: fanOutAgentErrModel{}}
	g := Goal("g", "task").Using(Agent("a1", "p1"))
	err := g.Execute(context.Background(), rt, NewState(nil))
	if err == nil || !strings.Contains(err.Error(), "agent-fail") {
		t.Errorf("expected agent-fail, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Execute: structured outputs
// ---------------------------------------------------------------------------

func TestGoal_WithOutputsParsesJSON(t *testing.T) {
	rt := &Runtime{Model: &countingModel{reply: `{"a":"x","b":2,"c":"y"}`}}
	g := Goal("g", "do").WithOutputs("a", "b")
	state := NewState(nil)
	if err := g.Execute(context.Background(), rt, state); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if state.Outputs["a"] != "x" {
		t.Errorf("a=%q", state.Outputs["a"])
	}
	if state.Outputs["b"] != "2" {
		t.Errorf("b=%q (numeric values are re-marshalled)", state.Outputs["b"])
	}
}
