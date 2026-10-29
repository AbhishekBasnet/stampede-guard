package retry

import (
	"math/rand/v2"
	"time"
)

func BackoffCeiling(attempt int, base, max time.Duration) time.Duration {
	c := base
	for i := 0; i < attempt; i++ {
		if c >= max/2 {
			return max
		}
		c *= 2
	}
	return min(c, max)
}

func FullJitter(attempt int, base, max time.Duration) time.Duration {
	c := BackoffCeiling(attempt, base, max)
	if c <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(c) + 1))
}
