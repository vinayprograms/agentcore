package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// supervision_test.go covers supervision.go: the runWithSupervision pipeline
// (pass-through, COMMIT → EXECUTE → POST → RECONCILE → SUPERVISE), the four
// verdict paths (Continue / Reorient / AskHuman / Halt), the byHuman
// short-circuit, max-reorient budget exhaustion, graceful degradation
// in COMMIT and POST phases, extractJSONObject, askHuman handshake,
// and effectiveSupervision context inheritance.
//
// jsonModel and the contains() helper live in helpers_test.go (shared).

// fakeSupervisor lets each test wire its desired Reconcile + Supervise outputs.
type fakeSupervisor struct {
	reconcile *ReconcileResult
	result    *SuperviseResult
	supErr    error

	preSeen    *PreCheckpoint
	postSeen   *PostCheckpoint
	reqSeen    *SuperviseRequest
	superviseN int // incremented per Supervise call
}

func (f *fakeSupervisor) Reconcile(pre *PreCheckpoint, post *PostCheckpoint) *ReconcileResult {
	f.preSeen = pre
	f.postSeen = post
	if f.reconcile != nil {
		return f.reconcile
	}
	return &ReconcileResult{StepID: pre.StepID}
}

func (f *fakeSupervisor) Supervise(ctx context.Context, req SuperviseRequest) (*SuperviseResult, error) {
	f.reqSeen = &req
	f.superviseN++
	if f.supErr != nil {
		return nil, f.supErr
	}
	if f.result != nil {
		return f.result, nil
	}
	return &SuperviseResult{Verdict: VerdictContinue}, nil
}

// stagedSupervisor returns scripted Supervise results by call number.
type stagedSupervisor struct {
	results []*SuperviseResult
	calls   int
}

func (s *stagedSupervisor) Reconcile(pre *PreCheckpoint, post *PostCheckpoint) *ReconcileResult {
	return &ReconcileResult{StepID: pre.StepID, Escalate: true, Triggers: []string{"x"}}
}

func (s *stagedSupervisor) Supervise(ctx context.Context, req SuperviseRequest) (*SuperviseResult, error) {
	idx := s.calls
	s.calls++
	if idx < len(s.results) {
		return s.results[idx], nil
	}
	if len(s.results) > 0 {
		return s.results[len(s.results)-1], nil
	}
	return &SuperviseResult{Verdict: VerdictContinue}, nil
}

// validJSONPre is a well-formed COMMIT response.
const validJSONPre = `{
  "interpretation":"do X",
  "scope_in":["a"],
  "scope_out":["b"],
  "approach":"plan",
  "predicted_output":"out",
  "confidence":"high",
  "assumptions":["q"]
}`

// validJSONPost is a well-formed POST response.
const validJSONPost = `{
  "met_commitment":true,
  "deviations":["d1"],
  "concerns":["c1"],
  "unexpected":["u1"]
}`

// ---------------------------------------------------------------------------
// runWithSupervision: pass-through and full pipeline paths.
// ---------------------------------------------------------------------------

func TestRunWithSupervision_NotSupervisedPassesThrough(t *testing.T) {
	called := 0
	out, err := runWithSupervision(context.Background(), &Runtime{}, "s", "goal", "do it", notSupervised,
		func(ctx context.Context, instruction string) (string, []string, error) {
			called++
			return "result", nil, nil
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "result" || called != 1 {
		t.Errorf("expected single passthrough call, got out=%q called=%d", out, called)
	}
}

func TestRunWithSupervision_LLMContinueNoTriggers(t *testing.T) {
	model := &jsonModel{replies: []string{validJSONPre, validJSONPost}}
	sup := &fakeSupervisor{
		reconcile: &ReconcileResult{Escalate: false},
	}
	rt := &Runtime{Model: model, Supervisor: sup}

	out, err := runWithSupervision(context.Background(), rt, "s1", "goal", "do it", byLLM,
		func(ctx context.Context, instruction string) (string, []string, error) {
			return "answer", []string{"t1"}, nil
		})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if out != "answer" {
		t.Errorf("got %q", out)
	}
}

func TestRunWithSupervision_SuperviseContinue(t *testing.T) {
	model := &jsonModel{replies: []string{validJSONPre, validJSONPost}}
	sup := &fakeSupervisor{
		reconcile: &ReconcileResult{Escalate: true, Triggers: []string{"x"}},
		result:    &SuperviseResult{Verdict: VerdictContinue},
	}
	rt := &Runtime{Model: model, Supervisor: sup}

	out, err := runWithSupervision(context.Background(), rt, "s1", "goal", "do it", byLLM,
		func(ctx context.Context, _ string) (string, []string, error) { return "answer", nil, nil })
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if out != "answer" {
		t.Errorf("got %q", out)
	}
}

func TestRunWithSupervision_SuperviseHalt(t *testing.T) {
	model := &jsonModel{replies: []string{validJSONPre, validJSONPost}}
	sup := &fakeSupervisor{
		reconcile: &ReconcileResult{Escalate: true},
		result:    &SuperviseResult{Verdict: VerdictHalt, Reason: "unsafe"},
	}
	rt := &Runtime{Model: model, Supervisor: sup}

	_, err := runWithSupervision(context.Background(), rt, "s1", "goal", "do it", byLLM,
		func(ctx context.Context, _ string) (string, []string, error) { return "x", nil, nil })
	if err == nil || !strings.Contains(err.Error(), "halted by supervision") {
		t.Errorf("expected halt error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "unsafe") {
		t.Errorf("reason not in error: %v", err)
	}
}

func TestRunWithSupervision_UnknownVerdictErrors(t *testing.T) {
	model := &jsonModel{replies: []string{validJSONPre, validJSONPost}}
	sup := &fakeSupervisor{
		reconcile: &ReconcileResult{Escalate: true},
		result:    &SuperviseResult{Verdict: Verdict("bogus")},
	}
	rt := &Runtime{Model: model, Supervisor: sup}

	_, err := runWithSupervision(context.Background(), rt, "s1", "goal", "do it", byLLM,
		func(ctx context.Context, _ string) (string, []string, error) { return "x", nil, nil })
	if err == nil || !strings.Contains(err.Error(), "unknown supervision verdict") {
		t.Errorf("expected unknown-verdict error, got: %v", err)
	}
}

func TestRunWithSupervision_ReorientReExecutesWithCorrection(t *testing.T) {
	model := &jsonModel{replies: []string{validJSONPre, validJSONPost, validJSONPost}}
	// stagedSupervisor returns Reorient once, then Continue.
	staged := &stagedSupervisor{
		results: []*SuperviseResult{
			{Verdict: VerdictReorient, Correction: "be more careful"},
			{Verdict: VerdictContinue},
		},
	}
	rt := &Runtime{Model: model, Supervisor: staged}

	var seen []string
	out, err := runWithSupervision(context.Background(), rt, "s1", "goal", "do it", byLLM,
		func(ctx context.Context, instruction string) (string, []string, error) {
			seen = append(seen, instruction)
			return "result-" + instruction, nil, nil
		})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("execute should run twice for reorient, got %d", len(seen))
	}
	if !strings.Contains(seen[1], "be more careful") {
		t.Errorf("correction not threaded into 2nd execute: %q", seen[1])
	}
	if !strings.Contains(out, "be more careful") {
		t.Errorf("output should reflect re-execute: %q", out)
	}
}

// ---------------------------------------------------------------------------
// Reorient budget exhaustion.
// ---------------------------------------------------------------------------

func TestRunWithSupervision_ReorientExecuteErrorPropagates(t *testing.T) {
	model := &jsonModel{replies: []string{validJSONPre, validJSONPost}}
	staged := &stagedSupervisor{
		results: []*SuperviseResult{
			{Verdict: VerdictReorient, Correction: "retry"},
		},
	}
	rt := &Runtime{Model: model, Supervisor: staged}

	want := errors.New("retry boom")
	_, err := runWithSupervision(context.Background(), rt, "s1", "goal", "do it", byLLM,
		func(ctx context.Context, instruction string) (string, []string, error) {
			// First call (EXECUTE) succeeds; second (reorient) fails.
			if len(instruction) > 6 { // "do it" vs "do it\n\nretry"
				return "", nil, want
			}
			return "ok", nil, nil
		})
	if !errors.Is(err, want) {
		t.Errorf("expected reorient execute error to propagate, got: %v", err)
	}
}

func TestRunWithSupervision_ReorientBudgetExhaustedHalt(t *testing.T) {
	model := &jsonModel{replies: []string{validJSONPre, validJSONPost, validJSONPost, validJSONPost}}
	sup := &fakeSupervisor{
		reconcile: &ReconcileResult{Escalate: true},
		result:    &SuperviseResult{Verdict: VerdictReorient, Correction: "fix it", Reason: "still wrong"},
	}
	rt := &Runtime{Model: model, Supervisor: sup, MaxReorientAttempts: 1}

	_, err := runWithSupervision(context.Background(), rt, "s1", "goal", "do it", byLLM,
		func(ctx context.Context, _ string) (string, []string, error) { return "x", nil, nil })
	if err == nil || !strings.Contains(err.Error(), "reorient budget exhausted") {
		t.Errorf("expected budget-exhausted error, got: %v", err)
	}
}

func TestRunWithSupervision_ReorientBudgetExhaustedAsksHuman(t *testing.T) {
	model := &jsonModel{replies: []string{validJSONPre, validJSONPost, validJSONPost, validJSONPost}}
	sup := &fakeSupervisor{
		reconcile: &ReconcileResult{Escalate: true},
		result:    &SuperviseResult{Verdict: VerdictReorient, Correction: "fix it"},
	}
	humanCh := make(chan string)
	rt := &Runtime{Model: model, Supervisor: sup, HumanCh: humanCh, MaxReorientAttempts: 1}

	go func() {
		<-humanCh
		humanCh <- "approved by human"
	}()

	calls := 0
	out, err := runWithSupervision(context.Background(), rt, "s1", "goal", "do it", byLLM,
		func(ctx context.Context, instruction string) (string, []string, error) {
			calls++
			return "out-" + instruction, nil, nil
		})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	// Initial execute + 1 reorient + human-approval re-execute = 3
	if calls != 3 {
		t.Errorf("expected 3 executes (initial + reorient + human), got %d", calls)
	}
	if !strings.Contains(out, "approved by human") {
		t.Errorf("human correction not in output: %q", out)
	}
}

// ---------------------------------------------------------------------------
// AskHuman verdict (LLM-drift detected, supervisor asks for human input).
// ---------------------------------------------------------------------------

func TestRunWithSupervision_AskHumanWithoutHumanChIsError(t *testing.T) {
	model := &jsonModel{replies: []string{validJSONPre, validJSONPost}}
	sup := &fakeSupervisor{
		reconcile: &ReconcileResult{Escalate: true},
		result:    &SuperviseResult{Verdict: VerdictAskHuman, Question: "really?"},
	}
	rt := &Runtime{Model: model, Supervisor: sup}

	_, err := runWithSupervision(context.Background(), rt, "s1", "goal", "do it", byLLM,
		func(ctx context.Context, _ string) (string, []string, error) { return "x", nil, nil })
	if err == nil || !strings.Contains(err.Error(), "no HumanCh wired") {
		t.Errorf("expected no-HumanCh error, got: %v", err)
	}
}

func TestRunWithSupervision_AskHumanApprovalContinues(t *testing.T) {
	model := &jsonModel{replies: []string{validJSONPre, validJSONPost, validJSONPost}}
	sup := &fakeSupervisor{
		reconcile: &ReconcileResult{Escalate: true},
		result:    &SuperviseResult{Verdict: VerdictAskHuman, Question: "ok?"},
	}
	humanCh := make(chan string)
	rt := &Runtime{Model: model, Supervisor: sup, HumanCh: humanCh}

	go func() {
		<-humanCh
		humanCh <- "looks good"
	}()

	calls := 0
	out, err := runWithSupervision(context.Background(), rt, "s1", "goal", "do it", byLLM,
		func(ctx context.Context, instruction string) (string, []string, error) {
			calls++
			return "out-" + instruction, nil, nil
		})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 executes after human approval, got %d", calls)
	}
	if !strings.Contains(out, "looks good") {
		t.Errorf("expected re-execute output to include human correction; got %q", out)
	}
}

// ---------------------------------------------------------------------------
// byHuman path: skips LLM-SUPERVISE, routes through RECONCILE to askHuman.
// ---------------------------------------------------------------------------

func TestRunWithSupervision_ByHumanAsksDirectly(t *testing.T) {
	model := &jsonModel{replies: []string{validJSONPre, validJSONPost}}
	sup := &fakeSupervisor{
		reconcile: &ReconcileResult{Escalate: true, Triggers: []string{"x"}},
		// result is NOT set — byHuman never calls Supervise
	}
	humanCh := make(chan string)
	rt := &Runtime{Model: model, Supervisor: sup, HumanCh: humanCh}

	go func() {
		<-humanCh
		humanCh <- "approved"
	}()

	calls := 0
	out, err := runWithSupervision(context.Background(), rt, "s1", "goal", "do it", byHuman,
		func(ctx context.Context, instruction string) (string, []string, error) {
			calls++
			return "done-" + instruction, nil, nil
		})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 executes for byHuman approval, got %d", calls)
	}
	if !strings.Contains(out, "approved") {
		t.Errorf("human approval not in output: %q", out)
	}
	if sup.reqSeen != nil {
		t.Error("byHuman should NOT call Supervise()")
	}
}

func TestRunWithSupervision_ByHumanDenialFails(t *testing.T) {
	model := &jsonModel{replies: []string{validJSONPre, validJSONPost}}
	sup := &fakeSupervisor{
		reconcile: &ReconcileResult{Escalate: false},
	}
	humanCh := make(chan string)
	rt := &Runtime{Model: model, Supervisor: sup, HumanCh: humanCh}

	go func() {
		<-humanCh
		humanCh <- "  " // whitespace = denial
	}()

	_, err := runWithSupervision(context.Background(), rt, "s1", "goal", "do it", byHuman,
		func(ctx context.Context, _ string) (string, []string, error) { return "x", nil, nil })
	if err == nil || !strings.Contains(err.Error(), "denied by human") {
		t.Errorf("expected denied-by-human error, got: %v", err)
	}
}

func TestRunWithSupervision_ByHumanCancelledContext(t *testing.T) {
	model := &jsonModel{replies: []string{validJSONPre, validJSONPost}}
	sup := &fakeSupervisor{
		reconcile: &ReconcileResult{Escalate: false},
	}
	humanCh := make(chan string)
	ctx, cancel := context.WithCancel(context.Background())
	rt := &Runtime{Model: model, Supervisor: sup, HumanCh: humanCh}

	go func() {
		<-humanCh
		cancel()
	}()

	_, err := runWithSupervision(ctx, rt, "s1", "goal", "do it", byHuman,
		func(ctx context.Context, _ string) (string, []string, error) { return "x", nil, nil })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestRunWithSupervision_ByHumanChClosedFails(t *testing.T) {
	model := &jsonModel{replies: []string{validJSONPre, validJSONPost}}
	sup := &fakeSupervisor{
		reconcile: &ReconcileResult{Escalate: false},
	}
	humanCh := make(chan string)
	rt := &Runtime{Model: model, Supervisor: sup, HumanCh: humanCh}

	go func() {
		<-humanCh
		close(humanCh)
	}()

	_, err := runWithSupervision(context.Background(), rt, "s1", "goal", "do it", byHuman,
		func(ctx context.Context, _ string) (string, []string, error) { return "x", nil, nil })
	if err == nil || !strings.Contains(err.Error(), "denied by human") {
		t.Errorf("closed channel should be treated as denial, got: %v", err)
	}
}

func TestRunWithSupervision_ExecuteErrorPropagates(t *testing.T) {
	model := &jsonModel{replies: []string{validJSONPre, validJSONPost}}
	rt := &Runtime{Model: model, Supervisor: &fakeSupervisor{}}

	want := errors.New("boom")
	_, err := runWithSupervision(context.Background(), rt, "s1", "goal", "do it", byLLM,
		func(ctx context.Context, _ string) (string, []string, error) { return "", nil, want })
	if !errors.Is(err, want) {
		t.Errorf("expected boom, got: %v", err)
	}
}

func TestRunWithSupervision_SupervisorErrorPropagates(t *testing.T) {
	model := &jsonModel{replies: []string{validJSONPre, validJSONPost}}
	sup := &fakeSupervisor{
		reconcile: &ReconcileResult{Escalate: true},
		supErr:    errors.New("supervisor down"),
	}
	rt := &Runtime{Model: model, Supervisor: sup}

	_, err := runWithSupervision(context.Background(), rt, "s1", "goal", "do it", byLLM,
		func(ctx context.Context, _ string) (string, []string, error) { return "x", nil, nil })
	if err == nil || !strings.Contains(err.Error(), "supervise phase") {
		t.Errorf("expected supervise-phase error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// SuperviseRequest carries Model for fallback.
// ---------------------------------------------------------------------------

func TestSuperviseRequest_CarriesModel(t *testing.T) {
	model := &jsonModel{replies: []string{validJSONPre, validJSONPost}}
	rt := &Runtime{Model: model, Supervisor: &fakeSupervisor{
		reconcile: &ReconcileResult{Escalate: true},
	}}

	_, err := runWithSupervision(context.Background(), rt, "s1", "goal", "do it", byLLM,
		func(ctx context.Context, _ string) (string, []string, error) { return "x", nil, nil })
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

// ---------------------------------------------------------------------------
// runCommitPhase / runPostPhase: graceful degradation paths.
// ---------------------------------------------------------------------------

func TestCommitPhase_ModelErrorDegradesToLowConfidence(t *testing.T) {
	model := &jsonModel{err: errors.New("offline")}
	pre := runCommitPhase(context.Background(), &Runtime{Model: model}, "s1", "goal", "do it")
	if pre.Confidence != "low" {
		t.Errorf("Confidence: %q, want low", pre.Confidence)
	}
	if len(pre.Assumptions) == 0 || !strings.Contains(pre.Assumptions[0], "Failed to obtain") {
		t.Errorf("expected failure assumption, got: %v", pre.Assumptions)
	}
}

func TestCommitPhase_MalformedJSONLeavesDefaults(t *testing.T) {
	model := &jsonModel{replies: []string{"sorry I cannot comply"}}
	pre := runCommitPhase(context.Background(), &Runtime{Model: model}, "s1", "goal", "do it")
	if pre.Confidence != "low" {
		t.Errorf("Confidence: %q, want default low", pre.Confidence)
	}
	if pre.Interpretation != "" {
		t.Errorf("Interpretation should be empty when no JSON parsed")
	}
}

func TestCommitPhase_JSONWithoutConfidenceDefaultsMedium(t *testing.T) {
	model := &jsonModel{replies: []string{`{"interpretation":"X","approach":"Y"}`}}
	pre := runCommitPhase(context.Background(), &Runtime{Model: model}, "s1", "goal", "do it")
	if pre.Confidence != "medium" {
		t.Errorf("Confidence: %q, want medium when JSON parses but confidence omitted", pre.Confidence)
	}
}

func TestCommitPhase_BadJSONInsideBracesIgnored(t *testing.T) {
	model := &jsonModel{replies: []string{`prefix {not valid json} suffix`}}
	pre := runCommitPhase(context.Background(), &Runtime{Model: model}, "s1", "goal", "do it")
	if pre.Confidence != "low" {
		t.Errorf("Confidence: %q, want low (bad JSON should not overwrite defaults)", pre.Confidence)
	}
}

func TestPostPhase_ModelErrorDegrades(t *testing.T) {
	model := &jsonModel{err: errors.New("offline")}
	pre := &PreCheckpoint{StepID: "s1"}
	post := runPostPhase(context.Background(), &Runtime{Model: model}, pre, "out")
	if post.MetCommitment {
		t.Errorf("MetCommitment should be false when model errs")
	}
	if len(post.Concerns) == 0 {
		t.Errorf("expected concern noting failure, got: %v", post.Concerns)
	}
}

func TestPostPhase_MalformedJSONLeavesDefaults(t *testing.T) {
	model := &jsonModel{replies: []string{"no braces here"}}
	pre := &PreCheckpoint{StepID: "s1"}
	post := runPostPhase(context.Background(), &Runtime{Model: model}, pre, "out")
	if !post.MetCommitment {
		t.Errorf("optimistic default should be MetCommitment=true when no JSON")
	}
}

// ---------------------------------------------------------------------------
// extractJSONObject: edge cases.
// ---------------------------------------------------------------------------

func TestExtractJSONObject(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`{"a":1}`, `{"a":1}`},
		{`prefix {"a":1} suffix`, `{"a":1}`},
		{`no braces`, ""},
		{`}{`, ""},
		{`{`, ""},
	}
	for _, c := range cases {
		if got := extractJSONObject(c.in); got != c.want {
			t.Errorf("extractJSONObject(%q): got %q, want %q", c.in, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// effectiveSupervision: inheritance from context.
// ---------------------------------------------------------------------------

func TestEffectiveSupervision_OwnTakesPrecedence(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxSupervision, byHuman)
	got := effectiveSupervision(ctx, byLLM)
	if got != byLLM {
		t.Errorf("own value should win, got %v", got)
	}
}

func TestEffectiveSupervision_FallsBackToContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxSupervision, byHuman)
	if got := effectiveSupervision(ctx, notSupervised); got != byHuman {
		t.Errorf("ctx fallback failed: %v", got)
	}
}

func TestEffectiveSupervision_NoCtxNoOwn(t *testing.T) {
	if got := effectiveSupervision(context.Background(), notSupervised); got != notSupervised {
		t.Errorf("expected notSupervised, got %v", got)
	}
}
