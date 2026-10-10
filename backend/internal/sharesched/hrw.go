package sharesched

import (
	"encoding/binary"
	"math"
	"time"

	"github.com/cespare/xxhash/v2"
)

// hrwUniform maps (salt, key, account) to a uniform value in (0,1]. The hash
// is fixed so every instance derives the same order for the same inputs.
func hrwUniform(salt int64, key string, accountID int64) float64 {
	var digest xxhash.Digest
	digest.Reset()
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(salt))
	_, _ = digest.Write(buf[:])
	_, _ = digest.WriteString(key)
	binary.LittleEndian.PutUint64(buf[:], uint64(accountID))
	_, _ = digest.Write(buf[:])
	// 53 high bits give an exact float64; +1 keeps the value away from 0.
	return float64(digest.Sum64()>>11+1) / float64(uint64(1)<<53)
}

// hrwScore is the weighted rendezvous score; the minimum wins and the full
// ascending order is the session's stable fallback order. A non-positive
// weight never wins.
func hrwScore(salt int64, key string, accountID int64, weight float64) float64 {
	if weight <= 0 {
		return math.Inf(1)
	}
	return -math.Log(hrwUniform(salt, key, accountID)) / weight
}

// deadlineStagger spreads forced migrations of one deadline over ten minutes
// so sessions do not rebuild their caches all at once.
func deadlineStagger(key string) time.Duration {
	if key == "" {
		return 0
	}
	return time.Duration(xxhash.Sum64String("stagger:"+key)%uint64(DeadlineStaggerSeconds)) * time.Second
}
