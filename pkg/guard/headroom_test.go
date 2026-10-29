package guard

import (
	"testing"

	"github.com/AbhishekBasnet/stampede-guard/pkg/policy"
)

var testHeadroomConfig = HeadroomConfig{TargetP99Ms: 250, MaxP99Ms: 1000, MaxErrorRate: 0.05, MaxInFlight: 200, MinSamples: 20}

func TestComputeHeadroom(t *testing.T) {
	tests := []struct {
		p99, errRate          float64
		inFlight, samples     int64
		latency, errScore     float64
		concurrency, headroom float64
	}{
		{250, 0, 0, 100, 1, 1, 1, 1},
		{625, 0, 0, 100, 0.5, 1, 1, 0.5},
		{1000, 0, 0, 100, 0, 1, 1, 0},
		{100, 0, 0, 100, 1, 1, 1, 1},
		{1500, 0, 0, 100, 0, 1, 1, 0},
		{100, 0.025, 0, 100, 1, 0.5, 1, 0.5},
		{100, 0.2, 0, 100, 1, 0, 1, 0},
		{100, 0, 100, 100, 1, 1, 0.5, 0.5},
		{100, 0, 300, 100, 1, 1, 0, 0},
		{400, 0.01, 50, 100, 0.8, 0.8, 0.75, 0.75},
		{5000, 0.5, 10, 19, 1, 1, 0.95, 0.95},
	}
	for _, tc := range tests {
		s, h := ComputeHeadroom(testHeadroomConfig, tc.p99, tc.errRate, tc.inFlight, tc.samples)
		t.Logf("p99 %v err %v inflight %d samples %d -> %+v headroom %v", tc.p99, tc.errRate, tc.inFlight, tc.samples, s, h)
		if !near(s.Latency, tc.latency, 1e-9) || !near(s.Error, tc.errScore, 1e-9) ||
			!near(s.Concurrency, tc.concurrency, 1e-9) || !near(h, tc.headroom, 1e-9) {
			t.Errorf("p99 %v err %v inflight %d samples %d: got %+v headroom %v, want %v %v %v headroom %v",
				tc.p99, tc.errRate, tc.inFlight, tc.samples, s, h, tc.latency, tc.errScore, tc.concurrency, tc.headroom)
		}
	}
}

func TestTargetMode(t *testing.T) {
	tests := []struct {
		h    float64
		want policy.Mode
	}{
		{1, policy.ModeNormal}, {0.75, policy.ModeNormal}, {0.5, policy.ModeNormal},
		{0.49, policy.ModeElevated}, {0.25, policy.ModeElevated},
		{0.24, policy.ModeBrownout}, {0.10, policy.ModeBrownout},
		{0.09, policy.ModeShed}, {0, policy.ModeShed},
	}
	for _, tc := range tests {
		got := TargetMode(tc.h)
		t.Logf("TargetMode(%v) = %v, want %v", tc.h, got, tc.want)
		if got != tc.want {
			t.Errorf("TargetMode(%v) = %v, want %v", tc.h, got, tc.want)
		}
	}
}
