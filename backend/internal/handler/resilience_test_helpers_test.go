package handler

import (
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// withHandlerResilience publishes settings for one test and restores them.
func withHandlerResilience(t *testing.T, mutate func(*resilience.Settings)) {
	t.Helper()
	orig := *resilience.Current()
	s := resilience.Defaults()
	if mutate != nil {
		mutate(&s)
	}
	resilience.Publish(s)
	resilience.ProcessNegativeCache().InvalidateGroup(0)
	t.Cleanup(func() {
		resilience.Publish(orig)
		resilience.ProcessNegativeCache().InvalidateGroup(0)
	})
}
