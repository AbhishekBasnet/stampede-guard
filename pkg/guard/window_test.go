package guard

import (
	"testing"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/pkg/policy"
)

func atMs(ms int64) time.Time { return time.UnixMilli(ms) }

func fill(w *Window, n int, at time.Time, errs int, p policy.Priority) {
	for i := 0; i < n; i++ {
		w.RecordAdmitted(at, 10*time.Millisecond, i < errs, p)
	}
}

func newFilledWindow() *Window {
	w := NewWindow(5)
	fill(w, 10, atMs(100200), 0, policy.PriorityUser)
	fill(w, 20, atMs(101500), 3, policy.PriorityUser)
	for i := 0; i < 5; i++ {
		w.RecordShed(atMs(101600), policy.PriorityUser)
	}
	fill(w, 1, atMs(102100), 0, policy.PriorityUser)
	return w
}

func TestWindowStats(t *testing.T) {
	w := newFilledWindow()
	s := w.Stats(atMs(102000))
	t.Logf("Stats(102.0) = %+v", s)
	if s.Admitted != 30 || s.Shed != 5 || s.Errors != 3 {
		t.Fatalf("counts: admitted %d shed %d errors %d, want 30 5 3", s.Admitted, s.Shed, s.Errors)
	}
	if !near(s.AdmittedRPS, 6, 1e-9) || !near(s.ShedRPS, 1, 1e-9) || !near(s.ErrorRate, 0.1, 1e-9) {
		t.Fatalf("rates: admitted %v shed %v error %v, want 6 1 0.1", s.AdmittedRPS, s.ShedRPS, s.ErrorRate)
	}
}

func TestWindowSlides(t *testing.T) {
	tests := []struct {
		nowMs int64
		want  int64
	}{{106900, 21}, {107000, 1}, {108000, 0}}
	w := newFilledWindow()
	for _, tc := range tests {
		s := w.Stats(atMs(tc.nowMs))
		t.Logf("Stats(%d ms) admitted = %d, want %d", tc.nowMs, s.Admitted, tc.want)
		if s.Admitted != tc.want {
			t.Errorf("Stats(%d ms) admitted = %d, want %d", tc.nowMs, s.Admitted, tc.want)
		}
	}
	if p99 := w.Stats(atMs(108000)).P99; p99 != 0 {
		t.Errorf("empty window P99 = %v, want 0", p99)
	}
}

func TestWindowRingReuse(t *testing.T) {
	w := newFilledWindow()
	fill(w, 1, atMs(106500), 0, policy.PriorityUser)
	s := w.Stats(atMs(107000))
	t.Logf("Stats(107.0) after reuse of second 100's slot: admitted = %d", s.Admitted)
	if s.Admitted != 2 {
		t.Fatalf("admitted = %d, want 2", s.Admitted)
	}
}

func TestWindowPerPriority(t *testing.T) {
	w := NewWindow(5)
	fill(w, 10, atMs(100500), 0, policy.PriorityPrefetch)
	w.RecordShed(atMs(100500), policy.PriorityBackground)
	s := w.Stats(atMs(101000))
	t.Logf("AdmittedRPSBy = %v, ShedRPSBy = %v", s.AdmittedRPSBy, s.ShedRPSBy)
	if !near(s.AdmittedRPSBy[policy.PriorityPrefetch], 2, 1e-9) {
		t.Errorf("AdmittedRPSBy[prefetch] = %v, want 2", s.AdmittedRPSBy[policy.PriorityPrefetch])
	}
	if !near(s.ShedRPSBy[policy.PriorityBackground], 0.2, 1e-9) {
		t.Errorf("ShedRPSBy[background] = %v, want 0.2", s.ShedRPSBy[policy.PriorityBackground])
	}
}

func TestWindowReset(t *testing.T) {
	w := newFilledWindow()
	w.Reset()
	if s := w.Stats(atMs(102000)); s.Admitted != 0 || s.Shed != 0 {
		t.Fatalf("after Reset: admitted %d shed %d, want 0 0", s.Admitted, s.Shed)
	}
}
