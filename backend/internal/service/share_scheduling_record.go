package service

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
)

// shareDecisionRecord is the context of one decision for statistics.
type shareDecisionRecord struct {
	gp       *shareGroupPolicy
	source   sharesched.KeySource
	platform string
	model    string
	routing  sharesched.Reason
	session  string
	idle     time.Duration
	cache    sharesched.CacheProfile
	start    time.Time
}

// shareDecisionSample is one decision as shown by the admin API.
type shareDecisionSample struct {
	At              time.Time `json:"at"`
	Node            string    `json:"node,omitempty"`
	Version         int64     `json:"version"`
	Scheduler       string    `json:"scheduler"`
	Platform        string    `json:"platform,omitempty"`
	Model           string    `json:"model,omitempty"`
	KeySource       string    `json:"key_source"`
	Session         string    `json:"session,omitempty"`
	Reason          string    `json:"reason"`
	Routing         string    `json:"routing,omitempty"`
	Bound           int64     `json:"bound,omitempty"`
	Target          int64     `json:"target,omitempty"`
	Ideal           int64     `json:"ideal,omitempty"`
	Served          int64     `json:"served,omitempty"`
	Original        int64     `json:"original,omitempty"`
	Agree           *bool     `json:"agree,omitempty"`
	IdealSkip       string    `json:"ideal_skip,omitempty"`
	Attempts        int       `json:"attempts"`
	WaitMs          int64     `json:"wait_ms,omitempty"`
	IdleMs          int64     `json:"idle_ms,omitempty"`
	CacheTTLSeconds int       `json:"cache_ttl_seconds,omitempty"`
	EstimatedTokens int       `json:"estimated_tokens,omitempty"`
	Queued          bool      `json:"queued,omitempty"`
	HotMigration    bool      `json:"hot_migration,omitempty"`
	ElapsedUs       int64     `json:"elapsed_us"`
}

// shareSessionFingerprint truncates a hash of the session key so neither
// samples nor Redis keys carry the raw session identifier.
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

func (s *ShareSchedulingService) recordDecision(rec shareDecisionRecord, d sharesched.Decision, out *shareOutcome) {
	now := time.Now()
	gid := rec.gp.groupID
	r := s.stats
	r.add(shareStatsKindShare, gid, now, shareStatsRequestsField, 1)
	r.add(shareStatsKindShare, gid, now, shareStatsReasonPrefix+string(out.reason), 1)
	r.add(shareStatsKindShare, gid, now, shareStatsKeySourcePrefix+string(rec.source), 1)
	if rec.routing != "" {
		r.add(shareStatsKindShare, gid, now, shareStatsReasonPrefix+string(rec.routing), 1)
	}
	if out.served > 0 {
		r.add(shareStatsKindShare, gid, now, shareStatsServedPrefix+strconv.FormatInt(out.served, 10), 1)
	}
	if d.Ideal > 0 {
		r.add(shareStatsKindShare, gid, now, shareStatsIdealPrefix+strconv.FormatInt(d.Ideal, 10), 1)
	}
	for id, reason := range out.skipped {
		r.add(shareStatsKindShare, gid, now, shareStatsSkipPrefix+strconv.FormatInt(id, 10)+":"+string(reason), 1)
	}
	idealSkip := d.IdealSkipReason(out.served, out.skipped)
	if idealSkip != "" {
		r.add(shareStatsKindShare, gid, now, shareStatsIdealSkipPrefix+string(idealSkip), 1)
	}
	r.add(shareStatsKindShare, gid, now, shareStatsAttemptsField, int64(out.attempts))
	if out.waited > 0 {
		r.add(shareStatsKindShare, gid, now, shareStatsWaitCountField, 1)
		r.add(shareStatsKindShare, gid, now, shareStatsWaitMsField, out.waited.Milliseconds())
		if out.waitHit {
			r.add(shareStatsKindShare, gid, now, shareStatsWaitHitField, 1)
		}
	}
	hot := shareHotMigration(d, out.served, out.write, rec.cache, rec.idle)
	if hot {
		r.add(shareStatsKindShare, gid, now, shareStatsHotMigrationField, 1)
		r.add(shareStatsKindShare, gid, now, shareStatsHotPrefix+string(out.reason), 1)
	}
	if !s.Settings().DecisionSamples {
		return
	}
	sample := s.newDecisionSample(rec, d, now)
	sample.Reason, sample.Served, sample.IdealSkip = string(out.reason), out.served, string(idealSkip)
	sample.Attempts, sample.WaitMs, sample.HotMigration = out.attempts, out.waited.Milliseconds(), hot
	sample.Queued = out.result != nil && !out.result.Acquired
	if raw, err := json.Marshal(sample); err == nil {
		r.addDecision(gid, raw)
	}
}

// recordShadow compares the share plan's first choice with the account the
// original scheduler served.
func (s *ShareSchedulingService) recordShadow(rec shareDecisionRecord, d sharesched.Decision, original int64) {
	now := time.Now()
	gid := rec.gp.groupID
	first := int64(0)
	if len(d.Steps) > 0 {
		first = d.Steps[0].AccountID
	}
	agree := first == original
	r := s.stats
	r.add(shareStatsKindShare, gid, now, shareStatsShadowField, 1)
	if agree {
		r.add(shareStatsKindShare, gid, now, shareStatsShadowAgreeField, 1)
	}
	r.add(shareStatsKindShare, gid, now, shareStatsShadowPrefix+string(d.Reason), 1)
	r.add(shareStatsKindShare, gid, now, shareStatsKeySourcePrefix+string(rec.source), 1)
	if d.Ideal > 0 {
		r.add(shareStatsKindShare, gid, now, shareStatsIdealPrefix+strconv.FormatInt(d.Ideal, 10), 1)
	}
	if original > 0 {
		r.add(shareStatsKindShare, gid, now, shareStatsServedPrefix+strconv.FormatInt(original, 10), 1)
	}
	hot := shareHotMigration(d, first, d.Write, rec.cache, rec.idle)
	if hot {
		r.add(shareStatsKindShare, gid, now, shareStatsHotMigrationField, 1)
		r.add(shareStatsKindShare, gid, now, shareStatsHotPrefix+string(d.Reason), 1)
	}
	if !s.Settings().DecisionSamples {
		return
	}
	sample := s.newDecisionSample(rec, d, now)
	sample.Reason, sample.Served, sample.Original, sample.Agree, sample.HotMigration = string(d.Reason), first, original, &agree, hot
	if raw, err := json.Marshal(sample); err == nil {
		r.addDecision(gid, raw)
	}
}

func (s *ShareSchedulingService) newDecisionSample(rec shareDecisionRecord, d sharesched.Decision, now time.Time) shareDecisionSample {
	return shareDecisionSample{At: now, Node: s.node, Version: rec.gp.version, Scheduler: string(rec.gp.policy.Scheduler),
		Platform: rec.platform, Model: rec.model, KeySource: string(rec.source), Session: shareSessionFingerprint(rec.session),
		Routing: string(rec.routing), Bound: d.Bound, Target: d.Target, Ideal: d.Ideal, IdleMs: rec.idle.Milliseconds(),
		CacheTTLSeconds: int(rec.cache.TTL / time.Second), EstimatedTokens: rec.cache.EstimatedTokens,
		ElapsedUs: now.Sub(rec.start).Microseconds()}
}
