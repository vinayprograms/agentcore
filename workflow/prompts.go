package workflow

import (
	"encoding/json"
	"fmt"
	"html"
	"strings"
)

// This file holds the prompt-construction and structured-output parsing
// helpers shared by Goal and Convergence. They live here rather than next to
// either type because they describe the LLM-message edge of the package —
// how prompts are assembled before each Chat call, and how structured
// replies are read back into State.Outputs.

// buildSystemPrompt constructs the system prompt for a Goal or a
// Convergence iteration, injecting Runtime.SystemContext when set.
func buildSystemPrompt(rt *Runtime) string {
	base := "You are a helpful assistant executing a goal."
	if rt.SystemContext != "" {
		return base + "\n\n" + rt.SystemContext
	}
	return base
}

// buildPrompt wraps a Goal's description with prior-output context and an
// optional structured-output instruction. Used by Goal.loop.
func buildPrompt(name, description string, state *State, outputs []string) string {
	var b strings.Builder

	if len(state.Outputs) > 0 {
		b.WriteString("<prior-goals>\n")
		for gname, out := range state.Outputs {
			fmt.Fprintf(&b, "<goal name=%q>\n%s\n</goal>\n", gname, html.EscapeString(out))
		}
		b.WriteString("</prior-goals>\n\n")
	}

	fmt.Fprintf(&b, "<goal name=%q>\n%s\n</goal>", name, html.EscapeString(description))

	if len(outputs) > 0 {
		b.WriteString("\n\nReturn a JSON object with exactly these fields: ")
		b.WriteString(strings.Join(outputs, ", "))
		b.WriteByte('.')
	}
	return b.String()
}

// buildConvergePrompt constructs the user prompt for one Convergence
// iteration, embedding prior-iteration history and the CONVERGED marker
// instruction. Used by Convergence.iterateSingle and iterateFanOut.
func buildConvergePrompt(name, description string, state *State, history []string, outputs []string) string {
	var b strings.Builder

	if len(state.Outputs) > 0 {
		b.WriteString("<prior-goals>\n")
		for gname, out := range state.Outputs {
			if gname == name {
				continue
			}
			fmt.Fprintf(&b, "<goal name=%q>\n%s\n</goal>\n", gname, html.EscapeString(out))
		}
		b.WriteString("</prior-goals>\n\n")
	}

	fmt.Fprintf(&b, "<goal name=%q>\n%s\n</goal>", name, html.EscapeString(description))

	if len(history) > 0 {
		b.WriteString("\n\n<convergence-history>\n")
		for i, h := range history {
			fmt.Fprintf(&b, "<iteration n=%d>\n%s\n</iteration>\n", i+1, html.EscapeString(h))
		}
		b.WriteString("</convergence-history>")
		b.WriteString("\n\nRefine the previous iteration. When satisfied, include the word CONVERGED in your response.")
	} else {
		b.WriteString("\n\nWhen satisfied with your result, include the word CONVERGED in your response.")
	}

	if len(outputs) > 0 {
		b.WriteString("\n\nReturn a JSON object with exactly these fields: ")
		b.WriteString(strings.Join(outputs, ", "))
		b.WriteByte('.')
	}
	return b.String()
}

// parseStructured extracts named fields from a JSON object embedded in an
// LLM response. The inverse of the structured-output instruction added by
// buildPrompt / buildConvergePrompt when WithOutputs(...) is declared.
//
// Returns a map of field-name → string value. Non-string field values are
// re-marshalled to JSON. Missing fields return an error; on any error, the
// caller should treat the LLM response as an unstructured raw text output.
func parseStructured(output string, fields []string) (map[string]string, error) {
	start := strings.Index(output, "{")
	end := strings.LastIndex(output, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON object found")
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(output[start:end+1]), &raw); err != nil {
		return nil, err
	}
	result := make(map[string]string, len(fields))
	for _, f := range fields {
		v, ok := raw[f]
		if !ok {
			return nil, fmt.Errorf("field %q missing from output", f)
		}
		switch s := v.(type) {
		case string:
			result[f] = s
		default:
			// v came from json.Unmarshal into map[string]any, so it is always
			// one of: bool, float64, []any, map[string]any, nil. All are
			// marshallable — we don't propagate Marshal's error.
			b, _ := json.Marshal(v)
			result[f] = string(b)
		}
	}
	return result, nil
}

// stepName returns the Name() of a Step by treating it as a Node. Used
// during fan-out construction (Goal.fanOut, Convergence.iterateFanOut) to
// label per-child outputs.
func stepName(s Step) string {
	if n, ok := s.(Node); ok {
		return n.Name()
	}
	return "unknown"
}
