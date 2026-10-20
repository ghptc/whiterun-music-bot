// Package perfgate evaluates monotonic elapsed samples, never wall-clock dates.
package perfgate

import (
	"fmt"
	"math"
	"sort"
	"time"
)

const MinimumSamples = 100

type Distribution struct {
	N     int     `json:"n"`
	P50MS float64 `json:"p50_ms"`
	P95MS float64 `json:"p95_ms"`
	P99MS float64 `json:"p99_ms"`
	MaxMS float64 `json:"max_ms"`
}

// Summarize uses nearest-rank quantiles. Samples are retained in the report.
func Summarize(values []time.Duration) Distribution {
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	if len(sorted) == 0 {
		return Distribution{}
	}
	at := func(q float64) float64 {
		return float64(sorted[int(math.Ceil(q*float64(len(sorted))))-1]) / float64(time.Millisecond)
	}
	return Distribution{len(sorted), at(.50), at(.95), at(.99), at(1)}
}

func Check(values []time.Duration, budget time.Duration) error {
	d := Summarize(values)
	if d.N < MinimumSamples {
		return fmt.Errorf("insufficient samples: %d < %d", d.N, MinimumSamples)
	}
	for _, v := range values {
		if v <= 0 {
			return fmt.Errorf("invalid elapsed sample: %s", v)
		}
	}
	if d.P95MS > float64(budget)/float64(time.Millisecond) {
		return fmt.Errorf("p95 %.3f ms exceeds %.3f ms", d.P95MS, float64(budget)/float64(time.Millisecond))
	}
	return nil
}
