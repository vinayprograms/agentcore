package workflow

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/vinayprograms/agentcore/workflow/security"
	"github.com/vinayprograms/agentkit/llm"
)

// workflow_test.go covers workflow.go: New / Input / Add / Supervise(...) /
// Security / Scope / Children, the Execute pipeline (bind → Validate → guard
// build → sequence loop → events), bind, and the package-level Validate (all
// rules including supervision wiring and $var preflight).

// ---------------------------------------------------------------------------
// Construction & navigation
// ---------------------------------------------------------------------------

func TestWorkflow_IndependentInstances(t *testing.T) {
	w1 := New("w1").Input(Parameter{Name: "a"}, Parameter{Name: "b", Default: "B"})
	w2 := New("w2").Input(Parameter{Name: "c"})

	if len(w1.parameters) != 2 {
		t.Errorf("w1 expected 2 parameters, got %d", len(w1.parameters))
	}
	if len(w2.parameters) != 1 {
		t.Errorf("w2 expected 1 parameter, got %d", len(w2.parameters))
	}
	if w1.parameters[0].Default != "" {
		t.Errorf("w1 parameter 'a' should be required, got %q", w1.parameters[0].Default)
	}
	if w1.parameters[1].Default != "B" {
		t.Errorf("w1 parameter 'b' default missing: %q", w1.parameters[1].Default)
	}
}

func TestWorkflow_ChildrenAreSequencesOnly(t *testing.T) {
	w := New("w").
		Input(Parameter{Name: "a"}, Parameter{Name: "b", Default: "B"}).
		Add(Sequence("first"), Sequence("second"))

	children := w.Children()
	if len(children) != 2 {
		t.Fatalf("workflow.Children() returned %d, want 2", len(children))
	}
	for i, c := range children {
		if c.Kind() != "sequence" {
			t.Errorf("child %d: kind=%q, want sequence", i, c.Kind())
		}
	}
	if children[0].Name() != "first" || children[1].Name() != "second" {
		t.Errorf("children names: %q, %q", children[0].Name(), children[1].Name())
	}
}

func TestWorkflow_SuperviseSetsMode(t *testing.T) {
	w := New("w").Supervise()
	if w.supervision != byLLM {
		t.Errorf("got %v", w.supervision)
	}
}

func TestWorkflow_SuperviseByHumanSetsByHuman(t *testing.T) {
	w := New("w").SuperviseByHuman()
	if w.supervision != byHuman {
		t.Errorf("got %v", w.supervision)
	}
}

// ---------------------------------------------------------------------------
// Deep-tree independence
// ---------------------------------------------------------------------------

func TestWorkflow_DeepTreeFullIndependence(t *testing.T) {
	critic := Agent("critic", "review").WithOutputs("finding")
	review := Goal("review", "examine").Using(critic).WithOutputs("verdict")
	refine := Convergence("refine", "polish", 5).Using(critic).WithOutputs("final")
	main := Sequence("main").Steps(review, refine)

	w := New("test").
		Input(Parameter{Name: "topic"}, Parameter{Name: "style", Default: "concise"}).
		Add(main)

	critic.WithOutputs("LEAK")
	review.Using(Agent("late", "joiner"))
	review.WithOutputs("LEAK")
	refine.within = 999
	refine.WithOutputs("LEAK")
	main.Steps(Goal("extra", "added"))
	main.Supervise()

	storedRun := w.sequences[0]
	if storedRun.supervision != notSupervised {
		t.Error("sequence.Supervise leaked into workflow")
	}
	if len(storedRun.steps) != 2 {
		t.Errorf("sequence.Steps mutation leaked: stored has %d steps, want 2", len(storedRun.steps))
	}

	storedReview := mustGoal(t, storedRun.steps[0])
	if len(storedReview.using) != 1 {
		t.Errorf("review.Using leaked: stored has %d agents", len(storedReview.using))
	}
	if !slices.Equal(storedReview.outputs, []string{"verdict"}) {
		t.Errorf("review.outputs leaked: %v", storedReview.outputs)
	}

	storedRefine := mustConvergence(t, storedRun.steps[1])
	if storedRefine.within != 5 {
		t.Errorf("refine.within leaked: %d", storedRefine.within)
	}
	if !slices.Equal(storedRefine.outputs, []string{"final"}) {
		t.Errorf("refine.outputs leaked: %v", storedRefine.outputs)
	}

	a1 := mustAgent(t, storedReview.using[0])
	a2 := mustAgent(t, storedRefine.using[0])
	if a1 == a2 {
		t.Error("review and refine share the same critic copy")
	}
}

func TestWorkflow_AddBackingArrayIsolated(t *testing.T) {
	base := Sequence("base").Steps(Goal("g", "t"))
	w := New("w").Add(base)

	for range 100 {
		base.Steps(Goal("late", "t"))
	}
	stored := w.sequences[0]
	if len(stored.steps) != 1 {
		t.Errorf("workflow.Add did not isolate sequence.steps: stored has %d", len(stored.steps))
	}
}

// ---------------------------------------------------------------------------
// bind: parameter binding
// ---------------------------------------------------------------------------

func TestWorkflow_BindUsesProvidedAndDefault(t *testing.T) {
	rt := &Runtime{Model: &countingModel{reply: "ok"}}
	wf := New("w").
		Input(Parameter{Name: "topic"}, Parameter{Name: "style", Default: "concise"}).
		Add(Sequence("main").Steps(Goal("g", "summarize $topic in $style")))
	state, err := wf.Execute(context.Background(), rt, map[string]string{"topic": "go"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if state.Inputs["topic"] != "go" || state.Inputs["style"] != "concise" {
		t.Errorf("inputs not bound: %v", state.Inputs)
	}
}

func TestWorkflow_BindMissingRequiredParameter(t *testing.T) {
	rt := &Runtime{Model: &countingModel{}}
	wf := New("w").
		Input(Parameter{Name: "topic"}).
		Add(Sequence("main").Steps(Goal("g", "summarize $topic")))
	_, err := wf.Execute(context.Background(), rt, nil)
	if err == nil || !strings.Contains(err.Error(), "required parameter missing") {
		t.Errorf("got: %v", err)
	}
}

func TestWorkflow_BindRejectsDuplicateParameters(t *testing.T) {
	rt := &Runtime{Model: &countingModel{}}
	wf := New("w").
		Input(Parameter{Name: "x"}, Parameter{Name: "x"}).
		Add(Sequence("main").Steps(Goal("g", "do it")))
	_, err := wf.Execute(context.Background(), rt, map[string]string{"x": "1"})
	if err == nil || !strings.Contains(err.Error(), "duplicate parameter") {
		t.Errorf("got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Execute: events, error propagation, cancellation
// ---------------------------------------------------------------------------

func TestWorkflow_PreflightFailureFiresHook(t *testing.T) {
	hooks := &hookSink{}
	rt := &Runtime{Model: &countingModel{}, Hooks: hooks}
	wf := New("w").Add(Sequence("main").Steps(Goal("g", "use $missing")))
	if _, err := wf.Execute(context.Background(), rt, nil); err == nil {
		t.Fatal("expected preflight error")
	}
	for _, e := range hooks.events {
		if _, ok := e.(PreflightFailed); ok {
			return
		}
	}
	t.Errorf("expected PreflightFailed event")
}

func TestWorkflow_StepErrorPropagates(t *testing.T) {
	rt := &Runtime{Model: loopErrModel{}}
	wf := New("w").Add(Sequence("main").Steps(Goal("g", "do it")))
	if _, err := wf.Execute(context.Background(), rt, nil); err == nil {
		t.Fatal("expected step error")
	}
}

func TestWorkflow_CancelledContextStopsBeforeFirstSequence(t *testing.T) {
	rt := &Runtime{Model: &countingModel{}}
	wf := New("w").Add(Sequence("main").Steps(Goal("g", "do it")))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := wf.Execute(ctx, rt, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("expected cancellation, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Security integration
// ---------------------------------------------------------------------------

func TestWorkflow_SecurityResearchWithoutScopeFailsValidate(t *testing.T) {
	wf := New("w").
		Security(security.Research).
		Add(Sequence("main").Steps(Goal("g", "do something")))

	model := &scriptedModel{replies: []*llm.ChatResponse{{Content: "done"}}}
	_, err := wf.Execute(context.Background(), &Runtime{Model: model}, nil)
	if err == nil || !strings.Contains(err.Error(), "Research requires a non-empty scope") {
		t.Errorf("got: %v", err)
	}
}

func TestWorkflow_SecurityResearchWithScopePassesValidate(t *testing.T) {
	wf := New("w").
		Security(security.Research).
		Scope("auth-token-handling").
		Add(Sequence("main").Steps(Goal("g", "do something")))

	model := &scriptedModel{replies: []*llm.ChatResponse{{Content: "done"}}}
	state, err := wf.Execute(context.Background(), &Runtime{Model: model}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if state.Outputs["g"] != "done" {
		t.Errorf("unexpected output: %v", state.Outputs)
	}
}

func TestWorkflow_SecurityBuildsGuardWhenDeclared(t *testing.T) {
	var seen *Runtime
	wf := New("w").
		Security(security.Default).
		Add(Sequence("main").Steps(&envCapture{dest: &seen}))

	model := &scriptedModel{replies: []*llm.ChatResponse{{Content: "done"}}}
	if _, err := wf.Execute(context.Background(), &Runtime{Model: model}, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if seen == nil || seen.Guard == nil {
		t.Error("Security() declared but Execute did not attach a Guard")
	}
}

func TestWorkflow_SecuritySkipsGuardWhenNotDeclared(t *testing.T) {
	var seen *Runtime
	wf := New("w").Add(Sequence("main").Steps(&envCapture{dest: &seen}))

	model := &scriptedModel{replies: []*llm.ChatResponse{{Content: "done"}}}
	if _, err := wf.Execute(context.Background(), &Runtime{Model: model}, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if seen == nil || seen.Guard != nil {
		t.Error("Guard attached without Security() declaration")
	}
}

func TestWorkflow_SecurityCallerEnvNotMutated(t *testing.T) {
	wf := New("w").
		Security(security.Default).
		Add(Sequence("main").Steps(Goal("g", "do something")))

	model := &scriptedModel{replies: []*llm.ChatResponse{{Content: "done"}}}
	rt := &Runtime{Model: model}
	if _, err := wf.Execute(context.Background(), rt, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rt.Guard != nil {
		t.Error("Execute mutated caller's rt.Guard")
	}
}

func TestWorkflow_SecurityPreflightErrorShape(t *testing.T) {
	wf := New("w").Security(security.Research)
	_, err := wf.Execute(context.Background(),
		&Runtime{Model: &scriptedModel{replies: []*llm.ChatResponse{{Content: ""}}}}, nil)
	if err == nil {
		t.Fatal("expected preflight error")
	}
	if errors.Is(err, context.Canceled) {
		t.Error("preflight error should not be context.Canceled")
	}
}

// ---------------------------------------------------------------------------
// Validate: composed checks
// ---------------------------------------------------------------------------

func TestValidate_RequiresAllRequiredFields(t *testing.T) {
	wf := New("w").Add(
		Sequence("main").Steps(
			Goal("g", "").Using(Agent("a", "")),
			Convergence("c", "", 0),
		),
	)
	err := Validate(wf, &Runtime{Model: stubModel{}})
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, want := range []string{
		"goal g: description is required",
		"agent a: prompt is required",
		"convergence c: description is required",
		"convergence c: 'within' must be > 0",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q\nfull error: %v", want, err)
		}
	}
}

func TestValidate_RequiresAtLeastOneSequence(t *testing.T) {
	wf := New("w")
	err := Validate(wf, &Runtime{Model: stubModel{}})
	if err == nil || !strings.Contains(err.Error(), "at least one sequence is required") {
		t.Errorf("got: %v", err)
	}
}

func TestValidate_RequiresAtLeastOneStepInSequence(t *testing.T) {
	wf := New("w").Add(Sequence("main"))
	err := Validate(wf, &Runtime{Model: stubModel{}})
	if err == nil || !strings.Contains(err.Error(), "sequence main: at least one step is required") {
		t.Errorf("got: %v", err)
	}
}

func TestValidate_TrimsWhitespaceOnAllNames(t *testing.T) {
	wf := New("   ").Input(Parameter{Name: " "}).Add(
		Sequence("   ").Steps(Goal("   ", "desc").Using(Agent(" ", "prompt"))),
	)
	err := Validate(wf, &Runtime{Model: stubModel{}})
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{
		"workflow: name is required",
		"workflow: parameter has empty name",
		"sequence: name is required",
		"goal: name is required",
		"agent: name is required",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q\nfull error: %v", want, err)
		}
	}
}

func TestValidate_RejectsEmptyOutputFieldNames(t *testing.T) {
	wf := New("w").Add(Sequence("main").Steps(
		Goal("g", "desc").WithOutputs("", "valid"),
		Convergence("c", "desc", 3).WithOutputs(" ", "ok"),
	))
	err := Validate(wf, &Runtime{Model: stubModel{}})
	for _, want := range []string{
		"goal g: output has empty name",
		"convergence c: output has empty name",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q\nfull error: %v", want, err)
		}
	}
}

func TestValidate_RejectsEmptyAgentOutputFieldNames(t *testing.T) {
	wf := New("w").Add(Sequence("main").Steps(
		Goal("g", "desc").Using(Agent("a", "prompt").WithOutputs("", "valid")),
	))
	err := Validate(wf, &Runtime{Model: stubModel{}})
	if err == nil || !strings.Contains(err.Error(), "agent a: output has empty name") {
		t.Errorf("got: %v", err)
	}
}

func TestValidate_RecursesIntoNestedGoalsInUsing(t *testing.T) {
	nested := Goal("nested", "")
	wf := New("w").Add(Sequence("main").Steps(
		Convergence("outer", "polish", 3).Using(nested),
	))
	err := Validate(wf, &Runtime{Model: stubModel{}})
	if err == nil || !strings.Contains(err.Error(), "goal nested: description is required") {
		t.Errorf("got: %v", err)
	}
}

func TestValidate_RejectsDuplicateSequenceNames(t *testing.T) {
	wf := New("w").
		Add(Sequence("dup").Steps(Goal("a", "x"))).
		Add(Sequence("dup").Steps(Goal("b", "y")))
	err := Validate(wf, &Runtime{Model: stubModel{}})
	if err == nil || !strings.Contains(err.Error(), "sequence dup: declared more than once") {
		t.Errorf("got: %v", err)
	}
}

func TestValidate_RejectsDuplicateParameterNames(t *testing.T) {
	wf := New("w").
		Input(Parameter{Name: "topic"}, Parameter{Name: "topic", Default: "x"}).
		Add(Sequence("main").Steps(Goal("g", "use $topic")))
	err := Validate(wf, &Runtime{Model: stubModel{}})
	if err == nil || !strings.Contains(err.Error(), "parameter topic: declared more than once") {
		t.Errorf("got: %v", err)
	}
}

func TestValidate_OutputCollisionAcrossSteps(t *testing.T) {
	wf := New("w").Add(Sequence("main").Steps(
		Goal("g1", "do it").WithOutputs("shared"),
		Goal("g2", "do that").WithOutputs("shared"),
	))
	err := Validate(wf, &Runtime{Model: stubModel{}})
	if err == nil || !strings.Contains(err.Error(), "conflicts with") {
		t.Errorf("got: %v", err)
	}
}

func TestValidate_RejectsUndeclaredVarsInGoalDescription(t *testing.T) {
	wf := New("w").
		Input(Parameter{Name: "topic"}).
		Add(Sequence("main").Steps(Goal("g", "summarize $topic and $missing")))
	err := Validate(wf, &Runtime{Model: stubModel{}})
	if err == nil || !strings.Contains(err.Error(), "$missing is not declared") {
		t.Errorf("got: %v", err)
	}
}

func TestValidate_AcceptsVarsResolvingToOutputs(t *testing.T) {
	wf := New("w").
		Input(Parameter{Name: "topic"}).
		Add(Sequence("main").Steps(
			Goal("plan", "draft a plan for $topic").WithOutputs("plan"),
			Goal("execute", "carry out $plan against $topic"),
		))
	if err := Validate(wf, &Runtime{Model: stubModel{}}); err != nil {
		t.Errorf("unexpected: %v", err)
	}
}

func TestValidate_RejectsUndeclaredVarsInAgentPrompt(t *testing.T) {
	wf := New("w").
		Input(Parameter{Name: "domain"}).
		Add(Sequence("main").Steps(
			Goal("g", "task").Using(
				Agent("critic", "You are a critic for $domain content reviewing $unknown"),
			),
		))
	err := Validate(wf, &Runtime{Model: stubModel{}})
	if err == nil || !strings.Contains(err.Error(), "$unknown is not declared") {
		t.Errorf("got: %v", err)
	}
}

func TestValidate_NilRuntime(t *testing.T) {
	wf := New("w").Add(Sequence("main").Steps(Goal("g", "do it")))
	err := Validate(wf, nil)
	if err == nil || !strings.Contains(err.Error(), "Runtime.Model is required") {
		t.Errorf("got: %v", err)
	}
}

// Supervision-wiring rules — the workflow scans the whole tree.

func TestValidate_RequiresSupervisorWhenAnyNodeSupervised(t *testing.T) {
	wf := New("w").Add(Sequence("main").Steps(Goal("g", "do it").Supervise()))
	err := Validate(wf, &Runtime{Model: stubModel{}})
	if err == nil || !strings.Contains(err.Error(), "Runtime.Supervisor is required") {
		t.Errorf("got: %v", err)
	}
}

func TestValidate_RequiresHumanChWhenAnyNodeSuperviseByHuman(t *testing.T) {
	wf := New("w").Add(Sequence("main").Steps(Convergence("c", "polish", 2).SuperviseByHuman()))
	err := Validate(wf, &Runtime{Model: stubModel{}, Supervisor: &fakeSupervisor{}})
	if err == nil || !strings.Contains(err.Error(), "Runtime.HumanCh is required") {
		t.Errorf("got: %v", err)
	}
}

func TestValidate_NoSupervisorRequiredWhenUnsupervised(t *testing.T) {
	wf := New("w").Add(Sequence("main").Steps(Goal("g", "do it")))
	if err := Validate(wf, &Runtime{Model: stubModel{}}); err != nil {
		t.Errorf("unsupervised workflow should validate without Supervisor: %v", err)
	}
}

func TestValidate_DetectsSupervisionAtWorkflowLevel(t *testing.T) {
	wf := New("w").SuperviseByHuman().Add(Sequence("main").Steps(Goal("g", "do it")))
	err := Validate(wf, &Runtime{Model: stubModel{}, Supervisor: &fakeSupervisor{}})
	if err == nil || !strings.Contains(err.Error(), "HumanCh is required") {
		t.Errorf("got: %v", err)
	}
}

func TestValidate_DetectsSupervisionAtSequenceLevel(t *testing.T) {
	wf := New("w").Add(Sequence("main").Supervise().Steps(Goal("g", "do it")))
	err := Validate(wf, &Runtime{Model: stubModel{}})
	if err == nil || !strings.Contains(err.Error(), "Supervisor is required") {
		t.Errorf("got: %v", err)
	}
}

func TestValidate_DetectsSupervisionInsideUsing(t *testing.T) {
	inner := Goal("inner", "x").Supervise()
	wf := New("w").Add(Sequence("main").Steps(Convergence("outer", "polish", 2).Using(inner)))
	err := Validate(wf, &Runtime{Model: stubModel{}})
	if err == nil || !strings.Contains(err.Error(), "Supervisor is required") {
		t.Errorf("got: %v", err)
	}
}
