package supervise

import (
	"testing"

	"github.com/vinayprograms/agentcore/workflow"
)

func TestReconcile_CleanExecutionNoTriggers(t *testing.T) {
	s := New(Config{})
	pre := &workflow.PreCheckpoint{StepID: "s1"}
	post := &workflow.PostCheckpoint{
		MetCommitment: true,
	}
	result := s.Reconcile(pre, post)
	if result.Escalate {
		t.Error("Escalate should be false for clean execution")
	}
	if len(result.Triggers) != 0 {
		t.Errorf("expected no triggers, got: %v", result.Triggers)
	}
	if result.StepID != "s1" {
		t.Errorf("StepID: got %q, want s1", result.StepID)
	}
}

func TestReconcile_CommitmentNotMet(t *testing.T) {
	s := New(Config{})
	pre := &workflow.PreCheckpoint{StepID: "s1"}
	post := &workflow.PostCheckpoint{
		MetCommitment: false,
	}
	result := s.Reconcile(pre, post)
	if !result.Escalate {
		t.Error("Escalate should be true")
	}
	if len(result.Triggers) != 1 || result.Triggers[0] != "commitment_not_met" {
		t.Errorf("triggers: %v", result.Triggers)
	}
}

func TestReconcile_DeviationsTrigger(t *testing.T) {
	s := New(Config{})
	pre := &workflow.PreCheckpoint{StepID: "s1"}
	post := &workflow.PostCheckpoint{
		MetCommitment: true,
		Deviations:    []string{"scope creep", "wrong approach"},
	}
	result := s.Reconcile(pre, post)
	if !result.Escalate {
		t.Error("Escalate should be true")
	}
	if len(result.Triggers) != 2 {
		t.Errorf("expected 2 triggers, got %d: %v", len(result.Triggers), result.Triggers)
	}
}

func TestReconcile_ConcernsTrigger(t *testing.T) {
	s := New(Config{})
	pre := &workflow.PreCheckpoint{StepID: "s1"}
	post := &workflow.PostCheckpoint{
		MetCommitment: true,
		Concerns:      []string{"output may be incomplete"},
	}
	result := s.Reconcile(pre, post)
	if !result.Escalate {
		t.Error("Escalate should be true")
	}
	if len(result.Triggers) != 1 {
		t.Fatalf("expected 1 trigger: %v", result.Triggers)
	}
	if result.Triggers[0] != "concern:output may be incomplete" {
		t.Errorf("trigger: %q", result.Triggers[0])
	}
}

func TestReconcile_UnexpectedTrigger(t *testing.T) {
	s := New(Config{})
	pre := &workflow.PreCheckpoint{StepID: "s1"}
	post := &workflow.PostCheckpoint{
		MetCommitment: true,
		Unexpected:    []string{"tool returned empty"},
	}
	result := s.Reconcile(pre, post)
	if !result.Escalate {
		t.Error("Escalate should be true")
	}
	if len(result.Triggers) != 1 {
		t.Fatalf("expected 1 trigger: %v", result.Triggers)
	}
	if result.Triggers[0] != "unexpected:tool returned empty" {
		t.Errorf("trigger: %q", result.Triggers[0])
	}
}

func TestReconcile_EmptyStringsFiltered(t *testing.T) {
	s := New(Config{})
	pre := &workflow.PreCheckpoint{StepID: "s1"}
	post := &workflow.PostCheckpoint{
		MetCommitment: true,
		Deviations:    []string{"", "real deviation", ""},
		Concerns:      []string{""},
	}
	result := s.Reconcile(pre, post)
	if !result.Escalate {
		t.Error("Escalate should be true")
	}
	if len(result.Triggers) != 1 {
		t.Errorf("empty strings should be excluded; got %d: %v", len(result.Triggers), result.Triggers)
	}
}

func TestReconcile_AllSignalsCombined(t *testing.T) {
	s := New(Config{})
	pre := &workflow.PreCheckpoint{StepID: "s1"}
	post := &workflow.PostCheckpoint{
		MetCommitment: false,
		Deviations:    []string{"d1"},
		Concerns:      []string{"c1"},
		Unexpected:    []string{"u1"},
	}
	result := s.Reconcile(pre, post)
	if !result.Escalate {
		t.Error("Escalate should be true")
	}
	if len(result.Triggers) != 4 {
		t.Errorf("expected 4 triggers, got %d: %v", len(result.Triggers), result.Triggers)
	}
}
