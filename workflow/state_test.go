package workflow

import (
	"slices"
	"testing"
)

// state_test.go covers state.go: NewState, fork, interpolate (every branch
// including resolution order, lone-$, and unknown vars), and the extractVars
// helper used by Validate.

func TestState_NewStateClonesInputs(t *testing.T) {
	in := map[string]string{"a": "1"}
	s := NewState(in)
	in["a"] = "mutated"
	if s.Inputs["a"] != "1" {
		t.Errorf("NewState should clone inputs; got %q", s.Inputs["a"])
	}
}

func TestState_ForkSeparatesOutputs(t *testing.T) {
	parent := NewState(map[string]string{"x": "X"})
	parent.Outputs["a"] = "from-parent"

	child := parent.fork()
	child.Outputs["b"] = "child-only"

	if parent.Outputs["b"] != "" {
		t.Errorf("child write leaked into parent: %v", parent.Outputs)
	}
	if child.Outputs["a"] != "from-parent" {
		t.Errorf("child should see parent's snapshot at fork: %v", child.Outputs)
	}
	if &parent.Inputs == &child.Inputs {
		// Inputs is shared by design; a == b on map header is irrelevant
	}
}

func TestState_InterpolateOutputsWinOverInputs(t *testing.T) {
	s := NewState(map[string]string{"x": "input"})
	s.Outputs["x"] = "output"
	if got := s.interpolate("$x"); got != "output" {
		t.Errorf("got %q", got)
	}
}

func TestState_InterpolateUnknownLeftLiteral(t *testing.T) {
	s := NewState(nil)
	if got := s.interpolate("$unknown rest"); got != "$unknown rest" {
		t.Errorf("got %q", got)
	}
}

func TestState_InterpolateLoneDollar(t *testing.T) {
	s := NewState(map[string]string{"x": "X"})
	cases := []struct{ in, want string }{
		{"price is $", "price is $"},
		{"$ at start", "$ at start"},
		{"between $ vars", "between $ vars"},
		{"$x and $", "X and $"},
		{"no dollars at all", "no dollars at all"}, // fast-path: no '$' in string
	}
	for _, c := range cases {
		if got := s.interpolate(c.in); got != c.want {
			t.Errorf("interpolate(%q): got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestExtractVars(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"no vars", nil},
		{"$a $b $c", []string{"a", "b", "c"}},
		{"$a $", []string{"a"}}, // lone $ ignored
		{"$a-not-ident $b", []string{"a", "b"}},
	}
	for _, c := range cases {
		got := extractVars(c.in)
		if !slices.Equal(got, c.want) {
			t.Errorf("extractVars(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestIsIdentChar(t *testing.T) {
	for _, c := range "abcXYZ_0179" {
		if !isIdentChar(byte(c)) {
			t.Errorf("isIdentChar(%q) should be true", c)
		}
	}
	for _, c := range " $-./@" {
		if isIdentChar(byte(c)) {
			t.Errorf("isIdentChar(%q) should be false", c)
		}
	}
}
