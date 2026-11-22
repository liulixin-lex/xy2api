//go:build unit

package repository

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	r "github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func capacityLatencies(samples []time.Duration) string {
	slices.Sort(samples)
	return fmt.Sprintf("p99=%s max=%s", samples[len(samples)*99/100], samples[len(samples)-1])
}
func TestCollabCapacitySharedConcurrency(t *testing.T) {
	for _, streams := range []int{10, 50} {
		t.Run(fmt.Sprint(streams), func(t *testing.T) {
			_, _, shared := capacityCache(t)
			ctx := context.Background()
			local := r.NewMemoryCollabRecordsStore()
			safe := r.NewSafeCollabRecordsStore(local, shared, 1024)
			defer safe.Close()
			for i := 0; i < streams; i++ {
				k := capacityKey(i)
				m := r.CollabRecordMutation{Kind: r.CollabRecordMutationTerminated, Terminated: r.CollabTerminatedRecord{Key: k, Event: capacityDigest(90), ExpiresAt: time.Now().Add(r.CollabRecordTTL)}}
				require.NoError(t, shared.Apply(ctx, m))
				require.True(t, local.ApplyMutation(m))
			}
			writes := make([]time.Duration, streams*32)
			reads := make([]time.Duration, streams*32)
			var complete, hits, accepted atomic.Int64
			var wg sync.WaitGroup
			start := make(chan struct{})
			before := r.CollabSafetySnapshot()
			for w := 0; w < streams; w++ {
				wg.Go(func() {
					<-start
					k := capacityKey(w)
					q := r.CollabRecordQuery{Key: k}
					for i := 0; i < 32; i++ {
						idx := w*32 + i
						begin := time.Now()
						if safe.PlaintextDelivered(ctx, r.CollabPlaintextRecord{Scope: k.Scope, Call: capacityDigest(100 + i), Arguments: capacityDigest(8), ExpiresAt: time.Now().Add(time.Hour)}) {
							accepted.Add(1)
						}
						writes[idx] = time.Since(begin)
						budget, cancel := r.WithCollabRecordReadBudget(ctx)
						begin = time.Now()
						got := safe.LookupBatch(budget, r.CollabRecordBatch{Scope: k.Scope, Queries: []r.CollabRecordQuery{q}, Records: true, Dispositions: true})
						reads[idx] = time.Since(begin)
						cancel()
						if got.RecordsComplete && got.DispositionsComplete && got.TreeLocalEndingsKnown {
							complete.Add(1)
						}
						if len(got.Dispositions[q].Terminated) == 1 {
							hits.Add(1)
						}
						time.Sleep(500 * time.Millisecond)
					}
				})
			}
			close(start)
			wg.Wait()
			after := r.CollabSafetySnapshot()
			require.Equal(t, int64(streams*32), accepted.Load())
			require.Equal(t, int64(streams*32), complete.Load())
			require.Equal(t, int64(streams*32), hits.Load())
			require.Equal(t, before.RecordDropped, after.RecordDropped)
			require.Equal(t, before.RecordErrors, after.RecordErrors)
			t.Logf("streams=%d complete=%d/%d terminations=%d drops=%d errors=%d mutation %s lookup %s", streams, complete.Load(), streams*32, hits.Load(), after.RecordDropped-before.RecordDropped, after.RecordErrors-before.RecordErrors, capacityLatencies(writes), capacityLatencies(reads))
		})
	}
}
