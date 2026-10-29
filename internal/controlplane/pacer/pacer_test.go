package pacer

import (
	"math"
	"testing"
	"time"

	"github.com/AbhishekBasnet/stampede-guard/pkg/policy"
)

var testCfg = AdaptiveConfig{
	MinRate: 20, MaxRate: 5000, StartRate: 20, AdditiveIncreasePerSec: 40,
	DecreaseFactor: 0.5, DecreaseCooldown: time.Second, HighHeadroom: 0.6,
}

var t0 = time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)

func TestAdaptiveNext(t *testing.T) {
	tests := []struct {
		name   string
		in     Input
		rate   float64
		reason string
	}{
		{"stale", Input{Health: Health{Stale: true}, CurrentRate: 500}, 20, "health_stale"},
		{"shed", Input{Health: Health{Mode: policy.ModeShed}, CurrentRate: 500}, 0, "paused_shed"},
		{"elevated", Input{Health: Health{Mode: policy.ModeElevated, Headroom: 0.3}, CurrentRate: 300}, 300, "hold_elevated"},
		{"increase", Input{Health: Health{Mode: policy.ModeNormal, Headroom: 0.9}, CurrentRate: 100}, 110, "increase"},
		{"hold", Input{Health: Health{Mode: policy.ModeNormal, Headroom: 0.55}, CurrentRate: 100}, 100, "hold_normal"},
		{"capped", Input{Health: Health{Mode: policy.ModeNormal, Headroom: 0.9}, CurrentRate: 195, RateCap: 200}, 200, "increase+capped"},
		{"deadline", Input{Health: Health{Mode: policy.ModeNormal, Headroom: 0.9}, CurrentRate: 100, RateCap: 200,
			Remaining: 60000, TimeLeft: 100 * time.Second}, 600, "deadline_pressure"},
		{"no deadline push when elevated", Input{Health: Health{Mode: policy.ModeElevated, Headroom: 0.3}, CurrentRate: 300,
			Remaining: 60000, TimeLeft: 100 * time.Second}, 300, "hold_elevated"},
		{"slow start", Input{Health: Health{Mode: policy.ModeNormal, Headroom: 0.9}}, 30, "increase"},
		{"cap below min", Input{Health: Health{Mode: policy.ModeNormal, Headroom: 0.9}, CurrentRate: 100, RateCap: 10}, 20, "increase+capped"},
		{"max clamp", Input{Health: Health{Mode: policy.ModeNormal, Headroom: 0.9}, CurrentRate: 4999}, 5000, "increase"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.in.Now = t0
			tt.in.TickSeconds = 0.25
			p := &Adaptive{Cfg: testCfg}
			got := p.Next(tt.in)
			t.Logf("rate=%v reason=%s", got.Rate, got.Reason)
			if math.Abs(got.Rate-tt.rate) > 1e-9 || got.Reason != tt.reason {
				t.Fatalf("Next = %+v, want rate %v reason %q", got, tt.rate, tt.reason)
			}
		})
	}
}

func TestAdaptiveBrownoutSequence(t *testing.T) {
	steps := []struct {
		at     time.Duration
		cur    float64
		rate   float64
		reason string
	}{
		{0, 400, 200, "decrease_brownout"},
		{250 * time.Millisecond, 200, 200, "hold_decrease_cooldown"},
		{time.Second, 200, 100, "decrease_brownout"},
		{2 * time.Second, 100, 50, "decrease_brownout"},
		{3 * time.Second, 50, 25, "decrease_brownout"},
		{4 * time.Second, 25, 20, "decrease_brownout"},
	}
	p := &Adaptive{Cfg: testCfg}
	for _, s := range steps {
		got := p.Next(Input{
			Now: t0.Add(s.at), TickSeconds: 0.25, CurrentRate: s.cur,
			Health: Health{Mode: policy.ModeBrownout, Headroom: 0.2},
		})
		t.Logf("t+%v cur=%v -> %v %s", s.at, s.cur, got.Rate, got.Reason)
		if math.Abs(got.Rate-s.rate) > 1e-9 || got.Reason != s.reason {
			t.Fatalf("t+%v: got %+v, want rate %v reason %q", s.at, got, s.rate, s.reason)
		}
	}
}

func TestUnlimitedAndStatic(t *testing.T) {
	if d := (Unlimited{}).Next(Input{}); d.Rate != 100000 || d.Reason != "unlimited" {
		t.Fatalf("Unlimited = %+v", d)
	}
	if d := (Static{Rate: 75}).Next(Input{Health: Health{Stale: true}}); d.Rate != 75 || d.Reason != "static" {
		t.Fatalf("Static = %+v", d)
	}
}

func TestEstimateAmplification(t *testing.T) {
	cfg := AmplificationConfig{DefaultPrior: 1.5, MinSent: 500, MinElapsed: 20 * time.Second}
	tests := []struct {
		name           string
		prior          float64
		sent, requests int64
		elapsed        time.Duration
		want           float64
	}{
		{"too few sent", 0, 100, 1000, 60 * time.Second, 1.5},
		{"keep higher prior", 2.0, 1000, 1500, 60 * time.Second, 2.0},
		{"observed wins", 1.5, 1000, 2500, 60 * time.Second, 2.5},
		{"too early", 1.5, 1000, 2500, 10 * time.Second, 1.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateAmplification(cfg, tt.prior, tt.sent, tt.requests, tt.elapsed)
			t.Logf("got %v", got)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRateCap(t *testing.T) {
	tests := []struct {
		name                            string
		capacity, util, base, amp, want float64
	}{
		{"spare over amplification", 900, 0.8, 333, 1.9, 203.68},
		{"no capacity", 0, 0.8, 333, 1.9, 0},
		{"spare floored at one", 900, 0.8, 800, 2, 0.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RateCap(tt.capacity, tt.util, tt.base, tt.amp)
			t.Logf("got %v", got)
			if math.Abs(got-tt.want) > 0.01 {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
