package schedquality

import (
	"fmt"
	"math"
)

// At x=0.5, a=b=1e3, 1e4, 1e5, 1e6, 1e7, 1e8 converge in
// 41, 88, 189, 408, 879, 1894 iterations respectively. Leave ample headroom
// for large counts; tests pass a lower limit to exercise non-convergence.
const ruleBBetaMaxIterations = 100000

// Keep the reference engine's operation order, positive tiny-value guards,
// and stopping rule. Inputs converging within its old 179-iteration limit
// remain bit-for-bit identical; exhausting the limit must not yield a verdict.
func ruleBBetaFraction(a, b, x float64, maxIterations int) (float64, error) {
	qab, qap, qam := a+b, a+1, a-1
	c, d := 1.0, 1-qab*x/qap
	if math.Abs(d) < 1e-30 {
		d = 1e-30
	}
	d = 1 / d
	h := d
	for m := 1; m <= maxIterations; m++ {
		mf := float64(m)
		aa := mf * (b - mf) * x / ((qam + 2*mf) * (a + 2*mf))
		d = 1 + aa*d
		if math.Abs(d) < 1e-30 {
			d = 1e-30
		}
		c = 1 + aa/c
		if math.Abs(c) < 1e-30 {
			c = 1e-30
		}
		d = 1 / d
		h *= d * c
		aa = -(a + mf) * (qab + mf) * x / ((a + 2*mf) * (qap + 2*mf))
		d = 1 + aa*d
		if math.Abs(d) < 1e-30 {
			d = 1e-30
		}
		c = 1 + aa/c
		if math.Abs(c) < 1e-30 {
			c = 1e-30
		}
		d = 1 / d
		dd := d * c
		h *= dd
		if math.Abs(dd-1) < 3e-10 {
			return h, nil
		}
	}
	return 0, fmt.Errorf("rule B beta fraction: did not converge after %d iterations", maxIterations)
}

func ruleBBetaCDF(a, b, x float64, maxIterations int) (float64, error) {
	if x <= 0 {
		return 0, nil
	}
	if x >= 1 {
		return 1, nil
	}
	ab, _ := math.Lgamma(a + b)
	ga, _ := math.Lgamma(a)
	gb, _ := math.Lgamma(b)
	bt := math.Exp(ab - ga - gb + a*math.Log(x) + b*math.Log1p(-x))
	if x < (a+1)/(a+b+2) {
		fraction, err := ruleBBetaFraction(a, b, x, maxIterations)
		if err != nil {
			return 0, err
		}
		return bt * fraction / a, nil
	}
	fraction, err := ruleBBetaFraction(b, a, 1-x, maxIterations)
	if err != nil {
		return 0, err
	}
	return 1 - bt*fraction/b, nil
}
