package retry

import "sync"

// Tokens are counted in thousandths: adding 0.1 ten times in float64 gives 0.9999999999999999, which is below 1.
type Budget struct {
	mu         sync.Mutex
	milli      int64
	maxMilli   int64
	ratioMilli int64
}

func NewBudget(maxTokens int, ratio float64) *Budget {
	return &Budget{milli: int64(maxTokens) * 1000, maxMilli: int64(maxTokens) * 1000, ratioMilli: int64(ratio*1000 + 0.5)}
}

func (b *Budget) SetRatio(ratio float64) {
	b.mu.Lock()
	b.ratioMilli = int64(ratio*1000 + 0.5)
	b.mu.Unlock()
}

func (b *Budget) OnRequest() {
	b.mu.Lock()
	b.milli = min(b.milli+b.ratioMilli, b.maxMilli)
	b.mu.Unlock()
}

func (b *Budget) TryRetry() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.milli < 1000 {
		return false
	}
	b.milli -= 1000
	return true
}
