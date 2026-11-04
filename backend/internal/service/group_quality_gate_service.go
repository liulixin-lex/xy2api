package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
)

const groupQualityEvaluationInterval = time.Minute

type groupQualityMemberSnapshot struct {
	AccountID     int64
	Metric        groupQualityMetrics
	State         groupQualityAccountState
	Tier          int
	Pinned        bool
	ExpectedP90Ms *float64
}

type groupQualityDeparted struct {
	Member groupQualityMemberSnapshot
	Since  time.Time
}

type groupQualitySnapshot struct {
	GeneratedAt time.Time
	StaleSince  time.Time
	Stale       bool
	Policies    GroupQualityGatePolicies
	Metrics     map[int64]groupQualityMetrics
	Members     map[int64]map[int64]groupQualityMemberSnapshot
	Composition map[int64]groupQualityComposition
	Departed    map[int64]map[int64]groupQualityDeparted
}

type cachedGroupQualityPolicies struct {
	value     GroupQualityGatePolicies
	expiresAt time.Time
}

type groupQualityLatencyLookup struct {
	lookup func(accountID int64, now time.Time) (until time.Time, models []string)
}

// GroupQualityGateService publishes immutable, process-local snapshots.
type GroupQualityGateService struct {
	policyRepo       GroupQualityGateRepository
	accountRepo      AccountRepository
	groupRepo        GroupRepository
	snapshot         atomic.Pointer[groupQualitySnapshot]
	policyMu         sync.Mutex
	policyCache      atomic.Pointer[cachedGroupQualityPolicies]
	latencyHold      atomic.Pointer[groupQualityLatencyLookup]
	runMu            sync.Mutex
	processStartedAt time.Time
	now              func() time.Time
	cancel           context.CancelFunc
	done             chan struct{}
}

func NewGroupQualityGateService(policyRepo GroupQualityGateRepository, accountRepo AccountRepository, groupRepo GroupRepository) *GroupQualityGateService {
	now := time.Now().UTC()
	s := &GroupQualityGateService{policyRepo: policyRepo, accountRepo: accountRepo, groupRepo: groupRepo, processStartedAt: now, now: time.Now}
	s.snapshot.Store(&groupQualitySnapshot{GeneratedAt: now, Policies: GroupQualityGatePolicies{Groups: map[string]GroupQualityGatePolicy{}}, Members: map[int64]map[int64]groupQualityMemberSnapshot{}})
	return s
}

// SetLatencyHoldLookup supplies the live, model-scoped hold display without
// coupling the quality state machine to the latency tracker.
func (s *GroupQualityGateService) SetLatencyHoldLookup(lookup func(accountID int64, now time.Time) (until time.Time, models []string)) {
	if s != nil {
		s.latencyHold.Store(&groupQualityLatencyLookup{lookup: lookup})
	}
}

func (s *GroupQualityGateService) Start() {
	if s == nil || s.policyRepo == nil || s.accountRepo == nil || s.groupRepo == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.done = cancel, make(chan struct{})
	go func() {
		defer close(s.done)
		_ = s.evaluate(ctx)
		ticker := time.NewTicker(groupQualityEvaluationInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = s.evaluate(ctx)
			}
		}
	}()
}

func (s *GroupQualityGateService) Stop() {
	if s == nil || s.cancel == nil {
		return
	}
	s.cancel()
	if s.done != nil {
		<-s.done
	}
}

// Temporary compatibility stubs; the scheduling work package removes callers.

func (s *GroupQualityGateService) policy(groupID int64) GroupQualityGatePolicy {
	p := GroupQualityGatePolicy{}
	if s != nil {
		if snap := s.snapshot.Load(); snap != nil {
			p = snap.Policies.Groups[strconv.FormatInt(groupID, 10)]
		}
		if cached := s.policyCache.Load(); cached != nil {
			p = cached.value.Groups[strconv.FormatInt(groupID, 10)]
		}
	}
	return normalizeGroupQualityGatePolicy(p)
}

func (s *GroupQualityGateService) Enabled(groupID int64) bool {
	return s != nil && s.policy(groupID).V3.Enabled
}

func (s *GroupQualityGateService) OrderTier(groupID, accountID int64) int {
	if !s.Enabled(groupID) {
		return GroupQualityGateTierPass
	}
	p := s.policy(groupID)
	if containsInt64(p.PinnedAccountIDs, accountID) {
		return GroupQualityGateTierPass
	}
	if snap := s.snapshot.Load(); snap != nil {
		if member, ok := snap.Members[groupID][accountID]; ok {
			return member.Tier
		}
	}
	if p.V3.Strict {
		return GroupQualityGateTierRetest
	}
	return GroupQualityGateTierPass
}

func (s *GroupQualityGateService) ExpectedP90Ms(groupID, accountID int64) (int, bool) {
	if !s.Enabled(groupID) {
		return 0, false
	}
	snap := s.snapshot.Load()
	if snap == nil {
		return 0, false
	}
	value := snap.Members[groupID][accountID].ExpectedP90Ms
	if value == nil {
		return 0, false
	}
	return int(math.Round(*value)), true
}

func (s *GroupQualityGateService) MigrationTarget(groupID, accountID int64) bool {
	if !s.Enabled(groupID) {
		return false
	}
	snap := s.snapshot.Load()
	if snap == nil {
		return false
	}
	m, ok := snap.Members[groupID][accountID]
	if !ok {
		return false
	}
	p := *s.policy(groupID).V3
	if m.ExpectedP90Ms == nil || *m.ExpectedP90Ms > .9*float64(p.MaxTTFTP90Seconds)*1000 {
		return false
	}
	// Pinned members never bypass their actual dimension states.
	for _, dim := range m.State.dimensions() {
		if dim.state() == "fail" {
			return false
		}
	}
	if p.Strict {
		return m.Metric.ErrorSamples >= 20 && m.Metric.CacheSamples >= 20 &&
			m.Metric.ErrorRate != nil && *m.Metric.ErrorRate <= .8*p.MaxErrorRate &&
			m.Metric.CacheRate != nil && *m.Metric.CacheRate >= p.MinCacheKeepRate
	}
	now := s.now().UTC()
	return !m.State.Error.hasBad(now) && !m.State.Cache.hasBad(now)
}

func (s *GroupQualityGateService) Policies(ctx context.Context) (GroupQualityGatePolicies, error) {
	if s == nil || s.policyRepo == nil {
		return GroupQualityGatePolicies{Groups: map[string]GroupQualityGatePolicy{}}, nil
	}
	policies, err := s.loadPolicies(ctx)
	if err != nil {
		return policies, err
	}
	return normalizeGroupQualityGatePolicies(policies), nil
}

func (s *GroupQualityGateService) loadPolicies(ctx context.Context) (GroupQualityGatePolicies, error) {
	if cached := s.policyCache.Load(); cached != nil && time.Now().Before(cached.expiresAt) {
		return cloneGroupQualityGatePolicies(cached.value), nil
	}
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	if cached := s.policyCache.Load(); cached != nil && time.Now().Before(cached.expiresAt) {
		return cloneGroupQualityGatePolicies(cached.value), nil
	}
	policies, err := s.policyRepo.LoadPolicies(ctx)
	if err != nil {
		return GroupQualityGatePolicies{}, err
	}
	s.policyCache.Store(&cachedGroupQualityPolicies{value: cloneGroupQualityGatePolicies(policies), expiresAt: time.Now().Add(30 * time.Second)})
	return cloneGroupQualityGatePolicies(policies), nil
}

func cloneGroupQualityGatePolicies(in GroupQualityGatePolicies) GroupQualityGatePolicies {
	out := GroupQualityGatePolicies{Version: in.Version, Groups: make(map[string]GroupQualityGatePolicy, len(in.Groups))}
	for id, p := range in.Groups {
		p.PinnedAccountIDs = slices.Clone(p.PinnedAccountIDs)
		if p.V3 != nil {
			v := *p.V3
			p.V3 = &v
		}
		if p.legacyFields != nil {
			fields := make(map[string]json.RawMessage, len(p.legacyFields))
			for k, v := range p.legacyFields {
				fields[k] = slices.Clone(v)
			}
			p.legacyFields = fields
		}
		out.Groups[id] = p
	}
	return out
}

func normalizeGroupQualityGatePolicies(in GroupQualityGatePolicies) GroupQualityGatePolicies {
	out := cloneGroupQualityGatePolicies(in)
	for id, p := range out.Groups {
		out.Groups[id] = normalizeGroupQualityGatePolicy(p)
	}
	return out
}

var ErrGroupQualityGateVersionConflict = infraerrors.Conflict("GROUP_QUALITY_GATE_VERSION_CONFLICT", "group quality gate policy version conflict")
var ErrInvalidGroupQualityGatePolicy = infraerrors.BadRequest("INVALID_GROUP_QUALITY_GATE_POLICY", "invalid group quality gate policy")

func (s *GroupQualityGateService) UpdatePolicy(ctx context.Context, groupID int64, expectedVersion int, policy GroupQualityGatePolicy) (GroupQualityGatePolicies, error) {
	if s == nil || s.policyRepo == nil || s.groupRepo == nil {
		return GroupQualityGatePolicies{}, errors.New("group quality gate unavailable")
	}
	if groupID <= 0 {
		return GroupQualityGatePolicies{}, infraerrors.BadRequest("INVALID_GROUP_ID", "invalid group id")
	}
	group, err := s.groupRepo.GetByID(ctx, groupID)
	if err != nil {
		return GroupQualityGatePolicies{}, err
	}
	if group == nil {
		return GroupQualityGatePolicies{}, infraerrors.NotFound("GROUP_NOT_FOUND", "group not found")
	}
	if err := ValidateGroupQualityGatePolicy(policy, group, nil); err != nil {
		return GroupQualityGatePolicies{}, err
	}
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	current, err := s.policyRepo.LoadPolicies(ctx)
	if err != nil {
		return GroupQualityGatePolicies{}, err
	}
	if current.Version != expectedVersion {
		return current, ErrGroupQualityGateVersionConflict
	}
	current = cloneGroupQualityGatePolicies(current)
	id := strconv.FormatInt(groupID, 10)
	saved := current.Groups[id]
	v3 := *policy.V3
	saved.V3 = &v3
	current.Groups[id] = saved
	current.Version++
	updated, err := s.policyRepo.CompareAndSwapPolicies(ctx, expectedVersion, current)
	if err != nil {
		return updated, err
	}
	s.policyCache.Store(&cachedGroupQualityPolicies{value: cloneGroupQualityGatePolicies(updated), expiresAt: time.Now().Add(30 * time.Second)})
	return normalizeGroupQualityGatePolicies(updated), nil
}

func qualityAccountView(member groupQualityMemberSnapshot, now time.Time) GroupQualityAccountView {
	m, state := member.Metric, member.State
	row := GroupQualityAccountView{AccountID: member.AccountID, Tier: member.Tier, Pinned: member.Pinned,
		Status: groupQualityStatus(state, member.Tier, member.Pinned),
		TTFT:   GroupQualityTTFTView{State: state.TTFT.state(), Value: member.ExpectedP90Ms, Factor: m.Factor, Samples: m.TTFTSamples, Sessions: m.Sessions, BadUntil: state.TTFT.badUntil()},
		Error:  GroupQualityErrorView{State: state.Error.state(), Value: m.ErrorRate, Samples: m.ErrorSamples, Failures: m.Failures, WilsonLow: m.ErrorLow, WilsonHigh: m.ErrorHigh, BadUntil: state.Error.badUntil()},
		Cache:  GroupQualityCacheView{State: state.Cache.state(), Value: m.CacheRate, Samples: m.CacheSamples, WilsonLow: m.CacheLow, WilsonHigh: m.CacheHigh, BadUntil: state.Cache.badUntil()},
	}
	if !state.TTFT.hasBad(now) {
		row.TTFT.BadUntil = nil
	}
	if !state.Error.hasBad(now) {
		row.Error.BadUntil = nil
	}
	if !state.Cache.hasBad(now) {
		row.Cache.BadUntil = nil
	}
	return row
}

func (s *GroupQualityGateService) Status(ctx context.Context, groupID *int64) (*GroupQualityGateStatus, error) {
	out := &GroupQualityGateStatus{Groups: []GroupQualityGroupView{}}
	if s == nil || s.groupRepo == nil || s.accountRepo == nil {
		return out, nil
	}
	snap := s.snapshot.Load()
	if snap == nil {
		return out, nil
	}
	out.GeneratedAt, out.Stale = snap.GeneratedAt, snap.Stale
	groups, err := s.groupRepo.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	if groupID != nil {
		group, err := s.groupRepo.GetByID(ctx, *groupID)
		if err != nil {
			return nil, err
		}
		if group == nil {
			return nil, ErrGroupNotFound
		}
		groups = []Group{*group}
	}
	now := s.now().UTC()
	for _, group := range groups {
		policy := s.policy(group.ID)
		enabled := group.Platform == PlatformOpenAI && policy.V3.Enabled
		if groupID == nil && !enabled {
			continue
		}
		accounts, err := s.accountRepo.ListAllWithFilters(ctx, "", "", "", "", group.ID, "")
		if err != nil {
			return nil, err
		}
		composition := snap.Composition[group.ID]
		gv := GroupQualityGroupView{GroupID: group.ID, Enabled: enabled, Policy: *policy.V3, ExpectedP90Ms: composition.P90, ExpectedSamples: composition.Samples, Accounts: []GroupQualityAccountView{}}
		for _, account := range accounts {
			member, found := snap.Members[group.ID][account.ID]
			if !found || !enabled {
				member = groupQualityMemberSnapshot{AccountID: account.ID}
				member.Pinned = enabled && containsInt64(policy.PinnedAccountIDs, account.ID)
				p := *policy.V3
				p.Enabled = enabled
				member.Tier = groupQualityTier(member.State, p, member.Pinned, now)
			}
			row := qualityAccountView(member, now)
			if lookup := s.latencyHold.Load(); lookup != nil && lookup.lookup != nil {
				until, models := lookup.lookup(account.ID, now)
				if until.After(now) && len(models) > 0 {
					row.LatencyHold = &GroupQualityLatencyHoldView{Until: until, Models: slices.Clone(models)}
				}
			}
			gv.Accounts = append(gv.Accounts, row)
			switch row.Status {
			case "pinned":
				gv.Summary.Pinned++
			case "passing":
				gv.Summary.Passing++
			case "unknown":
				gv.Summary.Unknown++
			case "retest":
				gv.Summary.Retest++
			case "failing":
				gv.Summary.Failing++
			}
		}
		out.Groups = append(out.Groups, gv)
	}
	return out, nil
}

func (s *GroupQualityGateService) evaluate(ctx context.Context) error {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	now := s.now().UTC()
	policies, err := s.loadPolicies(ctx)
	if err != nil {
		s.markFailed(now, err)
		return err
	}
	groups, err := s.groupRepo.ListActive(ctx)
	if err != nil {
		s.markFailed(now, err)
		return err
	}
	groupIDs, enabledGroups := []int64{}, map[int64]bool{}
	for _, group := range groups {
		p := normalizeGroupQualityGatePolicy(policies.Groups[strconv.FormatInt(group.ID, 10)])
		if group.Platform == PlatformOpenAI && p.V3.Enabled {
			groupIDs = append(groupIDs, group.ID)
			enabledGroups[group.ID] = true
		}
	}
	members, err := s.policyRepo.LoadMembers(ctx, groupIDs)
	if err != nil {
		s.markFailed(now, err)
		return err
	}
	ids, seen, resets := []int64{}, map[int64]bool{}, map[int64]time.Time{}
	for _, accounts := range members {
		for _, a := range accounts {
			if a.Platform != PlatformOpenAI {
				continue
			}
			if !seen[a.ID] {
				ids = append(ids, a.ID)
				seen[a.ID] = true
			}
			resets[a.ID] = accountGroupQualityResetAt(&a)
		}
	}
	old := s.snapshot.Load()
	var observations []GroupQualityObservation
	observedAt := now
	var observationErr error
	if len(groupIDs) > 0 {
		observations, observedAt, observationErr = s.policyRepo.LoadObservations(ctx, ids, maxTime(now.Add(-groupQualityCompositionWindow), s.processStartedAt), now)
	}
	stale := observationErr != nil || observedAt.Before(now.Add(-5*time.Minute))
	metrics, composition := old.Metrics, old.Composition
	if !stale {
		eligible, failed, softFailed := prepareGroupQualityObservations(observations, now, s.processStartedAt, resets)
		baseline := buildGroupQualityBaselines(eligible, failed, now)
		metrics, composition = calculateGroupQualityMetrics(eligible, now, baseline, failed, softFailed, enabledGroups)
	}
	next := &groupQualitySnapshot{GeneratedAt: now, Stale: stale, Policies: policies, Metrics: metrics, Composition: composition, Members: map[int64]map[int64]groupQualityMemberSnapshot{}}
	if stale {
		reason := observationErr
		if reason == nil {
			reason = errors.New("usage data timestamp is stale")
		}
		next.StaleSince = qualityFailureSince(old, now, observedAt)
		logGroupQualityFailure(old, now, next.StaleSince, reason)
	}
	for groupID, accounts := range members {
		if !enabledGroups[groupID] {
			continue
		}
		p := normalizeGroupQualityGatePolicy(policies.Groups[strconv.FormatInt(groupID, 10)])
		next.Members[groupID] = map[int64]groupQualityMemberSnapshot{}
		for _, account := range accounts {
			if account.Platform != PlatformOpenAI {
				continue
			}
			previous, found := old.Members[groupID][account.ID]
			if !found {
				if departed, ok := old.Departed[groupID][account.ID]; ok && now.Sub(departed.Since) < 2*time.Hour {
					previous = departed.Member
				}
			}
			state := previous.State
			resetAt := resets[account.ID]
			reset := !resetAt.Equal(state.ResetAt)
			if reset {
				state = groupQualityAccountState{ResetAt: resetAt}
			}
			metric := metrics[account.ID]
			expected := groupQualityPrediction(metric, composition[groupID])
			if stale {
				metric, expected = previous.Metric, previous.ExpectedP90Ms
				if reset {
					metric, expected = groupQualityMetrics{}, nil
				}
			} else {
				state = evaluateGroupQualityMember(now, *p.V3, metric, expected, state)
			}
			pinned := containsInt64(p.PinnedAccountIDs, account.ID)
			tier := groupQualityTier(state, *p.V3, pinned, now)
			if stale && !reset && previous.AccountID != 0 {
				tier = previous.Tier
			}
			member := groupQualityMemberSnapshot{AccountID: account.ID, State: state, Metric: metric, ExpectedP90Ms: expected, Tier: tier, Pinned: pinned}
			if !stale || reset {
				logGroupQualityTier(previous, member, groupID)
			}
			next.Members[groupID][account.ID] = member
		}
	}
	next.Departed = departedGroupQualityMembers(old, next.Members, now)
	s.snapshot.Store(next)
	return observationErr
}

func departedGroupQualityMembers(old *groupQualitySnapshot, current map[int64]map[int64]groupQualityMemberSnapshot, now time.Time) map[int64]map[int64]groupQualityDeparted {
	out := map[int64]map[int64]groupQualityDeparted{}
	keep := func(groupID, accountID int64, departed groupQualityDeparted) {
		if _, present := current[groupID][accountID]; present || now.Sub(departed.Since) >= 2*time.Hour {
			return
		}
		if out[groupID] == nil {
			out[groupID] = map[int64]groupQualityDeparted{}
		}
		out[groupID][accountID] = departed
	}
	for groupID, members := range old.Members {
		for accountID, member := range members {
			keep(groupID, accountID, groupQualityDeparted{Member: member, Since: now})
		}
	}
	for groupID, departed := range old.Departed {
		for accountID, member := range departed {
			keep(groupID, accountID, member)
		}
	}
	return out
}

func logGroupQualityTier(old, next groupQualityMemberSnapshot, groupID int64) {
	// The initial snapshot establishes a tier; it is not a transition from 0.
	if old.AccountID == 0 || old.Tier == next.Tier {
		return
	}
	dimension, value := "unknown", (*float64)(nil)
	for i, state := range next.State.dimensions() {
		if state.state() == "fail" || (state.state() == "none" && !state.LastFail.IsZero()) {
			dimension = [3]string{"ttft", "error", "cache"}[i]
			value = [3]*float64{next.ExpectedP90Ms, next.Metric.ErrorRate, next.Metric.CacheRate}[i]
			break
		}
	}
	if dimension == "unknown" {
		for i, state := range old.State.dimensions() {
			if state.state() != next.State.dimensions()[i].state() {
				dimension = [3]string{"ttft", "error", "cache"}[i]
				value = [3]*float64{next.ExpectedP90Ms, next.Metric.ErrorRate, next.Metric.CacheRate}[i]
				break
			}
		}
	}
	var number any
	if value != nil {
		number = *value
	}
	slog.Warn("group_quality_gate.tier", "account_id", next.AccountID, "group_id", groupID, "from_tier", old.Tier, "to_tier", next.Tier, "dimension", dimension, "value", number)
}

func maxTime(values ...time.Time) time.Time {
	var out time.Time
	for _, v := range values {
		if v.After(out) {
			out = v
		}
	}
	return out
}

func accountGroupQualityResetAt(account *Account) time.Time {
	if account == nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, account.getExtraString("group_quality_reset_at"))
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func qualityFailureSince(old *groupQualitySnapshot, now, observedAt time.Time) time.Time {
	if old.Stale {
		return old.StaleSince
	}
	if !observedAt.IsZero() {
		return observedAt
	}
	return now
}
func logGroupQualityFailure(old *groupQualitySnapshot, now, since time.Time, err error) {
	if !old.Stale {
		slog.Warn("group_quality_gate.evaluate_failed", "reason", err.Error(), "stale_duration", now.Sub(since))
	}
}
func (s *GroupQualityGateService) markFailed(now time.Time, err error) {
	old := s.snapshot.Load()
	next := *old
	next.Stale = true
	next.StaleSince = qualityFailureSince(old, now, time.Time{})
	s.snapshot.Store(&next)
	logGroupQualityFailure(old, now, next.StaleSince, err)
}

var _ GroupQualityGateView = (*GroupQualityGateService)(nil)
