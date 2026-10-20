package perfgate

import (
	"testing"
	"time"
)

func TestGateRejectsSlowOrIncompleteEvidence(t *testing.T) {
	values := make([]time.Duration, 100)
	for i := range values {
		values[i] = time.Duration(i+1) * time.Millisecond
	}
	d := Summarize(values)
	if d.P50MS != 50 || d.P95MS != 95 || d.P99MS != 99 || d.MaxMS != 100 {
		t.Fatal(d)
	}
	if err := Check(values, 95*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	for i := 94; i < 100; i++ {
		values[i] = 3 * time.Second
	}
	if Check(values, 2*time.Second) == nil {
		t.Fatal("slow transport accepted")
	}
	if Check(values[:99], 5*time.Second) == nil {
		t.Fatal("undersampled release approved")
	}
	values[0] = -1
	if Check(values, 5*time.Second) == nil {
		t.Fatal("invalid monotonic sample accepted")
	}
}
