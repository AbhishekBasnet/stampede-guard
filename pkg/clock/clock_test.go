package clock

import (
	"testing"
	"time"
)

func TestFakeAdvanceAndSet(t *testing.T) {
	start := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	f := NewFake(start)
	t.Logf("NewFake(%v) -> Now() = %v", start, f.Now())

	f.Advance(5 * time.Second)
	t.Logf("Advance(5s) -> Now() = %v", f.Now())
	if got, want := f.Now(), start.Add(5*time.Second); !got.Equal(want) {
		t.Fatalf("after Advance: got %v, want %v", got, want)
	}

	f.Set(start)
	t.Logf("Set(%v) -> Now() = %v", start, f.Now())
	if got := f.Now(); !got.Equal(start) {
		t.Fatalf("after Set: got %v, want %v", got, start)
	}
}

func TestRealImplementsClock(t *testing.T) {
	var c Clock = Real{}
	now := c.Now()
	t.Logf("Real{}.Now() = %v (system time)", now)
	if d := time.Since(now); d < 0 || d > time.Second {
		t.Fatalf("Real clock is off by %v", d)
	}
}
