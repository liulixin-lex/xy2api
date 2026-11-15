//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestSessionAvoidIgnoredBeforeReset(t *testing.T) {
	for _, scope := range []string{"session", "sticky_change:session"} {
		for _, seconds := range []int{60, 600, 1200} {
			t.Run(scope+"/"+time.Duration(seconds).String(), func(t *testing.T) {
				original := *resilience.Current()
				changed := original
				changed.Session.AvoidSeconds = seconds
				resilience.Publish(changed)
				t.Cleanup(func() { resilience.Publish(original) })
				store := newMemResilienceStore()
				now := time.Now()
				reset := now.Add(-time.Minute)
				key := memSessionKey(7, scope)
				// Deadlines deliberately disagree with today's TTL. The stored
				// write time alone decides whether a reset invalidated a record.
				store.avoid[key] = map[int64]time.Time{1: now.Add(9 * time.Minute), 2: now.Add(10 * time.Second), 3: now.Add(time.Minute), 4: now.Add(time.Minute), 5: now.Add(time.Minute)}
				store.avoidWritten = map[string]map[int64]time.Time{key: {1: reset.Add(-time.Second), 2: reset.Add(time.Second), 3: reset}}
				avoided, err := sessionAvoidAfterReset(context.Background(), store, 7, scope, func(id int64) time.Time {
					if id == 5 {
						return time.Time{}
					}
					return reset
				})
				require.NoError(t, err)
				require.NotContains(t, avoided, int64(1), "long retained deadline cannot revive a pre-reset write")
				require.Contains(t, avoided, int64(2), "a post-reset write remains effective even after TTL changed")
				require.Contains(t, avoided, int64(3), "only strictly earlier writes are removed")
				require.NotContains(t, avoided, int64(4), "legacy metadata-free record is ignored after any reset")
				require.Contains(t, avoided, int64(5), "legacy record remains effective before the first reset")
			})
		}
	}
}
