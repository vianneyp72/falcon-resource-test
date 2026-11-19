package analyze

import "sort"

// Stat summarizes one metric over a window. CPU in millicores, memory in bytes.
type Stat struct {
	Avg  int64 `json:"avg"`
	P50  int64 `json:"p50"`
	P95  int64 `json:"p95"`
	Peak int64 `json:"peak"`
}

// Percentile uses nearest-rank on a copy of v (input order is not changed).
func Percentile(v []int64, p float64) int64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]int64(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	rank := int(p*float64(len(s))+0.999999) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(s) {
		rank = len(s) - 1
	}
	return s[rank]
}

// Summarize computes avg/p50/p95 from vals and Peak from maxes (rollup rows
// carry a bucket max that is higher than the bucket average).
func Summarize(vals, maxes []int64) Stat {
	if len(vals) == 0 {
		return Stat{}
	}
	var sum, peak int64
	for _, v := range vals {
		sum += v
	}
	for _, m := range maxes {
		if m > peak {
			peak = m
		}
	}
	return Stat{Avg: sum / int64(len(vals)), P50: Percentile(vals, 0.5), P95: Percentile(vals, 0.95), Peak: peak}
}

func Pct(part, whole int64) float64 {
	if whole <= 0 {
		return 0
	}
	return float64(part) * 100 / float64(whole)
}
