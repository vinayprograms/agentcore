// Package workflow implements agentcore's execution model: a composition tree
// of typed nodes where each primitive owns its own execution loop.
//
// The package exposes constructors (workflow.New, workflow.Agent, workflow.Goal,
// workflow.Convergence, workflow.Sequence) that return values of unexported types.
// Direct struct-literal construction is impossible by design — every composition
// boundary deep-copies its arguments, so a node placed into one parent cannot
// alias into another.
//
// Two orthogonal interfaces describe what nodes are:
//
//   - Node: anything traversable for validation and observability.
//   - Step: anything that executes (Agent, Goal, Convergence).
//
// Node and Step are independent — Step does not embed Node. The concrete types
// that need both implement each separately.
package workflow

import (
	"context"
	"reflect"
	"strings"
)

// Kind identifies the type of a workflow node returned by Node.Kind().
//
// Valid values are exactly:
//
//	"workflow", "run", "goal", "convergence", "agent"
//
// The set is closed — only types defined in this package implement Node, and
// each returns one of these literals. Compare against literals at the call
// site:
//
//	if n.Kind() == "agent" { ... }
type Kind string

// Node is any element that can be visited when walking the workflow tree.
// Every node carries a name and a kind, so a traversal can act on a node
// without relying on type assertions to unexported types.
type Node interface {
	Name() string
	Kind() Kind
	Children() []Node
}

// Step owns its own execution loop. It reads from and writes to State,
// using Runtime for all external dependencies.
//
// The unexported clone method closes the interface to this package — only
// types defined here can implement Step. clone is called at every composition
// boundary to enforce node independence.
type Step interface {
	Execute(ctx context.Context, rt *Runtime, state *State) error
	clone() Step
}

// kindOf derives a node's Kind from the concrete Go type, so renaming a type
// (e.g. agent → persona) automatically updates the kind reported by Node.Kind.
// The kind is the dereferenced type's name, lowercased so kinds remain a
// stable contract regardless of whether the underlying type is exported.
func kindOf(v any) Kind {
	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return Kind(strings.ToLower(t.Name()))
}
