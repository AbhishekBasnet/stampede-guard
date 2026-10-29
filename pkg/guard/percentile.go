package guard

var BucketBoundsMs = [...]float64{1, 2, 5, 10, 20, 30, 50, 75, 100, 150, 200, 250, 300, 400, 500, 650, 800, 1000, 1500, 2000, 3000, 5000, 10000}

const NumBuckets = len(BucketBoundsMs) + 1

func BucketIndex(ms float64) int {
	for i, b := range BucketBoundsMs {
		if ms <= b {
			return i
		}
	}
	return len(BucketBoundsMs)
}

// The overflow bucket has no upper bound, so it is treated as 10000..20000 ms.
func Percentile(counts [NumBuckets]int64, q float64) float64 {
	var total int64
	for _, c := range counts {
		total += c
	}
	if total == 0 {
		return 0
	}
	rank := q * float64(total)
	var cum int64
	for i, c := range counts {
		if c == 0 {
			continue
		}
		if float64(cum+c) >= rank {
			lower := 0.0
			if i > 0 {
				lower = BucketBoundsMs[i-1]
			}
			upper := 20000.0
			if i < len(BucketBoundsMs) {
				upper = BucketBoundsMs[i]
			}
			within := (rank - float64(cum)) / float64(c)
			return lower + (upper-lower)*within
		}
		cum += c
	}
	return 20000
}
