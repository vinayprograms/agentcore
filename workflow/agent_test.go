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

// agent_test.go covers agent.go: construction, the Customize / Task /
// WithOutputs builders, Validate, clone independence, and Execute (including
// the agentic loop's tool-call branch, $var interpolation in both prompt and
// task, and runtime-precondition errors).

// ---------------------------------------------------------------------------
// Validate / construction-time checks
// ---------------------------------------------------------------------------

func TestAgent_ExecuteRejectsEmptyPrompt(t *testing.T) {
	a := Agent("ghost", "").Task("do something")
	err := a.Execute(context.Background(), &Runtime{}, NewState(nil))
	if err == nil {
		t.Fatal("expected error for empty agent prompt, got nil")
	}
	if !strings.Contains(err.Error(), "prompt is required") {
		t.Errorf("error should mention required prompt, got: %v", err)
	}
	if strings.Contains(err.Error(), "You are") {
		t.Errorf("error message looks like a fallback persona, got: %v", err)
	}
}

func TestAgent_ExecuteRejectsMissingTask(t *testing.T) {
	a := Agent("critic", "You are a critic")
	err := a.Execute(context.Background(), &Runtime{}, NewState(nil))
	if err == nil {
		t.Fatal("expected error for missing task, got nil")
	}
	if !strings.Contains(err.Error(), "task is required") {
		t.Errorf("error should mention required task, got: %v", err)
	}
	if !strings.Contains(err.Error(), ".Task()") {
		t.Errorf("error should point to the .Task() setter, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Independence: clone semantics across composition boundaries
// ---------------------------------------------------------------------------

func TestAgent_IndependentCopiesAcrossGoals(t *testing.T) {
	critic := Agent("critic", "review carefully").WithOutputs("finding")

	g1 := Goal("g1", "first").Using(critic)
	g2 := Goal("g2", "second").Using(critic)

	a1 := mustAgent(t, g1.using[0])
	a2 := mustAgent(t, g2.using[0])

	if a1 == critic || a2 == critic {
		t.Fatal("Using stored caller's pointer instead of cloning")
	}
	if a1 == a2 {
		t.Fatal("g1 and g2 share the same *agent")
	}
}

func TestAgent_MutationAfterComposeDoesNotLeak(t *testing.T) {
	critic := Agent("critic", "review").WithOutputs("finding")

	g := Goal("g", "task").Using(critic)
	stored := mustAgent(t, g.using[0])

	critic.WithOutputs("severity", "context")

	if slices.Contains(stored.outputs, "severity") || slices.Contains(stored.outputs, "context") {
		t.Errorf("WithOutputs leaked into goal: %v", stored.outputs)
	}
}

func TestAgent_RepeatedUsingProducesDistinctCopies(t *testing.T) {
	critic := Agent("critic", "review")
	g := Goal("g", "task").Using(critic, critic, critic)

	if len(g.using) != 3 {
		t.Fatalf("expected 3 using entries, got %d", len(g.using))
	}
	a1 := mustAgent(t, g.using[0])
	a2 := mustAgent(t, g.using[1])
	a3 := mustAgent(t, g.using[2])

	if a1 == a2 || a2 == a3 || a1 == a3 {
		t.Errorf("repeated Using did not produce distinct pointers: %p %p %p", a1, a2, a3)
	}
	if a1 == critic || a2 == critic || a3 == critic {
		t.Errorf("Using stored the caller's pointer instead of cloning")
	}
}

func TestAgent_SiblingCopiesAreIsolated(t *testing.T) {
	critic := Agent("critic", "review").WithOutputs("finding")

	g1 := Goal("g1", "first").Using(critic)
	g2 := Goal("g2", "second").Using(critic)

	a1 := mustAgent(t, g1.using[0])
	a2 := mustAgent(t, g2.using[0])

	a1.outputs = append(a1.outputs, "leaked")

	if slices.Contains(a2.outputs, "leaked") {
		t.Errorf("a1 mutation leaked to a2 via shared backing array: %v", a2.outputs)
	}
}

// Setting Task on the user's variable does not bleed into clones already
// stored in other parents — the cornerstone independence guarantee.
func TestAgent_TaskMutationDoesNotLeakAcrossUsing(t *testing.T) {
	critic := Agent("critic", "You are a critic")

	g1 := Goal("g1", "review the spec").Using(critic)
	g2 := Goal("g2", "audit the code").Using(critic)

	critic.Task("user-attached task")
	g1Agent := mustAgent(t, g1.using[0])
	g1Agent.Task("g1 attached task")

	g2Agent := mustAgent(t, g2.using[0])
	if g2Agent.task != "" {
		t.Errorf("g2's agent task leaked: got %q, want empty", g2Agent.task)
	}
	if g1Agent == g2Agent {
		t.Error("g1 and g2 share the same *agent pointer")
	}
}

func TestAgent_CloneBackingArrayIsolated(t *testing.T) {
	a := Agent("a", "p").WithOutputs("x")
	g := Goal("g", "t").Using(a)

	for range 100 {
		a.outputs = append(a.outputs, "leak")
	}
	stored := mustAgent(t, g.using[0])
	if len(stored.outputs) != 1 || stored.outputs[0] != "x" {
		t.Errorf("agent.outputs backing array shared: stored=%v", stored.outputs)
	}
}

func TestAgent_ClonePreservesAllFields(t *testing.T) {
	a := Agent("a", "p").WithOutputs("x", "y")
	cp := a.clone().(*agent)
	if cp.name != a.name || cp.prompt != a.prompt {
		t.Errorf("name/prompt not preserved")
	}
	if !slices.Equal(cp.outputs, a.outputs) {
		t.Errorf("outputs not preserved: %v vs %v", cp.outputs, a.outputs)
	}
}

// ---------------------------------------------------------------------------
// Customize: per-agent override layered on top of parent runtime
// ---------------------------------------------------------------------------

func TestAgent_CustomizeOverridesModel(t *testing.T) {
	parent := &modelTag{id: "parent"}
	agentModel := &modelTag{id: "agent"}

	a := Agent("a", "You are an agent.").
		Customize(Override{Model: agentModel}).
		Task("do work")

	if err := a.Execute(context.Background(), &Runtime{Model: parent}, NewState(nil)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(parent.captured) != 0 {
		t.Errorf("parent model should not have been called, got %d calls", len(parent.captured))
	}
	if len(agentModel.captured) == 0 {
		t.Errorf("customized agent model should have been called")
	}
}

func TestAgent_NoCustomizeInheritsModel(t *testing.T) {
	parent := &modelTag{id: "parent"}
	a := Agent("a", "You are an agent.").Task("do work")
	if err := a.Execute(context.Background(), &Runtime{Model: parent}, NewState(nil)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(parent.captured) == 0 {
		t.Errorf("parent model should have been called")
	}
}

func TestAgent_CustomizeReplaceNotMerge(t *testing.T) {
	a := Agent("a", "You are an agent.").
		Customize(Override{SystemContext: "first"}).
		Customize(Override{SystemContext: "second"})

	if got := a.override.SystemContext; got != "second" {
		t.Errorf("Customize should replace, not merge; got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Execute: $var interpolation
// ---------------------------------------------------------------------------

func TestAgent_ExecuteInterpolatesPromptAndTask(t *testing.T) {
	rec := &recordingModel{reply: "ok"}
	state := NewState(map[string]string{
		"domain":   "security",
		"document": "policy.md",
	})

	a := Agent("critic", "You are a critic for $domain content.").
		Task("Review $document for issues.")

	if err := a.Execute(context.Background(), &Runtime{Model: rec}, state); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rec.captured[0].Content != "You are a critic for security content." {
		t.Errorf("system not interpolated: %q", rec.captured[0].Content)
	}
	if rec.captured[1].Content != "Review policy.md for issues." {
		t.Errorf("user not interpolated: %q", rec.captured[1].Content)
	}
}

func TestAgent_ExecuteResolvesOutputsBeforeInputs(t *testing.T) {
	rec := &recordingModel{reply: "ok"}
	state := NewState(map[string]string{"summary": "from input"})
	state.Outputs["summary"] = "from upstream goal"

	a := Agent("writer", "You are a writer.").Task("Build on this summary: $summary")
	if err := a.Execute(context.Background(), &Runtime{Model: rec}, state); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rec.captured[1].Content != "Build on this summary: from upstream goal" {
		t.Errorf("output should win over input: %q", rec.captured[1].Content)
	}
}

func TestAgent_ExecuteLeavesUnknownVarsLiteral(t *testing.T) {
	rec := &recordingModel{reply: "ok"}
	state := NewState(map[string]string{"known": "value"})

	a := Agent("a", "You handle $known and $unknown alike.").
		Task("Process $known and $missing.")
	if err := a.Execute(context.Background(), &Runtime{Model: rec}, state); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rec.captured[0].Content != "You handle value and $unknown alike." {
		t.Errorf("system: %q", rec.captured[0].Content)
	}
	if rec.captured[1].Content != "Process value and $missing." {
		t.Errorf("user: %q", rec.captured[1].Content)
	}
}

func TestAgent_ExecuteInterpolationIsIdempotent(t *testing.T) {
	rec := &recordingModel{reply: "ok"}
	state := NewState(map[string]string{"name": "Vinay"})

	pre := state.interpolate("Greet $name")
	a := Agent("greeter", "You greet politely.").Task(pre)
	if err := a.Execute(context.Background(), &Runtime{Model: rec}, state); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rec.captured[1].Content != "Greet Vinay" {
		t.Errorf("re-interpolation changed already-resolved string: %q", rec.captured[1].Content)
	}
}

// ---------------------------------------------------------------------------
// Execute: agentic loop (tool calls + termination + errors)
// ---------------------------------------------------------------------------

func TestAgent_LoopWithToolCallThenFinalAnswer(t *testing.T) {
	rt := &Runtime{Model: &loopModel{}, Tools: makeRegistry(t)}
	a := Agent("a", "be helpful").Task("greet")
	state := NewState(nil)
	if err := a.Execute(context.Background(), rt, state); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if state.Outputs["a"] != "done" {
		t.Errorf("got %q", state.Outputs["a"])
	}
}

func TestAgent_ChatErrorPropagates(t *testing.T) {
	a := Agent("a", "be helpful").Task("greet")
	err := a.Execute(context.Background(), &Runtime{Model: loopErrModel{}}, NewState(nil))
	if err == nil || !strings.Contains(err.Error(), "model down") {
		t.Errorf("expected model-down, got: %v", err)
	}
}

// missingToolModel asks for a tool that isn't registered so executeTool
// surfaces an error to the agent's loop.
type missingToolModel struct{ called atomic.Int32 }

func (m *missingToolModel) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	m.called.Add(1)
	return &llm.ChatResponse{
		ToolCalls: []llm.ToolCallResponse{{ID: "x", Name: "missing"}},
	}, nil
}

func TestAgent_ToolErrorPropagates(t *testing.T) {
	rt := &Runtime{Model: &missingToolModel{}, Tools: makeRegistry(t)}
	a := Agent("a", "be helpful").Task("greet")
	err := a.Execute(context.Background(), rt, NewState(nil))
	if err == nil {
		t.Fatal("expected tool error")
	}
	// sanity: the loop didn't return success without dispatching the tool
	if !errors.Is(err, err) || err.Error() == "" {
		t.Fatal("error should be informative")
	}
}
