package schedquality

import "math"

func WilsonLower(bad, n int) float64 {
	if bad == 0 || n == 0 {
		return 0
	}
	p := float64(bad) / float64(n)
	z2 := WilsonZ * WilsonZ
	return (p + z2/(2*float64(n)) - WilsonZ*math.Sqrt((p*(1-p)+z2/(4*float64(n)))/float64(n))) / (1 + z2/float64(n))
}
