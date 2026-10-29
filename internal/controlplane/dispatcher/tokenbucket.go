package dispatcher

import "math"

type TokenBucket struct{ Tokens float64 }

func (b *TokenBucket) Take(rate, dt float64) int {
	b.Tokens += rate * dt
	if limit := max(rate, 1); b.Tokens > limit {
		b.Tokens = limit
	}
	return int(math.Floor(b.Tokens))
}

func (b *TokenBucket) Spend(n int) { b.Tokens -= float64(n) }
