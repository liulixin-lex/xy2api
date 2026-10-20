package service

import (
	"strconv"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
)

// shareDecisionRecord is the context of one decision for statistics.
type shareDecisionRecord struct {
	gp      *shareGroupPolicy
	routing sharesched.Reason
	idle    time.Duration
	cache   sharesched.CacheProfile
}

// shareSessionFingerprint truncates a hash of the session key so neither
// explanations nor Redis keys carry the raw session identifier.
func shareSessionFingerprint(key string) string {
	if key == "" {
		return ""
	}
	return strconv.FormatUint(xxhash.Sum64String(key), 36)
}

// shareHotMigration reports a binding moved off an account whose cache was
// still warm: the migrations share scheduling promises to avoid.
func shareHotMigration(d sharesched.Decision, served int64, write sharesched.Write, cache sharesched.CacheProfile, idle time.Duration) bool {
	return d.Bound > 0 && served > 0 && served != d.Bound && write == sharesched.WriteReplace && cache.Warm(idle)
}

// recordDecision counts what the report reads (design §7.7): the decision
// reason, attempts, skipped accounts, bounded waits and hot migrations.
func (s *ShareSchedulingService) recordDecision(rec shareDecisionRecord, d sharesched.Decision, out *shareOutcome) {
	now := time.Now()
	gid := rec.gp.groupID
	var buf [12]shareStatsDelta
	deltas := append(buf[:0],
		shareStatsDelta{shareStatsRequestsField, 1},
		shareStatsDelta{shareStatsReasonPrefix + string(out.reason), 1},
		shareStatsDelta{shareStatsAttemptsField, int64(out.attempts)})
	if rec.routing != "" {
		deltas = append(deltas, shareStatsDelta{shareStatsReasonPrefix + string(rec.routing), 1})
	}
	for id, reason := range out.skipped {
		deltas = append(deltas, shareStatsDelta{shareStatsSkipPrefix + strconv.FormatInt(id, 10) + ":" + string(reason), 1})
	}
	if out.waited > 0 {
		deltas = append(deltas, shareStatsDelta{shareStatsWaitCountField, 1}, shareStatsDelta{shareStatsWaitMsField, out.waited.Milliseconds()})
		if out.waitHit {
			deltas = append(deltas, shareStatsDelta{shareStatsWaitHitField, 1})
		}
	}
	if shareHotMigration(d, out.served, out.write, rec.cache, rec.idle) {
		deltas = append(deltas, shareStatsDelta{shareStatsHotMigrationField, 1}, shareStatsDelta{shareStatsHotPrefix + string(out.reason), 1})
	}
	s.stats.addAll(shareStatsKindShare, gid, now, deltas)
}
