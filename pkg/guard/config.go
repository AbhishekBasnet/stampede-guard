package guard

import (
	"time"

	"github.com/AbhishekBasnet/stampede-guard/pkg/policy"
)

type Config struct {
	WindowSeconds   int
	EvalInterval    time.Duration
	Cooldown        time.Duration
	TargetP99       time.Duration
	MaxP99          time.Duration
	MaxErrorRate    float64
	MaxInFlight     int64
	CriticalReserve int64
	MinSamples      int64
	Enforce         bool
	RoutePriorities map[string]policy.Priority // key "METHOD /exact/path"; missing route = user
	ExemptPrefixes  []string
	AdminToken      string
	MaxCampaignKeys int
}

func DefaultConfig() Config {
	return Config{
		WindowSeconds:   5,
		EvalInterval:    250 * time.Millisecond,
		Cooldown:        10 * time.Second,
		TargetP99:       250 * time.Millisecond,
		MaxP99:          time.Second,
		MaxErrorRate:    0.05,
		MaxInFlight:     200,
		CriticalReserve: 20,
		MinSamples:      20,
		Enforce:         true,
		RoutePriorities: map[string]policy.Priority{},
		ExemptPrefixes:  []string{"/guard/", "/admin/", "/metrics", "/healthz", "/v1/policy"},
		MaxCampaignKeys: 1000,
	}
}
