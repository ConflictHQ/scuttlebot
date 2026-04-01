package llm

import "context"

// StubProvider returns a fixed response. Useful in tests and as a no-op placeholder.
type StubProvider struct {
	Response string
	Err      error
}

func (s *StubProvider) Summarize(_ context.Context, _ string) (string, error) {
	return s.Response, s.Err
}
