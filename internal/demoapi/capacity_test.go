package demoapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/pkg/clock"
)

func newTestCapacity(conns int, wait time.Duration, respectCancel bool) *Capacity {
	return NewCapacity(conns, wait, respectCancel, NewChaos(clock.NewFake(time.Unix(1000, 0))))
}

func waitForInUse(t *testing.T, c *Capacity, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for c.InUse() != want {
		if time.Now().After(deadline) {
			t.Fatalf("InUse = %d, want %d", c.InUse(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCapacityPoolTimeout(t *testing.T) {
	c := newTestCapacity(1, 50*time.Millisecond, false)
	done := make(chan error, 1)
	go func() { done <- c.Do(context.Background(), 200*time.Millisecond) }()
	waitForInUse(t, c, 1)

	start := time.Now()
	err := c.Do(context.Background(), 200*time.Millisecond)
	elapsed := time.Since(start)
	t.Logf("second Do returned %v after %v", err, elapsed)
	if !errors.Is(err, ErrPoolTimeout) {
		t.Fatalf("err = %v, want ErrPoolTimeout", err)
	}
	if elapsed < 40*time.Millisecond || elapsed > 150*time.Millisecond {
		t.Fatalf("timed out after %v, want about 50ms", elapsed)
	}
	if err := <-done; err != nil {
		t.Fatalf("first Do: %v", err)
	}
	if c.InUse() != 0 {
		t.Fatalf("InUse after release = %d, want 0", c.InUse())
	}
}

func TestCapacityRespectCancel(t *testing.T) {
	tests := []struct {
		name          string
		respectCancel bool
		wantErr       bool
	}{
		{"respects cancel", true, true},
		{"ignores cancel", false, false},
	}
	for _, tc := range tests {
		c := newTestCapacity(1, time.Second, tc.respectCancel)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		start := time.Now()
		err := c.Do(ctx, 100*time.Millisecond)
		cancel()
		t.Logf("%s: err = %v after %v", tc.name, err, time.Since(start))
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, want error = %v", tc.name, err, tc.wantErr)
		}
		if tc.wantErr && !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%s: err = %v, want DeadlineExceeded", tc.name, err)
		}
		if c.InUse() != 0 {
			t.Errorf("%s: connection not released", tc.name)
		}
	}
}

func TestCapacityChaosHoldsConnection(t *testing.T) {
	clk := clock.NewFake(time.Unix(1000, 0))
	chaos := NewChaos(clk)
	chaos.Set(ChaosSlow, 100*time.Millisecond, time.Minute)
	c := NewCapacity(1, time.Second, false, chaos)

	start := time.Now()
	if err := c.Do(context.Background(), 0); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 90*time.Millisecond {
		t.Fatalf("Do took %v, want at least the 100ms of chaos latency", elapsed)
	}
}
