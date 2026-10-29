package guard

import (
	"sync"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/pkg/policy"
)

type bucket struct {
	sec        int64
	admitted   int64
	shed       int64
	errors     int64
	hist       [NumBuckets]int64
	admittedBy [4]int64
	shedBy     [4]int64
}

type Window struct {
	mu      sync.Mutex
	seconds int64
	ring    []bucket
}

type WindowStats struct {
	Admitted, Shed, Errors int64
	AdmittedRPS, ShedRPS   float64
	ErrorRate              float64
	P50, P95, P99          float64
	AdmittedRPSBy          [4]float64
	ShedRPSBy              [4]float64
}

func NewWindow(seconds int) *Window {
	w := &Window{seconds: int64(seconds), ring: make([]bucket, seconds+1)}
	w.Reset()
	return w
}

func (w *Window) Reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := range w.ring {
		w.ring[i] = bucket{sec: -1}
	}
}

func (w *Window) slot(sec int64) *bucket {
	b := &w.ring[sec%int64(len(w.ring))]
	if b.sec != sec {
		*b = bucket{sec: sec}
	}
	return b
}

func prioIndex(p policy.Priority) int {
	if p < policy.PriorityCritical || p > policy.PriorityBackground {
		return int(policy.PriorityUser)
	}
	return int(p)
}

func (w *Window) RecordAdmitted(now time.Time, latency time.Duration, isError bool, p policy.Priority) {
	w.mu.Lock()
	defer w.mu.Unlock()
	b := w.slot(now.Unix())
	b.admitted++
	b.admittedBy[prioIndex(p)]++
	if isError {
		b.errors++
	}
	b.hist[BucketIndex(float64(latency)/float64(time.Millisecond))]++
}

func (w *Window) RecordShed(now time.Time, p policy.Priority) {
	w.mu.Lock()
	defer w.mu.Unlock()
	b := w.slot(now.Unix())
	b.shed++
	b.shedBy[prioIndex(p)]++
}

// The current second is excluded so the numbers stay stable at the cost of up to 1 s of lag.
func (w *Window) Stats(now time.Time) WindowStats {
	w.mu.Lock()
	defer w.mu.Unlock()
	var s WindowStats
	var hist [NumBuckets]int64
	var admittedBy, shedBy [4]int64
	nowSec := now.Unix()
	for sec := nowSec - w.seconds; sec <= nowSec-1; sec++ {
		if sec < 0 {
			continue
		}
		b := &w.ring[sec%int64(len(w.ring))]
		if b.sec != sec {
			continue
		}
		s.Admitted += b.admitted
		s.Shed += b.shed
		s.Errors += b.errors
		for i := range hist {
			hist[i] += b.hist[i]
		}
		for i := range admittedBy {
			admittedBy[i] += b.admittedBy[i]
			shedBy[i] += b.shedBy[i]
		}
	}
	secs := float64(w.seconds)
	s.AdmittedRPS = float64(s.Admitted) / secs
	s.ShedRPS = float64(s.Shed) / secs
	if s.Admitted > 0 {
		s.ErrorRate = float64(s.Errors) / float64(s.Admitted)
	}
	for i := range admittedBy {
		s.AdmittedRPSBy[i] = float64(admittedBy[i]) / secs
		s.ShedRPSBy[i] = float64(shedBy[i]) / secs
	}
	s.P50, s.P95, s.P99 = Percentile(hist, 0.50), Percentile(hist, 0.95), Percentile(hist, 0.99)
	return s
}
