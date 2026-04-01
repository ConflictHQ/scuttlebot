package oracle

import "context"

// StubProvider returns a fixed summary. Used in tests and when no LLM is configured.
type StubProvider struct {
	Response string
	Err      error
}

func (s *StubProvider) Summarize(_ context.Context, _ string) (string, error) {
	return s.Response, s.Err
}
