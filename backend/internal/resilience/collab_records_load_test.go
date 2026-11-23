package resilience

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func capacityLatencies(samples []time.Duration) string {
	slices.Sort(samples)
	return fmt.Sprintf("p99=%s max=%s", samples[len(samples)*99/100], samples[len(samples)-1])
}
func TestCollabCapacityLocalConcurrency(t *testing.T) {
	for _, streams := range []int{10, 50} {
		t.Run(fmt.Sprint(streams), func(t *testing.T) {
			s := NewMemoryCollabRecordsStore()
			ctx := context.Background()
			keys := make([]CollabDeliveryKey, 3500)
			for i := range keys {
				k := capacityKey(i)
				tenant := 0
				if i%100 >= 90 {
					tenant = 1
				}
				if i%100 == 99 {
					tenant = 2
				}
				k.Scope.Tenant = capacityDigest(tenant)
				keys[i] = k
				require.True(t, s.Terminated(ctx, CollabTerminatedRecord{Key: k, Event: capacityDigest(90), ExpiresAt: time.Now().Add(CollabRecordTTL)}))
			}
			var wg sync.WaitGroup
			var complete, accepted atomic.Int64
			start := make(chan struct{})
			writes := make([]time.Duration, streams*32)
			reads := make([]time.Duration, streams*32)
			registry := make([]time.Duration, streams*32)
			treeWait := make([]time.Duration, streams*32)
			for w := 0; w < streams; w++ {
				wg.Go(func() {
					<-start
					k := keys[w]
					q := CollabRecordQuery{Key: k}
					for i := 0; i < 32; i++ {
						index := w*32 + i
						begin := time.Now()
						token, ok := s.PendingIssued(ctx, CollabIssuedRecord{Scope: k.Scope, Call: capacityDigest(100 + i), Digest: k.Digest, Account: capacityDigest(7), ExpiresAt: time.Now().Add(time.Hour)})
						if ok && s.ConfirmIssued(ctx, token) {
							accepted.Add(1)
						}
						writes[index] = time.Since(begin)
						begin = time.Now()
						got := s.LookupBatch(ctx, CollabRecordBatch{Scope: k.Scope, Queries: []CollabRecordQuery{q}, Records: true, Dispositions: true})
						reads[index] = time.Since(begin)
						if got.RecordsComplete && got.DispositionsComplete && got.TreeLocalEndingsKnown {
							complete.Add(1)
						}
						begin = time.Now()
						s.mu.RLock()
						tree := s.trees[k.Scope]
						s.mu.RUnlock()
						registry[index] = time.Since(begin)
						begin = time.Now()
						tree.mu.RLock()
						tree.mu.RUnlock() //nolint:staticcheck // Measure lock contention without protected work.
						treeWait[index] = time.Since(begin)
						time.Sleep(10 * time.Millisecond)
					}
				})
			}
			close(start)
			wg.Wait()
			require.Equal(t, int64(streams*32), accepted.Load())
			t.Logf("streams=%d complete=%d/%d confirmed=%d mutation %s lookup %s sampled registry wait %s tree wait %s", streams, complete.Load(), streams*32, accepted.Load(), capacityLatencies(writes), capacityLatencies(reads), capacityLatencies(registry), capacityLatencies(treeWait))
		})
	}
}
