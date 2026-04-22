package native

import "testing"

// TestDecayFactor_Continuity locks down the Shodh hybrid decay curve:
// exponential in the first 3 days, power-law thereafter. Values match
// Python's src/ogham/lifecycle.py::hybrid_decay_factor for cross-stack
// parity.
func TestDecayFactor_Continuity(t *testing.T) {
	cases := []struct {
		name    string
		ageDays float64
		lambda  float64
		beta    float64
		want    float64
		tol     float64
	}{
		{"day 0 exponential", 0, 0.1, 0.4, 1.0, 1e-6},
		{"day 2 exponential", 2, 0.1, 0.4, 0.8187, 1e-3},
		{"day 30 power-law", 30, 0.1, 0.4, 0.398, 1e-2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DecayFactor(c.ageDays, c.lambda, c.beta)
			if absFloat(got-c.want) > c.tol {
				t.Errorf("DecayFactor(%v, %v, %v) = %v, want ~%v",
					c.ageDays, c.lambda, c.beta, got, c.want)
			}
		})
	}
}

func absFloat(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
