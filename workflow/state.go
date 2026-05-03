package workflow

import (
	"maps"
	"strings"
)

// State carries mutable data through execution.
// Inputs are bound at workflow start; Outputs accumulate as steps complete.
// Failures records goals that hit their WITHIN cap without converging.
//
// CurrentGoal and supervision status are passed via context.Context, not here.
type State struct {
	Inputs   map[string]string
	Outputs  map[string]string
	Failures map[string]int // goal name → iteration cap reached
}

// NewState creates a State with the given inputs.
func NewState(inputs map[string]string) *State {
	return &State{
		Inputs:   maps.Clone(inputs),
		Outputs:  make(map[string]string),
		Failures: make(map[string]int),
	}
}

// fork returns a child State that shares the parent's read view (Inputs +
// current Outputs) but writes to its own Outputs map. Used for parallel
// agent fan-out inside a Goal or Convergence so goroutines don't race.
func (s *State) fork() *State {
	return &State{
		Inputs:   s.Inputs, // shared; callers must not mutate
		Outputs:  maps.Clone(s.Outputs),
		Failures: make(map[string]int),
	}
}

// interpolate replaces $var references in text with values from Outputs
// (checked first) then Inputs. Unknown variables are left as-is.
func (s *State) interpolate(text string) string {
	if !strings.Contains(text, "$") {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	i := 0
	for i < len(text) {
		if text[i] != '$' {
			b.WriteByte(text[i])
			i++
			continue
		}
		j := i + 1
		for j < len(text) && isIdentChar(text[j]) {
			j++
		}
		if j == i+1 {
			b.WriteByte('$')
			i++
			continue
		}
		name := text[i+1 : j]
		if v, ok := s.Outputs[name]; ok {
			b.WriteString(v)
		} else if v, ok := s.Inputs[name]; ok {
			b.WriteString(v)
		} else {
			b.WriteString(text[i:j])
		}
		i = j
	}
	return b.String()
}

func isIdentChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '_'
}

// extractVars returns the names of all $var references in text. Used at
// preflight to verify every reference resolves to a declared parameter or
// upstream output.
func extractVars(text string) []string {
	if !strings.Contains(text, "$") {
		return nil
	}
	var names []string
	i := 0
	for i < len(text) {
		if text[i] != '$' {
			i++
			continue
		}
		j := i + 1
		for j < len(text) && isIdentChar(text[j]) {
			j++
		}
		if j > i+1 {
			names = append(names, text[i+1:j])
		}
		i = j
	}
	return names
}
