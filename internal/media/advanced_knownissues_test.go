//go:build qa008_knownissues

package media

import (
	"context"
	"testing"
)

// Intentionally red until QA008-D01 is fixed. Excluded from normal CI; the
// report records this defect explicitly instead of changing the accepted rule.
func TestKnownIssuesNegativeExtractorDuration(t *testing.T) {
	r, _ := resolverFixture(t, `{"id":"abcdefghijk","title":"Song","duration":-1}`)
	tr, err := r.Resolve(context.Background(), "https://youtu.be/abcdefghijk")
	if err == nil && tr.Duration < 0 {
		t.Fatalf("QA008-D01: malformed provider duration escaped: %v", tr.Duration)
	}
}
