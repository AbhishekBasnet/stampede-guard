package dispatcher

import "testing"

func TestTokenBucket(t *testing.T) {
	total := func(rate float64, ticks int) int {
		var b TokenBucket
		sum := 0
		for range ticks {
			n := b.Take(rate, 0.25)
			b.Spend(n)
			sum += n
		}
		return sum
	}
	tests := []struct {
		name  string
		rate  float64
		ticks int
		want  int
	}{
		{"rate 100 over one second", 100, 4, 100},
		{"rate 2 over two seconds", 2, 8, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := total(tt.rate, tt.ticks)
			t.Logf("total=%d", got)
			if got != tt.want {
				t.Fatalf("total = %d, want %d", got, tt.want)
			}
		})
	}

	b := TokenBucket{Tokens: 0.7}
	if n := b.Take(0, 0.25); n != 0 {
		t.Fatalf("Take(0, 0.25) with 0.7 tokens = %d, want 0", n)
	}
}
