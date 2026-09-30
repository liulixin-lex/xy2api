package responseturn

import (
	"fmt"
	"testing"
	"time"
)

// A response event may request a metadata snapshot. Retained idempotency
// tombstones must not turn every such event into a process-wide scan.
func BenchmarkReviewRetainedMetadataSnapshot(b *testing.B) {
	for _, count := range []int{1, 10000, 100000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			now := time.Now()
			m := NewManager(Config{DisableBackground: true, Now: func() time.Time { return now }})
			for i := 0; i < count; i++ {
				id := fmt.Sprintf("retained-%d", i)
				m.turns[id] = &Turn{manager: m, id: id, created: now, finished: now, state: StateCompleted, store: true}
			}
			target := m.turns["retained-0"]
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = target.Snapshot()
			}
		})
	}
}
