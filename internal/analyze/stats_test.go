package analyze

import "testing"

func TestPercentileNearestRank(t *testing.T) {
	v := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	cases := map[float64]int64{0.5: 5, 0.95: 10, 0.9: 9, 0.1: 1, 1: 10}
	for p, want := range cases {
		if got := Percentile(v, p); got != want {
			t.Errorf("p%.0f = %d want %d", p*100, got, want)
		}
	}
	if Percentile(nil, 0.5) != 0 {
		t.Error("empty input should be 0")
	}
}

func TestSummarize(t *testing.T) {
	s := Summarize([]int64{30, 10, 20}, []int64{30, 10, 50})
	if s.Avg != 20 || s.P50 != 20 || s.P95 != 30 || s.Peak != 50 {
		t.Fatalf("got %+v", s)
	}
	if (Summarize(nil, nil) != Stat{}) {
		t.Fatal("empty should be zero")
	}
}

func TestPct(t *testing.T) {
	if Pct(1, 4) != 25 || Pct(1, 0) != 0 {
		t.Fatal("pct")
	}
}
