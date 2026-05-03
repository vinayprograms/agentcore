package workflow

// Parameter declares one entry in a workflow's input set. Construct via
// struct literal:
//
//	workflow.Parameter{Name: "topic"}                       // required
//	workflow.Parameter{Name: "style", Default: "formal"}    // optional with default
//
// An empty Default means the parameter is required at execution time. A
// non-empty Default makes the parameter optional, falling back to that value
// when the caller does not supply one.
//
// Parameter is a value type containing only strings, which are immutable in
// Go. Shallow copy is deep copy; no clone or independence work is needed.
type Parameter struct {
	Name    string
	Default string
}
