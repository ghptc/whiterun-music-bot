package media

import (
	"context"
	"testing"
)

// QA008-D01 must reject malformed metadata before it can enter a cache or queue.
func TestDefectNegativeExtractorDuration(t *testing.T) {
	r, _ := resolverFixture(t, `{"id":"abcdefghijk","title":"Song","duration":-1}`)
	tr, err := r.Resolve(context.Background(), "https://youtu.be/abcdefghijk")
	if err == nil || tr.URL != "" || tr.Duration != 0 {
		t.Fatalf("QA008-D01: malformed provider duration escaped: %v", tr.Duration)
	}
}
