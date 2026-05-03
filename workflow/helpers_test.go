package workflow

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/vinayprograms/agentkit/llm"
	"github.com/vinayprograms/agentkit/mcp"
	"github.com/vinayprograms/agentkit/tools"
)

// Compile-time assertion: fakeMCP satisfies the MCPManager interface.
var _ MCPManager = (*fakeMCP)(nil)

// Shared test fixtures. One file per source file is the rule for tests; this
// file is the explicit exception — fakes and helpers used by more than one
// _test.go land here so each per-source test file stays focused on the
// behaviour it asserts.

// ---------------------------------------------------------------------------
// LLM fakes
// ---------------------------------------------------------------------------

// recordingModel captures every message it was sent so tests can assert what
// the agent transmitted after applying any internal transformations
// (interpolation, system-context layering, structured-output instructions).
type recordingModel struct {
	mu       sync.Mutex
	captured []llm.Message
	reply    string
}

func (m *recordingModel) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	m.mu.Lock()
	m.captured = append(m.captured, req.Messages...)
	m.mu.Unlock()
	return &llm.ChatResponse{Content: m.reply}, nil
}

// scriptedModel returns scripted responses by call number. Past the script
// length it repeats the last response. Used to drive multi-call flows.
type scriptedModel struct {
	replies []*llm.ChatResponse
	calls   int32
}

func (m *scriptedModel) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	idx := int(atomic.AddInt32(&m.calls, 1)) - 1
	if idx >= len(m.replies) {
		idx = len(m.replies) - 1
	}
	return m.replies[idx], nil
}

// jsonModel is a string-script variant used by the supervision pipeline tests
// (which need to control the JSON the COMMIT and POST phases see). It also
// lets a single test inject a model error.
type jsonModel struct {
	replies  []string
	idx      int
	captured []llm.ChatRequest
	err      error
}

func (m *jsonModel) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	m.captured = append(m.captured, req)
	if m.err != nil {
		return nil, m.err
	}
	r := ""
	if m.idx < len(m.replies) {
		r = m.replies[m.idx]
		m.idx++
	} else if len(m.replies) > 0 {
		r = m.replies[len(m.replies)-1]
	}
	return &llm.ChatResponse{Content: r}, nil
}

// countingModel counts calls and returns the same canned text every time.
type countingModel struct {
	calls int
	reply string
}

func (m *countingModel) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	m.calls++
	return &llm.ChatResponse{Content: m.reply}, nil
}

// modelTag is unique-by-pointer so tests can verify which Model an agent or
// goal actually invoked — llm.Model is an interface, so identity by pointer
// is the only reliable signal.
type modelTag struct {
	id       string
	captured []llm.Message
}

func (m *modelTag) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	m.captured = append(m.captured, req.Messages...)
	return &llm.ChatResponse{Content: "ok"}, nil
}

// loopModel returns one tool call, then a final text response — used to drive
// the agentic loop's tool-call branch in goals, convergences, and agents.
type loopModel struct {
	calls atomic.Int32
}

func (m *loopModel) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	n := m.calls.Add(1)
	if n == 1 {
		return &llm.ChatResponse{
			ToolCalls: []llm.ToolCallResponse{
				{ID: "c1", Name: "echo", Args: map[string]any{"text": "hello"}},
			},
		}, nil
	}
	return &llm.ChatResponse{Content: "done"}, nil
}

// loopErrModel surfaces an error from Chat so tests can check error
// propagation through the various Execute paths.
type loopErrModel struct{}

func (loopErrModel) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	return nil, errors.New("model down")
}

// stubModel satisfies llm.Model with no behaviour — useful when Validate
// runs but Execute never does.
type stubModel struct{}

func (stubModel) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{}, nil
}

// ---------------------------------------------------------------------------
// Step fakes
// ---------------------------------------------------------------------------

// runtimeCapture records the *Runtime its Execute is called with via a shared
// destination pointer, so tests can verify Override propagation through
// composition boundaries.
type runtimeCapture struct {
	dest **Runtime
}

func (rc *runtimeCapture) Execute(ctx context.Context, rt *Runtime, state *State) error {
	*rc.dest = rt
	return nil
}
func (rc *runtimeCapture) Children() []Node { return nil }
func (rc *runtimeCapture) Name() string     { return "runtimeCapture" }
func (rc *runtimeCapture) Kind() Kind       { return kindOf(rc) }
func (rc *runtimeCapture) Validate() error  { return nil }
func (rc *runtimeCapture) clone() Step {
	cp := *rc
	return &cp
}

// envCapture is the same idea, used by workflow-Execute tests that want to
// inspect the derived per-execution Runtime (e.g., to confirm the content
// guard was attached without mutating the caller's *Runtime).
type envCapture struct {
	dest **Runtime
}

func (e *envCapture) Execute(ctx context.Context, rt *Runtime, state *State) error {
	*e.dest = rt
	return nil
}
func (e *envCapture) Children() []Node { return nil }
func (e *envCapture) Name() string     { return "envCapture" }
func (e *envCapture) Kind() Kind       { return kindOf(e) }
func (e *envCapture) clone() Step {
	cp := *e
	return &cp
}

var (
	_ Step = (*envCapture)(nil)
	_ Node = (*envCapture)(nil)
)

// hookSink records every workflow event for later assertion.
type hookSink struct {
	events []any
}

func (h *hookSink) Fire(ctx context.Context, event any) {
	h.events = append(h.events, event)
}

// ---------------------------------------------------------------------------
// Type assertions used by clone-independence tests
// ---------------------------------------------------------------------------

func mustAgent(t testingTB, s Step) *agent {
	t.Helper()
	a, ok := s.(*agent)
	if !ok {
		t.Fatalf("expected *agent, got %T", s)
	}
	return a
}

func mustGoal(t testingTB, s Step) *goal {
	t.Helper()
	g, ok := s.(*goal)
	if !ok {
		t.Fatalf("expected *goal, got %T", s)
	}
	return g
}

func mustConvergence(t testingTB, s Step) *convergence {
	t.Helper()
	c, ok := s.(*convergence)
	if !ok {
		t.Fatalf("expected *convergence, got %T", s)
	}
	return c
}

// testingTB is a minimal cover of *testing.T's surface used by these helpers.
// It avoids importing "testing" into helpers and makes them reusable.
type testingTB interface {
	Helper()
	Fatalf(format string, args ...any)
}

// ---------------------------------------------------------------------------
// Tools registry helper
// ---------------------------------------------------------------------------

// fakeMCP satisfies workflow.MCPManager without requiring a real MCP server.
// Tests pre-load tools and a CallTool stub, and the workflow's MCP branches
// in allToolDefs / executeTool dispatch through it.
type fakeMCP struct {
	tools     []mcp.ToolWithServer
	finder    func(name string) (string, bool)
	caller    func(ctx context.Context, server, tool string, args map[string]any) (*mcp.Result, error)
	callsSeen []mcpCall
}

type mcpCall struct {
	server, tool string
	args         map[string]any
}

func (f *fakeMCP) AllTools() []mcp.ToolWithServer { return f.tools }

func (f *fakeMCP) FindTool(name string) (string, bool) {
	if f.finder != nil {
		return f.finder(name)
	}
	return "", false
}

func (f *fakeMCP) CallTool(ctx context.Context, server, tool string, args map[string]any) (*mcp.Result, error) {
	f.callsSeen = append(f.callsSeen, mcpCall{server: server, tool: tool, args: args})
	if f.caller != nil {
		return f.caller(ctx, server, tool, args)
	}
	return &mcp.Result{}, nil
}

// echoTool returns its single string arg unchanged. Used to populate a small
// tools.Registry for tests that drive tool dispatch.
type echoTool struct{}

func (echoTool) Name() string        { return "echo" }
func (echoTool) Description() string { return "echo the input" }
func (echoTool) Parameters() map[string]tools.Param {
	return map[string]tools.Param{
		"text": {Type: tools.StringParam, Description: "input", Required: true},
	}
}
func (echoTool) Execute(ctx context.Context, args tools.Args) (string, error) {
	v, _ := args.String("text")
	return v, nil
}

func makeRegistry(t testingTB) *tools.Registry {
	t.Helper()
	reg := tools.NewRegistry()
	if err := reg.Register(tools.New(echoTool{})); err != nil {
		t.Fatalf("register: %v", err)
	}
	return reg
}

// ---------------------------------------------------------------------------
// Convenience string contains (assertion readability)
// ---------------------------------------------------------------------------

func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
