package document

import "unicode/utf8"

// RuneTokenizer is a deterministic, model-independent input splitter. A
// profile using it should keep chunk limits conservative relative to the
// embedding model's actual context window.
type RuneTokenizer struct{}

func (RuneTokenizer) Identity() TokenizerIdentity {
	return TokenizerIdentity{Name: "utf8-rune", Revision: "v1"}
}

func (RuneTokenizer) PrefixTokenCountsMonotonic() bool { return true }

func (RuneTokenizer) Tokenize(text string, limit int) ([]TokenBoundary, error) {
	count := utf8.RuneCountInString(text)
	if count > limit {
		return nil, ErrTokenizerLimit
	}
	result := make([]TokenBoundary, count)
	for index := range result {
		result[index] = TokenBoundary{Start: index, End: index + 1}
	}
	return result, nil
}
