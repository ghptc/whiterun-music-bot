package logstore

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestFIX029MixedBurstKeepsAccountingAndPhysicalQuota(t *testing.T) {
	for _, limit := range []int64{1 << 20, 4 << 20} {
		t.Run(fmt.Sprintf("%dMiB", limit>>20), func(t *testing.T) {
			s := openTest(t, Options{MaxBytes: limit, Buffer: 4096, FlushInterval: time.Hour})
			log := logger(s)
			for i := 0; i < 200; i++ {
				payload := "small"
				if i%7 == 0 {
					payload = strings.Repeat("x", 8192)
				}
				log.Info("mixed_burst", "position", i, "detail", payload)
			}
			closeTest(t, s)
			m := s.Metrics()
			if m.Written != 200 || m.Lost != 0 || m.Overflow != 0 || m.Failures != 0 || m.Rows+m.Removed != m.Written {
				t.Fatalf("burst accounting: %+v", m)
			}
			bytes, err := PhysicalBytes(s.opt.Path)
			if err != nil || bytes > limit {
				t.Fatalf("physical quota: %d > %d, %v", bytes, limit, err)
			}
			t.Logf("limit=%d written=%d retained=%d removed=%d batches=%d bytes=%d", limit, m.Written, m.Rows, m.Removed, m.Batches, bytes)
		})
	}
}
