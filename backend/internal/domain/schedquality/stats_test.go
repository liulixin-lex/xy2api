//go:build unit

package schedquality

import (
	"math"
	"testing"
)

func TestWilsonLower(t *testing.T) {
	for _, c := range []struct {
		bad, n int
		want   float64
	}{{0, 20, 0}, {0, 0, 0}, {1, 0, 0}, {5, 10, 0.23658959361548731}} {
		if c.want == 0 && WilsonLower(c.bad, c.n) != 0 {
			t.Fatal("zero must be exact")
		}
		if got := WilsonLower(c.bad, c.n); math.Abs(got-c.want) > 1e-12 {
			t.Fatalf("%d/%d: %v", c.bad, c.n, got)
		}
	}
}
