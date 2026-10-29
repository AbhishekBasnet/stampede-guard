package pacer

import (
	"time"

	"github.com/AbhishekBasnet/stampede-guard/pkg/policy"
)

type Health struct {
	Stale    bool
	Mode     policy.Mode
	Headroom float64
}

type Input struct {
	Now         time.Time
	TickSeconds float64
	Health      Health
	CurrentRate float64
	RateCap     float64
	Remaining   int64
	TimeLeft    time.Duration
}

type Decision struct {
	Rate   float64
	Reason string
}

type Pacer interface{ Next(in Input) Decision }

type Unlimited struct{}

func (Unlimited) Next(Input) Decision { return Decision{Rate: 100000, Reason: "unlimited"} }

type Static struct{ Rate float64 }

func (s Static) Next(Input) Decision { return Decision{Rate: s.Rate, Reason: "static"} }
