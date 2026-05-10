package supervise

import (
	"testing"

	"github.com/vinayprograms/agentcore/workflow"
)

func TestNew_ReturnsSupervisorInterface(t *testing.T) {
	s := New(Config{})
	if s == nil {
		t.Fatal("New returned nil")
	}
	var _ workflow.Supervisor = s
}

func TestConfig_ZeroValueIsUsable(t *testing.T) {
	s := New(Config{})
	if s == nil {
		t.Fatal("New(Config{}) returned nil")
	}
}
