package pacer

import "time"

type AmplificationConfig struct {
	DefaultPrior float64
	MinSent      int64
	MinElapsed   time.Duration
}

func EstimateAmplification(c AmplificationConfig, prior float64, sent, campaignRequests int64, elapsed time.Duration) float64 {
	if prior <= 0 {
		prior = c.DefaultPrior
	}
	if sent < c.MinSent || elapsed < c.MinElapsed {
		return prior
	}
	return max(prior, float64(campaignRequests)/float64(sent))
}

func RateCap(capacityRPS, targetUtilization, baselineRPS, amplification float64) float64 {
	if capacityRPS <= 0 || amplification <= 0 {
		return 0
	}
	spare := capacityRPS*targetUtilization - baselineRPS
	if spare <= 1 {
		spare = 1
	}
	return spare / amplification
}
