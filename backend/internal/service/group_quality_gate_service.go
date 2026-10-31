package service

import (
	"context"
	"errors"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const groupQualityEvaluationInterval = time.Minute

type groupQualityMemberSnapshot struct {
	AccountID int64
	Metric    GroupQualityMetrics
	Result    GroupQualityMemberResult
	State     groupQualityAccountState
}

type groupQualitySnapshot struct {
	GeneratedAt time.Time
	ObservedAt  time.Time
	Stale       bool
	Policies    GroupQualityGatePolicies
	Metrics     map[int64]GroupQualityMetrics
	Members     map[int64]map[int64]groupQualityMemberSnapshot
}

type cachedGroupQualityPolicies struct {
	value     GroupQualityGatePolicies
	expiresAt time.Time
}

// GroupQualityGateService evaluates quality in-process and publishes immutable views.
type GroupQualityGateService struct {
	policyRepo  GroupQualityGateRepository
	accountRepo AccountRepository
	groupRepo   GroupRepository

	snapshot         atomic.Pointer[groupQualitySnapshot]
	policyMu         sync.Mutex
	policyCache      atomic.Pointer[cachedGroupQualityPolicies]
	runMu            sync.Mutex
	processStartedAt time.Time
	cancel           context.CancelFunc
	done             chan struct{}
}

// NewGroupQualityGateService creates the evaluator. Start performs the first evaluation synchronously.
func NewGroupQualityGateService(policyRepo GroupQualityGateRepository, accountRepo AccountRepository, groupRepo GroupRepository) *GroupQualityGateService {
	s := &GroupQualityGateService{policyRepo: policyRepo, accountRepo: accountRepo, groupRepo: groupRepo, processStartedAt: time.Now().UTC()}
	s.snapshot.Store(&groupQualitySnapshot{GeneratedAt: time.Now().UTC(), Policies: GroupQualityGatePolicies{Groups: map[string]GroupQualityGatePolicy{}}, Metrics: map[int64]GroupQualityMetrics{}, Members: map[int64]map[int64]groupQualityMemberSnapshot{}})
	return s
}

// Start runs one evaluation immediately and then once per minute.
func (s *GroupQualityGateService) Start() {
	if s == nil || s.policyRepo == nil || s.accountRepo == nil || s.groupRepo == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.done = cancel, make(chan struct{})
	_ = s.evaluate(ctx)
	go func() {
		defer close(s.done)
		t := time.NewTicker(groupQualityEvaluationInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = s.evaluate(ctx)
			}
		}
	}()
}

// Stop terminates the evaluator loop.
func (s *GroupQualityGateService) Stop() {
	if s == nil || s.cancel == nil {
		return
	}
	s.cancel()
	if s.done != nil {
		<-s.done
	}
}

func (s *GroupQualityGateService) Vetoed(groupID, accountID int64) bool {
	if s == nil {
		return false
	}
	v := s.snapshot.Load()
	if v == nil {
		return false
	}
	members := v.Members[groupID]
	member, ok := members[accountID]
	return ok && member.Result.State == "ejected" && (member.State.Until.IsZero() || time.Now().Before(member.State.Until)) && !member.Result.Pinned && !member.Result.GuardHeld
}

func (s *GroupQualityGateService) OrderTier(groupID, accountID int64) int {
	if s == nil {
		return GroupQualityGateTierFast
	}
	v := s.snapshot.Load()
	if v == nil {
		return GroupQualityGateTierFast
	}
	if member, ok := v.Members[groupID][accountID]; ok {
		if member.Result.State == "ejected" && !member.State.Until.IsZero() && !time.Now().Before(member.State.Until) {
			return GroupQualityGateTierTrial
		}
		return member.Result.Tier
	}
	return GroupQualityGateTierFast
}

func (s *GroupQualityGateService) Policies(ctx context.Context) (GroupQualityGatePolicies, error) {
	if s == nil || s.policyRepo == nil {
		return GroupQualityGatePolicies{Groups: map[string]GroupQualityGatePolicy{}}, nil
	}
	return s.loadPolicies(ctx)
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
	policies = normalizeGroupQualityGatePolicies(policies)
	s.policyCache.Store(&cachedGroupQualityPolicies{value: policies, expiresAt: time.Now().Add(30 * time.Second)})
	return cloneGroupQualityGatePolicies(policies), nil
}

func cloneGroupQualityGatePolicies(in GroupQualityGatePolicies) GroupQualityGatePolicies {
	out := GroupQualityGatePolicies{Version: in.Version, Groups: make(map[string]GroupQualityGatePolicy, len(in.Groups))}
	for id, policy := range in.Groups {
		policy.PinnedAccountIDs = append([]int64(nil), policy.PinnedAccountIDs...)
		out.Groups[id] = policy
	}
	return out
}

func normalizeGroupQualityGatePolicies(in GroupQualityGatePolicies) GroupQualityGatePolicies {
	if in.Groups == nil {
		in.Groups = map[string]GroupQualityGatePolicy{}
	}
	for id, policy := range in.Groups {
		in.Groups[id] = normalizeGroupQualityGatePolicy(policy)
	}
	return in
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
	accounts, err := s.accountRepo.ListAllWithFilters(ctx, "", "", "", "", groupID, "")
	if err != nil {
		return GroupQualityGatePolicies{}, err
	}
	ids := make([]int64, 0, len(accounts))
	for _, a := range accounts {
		ids = append(ids, a.ID)
	}
	policy = normalizeGroupQualityGatePolicy(policy)
	if err := ValidateGroupQualityGatePolicy(policy, group, ids); err != nil {
		return GroupQualityGatePolicies{}, ErrInvalidGroupQualityGatePolicy.WithCause(err)
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
	if current.Groups == nil {
		current.Groups = map[string]GroupQualityGatePolicy{}
	}
	current.Groups[strconv.FormatInt(groupID, 10)] = policy
	current.Version++
	updated, err := s.policyRepo.CompareAndSwapPolicies(ctx, expectedVersion, current)
	if err != nil {
		return updated, err
	}
	// A newly-enabled group enters status only after the next regular evaluation.
	s.policyCache.Store(nil)
	return updated, nil
}

func (s *GroupQualityGateService) Status(ctx context.Context, groupID *int64) (*GroupQualityGateStatus, error) {
	if s == nil {
		return &GroupQualityGateStatus{AccountMetrics: map[string]GroupQualityMetricView{}}, nil
	}
	snap := s.snapshot.Load()
	if snap == nil {
		snap = &groupQualitySnapshot{GeneratedAt: time.Now().UTC(), Policies: GroupQualityGatePolicies{Groups: map[string]GroupQualityGatePolicy{}}, Metrics: map[int64]GroupQualityMetrics{}, Members: map[int64]map[int64]groupQualityMemberSnapshot{}}
	}
	out := &GroupQualityGateStatus{GeneratedAt: snap.GeneratedAt, Stale: snap.Stale, AccountMetrics: map[string]GroupQualityMetricView{}}
	active, err := s.groupRepo.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	groups := make([]*Group, 0, len(active))
	for i := range active {
		groups = append(groups, &active[i])
	}
	if groupID != nil {
		group, err := s.groupRepo.GetByID(ctx, *groupID)
		if err != nil {
			return nil, err
		}
		if group == nil {
			return nil, infraerrors.NotFound("GROUP_NOT_FOUND", "group not found")
		}
		groups = []*Group{group}
	}
	for _, group := range groups {
		policy, configured := snap.Policies.Groups[strconv.FormatInt(group.ID, 10)]
		policy = normalizeGroupQualityGatePolicy(policy)
		if !configured {
			policy = defaultGroupQualityGatePolicy()
		}
		enabled := group.Platform == PlatformOpenAI && configured && policy.Enabled
		if groupID == nil && !enabled {
			continue
		}
		// A policy written after this snapshot is intentionally visible on the next evaluation only.
		if enabled && snap.Members[group.ID] == nil {
			continue
		}
		accounts, err := s.accountRepo.ListAllWithFilters(ctx, "", "", "", "", group.ID, "")
		if err != nil {
			return nil, err
		}
		gv := GroupQualityGroupView{GroupID: group.ID, Enabled: enabled, FastWarmTTFTP90Ms: policy.FastWarmTTFTP90Ms, Accounts: make([]GroupQualityAccountView, 0, len(accounts))}
		now := time.Now().UTC()
		for _, acc := range accounts {
			metric := snap.Metrics[acc.ID]
			out.AccountMetrics[strconv.FormatInt(acc.ID, 10)] = metricView(metric)
			member := snap.Members[group.ID][acc.ID]
			if member.AccountID == 0 {
				member = groupQualityMemberSnapshot{AccountID: acc.ID, Metric: metric, Result: GroupQualityMemberResult{Usable: groupMemberUsable(group, &acc, time.Now()), Passing: true, State: "eligible", Tier: GroupQualityGateTierFast}}
			}
			reasons := []string(nil)
			if enabled {
				if member.Result.State == "ejected" && len(member.State.Reasons) > 0 {
					reasons = append(reasons, member.State.Reasons...)
				} else {
					reasons = qualityReasons(policy, metric)
				}
			}
			state, tier := member.Result.State, member.Result.Tier
			passing, guardHeld := member.Result.Passing, member.Result.GuardHeld
			ejected := state == "ejected" && (member.State.Until.IsZero() || now.Before(member.State.Until))
			if state == "ejected" && !ejected {
				state, tier = "trial", GroupQualityGateTierTrial
				passing, guardHeld = metric.Passing(policy), false
				reasons = nil
			}
			vetoed := ejected && !member.Result.Pinned && !member.Result.GuardHeld
			row := GroupQualityAccountView{AccountID: acc.ID, State: state, Strikes: member.State.Strike, Usable: member.Result.Usable, Passing: passing, GuardHeld: guardHeld, Pinned: member.Result.Pinned, Vetoed: vetoed, Tier: tier, Headroom: qualityHeadroom(policy, metric), Reasons: reasons}
			if ejected && !member.State.Until.IsZero() {
				u := member.State.Until
				row.Until = &u
			}
			gv.Accounts = append(gv.Accounts, row)
			switch row.State {
			case "trial":
				gv.Summary.Trial++
			case "ejected":
				if row.Vetoed {
					gv.Summary.Ejected++
				} else {
					gv.Summary.Eligible++
				}
			default:
				gv.Summary.Eligible++
			}
		}
		out.Groups = append(out.Groups, gv)
	}
	return out, nil
}

func metricView(m GroupQualityMetrics) GroupQualityMetricView {
	return GroupQualityMetricView{Calls: m.Calls, SoftFailures: m.SoftFailures, ErrorRate: m.ErrorRate(), WarmSamples: m.WarmSamples, WarmTTFTP90Ms: m.WarmTTFTP90Ms, ZeroCacheSamples: m.ZeroCacheSamples, WarmZeroCacheRate: m.WarmZeroCacheRate}
}

func qualityHeadroom(p GroupQualityGatePolicy, m GroupQualityMetrics) float64 {
	values := []float64{}
	if m.Calls >= 20 && m.SoftFailures >= 5 {
		values = append(values, 1-m.ErrorRate()/p.MaxErrorRate)
	}
	if m.WarmSamples >= 10 {
		values = append(values, 1-float64(m.WarmTTFTP90Ms)/float64(p.MaxWarmTTFTP90Ms))
	}
	if len(values) == 0 {
		return 0
	}
	out := values[0]
	for _, v := range values[1:] {
		if v < out {
			out = v
		}
	}
	return out
}

func qualityReasons(p GroupQualityGatePolicy, m GroupQualityMetrics) []string {
	return groupQualityReasons(p, m)
}

func (s *GroupQualityGateService) evaluate(ctx context.Context) error {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	now := time.Now().UTC()
	policies, err := s.loadPolicies(ctx)
	if err != nil {
		s.markFailed(err)
		return err
	}
	groups, err := s.groupRepo.ListActive(ctx)
	if err != nil {
		s.markFailed(err)
		return err
	}
	membersByGroup := map[int64][]Account{}
	ids, seen := make([]int64, 0), map[int64]bool{}
	for _, group := range groups {
		if group.Platform != PlatformOpenAI {
			continue
		}
		accounts, e := s.accountRepo.ListAllWithFilters(ctx, "", "", "", "", group.ID, "")
		if e != nil {
			s.markFailed(e)
			return e
		}
		membersByGroup[group.ID] = accounts
		for _, account := range accounts {
			if !seen[account.ID] {
				seen[account.ID] = true
				ids = append(ids, account.ID)
			}
		}
	}
	old := s.snapshot.Load()
	from := now.Add(-2 * time.Hour)
	for _, members := range old.Members {
		for _, member := range members {
			if member.State.State == "trial" && !member.State.TrialSince.IsZero() && member.State.TrialSince.Before(from) {
				from = member.State.TrialSince
			}
		}
	}
	observations, observedAt, err := s.policyRepo.LoadObservations(ctx, ids, from.Add(-5*time.Minute), now)
	if err != nil {
		s.markFailed(err)
		return err
	}
	if observedAt.Before(now.Add(-5*time.Minute)) || observedAt.After(now.Add(5*time.Minute)) {
		s.markFailed(errors.New("metric data timestamp is stale"))
		return errors.New("metric data timestamp is stale")
	}
	byAccount := make(map[int64][]GroupQualityObservation, len(ids))
	for _, observation := range observations {
		byAccount[observation.AccountID] = append(byAccount[observation.AccountID], observation)
	}
	fullMetrics := make(map[int64]GroupQualityMetrics, len(ids))
	for _, id := range ids {
		fullMetrics[id] = groupQualityMetricsFor(byAccount[id], now, now.Add(-15*time.Minute), now.Add(-30*time.Minute), now.Add(-2*time.Hour), time.Time{})
	}
	next := &groupQualitySnapshot{GeneratedAt: now, ObservedAt: observedAt, Policies: policies, Metrics: fullMetrics, Members: make(map[int64]map[int64]groupQualityMemberSnapshot, len(membersByGroup))}
	for i := range groups {
		group := &groups[i]
		if group.Platform != PlatformOpenAI {
			continue
		}
		policy, configured := policies.Groups[strconv.FormatInt(group.ID, 10)]
		policy = normalizeGroupQualityGatePolicy(policy)
		if !configured {
			policy = defaultGroupQualityGatePolicy()
		}
		accounts := membersByGroup[group.ID]
		byID := make(map[int64]groupQualityMemberSnapshot, len(accounts))
		for i := range accounts {
			account := &accounts[i]
			previousMember := old.Members[group.ID][account.ID]
			previous := previousMember.State
			resetAt := accountGroupQualityResetAt(account)
			if resetAt.After(previous.ResetAt) {
				previous = groupQualityAccountState{State: "trial", TrialSince: resetAt, ResetAt: resetAt, Tier: GroupQualityGateTierTrial, TierAt: resetAt}
			}
			pinned := group.Platform == PlatformOpenAI && configured && policy.Enabled && containsInt64(policy.PinnedAccountIDs, account.ID)
			usable := groupMemberUsable(group, account, now)
			metric := fullMetrics[account.ID]
			state := previous
			passing := true
			if group.Platform == PlatformOpenAI && configured && policy.Enabled {
				errorStart, ttftStart, cacheStart := qualityValidStarts(now, s.processStartedAt, resetAt, previous)
				trialStart := maxTime(previous.TrialSince, s.processStartedAt, resetAt)
				metric = groupQualityMetricsFor(byAccount[account.ID], now, errorStart, ttftStart, cacheStart, trialStart)
				state, _ = evaluateGroupQualityMember(now, policy, metric, previous, pinned)
				passing = state.State != "ejected" && metric.Passing(policy)
			} else {
				state = groupQualityAccountState{State: "eligible", Tier: GroupQualityGateTierFast, TierAt: previous.TierAt, Strike: previous.Strike, LastEjected: previous.LastEjected, LastEjectionEnd: previous.LastEjectionEnd, ResetAt: resetAt}
			}
			tier := resolveGroupQualityTier(now, policy, metric, state, previous, configured && policy.Enabled, passing)
			state.Tier = tier
			if state.State == "ejected" || state.State == "trial" {
				state.TierAt = time.Time{}
			} else if metric.WarmSamples == 0 || metric.WarmSamples >= 10 || metric.ZeroCacheSamples >= 20 && metric.WarmZeroCacheRate > 0.30 || previous.TierAt.IsZero() || now.Sub(previous.TierAt) > 6*time.Hour || previous.State == "ejected" || previous.State == "trial" {
				state.TierAt = now
			} else {
				state.TierAt = previous.TierAt
			}
			result := GroupQualityMemberResult{Usable: usable, Passing: passing, Pinned: pinned, State: state.State, Tier: tier}
			byID[account.ID] = groupQualityMemberSnapshot{AccountID: account.ID, Metric: metric, State: state, Result: result}
			logGroupQualityTransition(previous, state, group.ID, account.ID, policy, metric)
		}
		if group.Platform == PlatformOpenAI && configured && policy.Enabled {
			applyQualityGuard(accounts, byID, policy)
			for accountID, member := range byID {
				if member.Result.GuardHeld && !old.Members[group.ID][accountID].Result.GuardHeld {
					slog.Info("group_quality_gate.guard_hold", "group_id", group.ID, "account_id", accountID,
						"error_rate", member.Metric.ErrorRate(), "max_error_rate", policy.MaxErrorRate,
						"warm_ttft_p90_ms", member.Metric.WarmTTFTP90Ms, "max_warm_ttft_p90_ms", policy.MaxWarmTTFTP90Ms)
				}
			}
		}
		next.Members[group.ID] = byID
	}
	s.snapshot.Store(next)
	return nil
}

func qualityValidStarts(now, processStart, resetAt time.Time, state groupQualityAccountState) (time.Time, time.Time, time.Time) {
	errorStart := maxTime(now.Add(-15*time.Minute), processStart, resetAt, state.LastEjectionEnd)
	ttftStart := maxTime(now.Add(-30*time.Minute), processStart, resetAt, state.LastEjectionEnd)
	cacheStart := maxTime(now.Add(-2*time.Hour), processStart, resetAt, state.LastEjectionEnd)
	if state.State == "ejected" && !state.EjectedAt.IsZero() {
		errorStart = maxTime(errorStart, state.EjectedAt)
		ttftStart = maxTime(ttftStart, state.EjectedAt)
		cacheStart = maxTime(cacheStart, state.EjectedAt)
	}
	if state.State == "trial" && !state.TrialSince.IsZero() {
		errorStart = maxTime(errorStart, state.TrialSince)
		ttftStart = maxTime(ttftStart, state.TrialSince)
		cacheStart = maxTime(cacheStart, state.TrialSince)
	}
	return errorStart, ttftStart, cacheStart
}

func groupQualityMetricsFor(observations []GroupQualityObservation, now, errorSince, ttftSince, cacheSince, trialSince time.Time) GroupQualityMetrics {
	m := GroupQualityMetrics{}
	seenFailures := map[string]bool{}
	ttftValues := []int{}
	type trialEvent struct {
		at           time.Time
		requestID    string
		softFailures int64
	}
	trialRequests := map[string]trialEvent{}
	seenTrialFailures := map[string]bool{}
	zeroCount := 0
	for _, o := range observations {
		if o.At.After(now) {
			continue
		}
		if !errorSince.IsZero() && o.At.After(errorSince) && (o.Success || o.SoftFailure) {
			if o.SoftFailure {
				key := strconv.FormatInt(o.AccountID, 10) + "\x00" + o.RequestID + "\x00" + strconv.FormatInt(o.At.UnixMilli(), 10)
				if !seenFailures[key] {
					seenFailures[key] = true
					m.Calls++
					m.SoftFailures++
				}
			} else {
				m.Calls++
			}
		}
		if !ttftSince.IsZero() && o.At.After(ttftSince) && o.Success && o.Warm && o.Streaming && o.FirstTokenMs > 0 {
			m.WarmSamples++
			ttftValues = append(ttftValues, o.FirstTokenMs)
		}
		if !cacheSince.IsZero() && o.At.After(cacheSince) && o.Success && o.Warm && o.PromptTokens >= 2048 {
			m.ZeroCacheSamples++
			if o.CacheReadTokens == 0 {
				zeroCount++
			}
		}
		if !trialSince.IsZero() && o.At.After(trialSince) && (o.Success || o.SoftFailure) {
			requestID := o.RequestID
			if requestID == "" {
				requestID = "at:" + strconv.FormatInt(o.At.UnixMilli(), 10)
			}
			event, ok := trialRequests[requestID]
			if !ok || o.At.Before(event.at) {
				event.at = o.At
			}
			event.requestID = requestID
			if o.SoftFailure {
				failureKey := requestID + "\x00" + strconv.FormatInt(o.At.UnixMilli(), 10)
				if !seenTrialFailures[failureKey] {
					seenTrialFailures[failureKey] = true
					event.softFailures++
				}
			}
			trialRequests[requestID] = event
		}
	}
	if len(ttftValues) > 0 {
		sort.Ints(ttftValues)
		pos := 0.90 * float64(len(ttftValues)-1)
		low := int(math.Floor(pos))
		high := int(math.Ceil(pos))
		value := float64(ttftValues[low])
		if high > low {
			value += (float64(ttftValues[high]) - value) * (pos - float64(low))
		}
		m.WarmTTFTP90Ms = int(math.Round(value))
	}
	if m.ZeroCacheSamples > 0 {
		m.WarmZeroCacheRate = float64(zeroCount) / float64(m.ZeroCacheSamples)
	}
	trialEvents := make([]trialEvent, 0, len(trialRequests))
	for _, event := range trialRequests {
		trialEvents = append(trialEvents, event)
	}
	sort.Slice(trialEvents, func(i, j int) bool {
		if trialEvents[i].at.Equal(trialEvents[j].at) {
			return trialEvents[i].requestID < trialEvents[j].requestID
		}
		return trialEvents[i].at.Before(trialEvents[j].at)
	})
	m.TrialRequests = int64(len(trialEvents))
	for i, event := range trialEvents {
		if i >= 20 {
			break
		}
		m.TrialSoftFailures += event.softFailures
	}
	return m
}

func maxTime(values ...time.Time) time.Time {
	var out time.Time
	for _, value := range values {
		if value.After(out) {
			out = value
		}
	}
	return out
}

func accountGroupQualityResetAt(account *Account) time.Time {
	if account == nil {
		return time.Time{}
	}
	raw := account.getExtraString("group_quality_reset_at")
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func resolveGroupQualityTier(now time.Time, policy GroupQualityGatePolicy, metric GroupQualityMetrics, state, previous groupQualityAccountState, enabled, passing bool) int {
	if enabled && state.State == "trial" {
		return GroupQualityGateTierTrial
	}
	if enabled && (state.State == "ejected" || !passing) {
		return GroupQualityGateTierOther
	}
	if metric.ZeroCacheSamples >= 20 && metric.WarmZeroCacheRate > 0.30 {
		return GroupQualityGateTierOther
	}
	if metric.WarmSamples == 0 {
		return GroupQualityGateTierFast
	}
	if metric.WarmSamples < 10 {
		if !enabled {
			if previous.Tier == GroupQualityGateTierOther && !previous.TierAt.IsZero() && now.Sub(previous.TierAt) <= 6*time.Hour {
				return GroupQualityGateTierOther
			}
			return GroupQualityGateTierFast
		}
		if previous.State == "ejected" || previous.State == "trial" || previous.TierAt.IsZero() || now.Sub(previous.TierAt) > 6*time.Hour {
			return GroupQualityGateTierFast
		}
		return previous.Tier
	}
	if metric.WarmTTFTP90Ms <= policy.FastWarmTTFTP90Ms {
		return GroupQualityGateTierFast
	}
	return GroupQualityGateTierOther
}

func logGroupQualityTransition(old, next groupQualityAccountState, groupID, accountID int64, policy GroupQualityGatePolicy, metric GroupQualityMetrics) {
	if next.State == "ejected" && (old.State != "ejected" || !old.Until.Equal(next.Until)) {
		slog.Info("group_quality_gate.eject", "group_id", groupID, "account_id", accountID, "error_rate", metric.ErrorRate(), "max_error_rate", policy.MaxErrorRate, "warm_ttft_p90_ms", metric.WarmTTFTP90Ms, "max_warm_ttft_p90_ms", policy.MaxWarmTTFTP90Ms, "until", next.Until, "strikes", next.Strike)
	}
	if next.State == "trial" && old.State != "trial" {
		slog.Info("group_quality_gate.trial", "group_id", groupID, "account_id", accountID, "error_rate", metric.ErrorRate(), "max_error_rate", policy.MaxErrorRate, "warm_ttft_p90_ms", metric.WarmTTFTP90Ms, "max_warm_ttft_p90_ms", policy.MaxWarmTTFTP90Ms)
	}
	if old.State == "ejected" && next.State != "ejected" {
		slog.Info("group_quality_gate.recover", "group_id", groupID, "account_id", accountID, "error_rate", metric.ErrorRate(), "max_error_rate", policy.MaxErrorRate, "warm_ttft_p90_ms", metric.WarmTTFTP90Ms, "max_warm_ttft_p90_ms", policy.MaxWarmTTFTP90Ms, "state", next.State)
	}
}

func applyQualityGuard(accounts []Account, members map[int64]groupQualityMemberSnapshot, policy GroupQualityGatePolicy) {
	usable, countable, passing := 0, 0, 0
	for _, a := range accounts {
		m := members[a.ID]
		if m.Result.Usable {
			usable++
			if !m.Result.Pinned || m.Result.Passing {
				countable++
			}
			if m.Result.Passing || m.Result.GuardHeld {
				passing++
			}
		}
	}
	target := groupQualityMaxInt(2, (usable+1)/2)
	if target > countable {
		target = countable
	}
	if passing >= target {
		return
	}
	type candidate struct {
		id    int64
		score float64
	}
	candidates := []candidate{}
	for _, a := range accounts {
		m := members[a.ID]
		if !m.Result.Usable || m.Result.Pinned || m.Result.State != "ejected" {
			continue
		}
		score := 0.0
		if m.Metric.Calls >= 20 && m.Metric.SoftFailures >= 5 {
			score = m.Metric.ErrorRate() / policy.MaxErrorRate
		}
		if m.Metric.WarmSamples >= 10 {
			score = math.Max(score, float64(m.Metric.WarmTTFTP90Ms)/float64(policy.MaxWarmTTFTP90Ms))
		}
		candidates = append(candidates, candidate{a.ID, score})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].score < candidates[j].score })
	for _, c := range candidates {
		if passing >= target {
			break
		}
		m := members[c.id]
		m.Result.GuardHeld = true
		m.Result.Tier = GroupQualityGateTierOther
		members[c.id] = m
		passing++
	}
	if passing < target {
		for _, a := range accounts {
			m := members[a.ID]
			if m.Result.Usable && !m.Result.Pinned && !m.Result.Passing && !m.Result.GuardHeld {
				m.Result.GuardHeld = true
				members[a.ID] = m
			}
		}
	}
}

func groupQualityMaxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func (s *GroupQualityGateService) markFailed(err error) {
	old := s.snapshot.Load()
	if old == nil {
		old = &groupQualitySnapshot{GeneratedAt: time.Now().UTC(), Policies: GroupQualityGatePolicies{Groups: map[string]GroupQualityGatePolicy{}}, Metrics: map[int64]GroupQualityMetrics{}, Members: map[int64]map[int64]groupQualityMemberSnapshot{}}
	}
	next := *old
	next.Stale = true
	s.snapshot.Store(&next)
	slog.Warn("group_quality_gate.evaluate_failed", "error", err)
}

var _ GroupQualityGateView = (*GroupQualityGateService)(nil)
