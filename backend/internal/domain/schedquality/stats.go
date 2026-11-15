package schedquality

import (
	"math"
	"sort"
)

func WilsonLower(bad, n int) float64 {
	if bad == 0 || n == 0 {
		return 0
	}
	p := float64(bad) / float64(n)
	z2 := WilsonZ * WilsonZ
	return (p + z2/(2*float64(n)) - WilsonZ*math.Sqrt((p*(1-p)+z2/(4*float64(n)))/float64(n))) / (1 + z2/float64(n))
}

type latency struct {
	ms      int
	timeout bool
}

func sortLatencies(values []latency) {
	sort.Slice(values, func(i, j int) bool {
		if values[i].timeout != values[j].timeout {
			return !values[i].timeout
		}
		return values[i].ms < values[j].ms
	})
}
