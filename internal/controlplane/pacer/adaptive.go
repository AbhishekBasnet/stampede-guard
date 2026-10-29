package pacer

import (
	"time"

	"github.com/AbhishekBasnet/stampede-guard/pkg/policy"
)

type AdaptiveConfig struct {
	MinRate, MaxRate, StartRate float64
	AdditiveIncreasePerSec      float64
	DecreaseFactor              float64
	DecreaseCooldown            time.Duration
	HighHeadroom                float64
}

type Adaptive struct {
	Cfg          AdaptiveConfig
	lastDecrease time.Time
}

func (p *Adaptive) Next(in Input) Decision {
	c := p.Cfg
	if in.Health.Stale {
		return Decision{c.MinRate, "health_stale"}
	}
	rate := in.CurrentRate
	if rate <= 0 {
		rate = c.StartRate
	}
	var reason string
	switch in.Health.Mode {
	case policy.ModeShed:
		return Decision{0, "paused_shed"}
	case policy.ModeBrownout:
		if p.lastDecrease.IsZero() || in.Now.Sub(p.lastDecrease) >= c.DecreaseCooldown {
			rate *= c.DecreaseFactor
			p.lastDecrease = in.Now
			reason = "decrease_brownout"
		} else {
			reason = "hold_decrease_cooldown"
		}
	case policy.ModeElevated:
		reason = "hold_elevated"
	default:
		if in.Health.Headroom >= c.HighHeadroom {
			rate += c.AdditiveIncreasePerSec * in.TickSeconds
			reason = "increase"
		} else {
			reason = "hold_normal"
		}
	}
	if in.RateCap > 0 && rate > in.RateCap {
		rate = in.RateCap
		reason += "+capped"
	}
	if in.Health.Mode == policy.ModeNormal && in.TimeLeft > 0 {
		needed := float64(in.Remaining) / in.TimeLeft.Seconds()
		if needed > rate {
			rate = needed
			reason = "deadline_pressure"
		}
	}
	rate = max(c.MinRate, min(rate, c.MaxRate))
	return Decision{rate, reason}
}
