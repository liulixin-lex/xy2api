package schedquality

import "math"

// Keep the reference engine's operation order, positive tiny-value guards,
// 179-iteration limit and stopping rule, including at decision boundaries.
func ruleBBetaFraction(a, b, x float64) float64 {
	qab, qap, qam := a+b, a+1, a-1
	c, d := 1.0, 1-qab*x/qap
	if math.Abs(d) < 1e-30 {
		d = 1e-30
	}
	d = 1 / d
	h := d
	for m := 1; m < 180; m++ {
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
			break
		}
	}
	return h
}

func ruleBBetaCDF(a, b, x float64) float64 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}
	ab, _ := math.Lgamma(a + b)
	ga, _ := math.Lgamma(a)
	gb, _ := math.Lgamma(b)
	bt := math.Exp(ab - ga - gb + a*math.Log(x) + b*math.Log1p(-x))
	if x < (a+1)/(a+b+2) {
		return bt * ruleBBetaFraction(a, b, x) / a
	}
	return 1 - bt*ruleBBetaFraction(b, a, 1-x)/b
}

func ruleBBetaQuantile(a, b, confidence float64) float64 {
	lower, upper := 0.0, 1.0
	for q := 0; q < 24; q++ {
		mid := (lower + upper) / 2
		if ruleBBetaCDF(a, b, mid) < confidence {
			lower = mid
		} else {
			upper = mid
		}
	}
	return (lower + upper) / 2
}
