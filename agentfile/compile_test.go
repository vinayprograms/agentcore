package agentfile

import (
	"context"
	"strings"
	"testing"

	"github.com/vinayprograms/agentkit/llm"
	"github.com/vinayprograms/agentkit/tools"
)

// fakeModel is a no-op llm.Model used by Compile tests. Compile does not
// invoke the model itself (semantic validation only); the value flows into
// Override.Model and possibly security.Build.
type fakeModel struct{ name string }

func (m fakeModel) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{Content: ""}, nil
}

func mustCompile(t *testing.T, src string, profiles map[string]llm.Model, registry *tools.Registry) {
	t.Helper()
	spec, err := Parse(src, nil, Config{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, _, err := Compile(spec, fakeModel{name: "default"}, profiles, registry); err != nil {
		t.Fatalf("compile: %v", err)
	}
}

func compileErr(t *testing.T, src string, profiles map[string]llm.Model, registry *tools.Registry) error {
	t.Helper()
	spec, err := Parse(src, nil, Config{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	_, _, err = Compile(spec, fakeModel{name: "default"}, profiles, registry)
	return err
}

// ---------------------------------------------------------------------------
// Happy paths
// ---------------------------------------------------------------------------

func TestCompile_MinimalWorkflow(t *testing.T) {
	src := `NAME minimal
GOAL hello "Say hello"
RUN main USING hello
`
	spec, err := Parse(src, nil, Config{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	wf, rt, err := Compile(spec, fakeModel{}, nil, nil)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if wf == nil {
		t.Fatal("workflow should not be nil")
	}
	if rt == nil {
		t.Fatal("Runtime should always be non-nil")
	}
	if rt.Guard != nil {
		t.Errorf("Guard should be zero when no SECURITY declared, got %+v", rt.Guard)
	}
}

func TestCompile_WithAgentsAndUsing(t *testing.T) {
	src := `NAME story
INPUT topic
AGENT writer "You write."
AGENT critic "You critique."
GOAL draft "Write about $topic" USING writer
GOAL polish "Critique the draft" USING critic
RUN main USING draft, polish
`
	mustCompile(t, src, nil, nil)
}

func TestCompile_InputDefault(t *testing.T) {
	src := `NAME d
INPUT style DEFAULT "concise"
GOAL g "Use $style"
RUN main USING g
`
	mustCompile(t, src, nil, nil)
}

func TestCompile_ConvergeLiteralWithin(t *testing.T) {
	src := `NAME c
AGENT writer "You write."
CONVERGE refine "Polish" USING writer WITHIN 3
RUN main USING refine
`
	mustCompile(t, src, nil, nil)
}

func TestCompile_ConvergeVarWithin(t *testing.T) {
	src := `NAME c
INPUT max_iter DEFAULT "5"
AGENT writer "You write."
CONVERGE refine "Polish" USING writer WITHIN $max_iter
RUN main USING refine
`
	mustCompile(t, src, nil, nil)
}

func TestCompile_AgentOutputs(t *testing.T) {
	src := `NAME o
AGENT scanner "Scan" -> vulnerabilities, severity
GOAL g "Run" USING scanner
RUN main USING g
`
	mustCompile(t, src, nil, nil)
}

func TestCompile_GoalOutputs(t *testing.T) {
	src := `NAME o
GOAL g "Summarize" -> headline, body
RUN main USING g
`
	mustCompile(t, src, nil, nil)
}

func TestCompile_SupervisedHuman(t *testing.T) {
	src := `SUPERVISED HUMAN
NAME s
GOAL g "Deploy"
RUN main USING g
`
	mustCompile(t, src, nil, nil)
}

func TestCompile_SecurityResearchWithScope(t *testing.T) {
	src := `NAME s
SECURITY research "authorized pentest lab"
GOAL g "Probe"
RUN main USING g
`
	spec, err := Parse(src, nil, Config{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	_, rt, err := Compile(spec, fakeModel{}, nil, nil)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if rt == nil || rt.Guard == nil {
		t.Error("expected non-nil Runtime with Guard for SECURITY directive")
	}
}

func TestCompile_RequiresPopulatesProfileAndRuntime(t *testing.T) {
	src := `NAME r
AGENT critic "You critique." REQUIRES "reasoning-heavy"
GOAL g "Review" USING critic
RUN main USING g
`
	spec, err := Parse(src, nil, Config{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	profiles := map[string]llm.Model{"reasoning-heavy": fakeModel{name: "rh"}}
	_, rt, err := Compile(spec, fakeModel{name: "default"}, profiles, nil)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if rt == nil {
		t.Error("Runtime should be non-nil when any agent has REQUIRES")
	}
}

// ---------------------------------------------------------------------------
// Validation failures
// ---------------------------------------------------------------------------

func TestCompile_UndefinedAgentInUsing(t *testing.T) {
	src := `NAME x
GOAL g "Run" USING ghost
RUN main USING g
`
	err := compileErr(t, src, nil, nil)
	if err == nil || !strings.Contains(err.Error(), `USING "ghost"`) {
		t.Errorf("got: %v", err)
	}
}

func TestCompile_UndefinedGoalInRun(t *testing.T) {
	src := `NAME x
GOAL real "Real"
RUN main USING missing
`
	err := compileErr(t, src, nil, nil)
	if err == nil || !strings.Contains(err.Error(), `USING "missing"`) {
		t.Errorf("got: %v", err)
	}
}

func TestCompile_DuplicateAgentName(t *testing.T) {
	src := `NAME x
AGENT a "p1"
AGENT a "p2"
GOAL g "g"
RUN main USING g
`
	err := compileErr(t, src, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("got: %v", err)
	}
}

func TestCompile_DuplicateGoalName(t *testing.T) {
	src := `NAME x
GOAL g "1"
GOAL g "2"
RUN main USING g
`
	err := compileErr(t, src, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("got: %v", err)
	}
}

func TestCompile_DuplicateStepName(t *testing.T) {
	src := `NAME x
GOAL g "g"
RUN main USING g
RUN main USING g
`
	err := compileErr(t, src, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("got: %v", err)
	}
}

func TestCompile_RequiresProfileMissingFromMap(t *testing.T) {
	src := `NAME x
AGENT critic "You critique." REQUIRES "ghost"
GOAL g "Run" USING critic
RUN main USING g
`
	err := compileErr(t, src, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("got: %v", err)
	}
}

func TestCompile_SecurityResearchRequiresScope(t *testing.T) {
	// Parser may require scope on the source-format level. We hand-build the
	// Spec to drive the Compile-side check.
	spec := &Spec{
		Name:         "x",
		SecurityMode: "research",
		Goals:        []Goal{{Name: "g", Outcome: "Probe"}},
		Steps:        []Step{{Name: "main", Type: StepRUN, UsingGoals: []string{"g"}}},
	}
	_, _, err := Compile(spec, fakeModel{}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "research requires a scope") {
		t.Errorf("got: %v", err)
	}
}

func TestCompile_UnsupervisedInsideHumanScope(t *testing.T) {
	src := `SUPERVISED HUMAN
NAME x
GOAL relaxed "Run" UNSUPERVISED
RUN main USING relaxed
`
	err := compileErr(t, src, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "UNSUPERVISED cannot exist") {
		t.Errorf("got: %v", err)
	}
}

func TestCompile_UnsupervisedAgentInsideHumanScope(t *testing.T) {
	src := `SUPERVISED HUMAN
NAME x
AGENT lax "p" UNSUPERVISED
GOAL g "Run" USING lax
RUN main USING g
`
	err := compileErr(t, src, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "UNSUPERVISED") {
		t.Errorf("got: %v", err)
	}
}

func TestCompile_UnsupervisedStepInsideHumanScope(t *testing.T) {
	src := `SUPERVISED HUMAN
NAME x
GOAL g "Run"
RUN main USING g UNSUPERVISED
`
	err := compileErr(t, src, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "UNSUPERVISED") {
		t.Errorf("got: %v", err)
	}
}

func TestCompile_AgentFROMUnresolvedAtCompile(t *testing.T) {
	// Hand-built Spec retaining FROM but no Prompt — would only happen if
	// a caller constructed the Spec programmatically without resolving FROM.
	// Parse with a non-empty baseDir would never produce this state.
	spec := &Spec{
		Name:   "x",
		Agents: []Agent{{Name: "critic", FromPath: "agents/critic.md"}},
		Goals:  []Goal{{Name: "g", Outcome: "Run", UsingAgent: []string{"critic"}}},
		Steps:  []Step{{Name: "main", Type: StepRUN, UsingGoals: []string{"g"}}},
	}
	_, _, err := Compile(spec, fakeModel{}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "unresolved") {
		t.Errorf("got: %v", err)
	}
}

func TestParse_NilBaseRootRejectsFROM(t *testing.T) {
	src := `NAME x
AGENT critic FROM agents/critic.md
GOAL g "Run" USING critic
RUN main USING g
`
	_, err := Parse(src, nil, Config{})
	if err == nil || !strings.Contains(err.Error(), "baseRoot is nil") {
		t.Errorf("got: %v", err)
	}
}

func TestCompile_UnknownSecurityMode(t *testing.T) {
	spec := &Spec{
		Name:         "x",
		SecurityMode: "unknown",
		Goals:        []Goal{{Name: "g", Outcome: "g"}},
		Steps:        []Step{{Name: "main", Type: StepRUN, UsingGoals: []string{"g"}}},
	}
	_, _, err := Compile(spec, fakeModel{}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown SECURITY mode") {
		t.Errorf("got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// allowed-tools wiring
// ---------------------------------------------------------------------------

func TestCompile_AllowedToolsRequiresRegistry(t *testing.T) {
	spec := &Spec{
		Name:   "x",
		Agents: []Agent{{Name: "a", Prompt: "p", IsSkill: true, SkillDir: "/x", AllowedTools: []string{"read"}}},
		Goals:  []Goal{{Name: "g", Outcome: "g", UsingAgent: []string{"a"}}},
		Steps:  []Step{{Name: "main", Type: StepRUN, UsingGoals: []string{"g"}}},
	}
	_, _, err := Compile(spec, fakeModel{}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "non-nil tools registry") {
		t.Errorf("got: %v", err)
	}
}

func TestCompile_AllowedToolsMissingFromRegistry(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(tools.New(tools.Pwd()))
	spec := &Spec{
		Name:   "x",
		Agents: []Agent{{Name: "a", Prompt: "p", IsSkill: true, SkillDir: "/x", AllowedTools: []string{"missing"}}},
		Goals:  []Goal{{Name: "g", Outcome: "g", UsingAgent: []string{"a"}}},
		Steps:  []Step{{Name: "main", Type: StepRUN, UsingGoals: []string{"g"}}},
	}
	_, _, err := Compile(spec, fakeModel{}, nil, reg)
	if err == nil || !strings.Contains(err.Error(), `"missing"`) {
		t.Errorf("got: %v", err)
	}
}

func TestCompile_AllowedToolsWiredAsSubset(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(tools.New(tools.Pwd()))
	reg.Register(tools.New(tools.Hostname()))
	spec := &Spec{
		Name:   "x",
		Agents: []Agent{{Name: "a", Prompt: "p", IsSkill: true, SkillDir: "/x", AllowedTools: []string{"pwd"}}},
		Goals:  []Goal{{Name: "g", Outcome: "g", UsingAgent: []string{"a"}}},
		Steps:  []Step{{Name: "main", Type: StepRUN, UsingGoals: []string{"g"}}},
	}
	if _, _, err := Compile(spec, fakeModel{}, nil, reg); err != nil {
		t.Fatalf("compile: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Convergence validation
// ---------------------------------------------------------------------------

func TestCompile_ConvergeWithinLiteralAndVarBoth(t *testing.T) {
	limit := 3
	spec := &Spec{
		Name: "x",
		Goals: []Goal{
			{Name: "c", Outcome: "polish", IsConverge: true, WithinLimit: &limit, WithinVar: "max"},
		},
		Steps: []Step{{Name: "main", Type: StepRUN, UsingGoals: []string{"c"}}},
	}
	_, _, err := Compile(spec, fakeModel{}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("got: %v", err)
	}
}

func TestCompile_ConvergeWithinMissing(t *testing.T) {
	spec := &Spec{
		Name:  "x",
		Goals: []Goal{{Name: "c", Outcome: "polish", IsConverge: true}},
		Steps: []Step{{Name: "main", Type: StepRUN, UsingGoals: []string{"c"}}},
	}
	_, _, err := Compile(spec, fakeModel{}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "requires WITHIN") {
		t.Errorf("got: %v", err)
	}
}

func TestCompile_ConvergeWithinZero(t *testing.T) {
	zero := 0
	spec := &Spec{
		Name:  "x",
		Goals: []Goal{{Name: "c", Outcome: "polish", IsConverge: true, WithinLimit: &zero}},
		Steps: []Step{{Name: "main", Type: StepRUN, UsingGoals: []string{"c"}}},
	}
	_, _, err := Compile(spec, fakeModel{}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "must be > 0") {
		t.Errorf("got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// AST accessors
// ---------------------------------------------------------------------------

func TestSpec_ProfilesUniqueAndOrdered(t *testing.T) {
	spec := &Spec{Agents: []Agent{
		{Name: "a", Requires: "x"},
		{Name: "b"},
		{Name: "c", Requires: "y"},
		{Name: "d", Requires: "x"},
	}}
	got := spec.Profiles()
	if len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Errorf("profiles=%v", got)
	}
}

func TestSpec_HasSupervisionCovers(t *testing.T) {
	cases := []struct {
		name string
		spec *Spec
		want bool
	}{
		{"none", &Spec{}, false},
		{"global", &Spec{Supervised: true}, true},
		{"goal", &Spec{Goals: []Goal{{Supervision: SupervisionEnabled}}}, true},
		{"step", &Spec{Steps: []Step{{Supervision: SupervisionEnabled}}}, true},
		{"agent", &Spec{Agents: []Agent{{Supervision: SupervisionEnabled}}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.spec.HasSupervision(); got != tc.want {
				t.Errorf("HasSupervision=%v want %v", got, tc.want)
			}
		})
	}
}

func TestSpec_HasHumanSupervisionByNode(t *testing.T) {
	cases := []struct {
		name string
		spec *Spec
		want bool
	}{
		{"none", &Spec{}, false},
		{"global human", &Spec{Supervised: true, HumanOnly: true}, true},
		{"goal", &Spec{Goals: []Goal{{Supervision: SupervisionEnabled, HumanOnly: true}}}, true},
		{"step", &Spec{Steps: []Step{{Supervision: SupervisionEnabled, HumanOnly: true}}}, true},
		{"agent", &Spec{Agents: []Agent{{Supervision: SupervisionEnabled, HumanOnly: true}}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.spec.HasHumanSupervision(); got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestSupervisionMode_String(t *testing.T) {
	if SupervisionEnabled.String() != "supervised" {
		t.Error("Enabled")
	}
	if SupervisionDisabled.String() != "unsupervised" {
		t.Error("Disabled")
	}
	if SupervisionInherit.String() != "inherit" {
		t.Error("Inherit")
	}
}

func TestSupervisionMode_IsSet(t *testing.T) {
	if SupervisionInherit.IsSet() {
		t.Error("Inherit is set")
	}
	if !SupervisionEnabled.IsSet() {
		t.Error("Enabled not set")
	}
}

// ---------------------------------------------------------------------------
// IsSupervised / RequiresHuman propagation (covers every node-kind branch)
// ---------------------------------------------------------------------------

func TestPropagation_StepIsSupervised(t *testing.T) {
	spec := &Spec{Supervised: true}
	cases := []struct {
		mode SupervisionMode
		want bool
	}{
		{SupervisionInherit, true},
		{SupervisionEnabled, true},
		{SupervisionDisabled, false},
	}
	for _, tc := range cases {
		s := &Step{Supervision: tc.mode}
		if got := s.IsSupervised(spec); got != tc.want {
			t.Errorf("mode=%v got=%v want=%v", tc.mode, got, tc.want)
		}
	}
}

func TestPropagation_StepRequiresHuman(t *testing.T) {
	// Enabled + HumanOnly → true
	s := &Step{Supervision: SupervisionEnabled, HumanOnly: true}
	if !s.RequiresHuman(&Spec{}) {
		t.Error("explicit human should require human")
	}
	// Enabled + not human → false
	s2 := &Step{Supervision: SupervisionEnabled}
	if s2.RequiresHuman(&Spec{}) {
		t.Error("explicit non-human should not require human")
	}
	// Inherit, workflow human → true
	s3 := &Step{Supervision: SupervisionInherit}
	if !s3.RequiresHuman(&Spec{Supervised: true, HumanOnly: true}) {
		t.Error("inherited from human should require human")
	}
	// Inherit, workflow not human, step also not human → false
	if s3.RequiresHuman(&Spec{Supervised: true}) {
		t.Error("inherited non-human should not require human")
	}
	// Disabled → false
	s4 := &Step{Supervision: SupervisionDisabled, HumanOnly: true}
	if s4.RequiresHuman(&Spec{Supervised: true, HumanOnly: true}) {
		t.Error("explicit Disabled should not require human")
	}
}

func TestPropagation_GoalIsSupervisedAndRequiresHuman(t *testing.T) {
	g := &Goal{Supervision: SupervisionEnabled, HumanOnly: true}
	if !g.IsSupervised(&Spec{}) {
		t.Error("explicit Enabled goal is supervised")
	}
	if !g.RequiresHuman(&Spec{}) {
		t.Error("explicit human goal requires human")
	}
	g2 := &Goal{Supervision: SupervisionInherit}
	if !g2.RequiresHuman(&Spec{Supervised: true, HumanOnly: true}) {
		t.Error("inherit human goal under human workflow requires human")
	}
	if g2.RequiresHuman(&Spec{Supervised: true}) {
		t.Error("inherit goal under non-human workflow does not require human")
	}
	g3 := &Goal{Supervision: SupervisionEnabled}
	if g3.RequiresHuman(&Spec{}) {
		t.Error("explicit Enabled goal without HUMAN does not require human")
	}
}

func TestPropagation_AgentIsSupervisedAndRequiresHuman(t *testing.T) {
	a := &Agent{Supervision: SupervisionEnabled, HumanOnly: true}
	if !a.IsSupervised(&Spec{}) {
		t.Error("explicit Enabled agent is supervised")
	}
	if !a.RequiresHuman(&Spec{}) {
		t.Error("explicit human agent requires human")
	}
	a2 := &Agent{Supervision: SupervisionInherit}
	if !a2.RequiresHuman(&Spec{Supervised: true, HumanOnly: true}) {
		t.Error("inherit human agent under human workflow requires human")
	}
	if a2.RequiresHuman(&Spec{Supervised: true}) {
		t.Error("inherit agent under non-human workflow does not require human")
	}
	a3 := &Agent{Supervision: SupervisionEnabled}
	if a3.RequiresHuman(&Spec{}) {
		t.Error("explicit Enabled agent without HUMAN does not require human")
	}
}

// ---------------------------------------------------------------------------
// Security mode coverage
// ---------------------------------------------------------------------------

func TestCompile_SecurityDefaultAndParanoid(t *testing.T) {
	for _, mode := range []string{"default", "paranoid"} {
		t.Run(mode, func(t *testing.T) {
			spec := &Spec{
				Name:         "x",
				SecurityMode: mode,
				Goals:        []Goal{{Name: "g", Outcome: "g"}},
				Steps:        []Step{{Name: "main", Type: StepRUN, UsingGoals: []string{"g"}}},
			}
			_, rt, err := Compile(spec, fakeModel{}, nil, nil)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			if rt == nil || rt.Guard == nil {
				t.Errorf("expected non-nil Runtime with Guard for SECURITY %s", mode)
			}
		})
	}
}

func TestCompile_ExplicitGoalSupervised(t *testing.T) {
	src := `NAME x
GOAL critical "Do important" SUPERVISED
GOAL deploy "Deploy" SUPERVISED HUMAN
RUN main USING critical, deploy
`
	mustCompile(t, src, nil, nil)
}

func TestCompile_ConvergenceSupervisedHuman(t *testing.T) {
	src := `NAME x
AGENT writer "p"
CONVERGE refine "Polish" USING writer WITHIN 3 SUPERVISED HUMAN
RUN main USING refine
`
	mustCompile(t, src, nil, nil)
}

func TestCompile_ConvergenceSupervised(t *testing.T) {
	src := `NAME x
AGENT writer "p"
CONVERGE refine "Polish" USING writer WITHIN 3 SUPERVISED
RUN main USING refine
`
	mustCompile(t, src, nil, nil)
}

func TestCompile_ConvergeWithOutputs(t *testing.T) {
	src := `NAME x
AGENT writer "p"
CONVERGE refine "Polish" -> final USING writer WITHIN 3
RUN main USING refine
`
	mustCompile(t, src, nil, nil)
}

func TestCompile_DefaultModelNilNoREQUIRES(t *testing.T) {
	// An agent without REQUIRES and a nil defaultModel should still compile —
	// Override.Model just stays nil and the caller is expected to layer
	// rt.Model later.
	src := `NAME x
AGENT a "p"
GOAL g "g" USING a
RUN main USING g
`
	spec, err := Parse(src, nil, Config{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, _, err := Compile(spec, nil, nil, nil); err != nil {
		t.Fatalf("compile: %v", err)
	}
}

func TestStepType_String(t *testing.T) {
	if StepRUN.String() != "RUN" {
		t.Error("RUN")
	}
	if StepType(99).String() != "UNKNOWN" {
		t.Error("UNKNOWN fallback")
	}
}
