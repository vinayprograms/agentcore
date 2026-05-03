package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// supervision_test.go covers supervision.go: the runWithSupervision pipeline
// (pass-through, COMMIT → EXECUTE → POST → RECONCILE → SUPERVISE), the four
// verdict paths (Continue / Reorient / Pause / unknown), graceful degradation
// in COMMIT and POST phases, extractJSONObject, the Validate rules around
// Supervisor / HumanCh wiring, and effectiveSupervision context inheritance.
//
// jsonModel and the contains() helper live in helpers_test.go (shared).

// fakeSupervisor lets each test wire its desired Reconcile + Supervise outputs.
type fakeSupervisor struct {
	reconcile *ReconcileResult
	result    *SuperviseResult
	supErr    error

	preSeen  *PreCheckpoint
	postSeen *PostCheckpoint
	reqSeen  *SuperviseRequest
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
	if f.supErr != nil {
		return nil, f.supErr
	}
	if f.result != nil {
		return f.result, nil
	}
	return &SuperviseResult{Verdict: VerdictContinue}, nil
}

// recordingStore captures every persisted checkpoint.
type recordingStore struct {
	pres        []*PreCheckpoint
	posts       []*PostCheckpoint
	reconciles  []*ReconcileResult
	supervises  []*SuperviseResult
}

func (s *recordingStore) SavePre(p *PreCheckpoint) error              { s.pres = append(s.pres, p); return nil }
func (s *recordingStore) SavePost(p *PostCheckpoint) error            { s.posts = append(s.posts, p); return nil }
func (s *recordingStore) SaveReconcile(r *ReconcileResult) error      { s.reconciles = append(s.reconciles, r); return nil }
func (s *recordingStore) SaveSupervise(r *SuperviseResult) error      { s.supervises = append(s.supervises, r); return nil }

// validJSONPre is a well-formed COMMIT response.
const validJSONPre = `{
  "interpretation":"do X",
  "scope_in":["a"],
  "scope_out":["b"],
  "approach":"plan",
  "tools_planned":["t"],
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
	store := &recordingStore{}
	rt := &Runtime{Model: model, Supervisor: sup, CheckpointStore: store}

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
	if len(store.pres) != 1 || len(store.posts) != 1 || len(store.reconciles) != 1 {
		t.Errorf("checkpoints: pre=%d post=%d rec=%d", len(store.pres), len(store.posts), len(store.reconciles))
	}
	if len(store.supervises) != 0 {
		t.Errorf("supervise should be skipped when no triggers + not human; got %d", len(store.supervises))
	}
}

func TestRunWithSupervision_SuperviseContinuePersistsResult(t *testing.T) {
	model := &jsonModel{replies: []string{validJSONPre, validJSONPost}}
	sup := &fakeSupervisor{
		reconcile: &ReconcileResult{Escalate: true, Triggers: []string{"x"}},
		result:    &SuperviseResult{Verdict: VerdictContinue},
	}
	store := &recordingStore{}
	rt := &Runtime{Model: model, Supervisor: sup, CheckpointStore: store}

	out, err := runWithSupervision(context.Background(), rt, "s1", "goal", "do it", byLLM,
		func(ctx context.Context, _ string) (string, []string, error) { return "answer", nil, nil })
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if out != "answer" {
		t.Errorf("got %q", out)
	}
	if len(store.supervises) != 1 {
		t.Errorf("supervise should be persisted, got %d", len(store.supervises))
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
	model := &jsonModel{replies: []string{validJSONPre, validJSONPost}}
	sup := &fakeSupervisor{
		reconcile: &ReconcileResult{Escalate: true, Triggers: []string{"x"}},
		result:    &SuperviseResult{Verdict: VerdictReorient, Correction: "be more careful"},
	}
	rt := &Runtime{Model: model, Supervisor: sup}

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

func TestRunWithSupervision_PauseLLMWithoutHumanChIsError(t *testing.T) {
	model := &jsonModel{replies: []string{validJSONPre, validJSONPost}}
	sup := &fakeSupervisor{
		reconcile: &ReconcileResult{Escalate: true},
		result:    &SuperviseResult{Verdict: VerdictPause, Question: "really?"},
	}
	rt := &Runtime{Model: model, Supervisor: sup}

	_, err := runWithSupervision(context.Background(), rt, "s1", "goal", "do it", byLLM,
		func(ctx context.Context, _ string) (string, []string, error) { return "x", nil, nil })
	if err == nil || !strings.Contains(err.Error(), "paused by supervision") {
		t.Errorf("expected paused-by-supervision error, got: %v", err)
	}
}

func TestRunWithSupervision_PauseHumanApprovalContinues(t *testing.T) {
	model := &jsonModel{replies: []string{validJSONPre, validJSONPost}}
	sup := &fakeSupervisor{
		// human-required mode forces SUPERVISE even with no triggers
		reconcile: &ReconcileResult{Escalate: false},
		result:    &SuperviseResult{Verdict: VerdictPause, Question: "ok?"},
	}
	humanCh := make(chan string) // unbuffered: deterministic handshake
	rt := &Runtime{Model: model, Supervisor: sup, HumanCh: humanCh}

	// The pipeline first sends Question, then reads the next message as approval.
	go func() {
		<-humanCh               // receive the question
		humanCh <- "looks good" // approve
	}()

	calls := 0
	out, err := runWithSupervision(context.Background(), rt, "s1", "goal", "do it", byHuman,
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
	if !strings.Contains(out, "Human correction") {
		t.Errorf("expected re-execute output to include human correction; got %q", out)
	}
}

func TestRunWithSupervision_PauseHumanDenialFails(t *testing.T) {
	model := &jsonModel{replies: []string{validJSONPre, validJSONPost}}
	sup := &fakeSupervisor{
		reconcile: &ReconcileResult{Escalate: false},
		result:    &SuperviseResult{Verdict: VerdictPause, Question: "ok?"},
	}
	humanCh := make(chan string) // unbuffered
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

func TestRunWithSupervision_PauseHumanCancelledContext(t *testing.T) {
	model := &jsonModel{replies: []string{validJSONPre, validJSONPost}}
	sup := &fakeSupervisor{
		reconcile: &ReconcileResult{Escalate: false},
		result:    &SuperviseResult{Verdict: VerdictPause, Question: "ok?"},
	}
	humanCh := make(chan string) // unbuffered: question send blocks until drained
	ctx, cancel := context.WithCancel(context.Background())
	rt := &Runtime{Model: model, Supervisor: sup, HumanCh: humanCh}

	go func() {
		<-humanCh // drain question, then cancel without responding
		cancel()
	}()

	_, err := runWithSupervision(ctx, rt, "s1", "goal", "do it", byHuman,
		func(ctx context.Context, _ string) (string, []string, error) { return "x", nil, nil })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestRunWithSupervision_PauseHumanChClosedFails(t *testing.T) {
	model := &jsonModel{replies: []string{validJSONPre, validJSONPost}}
	sup := &fakeSupervisor{
		reconcile: &ReconcileResult{Escalate: false},
		result:    &SuperviseResult{Verdict: VerdictPause, Question: "ok?"},
	}
	humanCh := make(chan string) // unbuffered
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
	// No braces in the response.
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
	post := runPostPhase(context.Background(), &Runtime{Model: model}, pre, "out", []string{"t"})
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
	post := runPostPhase(context.Background(), &Runtime{Model: model}, pre, "out", nil)
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

// (The Validate-time supervision-wiring rules live in workflow_test.go,
// since they are checks composed by the package-level Validate.)

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

// (Workflow.Supervise / Sequence.SuperviseByHuman setter checks live in
// workflow_test.go and sequence_test.go respectively.)
