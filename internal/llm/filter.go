package llm

import (
	"fmt"
	"regexp"
)

// ModelFilter applies allow/block regex patterns to a slice of ModelInfo.
// Patterns are matched against model ID.
type ModelFilter struct {
	allowlist []*regexp.Regexp
	blocklist []*regexp.Regexp
}

// NewModelFilter compiles regex allow/block patterns.
// Returns an error if any pattern is invalid.
func NewModelFilter(allow, block []string) (*ModelFilter, error) {
	f := &ModelFilter{}
	for _, pat := range allow {
		r, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("llm: allow pattern %q: %w", pat, err)
		}
		f.allowlist = append(f.allowlist, r)
	}
	for _, pat := range block {
		r, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("llm: block pattern %q: %w", pat, err)
		}
		f.blocklist = append(f.blocklist, r)
	}
	return f, nil
}

// Apply filters models: removes those matching any blocklist pattern, then
// (if allowlist is non-empty) keeps only those matching at least one allowlist pattern.
func (f *ModelFilter) Apply(models []ModelInfo) []ModelInfo {
	out := make([]ModelInfo, 0, len(models))
	for _, m := range models {
		if f.blocked(m.ID) {
			continue
		}
		if len(f.allowlist) > 0 && !f.allowed(m.ID) {
			continue
		}
		out = append(out, m)
	}
	return out
}

func (f *ModelFilter) allowed(id string) bool {
	for _, r := range f.allowlist {
		if r.MatchString(id) {
			return true
		}
	}
	return false
}

func (f *ModelFilter) blocked(id string) bool {
	for _, r := range f.blocklist {
		if r.MatchString(id) {
			return true
		}
	}
	return false
}
