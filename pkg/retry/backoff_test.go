package retry

import (
	"testing"
	"time"
)

func TestBackoffCeiling(t *testing.T) {
	const base, max = 200 * time.Millisecond, 10 * time.Second
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 200 * time.Millisecond},
		{3, 1600 * time.Millisecond},
		{6, 10 * time.Second},
		{100, 10 * time.Second},
	}
	for _, tc := range tests {
		got := BackoffCeiling(tc.attempt, base, max)
		t.Logf("BackoffCeiling(%d) = %v, want %v", tc.attempt, got, tc.want)
		if got != tc.want {
			t.Errorf("BackoffCeiling(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

func TestFullJitterStaysInRange(t *testing.T) {
	const base, max = 200 * time.Millisecond, 10 * time.Second
	for attempt := 0; attempt < 10; attempt++ {
		ceiling := BackoffCeiling(attempt, base, max)
		for i := 0; i < 1000; i++ {
			if d := FullJitter(attempt, base, max); d < 0 || d > ceiling {
				t.Fatalf("FullJitter(%d) = %v, outside [0, %v]", attempt, d, ceiling)
			}
		}
	}
}

func TestFullJitterZeroCeiling(t *testing.T) {
	if d := FullJitter(3, 0, time.Second); d != 0 {
		t.Fatalf("FullJitter with zero base = %v, want 0", d)
	}
}
