package guard

import "time"

type Snapshot struct {
	At               time.Time                `json:"at"`
	Mode             string                   `json:"mode"`
	ComputedMode     string                   `json:"computed_mode"`
	ModeSince        time.Time                `json:"mode_since"`
	OverrideActive   bool                     `json:"override_active"`
	OverrideUntil    *time.Time               `json:"override_until,omitempty"`
	Enforce          bool                     `json:"enforce"`
	Headroom         float64                  `json:"headroom"`
	Scores           Scores                   `json:"scores"`
	InFlight         int64                    `json:"in_flight"`
	MaxInFlight      int64                    `json:"max_in_flight"`
	WindowSeconds    int                      `json:"window_s"`
	OfferedRPS       float64                  `json:"offered_rps"`
	AdmittedRPS      float64                  `json:"admitted_rps"`
	ShedRPS          float64                  `json:"shed_rps"`
	ErrorRate        float64                  `json:"error_rate"`
	P50Ms            float64                  `json:"p50_ms"`
	P95Ms            float64                  `json:"p95_ms"`
	P99Ms            float64                  `json:"p99_ms"`
	ByPriority       map[string]PriorityStats `json:"by_priority"`
	CampaignRequests map[string]int64         `json:"campaign_requests"`
}

type PriorityStats struct {
	AdmittedRPS float64 `json:"admitted_rps"`
	ShedRPS     float64 `json:"shed_rps"`
}
