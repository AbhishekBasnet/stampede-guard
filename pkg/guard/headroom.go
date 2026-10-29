package guard

import "github.com/AbhishekBasnet/stampede-guard/pkg/policy"

type Scores struct {
	Latency     float64 `json:"latency"`
	Error       float64 `json:"error"`
	Concurrency float64 `json:"concurrency"`
}

type HeadroomConfig struct {
	TargetP99Ms  float64
	MaxP99Ms     float64
	MaxErrorRate float64
	MaxInFlight  int64
	MinSamples   int64
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// Below MinSamples the latency and error scores are ignored (1), since a few requests prove nothing.
func ComputeHeadroom(c HeadroomConfig, p99Ms, errorRate float64, inFlight, samples int64) (Scores, float64) {
	s := Scores{Latency: 1, Error: 1}
	if samples >= c.MinSamples {
		s.Latency = clamp01(1 - (p99Ms-c.TargetP99Ms)/(c.MaxP99Ms-c.TargetP99Ms))
		s.Error = clamp01(1 - errorRate/c.MaxErrorRate)
	}
	s.Concurrency = clamp01(1 - float64(inFlight)/float64(c.MaxInFlight))
	return s, min(s.Latency, s.Error, s.Concurrency)
}

func TargetMode(h float64) policy.Mode {
	switch {
	case h >= 0.50:
		return policy.ModeNormal
	case h >= 0.25:
		return policy.ModeElevated
	case h >= 0.10:
		return policy.ModeBrownout
	default:
		return policy.ModeShed
	}
}
