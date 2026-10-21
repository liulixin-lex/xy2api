package sharesched

import (
	"encoding/binary"
	"math"

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

// hrwDraw is the weight-independent part of the weighted rendezvous score,
// so one hash serves several weights of the same account.
func hrwDraw(salt int64, key string, accountID int64) float64 {
	return -math.Log(hrwUniform(salt, key, accountID))
}

// hrwWeighted is the weighted rendezvous score; the minimum wins and the
// full ascending order is the session's stable fallback order. A
// non-positive weight never wins.
func hrwWeighted(draw, weight float64) float64 {
	if weight <= 0 {
		return math.Inf(1)
	}
	return draw / weight
}
