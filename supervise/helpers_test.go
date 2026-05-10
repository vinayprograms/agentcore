package supervise

import (
	"context"

	"github.com/vinayprograms/agentkit/llm"
)

// scriptedModel returns canned Chat responses by call index.
type scriptedModel struct {
	replies []string
	calls   int
}

func (m *scriptedModel) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	r := ""
	if m.calls < len(m.replies) {
		r = m.replies[m.calls]
		m.calls++
	} else if len(m.replies) > 0 {
		r = m.replies[len(m.replies)-1]
	}
	return &llm.ChatResponse{Content: r}, nil
}

// errorModel returns an error on every Chat call.
type errorModel struct{ err string }

func (m *errorModel) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	return nil, &stubError{m.err}
}

type stubError struct{ msg string }

func (e *stubError) Error() string { return e.msg }
