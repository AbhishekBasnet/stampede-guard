package guard

import (
	"math"
	"testing"
)

func near(got, want, tol float64) bool { return math.Abs(got-want) <= tol }

func TestBucketIndex(t *testing.T) {
	tests := []struct {
		ms   float64
		want int
	}{{15, 4}, {20, 4}, {700, 16}, {20000, 23}}
	for _, tc := range tests {
		got := BucketIndex(tc.ms)
		t.Logf("BucketIndex(%v) = %d, want %d", tc.ms, got, tc.want)
		if got != tc.want {
			t.Errorf("BucketIndex(%v) = %d, want %d", tc.ms, got, tc.want)
		}
	}
}

func TestPercentile(t *testing.T) {
	tests := []struct {
		name   string
		counts map[int]int64
		q      float64
		want   float64
		tol    float64
	}{
		{"100 in bucket 4, p99", map[int]int64{4: 100}, 0.99, 19.9, 1e-9},
		{"90 in b4 + 10 in b16, p99", map[int]int64{4: 90, 16: 10}, 0.99, 785, 1e-9},
		{"90 in b4 + 10 in b16, p50", map[int]int64{4: 90, 16: 10}, 0.50, 15.5556, 1e-3},
		{"empty", nil, 0.99, 0, 1e-9},
		{"10 in overflow, p99", map[int]int64{23: 10}, 0.99, 19900, 1e-9},
	}
	for _, tc := range tests {
		var counts [NumBuckets]int64
		for i, c := range tc.counts {
			counts[i] = c
		}
		got := Percentile(counts, tc.q)
		t.Logf("%s: Percentile(q=%v) = %v, want %v", tc.name, tc.q, got, tc.want)
		if !near(got, tc.want, tc.tol) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
