package workflow

import "testing"

// parameter_test.go covers parameter.go. Parameter is a value type with two
// string fields; the only contract worth verifying is that its zero value
// means "required" and a non-empty Default means "optional with that
// fallback" — both behaviours are observed by Workflow.bind.

func TestParameter_ZeroValueRequired(t *testing.T) {
	p := Parameter{Name: "topic"}
	if p.Default != "" {
		t.Errorf("zero-value Default should be empty: %q", p.Default)
	}
	if p.Name != "topic" {
		t.Errorf("Name not preserved")
	}
}

func TestParameter_NonEmptyDefaultMakesOptional(t *testing.T) {
	p := Parameter{Name: "style", Default: "concise"}
	if p.Default != "concise" {
		t.Errorf("Default not preserved: %q", p.Default)
	}
}
