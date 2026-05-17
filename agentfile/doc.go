// Package agentfile parses Agentfile DSL source and compiles it to an
// executable workflow.Workflow plus a pre-populated workflow.Runtime base.
//
// The package exposes two entry points: Parse (source text + *os.Root →
// Spec, resolving FROM references and SKILL.md bundles inside the supplied
// root) and Compile (Spec → workflow.Workflow + workflow.Runtime). The
// parse/compile split matches the regexp and text/template packages.
// Using *os.Root means path-traversal escapes via "../" are blocked by the
// OS, not by application code.
//
// The package only translates Agentfile source into the workflow primitives
// shipped by agentcore/workflow; the runtime, supervisor, and content-guard
// implementations live in other agentcore packages. Consumers compose those
// (Tools, MCP, Telemetry, Supervisor, HumanCh) onto the returned Runtime
// before calling Execute.
//
// The Agentfile reference grammar is documented in REFERENCE.md.
package agentfile
