package service

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
)

// Adaptive weights (design §11): a share weight is the account's load
// factor times a bounded health factor (R1) and a slow-start factor after a
// long outage (R2). Neither has a setting; turning gateway resilience off
// turns both off.
const (
	// shareHealthWindow is the failure window of R1, aligned to minutes.
	shareHealthWindow = 30 * time.Minute
	// shareHealthRefresh is how often the background refresh reads it.
	shareHealthRefresh = time.Minute
	// shareHealthStale: older statistics are not used.
	shareHealthStale = 5 * time.Minute
	// shareHealthPrior is the number of pseudo-attempts at the tier
	// baseline that shrink small samples towards it.
	shareHealthPrior = 20.0
	// shareHealthBaselineAttempts is the sample a member needs to set the
	// tier baseline.
	shareHealthBaselineAttempts = 20
	// shareHealthDeadZone and shareHealthHalving: within 3 points of the
	// baseline nothing changes; beyond, every 5 points halve the weight.
	shareHealthDeadZone = 0.03
	shareHealthHalving  = 0.05
	// shareHealthFloor keeps observation traffic on a down-weighted account.
	shareHealthFloor = 0.2

	// shareRampStart and shareRampDuration: after a long outage the weight
	// climbs linearly from 10% to full over ten minutes.
	shareRampStart    = 0.1
	shareRampDuration = 10 * time.Minute
	// shareRampOutage is how long an observed outage lasts before its end
	// ramps.
	shareRampOutage = 5 * time.Minute
	// shareWeightFloor bounds the combined factors: an effective weight is
	// at least 5% of the load factor.
	shareWeightFloor = 0.05
	// shareHealthQueryTimeout bounds one refresh.
	shareHealthQueryTimeout = 10 * time.Second
	// shareHealthOnDemandTTL: the health of a group that does not run share
	// scheduling is read when an administrator looks at it, and reused this
	// long.
	shareHealthOnDemandTTL = time.Minute
)

// Ramp causes (design §7.3).
const (
	ShareRampBalancePause = "balance_pause"
	ShareRampHardPenalty  = "hard_penalty"
	ShareRampReenabled    = "reenabled"
)

// ShareSchedulingUpstreamAttempt is one failed upstream attempt as recorded
// in ops_error_logs.upstream_errors.
type ShareSchedulingUpstreamAttempt struct {
	AccountID  int64
	Kind       string
	StatusCode int
	Category   string
	Message    string
}

// ShareSchedulingAttemptCount is a number of identical attempts.
type ShareSchedulingAttemptCount struct {
	ShareSchedulingUpstreamAttempt
	Count int64
}

// ShareSchedulingAccountAttempts are the attempts of accounts over a
// window: their successful requests and their failed upstream attempts.
type ShareSchedulingAccountAttempts struct {
	Successes map[int64]int64
	Failures  []ShareSchedulingAttemptCount
}

// shareSoftFailureAttempt reports whether a failed attempt counts against
// the account's health: only the categories that gateway resilience answers
// with a soft cooldown (TRANSIENT, FIRST_OUTPUT_TIMEOUT, STALL) and
// transport errors, which the ops record leaves unclassified and resilience
// rates TRANSIENT. Balance, credentials, rate limits, capacity shedding,
// request errors and client cancellations have their own handling.
func shareSoftFailureAttempt(ev ShareSchedulingUpstreamAttempt) bool {
	if ev.AccountID <= 0 {
		return false
	}
	switch resilience.Category(ev.Category) {
	case resilience.CategoryTransient, resilience.CategoryFirstOutputTimeout, resilience.CategoryStall:
		return true
	case "":
		return ev.Kind == "request_error" && ev.StatusCode == 0 &&
			!strings.Contains(strings.ToLower(ev.Message), "context canceled")
	}
	return false
}

// shareHealthStats are an account's attempts in the window: its successful
// requests plus its soft failures.
type shareHealthStats struct {
	Attempts     int64
	SoftFailures int64
}

// shareHealthMember is one member of a tier.
type shareHealthMember struct {
	Healthy bool
	Stats   shareHealthStats
}

// shareHealth is the R1 evaluation of one member.
type shareHealth struct {
	Stats    shareHealthStats
	Rate     float64
	Baseline float64
	// Insufficient: no attempt in the window; the weight is not changed.
	Insufficient bool
	// Factor is nil when insufficient.
	Factor *float64
}

// shareTierHealth rates the members of one tier against its best healthy
// member: the baseline is the lowest raw failure rate of the healthy
// members with at least shareHealthBaselineAttempts attempts (or, when none
// has, with any). A tier with fewer than two healthy members is not
// adjusted.
func shareTierHealth(members []shareHealthMember) []shareHealth {
	baseline, found := 0.0, false
	for _, minAttempts := range []int64{shareHealthBaselineAttempts, 1} {
		for _, m := range members {
			if m.Healthy && m.Stats.Attempts >= minAttempts {
				rate := float64(m.Stats.SoftFailures) / float64(m.Stats.Attempts)
				if !found || rate < baseline {
					baseline, found = rate, true
				}
			}
		}
		if found {
			break
		}
	}
	healthy := 0
	for _, m := range members {
		if m.Healthy {
			healthy++
		}
	}
	out := make([]shareHealth, len(members))
	for i, m := range members {
		h := shareHealth{Stats: m.Stats, Baseline: baseline}
		if m.Stats.Attempts <= 0 {
			h.Insufficient = true
			out[i] = h
			continue
		}
		h.Rate = float64(m.Stats.SoftFailures) / float64(m.Stats.Attempts)
		factor := 1.0
		if healthy >= 2 {
			estimate := (float64(m.Stats.SoftFailures) + shareHealthPrior*baseline) / (float64(m.Stats.Attempts) + shareHealthPrior)
			factor = shareHealthFactorOf(estimate, baseline)
		}
		h.Factor = &factor
		out[i] = h
	}
	return out
}

// shareHealthFactorOf is the health factor of a shrunk failure rate: 1
// within the dead zone, then halved every shareHealthHalving, at least
// shareHealthFloor, quantized.
func shareHealthFactorOf(estimate, baseline float64) float64 {
	// math.Max: a test file of this package declares an int max.
	excess := math.Max(0, estimate-baseline-shareHealthDeadZone)
	return math.Max(shareHealthFloor, shareQuantize(math.Pow(2, -excess/shareHealthHalving)))
}

// shareQuantize rounds a factor to a multiple of 0.05 as a clean decimal
// (a count of twentieths divided once), so instances that read the same
// minute agree and the API shows plain values.
func shareQuantize(f float64) float64 {
	return math.Round(f*20) / 20
}

// shareRamp is the R2 slow start of one account.
type shareRamp struct {
	Factor float64
	// Cause is empty when the account is not ramping.
	Cause  string
	Since  time.Time
	FullAt time.Time
}

var shareNoRamp = shareRamp{Factor: 1}

// shareRampOf is the slow start of an account that recovered from a long
// outage: a balance pause or a hard penalty that ended (read from the
// account, the same on every instance), or an outage of at least
// shareRampOutage that this instance observed ending at observed. The latest
// recovery counts. Short soft cooldowns do not ramp.
func shareRampOf(acc *Account, observed, now time.Time) shareRamp {
	cause, since := "", time.Time{}
	consider := func(c string, at time.Time) {
		if at.IsZero() || at.After(now) || now.Sub(at) >= shareRampDuration {
			return
		}
		if cause == "" || at.After(since) {
			cause, since = c, at
		}
	}
	if acc != nil {
		if until := acc.TempUnschedulableUntil; until != nil {
			switch {
			case resilience.IsBalancePause(acc.TempUnschedulableReason):
				consider(ShareRampBalancePause, *until)
			case resilience.TempUnschedTier(acc.TempUnschedulableReason) == resilience.TierHard:
				consider(ShareRampHardPenalty, *until)
			}
		}
		if reset, limited := acc.RateLimitResetAt, acc.RateLimitedAt; reset != nil && limited != nil &&
			reset.Sub(*limited) > resilience.HardRateLimitThreshold {
			consider(ShareRampHardPenalty, *reset)
		}
	}
	consider(ShareRampReenabled, observed)
	if cause == "" {
		return shareNoRamp
	}
	elapsed := float64(now.Sub(since)) / float64(shareRampDuration)
	factor := shareQuantize(shareRampStart + (1-shareRampStart)*math.Min(1, elapsed))
	return shareRamp{Factor: math.Min(1, math.Max(shareRampStart, factor)), Cause: cause, Since: since, FullAt: since.Add(shareRampDuration)}
}

// shareWeightBreakdown is an account's share weight with its factors.
type shareWeightBreakdown struct {
	LoadFactor int
	// Health is the R1 health of the account in the group; nil when R1 is
	// not in effect.
	Health *shareHealth
	Ramp   shareRamp
	// Effective is the weight placement uses.
	Effective float64
}

// HealthFactor is the applied health factor; nil when none applies.
func (w shareWeightBreakdown) HealthFactor() *float64 {
	if w.Health == nil {
		return nil
	}
	return w.Health.Factor
}

// shareCombineWeight multiplies the load factor by both factors, at least
// shareWeightFloor of the load factor. A nil health factor leaves the
// weight unchanged.
func shareCombineWeight(acc *Account, health *float64, ramp shareRamp) shareWeightBreakdown {
	lf := acc.EffectiveLoadFactor()
	h := 1.0
	if health != nil {
		h = *health
	}
	effective := math.Max(float64(lf)*h*ramp.Factor, float64(lf)*shareWeightFloor)
	// Two decimals: the API shows the weight, and every instance places
	// with the same value.
	return shareWeightBreakdown{LoadFactor: lf, Ramp: ramp, Effective: math.Round(effective*100) / 100}
}

// shareWeight is an account's share weight in a group: its effective load
// factor times the adaptive factors. Candidates, the member view, the
// preview and explain all take weights from here.
func (s *ShareSchedulingService) shareWeight(groupID int64, acc *Account, now time.Time) shareWeightBreakdown {
	if s == nil || acc == nil || !resilience.Current().FailureOn() {
		return shareCombineWeight(acc, nil, shareNoRamp)
	}
	snap := s.health.Load()
	var health *shareHealth
	var observed time.Time
	if snap != nil {
		observed = snap.recovered[acc.ID]
		if table, ok := snap.groups[groupID]; ok && now.Sub(table.at) <= shareHealthStale {
			if h, ok := table.health[acc.ID]; ok {
				health = &h
			}
		}
	}
	if health == nil && s.groupPolicy(groupID) == nil {
		health = s.onDemandHealth(groupID, acc.ID, now)
	}
	var factor *float64
	if health != nil {
		factor = health.Factor
	}
	w := shareCombineWeight(acc, factor, shareRampOf(acc, observed, now))
	w.Health = health
	return w
}

// onDemandHealth is the cached on-demand health of an account in a group
// that does not run share scheduling; nil when none is cached.
func (s *ShareSchedulingService) onDemandHealth(groupID, accountID int64, now time.Time) *shareHealth {
	s.admin.mu.Lock()
	cached, ok := s.admin.health[groupID]
	s.admin.mu.Unlock()
	if !ok || now.Sub(cached.at) >= shareHealthOnDemandTTL {
		return nil
	}
	if h, ok := cached.value[accountID]; ok {
		return &h
	}
	return nil
}

// ensureGroupHealth reads, for a group that does not run share scheduling,
// the health its members would have, so that the switch confirmation shows
// the shares switching gives (design §11.1). It uses the same window, rule
// and functions as the refresh, for the group's members only, and keeps the
// result for shareHealthOnDemandTTL. Share groups use the refresh; with
// gateway resilience or the ops monitor off there is no health.
func (s *ShareSchedulingService) ensureGroupHealth(ctx context.Context, groupID int64, members []Account, now time.Time) {
	if s.repo == nil || s.groupPolicy(groupID) != nil {
		return
	}
	if !resilience.Current().FailureOn() || !s.opsMonitoringOn(ctx) {
		s.admin.mu.Lock()
		delete(s.admin.health, groupID)
		s.admin.mu.Unlock()
		return
	}
	s.admin.mu.Lock()
	cached, ok := s.admin.health[groupID]
	s.admin.mu.Unlock()
	if ok && now.Sub(cached.at) < shareHealthOnDemandTTL {
		return
	}
	accounts := make(map[int64]*Account, len(members))
	for i := range members {
		accounts[members[i].ID] = &members[i]
	}
	stats, err := s.readHealthStats(ctx, accounts, now)
	if err != nil {
		slog.Warn("share_scheduling.health_read_failed", "group_id", groupID, "error", err)
		return
	}
	s.admin.mu.Lock()
	if s.admin.health == nil {
		s.admin.health = map[int64]shareCached[map[int64]shareHealth]{}
	}
	s.admin.health[groupID] = shareCached[map[int64]shareHealth]{at: now, value: shareGroupHealth(members, stats)}
	s.admin.mu.Unlock()
}

// shareHealthSnapshot is the health table of the share groups. It is
// immutable once published.
type shareHealthSnapshot struct {
	// groups holds the health of each share group read at its own time; a
	// group is absent while its statistics could not be read.
	groups map[int64]shareGroupHealthTable
	// recovered is when an observed outage of an account ended.
	recovered map[int64]time.Time
}

type shareGroupHealthTable struct {
	at     time.Time
	health map[int64]shareHealth
}

// shareOutageObservation tracks one account across refreshes.
type shareOutageObservation struct {
	unhealthySince time.Time
	recovered      time.Time
}

// healthLoop refreshes the adaptive weights.
// It refreshes at once when started, then every shareHealthRefresh, and
// whenever kicked because a group started running share scheduling.
func (s *ShareSchedulingService) healthLoop(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(shareHealthRefresh)
	defer ticker.Stop()
	for {
		refreshCtx, cancel := context.WithTimeout(ctx, shareHealthQueryTimeout)
		s.refreshHealth(refreshCtx, time.Now())
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.healthKick:
		}
	}
}

// kickHealthIfNeeded asks the refresh to run now when a share group has no
// health yet, as after a broadcast switch. Kicks while one is pending are
// merged.
func (s *ShareSchedulingService) kickHealthIfNeeded(now time.Time) {
	snap := s.health.Load()
	for _, gid := range s.effectiveShareGroups() {
		if snap != nil {
			if table, ok := snap.groups[gid]; ok && now.Sub(table.at) <= shareHealthStale {
				continue
			}
		}
		select {
		case s.healthKick <- struct{}{}:
		default:
		}
		return
	}
}

// refreshGroupHealth puts the health of one share group in the table at
// once, for the instance that saves its switch to share scheduling: the
// confirmation showed shares with health, and placement follows them from
// the first request. The health the confirmation read is reused while it is
// fresh.
func (s *ShareSchedulingService) refreshGroupHealth(ctx context.Context, groupID int64, now time.Time) {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	if s.repo == nil || !resilience.Current().FailureOn() || !s.opsMonitoringOn(ctx) {
		return
	}
	s.admin.mu.Lock()
	cached, ok := s.admin.health[groupID]
	s.admin.mu.Unlock()
	table := shareGroupHealthTable{at: cached.at, health: cached.value}
	if !ok || now.Sub(cached.at) >= shareHealthOnDemandTTL {
		members, err := s.shareMembers(ctx, groupID)
		if err != nil {
			slog.Warn("share_scheduling.health_members_failed", "group_id", groupID, "error", err)
			return
		}
		accounts := make(map[int64]*Account, len(members))
		for i := range members {
			accounts[members[i].ID] = &members[i]
		}
		stats, err := s.readHealthStats(ctx, accounts, now)
		if err != nil {
			slog.Warn("share_scheduling.health_read_failed", "group_id", groupID, "error", err)
			return
		}
		table = shareGroupHealthTable{at: now, health: shareGroupHealth(members, stats)}
	}
	next := &shareHealthSnapshot{groups: map[int64]shareGroupHealthTable{groupID: table}}
	if old := s.health.Load(); old != nil {
		for gid, t := range old.groups {
			if gid != groupID {
				next.groups[gid] = t
			}
		}
		next.recovered = old.recovered
	}
	s.health.Store(next)
}

// effectiveShareGroups lists the groups that run share scheduling now.
func (s *ShareSchedulingService) effectiveShareGroups() []int64 {
	state := s.state.Load()
	if !state.settings.Enabled {
		return nil
	}
	ids := make([]int64, 0, len(state.groups))
	for id, gp := range state.groups {
		if gp.policy.Effective() == sharesched.SchedulerShare {
			ids = append(ids, id)
		}
	}
	return ids
}

// opsMonitoringOn mirrors OpsService.IsMonitoringEnabled: R1 reads the
// attempts the ops monitor records.
func (s *ShareSchedulingService) opsMonitoringOn(ctx context.Context) bool {
	if !s.opsEnabled {
		return false
	}
	if s.settings == nil {
		return true
	}
	raw, err := s.settings.GetValue(ctx, SettingKeyOpsMonitoringEnabled)
	if err != nil {
		return errors.Is(err, ErrSettingNotFound)
	}
	return parseOpsMonitoringEnabled(raw)
}

// refreshHealth reads the members of every share group, observes outages
// and, when the ops monitor records attempts, rates each member's health
// against its tier. It does nothing while no group runs share scheduling or
// gateway resilience is off.
func (s *ShareSchedulingService) refreshHealth(ctx context.Context, now time.Time) {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	groups := s.effectiveShareGroups()
	if len(groups) == 0 || !resilience.Current().FailureOn() || s.gateway == nil || s.gateway.accountRepo == nil {
		s.health.Store(nil)
		s.outages = nil
		return
	}
	members := make(map[int64][]Account, len(groups))
	accounts := map[int64]*Account{}
	for _, gid := range groups {
		list, err := s.shareMembers(ctx, gid)
		if err != nil {
			slog.Warn("share_scheduling.health_members_failed", "group_id", gid, "error", err)
			continue
		}
		members[gid] = list
		for i := range list {
			accounts[list[i].ID] = &list[i]
		}
	}
	snap := &shareHealthSnapshot{groups: map[int64]shareGroupHealthTable{}, recovered: s.observeOutages(accounts, now)}
	if s.repo != nil && s.opsMonitoringOn(ctx) {
		if stats, err := s.readHealthStats(ctx, accounts, now); err != nil {
			slog.Warn("share_scheduling.health_read_failed", "error", err)
		} else {
			for gid, list := range members {
				snap.groups[gid] = shareGroupHealthTable{at: now, health: shareGroupHealth(list, stats)}
			}
		}
	}
	s.health.Store(snap)
}

// observeOutages records when each account last became healthy after at
// least shareRampOutage of observed outage. An outage that a balance pause
// or a hard penalty explains is ramped from the account itself.
func (s *ShareSchedulingService) observeOutages(accounts map[int64]*Account, now time.Time) map[int64]time.Time {
	next := make(map[int64]shareOutageObservation, len(accounts))
	recovered := map[int64]time.Time{}
	for id, acc := range accounts {
		o := s.outages[id]
		if !acc.IsSchedulable() {
			if o.unhealthySince.IsZero() {
				o.unhealthySince = now
			}
		} else if !o.unhealthySince.IsZero() {
			if now.Sub(o.unhealthySince) >= shareRampOutage && shareRampOf(acc, time.Time{}, now).Cause == "" {
				o.recovered = now
			}
			o.unhealthySince = time.Time{}
		}
		if !o.recovered.IsZero() && now.Sub(o.recovered) < shareRampDuration {
			recovered[id] = o.recovered
		} else {
			o.recovered = time.Time{}
		}
		if !o.unhealthySince.IsZero() || !o.recovered.IsZero() {
			next[id] = o
		}
	}
	s.outages = next
	return recovered
}

// readHealthStats reads the attempts of the window that ends at the last
// whole minute.
func (s *ShareSchedulingService) readHealthStats(ctx context.Context, accounts map[int64]*Account, now time.Time) (map[int64]shareHealthStats, error) {
	ids := make([]int64, 0, len(accounts))
	for id := range accounts {
		ids = append(ids, id)
	}
	to := now.Truncate(time.Minute)
	attempts, err := s.repo.AccountAttempts(ctx, ids, to.Add(-shareHealthWindow), to)
	if err != nil {
		return nil, err
	}
	stats := make(map[int64]shareHealthStats, len(ids))
	for id, n := range attempts.Successes {
		st := stats[id]
		st.Attempts += n
		stats[id] = st
	}
	for _, f := range attempts.Failures {
		if !shareSoftFailureAttempt(f.ShareSchedulingUpstreamAttempt) {
			continue
		}
		st := stats[f.AccountID]
		st.Attempts += f.Count
		st.SoftFailures += f.Count
		stats[f.AccountID] = st
	}
	return stats, nil
}

// shareGroupHealth rates the members of a group tier by tier.
func shareGroupHealth(members []Account, stats map[int64]shareHealthStats) map[int64]shareHealth {
	tiers := map[int][]int{}
	for i := range members {
		tiers[members[i].Priority] = append(tiers[members[i].Priority], i)
	}
	out := make(map[int64]shareHealth, len(members))
	for _, idx := range tiers {
		in := make([]shareHealthMember, len(idx))
		for j, i := range idx {
			in[j] = shareHealthMember{Healthy: members[i].IsSchedulable(), Stats: stats[members[i].ID]}
		}
		for j, h := range shareTierHealth(in) {
			out[members[idx[j]].ID] = h
		}
	}
	return out
}
