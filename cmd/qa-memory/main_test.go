package main

import (
	"context"
	"testing"
	"time"
)

func TestSimulatedReferenceOrderAndLifecycle(t *testing.T) {
	for _, cycles := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		got, err := simulate(ctx, 100, cycles)
		cancel()
		if err != nil || got != 100 {
			t.Fatalf("cycles=%v selected=%d err=%v", cycles, got, err)
		}
	}
}
func TestSimulatedCanceledWorkCannotSelect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := simulate(ctx, 100, false)
	if err == nil || got != 0 {
		t.Fatalf("canceled: selected=%d err=%v", got, err)
	}
}

func TestSQLiteControlPreservesFixedQueue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n, err := sqliteControl(ctx, t.TempDir()+"/qa-only.db", 2)
	if err != nil || n != 2 {
		t.Fatalf("writes=%d error=%v", n, err)
	}
}
