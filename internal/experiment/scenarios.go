package experiment

import (
	"fmt"
	"strings"
	"time"
)

type ChaosSpec struct {
	Kind     string
	ExtraMs  int
	Duration time.Duration
}

type Scenario struct {
	ID, Name, Description, Hypothesis string
	Enforce                           bool
	NaiveFraction                     float64
	SyncMode                          string
	Pacing                            string
	StaticRate                        int
	Chaos                             *ChaosSpec
	Warmup                            time.Duration
	Observe                           time.Duration
}

func (s Scenario) IsPush() bool { return s.Pacing != "" }

const (
	pushTail     = 40 * time.Second
	pushMaxWatch = 420 * time.Second
	syncSpread   = "spread"
	syncAligned  = "aligned"
)

func slowdown() *ChaosSpec { return &ChaosSpec{Kind: "slow", ExtraMs: 500, Duration: 30 * time.Second} }

func All() []Scenario {
	return []Scenario{
		{
			ID: "S1", Name: "push-unlimited", Description: "Shadow-mode guard, naive clients, a push to every device at once.",
			Hypothesis:    "Offered load jumps far above 900 req/s within about 2 s (prefetch) and stays high for about 20 s (opens); p99 above 1 s for many seconds; many timeouts; orders fail.",
			NaiveFraction: 1, SyncMode: syncSpread, Pacing: "unlimited", Warmup: 20 * time.Second,
		},
		{
			ID: "S2", Name: "push-static", Description: "Shadow-mode guard, naive clients, a push throttled to a fixed 300/s.",
			Hypothesis:    "Lower peak, but 20,000 / 300 is about 67 s to send, and 300 x 1.9 + 333 is about 900 req/s, right at capacity: small changes in user behaviour tip it over. The number is a guess.",
			NaiveFraction: 1, SyncMode: syncSpread, Pacing: "static", StaticRate: 300, Warmup: 20 * time.Second,
		},
		{
			ID: "S3", Name: "push-adaptive", Description: "Shadow-mode guard, naive clients, a push paced by the closed loop.",
			Hypothesis:    "Rate climbs from 20/s, levels off near the predicted cap, backs off if ELEVATED or BROWNOUT; p99 stays mostly under 1 s; the campaign takes longer (roughly 100 to 250 s) but finishes before the 300 s deadline.",
			NaiveFraction: 1, SyncMode: syncSpread, Pacing: "adaptive", Warmup: 20 * time.Second,
		},
		{
			ID: "S4", Name: "push-full-guard", Description: "Enforcing guard, guarded clients, adaptive pacing: the full system.",
			Hypothesis: "Like S3 plus: prefetch is suppressed or shed when degraded, orders succeed about 100%, lowest client failure count.",
			Enforce:    true, NaiveFraction: 0, SyncMode: syncSpread, Pacing: "adaptive", Warmup: 20 * time.Second,
		},
		{
			ID: "S5", Name: "slowdown-naive", Description: "Shadow-mode guard, naive clients, +500 ms latency for 30 s, no push.",
			Hypothesis:    "Capacity drops about 10x for 30 s; fixed 1 s retries multiply load; errors continue after the slowdown ends; long or no recovery (metastable).",
			NaiveFraction: 1, SyncMode: syncSpread, Chaos: slowdown(), Warmup: 20 * time.Second, Observe: 120 * time.Second,
		},
		{
			ID: "S6", Name: "slowdown-guarded", Description: "Enforcing guard, guarded clients, +500 ms latency for 30 s, no push.",
			Hypothesis: "Shedding keeps the queue short, clients back off with jitter and a budget; recovery within seconds after the slowdown ends; orders mostly succeed.",
			Enforce:    true, NaiveFraction: 0, SyncMode: syncSpread, Chaos: slowdown(), Warmup: 20 * time.Second, Observe: 120 * time.Second,
		},
		{
			ID: "S7", Name: "sync-aligned", Description: "Shadow-mode guard, naive clients whose background syncs all fire on the minute.",
			Hypothesis:    "A spike of about 20,000 requests at the start of every minute; p99 spikes each minute while the average load looks fine. Averages hide the problem.",
			NaiveFraction: 1, SyncMode: syncAligned, Warmup: 70 * time.Second, Observe: 150 * time.Second,
		},
	}
}

func ByID(id string) (Scenario, bool) {
	for _, s := range All() {
		if strings.EqualFold(s.ID, id) {
			return s, true
		}
	}
	return Scenario{}, false
}

func Select(name string) ([]Scenario, error) {
	if name == "" || strings.EqualFold(name, "all") {
		return All(), nil
	}
	s, ok := ByID(name)
	if !ok {
		return nil, fmt.Errorf("unknown scenario %q, want all or S1 to S7", name)
	}
	return []Scenario{s}, nil
}
