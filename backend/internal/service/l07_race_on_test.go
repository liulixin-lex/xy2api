//go:build unit && race

package service

// l07RaceBuild: under the race detector allocs/op is not a property of the
// code (the detector allocates on its own), so the allocation tests skip.
const l07RaceBuild = true
