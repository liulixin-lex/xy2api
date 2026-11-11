//go:build unit

package schedquality

import "testing"

func TestClassOf(t *testing.T) {
	for _, effort := range []string{"", "none", "minimal", "low", "medium", "high", "xhigh", "max"} {
		for _, large := range []bool{false, true} {
			n := ClassSizeBoundaryTokens - 1
			if large {
				n++
			}
			want := ClassLight
			if large {
				want = ClassRegular
			}
			switch effort {
			case "high":
				if large {
					want = ClassHeavy
				} else {
					want = ClassRegular
				}
			case "xhigh", "max":
				if large {
					want = ClassXHeavy
				} else {
					want = ClassRegular
				}
			}
			if got := ClassOf(effort, n); got != want {
				t.Fatalf("%q %d: %v != %v", effort, n, got, want)
			}
		}
	}
	if EstimateTokens(530) != 100 {
		t.Fatal("estimate")
	}
}
func TestScaleTables(t *testing.T) {
	cases := []struct {
		model   string
		classes [4]float64
		tiers   [3]float64
	}{
		{"gpt-6.1-sol", [4]float64{.73, 1, 1.20, 1.43}, [3]float64{.65, 1, 1.28}},
		{"gpt-6-astra", [4]float64{.86, 1, 1.12, 1.20}, [3]float64{.62, 1.06, 1.09}},
		{"gpt-5.6-sol", [4]float64{.90, 1, 1.18, 1.62}, [3]float64{.73, .95, 1.09}},
		{"other", [4]float64{.75, 1, 1.20, 1.40}, [3]float64{.70, 1, 1.10}},
	}
	for _, c := range cases {
		for i, w := range c.classes {
			if Scale(c.model, RequestClass(i)) != w {
				t.Fatal(c.model, i)
			}
		}
		for i, w := range c.tiers {
			if TestScale(c.model, TestTier(i)) != w {
				t.Fatal(c.model, i)
			}
		}
	}
}
