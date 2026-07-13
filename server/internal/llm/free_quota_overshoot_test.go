package llm

import "testing"

// § free-allowance overshoot: a turn admitted under the free allotment flips
// to credits when its estimated cost clearly exceeds the remaining allowance
// (grace factor, default 120%).
func TestFreeQuotaOvershoot(t *testing.T) {
	cases := []struct {
		name      string
		est       float64
		remaining float64
		want      bool
	}{
		{"no finite allowance", 2.0, -1, false},
		{"well within", 0.5, 1.0, false},
		{"within grace (1.1 vs 1.0×1.2)", 1.1, 1.0, false},
		{"at grace edge (1.2 == 1.0×1.2)", 1.2, 1.0, false},
		{"past grace", 1.3, 1.0, true},
		{"$2 on $1 left", 2.0, 1.0, true},
		{"$2 on 1 cent left", 2.0, 0.01, true},
		{"zero remaining, any cost", 0.001, 0, true},
	}
	for _, c := range cases {
		if got := freeQuotaOvershoot(c.est, c.remaining); got != c.want {
			t.Errorf("%s: freeQuotaOvershoot(%v, %v) = %v, want %v", c.name, c.est, c.remaining, got, c.want)
		}
	}
}
