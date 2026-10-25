//go:build unit && race

package service

// The race detector perturbs allocation counts, so exact allocs/op gates skip.
const l17RaceEnabled = true
