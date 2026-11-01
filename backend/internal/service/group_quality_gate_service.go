package service

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
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
	StaleSince  time.Time
	Stale       bool
	Policies    GroupQualityGatePolicies
	Metrics     map[int64]GroupQualityMetrics
	Members     map[int64]map[int64]groupQualityMemberSnapshot
	// Resets holds the in-process manual reset already applied to each account;
	// a reset applies once to all of the account's groups, not again on joining.
	Resets map[int64]time.Time
	// Departed keeps the state of (group, account) pairs absent from the latest
	// membership until two hours after their ejection ended, so leaving and
	// rejoining a group keeps strikes and ejection history. Not shown in status.
	Departed map[int64]map[int64]groupQualityAccountState
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
	now              func() time.Time
	cancel           context.CancelFunc
	done             chan struct{}
}

// NewGroupQualityGateService creates the evaluator. Start launches the first evaluation in its background loop.
func NewGroupQualityGateService(policyRepo GroupQualityGateRepository, accountRepo AccountRepository, groupRepo GroupRepository) *GroupQualityGateService {
	s := &GroupQualityGateService{policyRepo: policyRepo, accountRepo: accountRepo, groupRepo: groupRepo, processStartedAt: time.Now().UTC(), now: time.Now}
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
	go func() {
		defer close(s.done)
		_ = s.evaluate(ctx)
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
	ejected := ok && member.Result.State == "ejected" && (member.State.Until.IsZero() || s.now().Before(member.State.Until)) && !member.Result.Pinned && !member.Result.GuardHeld
	if !ejected {
		return false
	}
	// Disabling the gate takes effect at once: UpdatePolicy stores the saved
	// policy in the in-memory cache, read here without I/O.
	if cached := s.policyCache.Load(); cached != nil && !cached.value.Groups[strconv.FormatInt(groupID, 10)].Enabled {
		return false
	}
	return true
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
		if member.Result.State == "ejected" && !member.State.Until.IsZero() && !s.now().Before(member.State.Until) {
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
		policy.PinnedAccountIDs = append([]int64{}, policy.PinnedAccountIDs...)
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
	if current.Groups == nil {
		current.Groups = map[string]GroupQualityGatePolicy{}
	}
	current.Groups[strconv.FormatInt(groupID, 10)] = policy
	current.Version++
	updated, err := s.policyRepo.CompareAndSwapPolicies(ctx, expectedVersion, current)
	if err != nil {
		return updated, err
	}
	// Vetoed sees the saved policy at once; a newly-enabled group enters status
	// only after the next regular evaluation.
	s.policyCache.Store(&cachedGroupQualityPolicies{value: normalizeGroupQualityGatePolicies(cloneGroupQualityGatePolicies(updated)), expiresAt: time.Now().Add(30 * time.Second)})
	return updated, nil
}

func (s *GroupQualityGateService) Status(ctx context.Context, groupID *int64) (*GroupQualityGateStatus, error) {
	if s == nil {
		return &GroupQualityGateStatus{Groups: []GroupQualityGroupView{}, AccountMetrics: map[string]GroupQualityMetricView{}}, nil
	}
	snap := s.snapshot.Load()
	if snap == nil {
		snap = &groupQualitySnapshot{GeneratedAt: time.Now().UTC(), Policies: GroupQualityGatePolicies{Groups: map[string]GroupQualityGatePolicy{}}, Metrics: map[int64]GroupQualityMetrics{}, Members: map[int64]map[int64]groupQualityMemberSnapshot{}}
	}
	out := &GroupQualityGateStatus{GeneratedAt: snap.GeneratedAt, Stale: snap.Stale, Groups: []GroupQualityGroupView{}, AccountMetrics: map[string]GroupQualityMetricView{}}
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
		now := s.now().UTC()
		for _, acc := range accounts {
			metric := snap.Metrics[acc.ID]
			out.AccountMetrics[strconv.FormatInt(acc.ID, 10)] = metricView(metric)
			member := snap.Members[group.ID][acc.ID]
			if member.AccountID == 0 {
				member = groupQualityMemberSnapshot{AccountID: acc.ID, Result: GroupQualityMemberResult{Usable: groupMemberUsable(group, &acc, now), Passing: true, State: "eligible", Tier: GroupQualityGateTierFast}}
			}
			effective := member.Metric
			reasons := []string{}
			if enabled {
				if member.Result.State == "ejected" && len(member.State.Reasons) > 0 {
					reasons = append(reasons, member.State.Reasons...)
				} else {
					reasons = groupQualityReasons(policy, member.Metric)
				}
			}
			state, tier := member.Result.State, member.Result.Tier
			passing, guardHeld := member.Result.Passing, member.Result.GuardHeld
			ejected := state == "ejected" && (member.State.Until.IsZero() || now.Before(member.State.Until))
			if state == "ejected" && !ejected {
				state, tier = "trial", GroupQualityGateTierTrial
				// The ejection round's triggering window predates EjectedAt.
				// Until a later evaluation supplies post-ejection data, it cannot
				// determine passing after expiry.
				if !snap.GeneratedAt.After(member.State.EjectedAt) {
					effective = GroupQualityMetrics{}
				}
				passing, guardHeld = !effective.EjectionReady(policy), false
				reasons = []string{}
			}
			vetoed := ejected && !member.Result.Pinned && !member.Result.GuardHeld
			decisionMetrics := qualityDecisionMetrics(effective)
			row := GroupQualityAccountView{AccountID: acc.ID, State: state, Strikes: member.State.Strike, Usable: member.Result.Usable, Passing: passing, GuardHeld: guardHeld, Pinned: member.Result.Pinned, Vetoed: vetoed, Tier: tier, Headroom: qualityHeadroom(policy, decisionMetrics), Metrics: decisionMetrics, Reasons: reasons}
			if ejected && !member.State.Until.IsZero() {
				u := member.State.Until
				row.Until = &u
			}
			gv.Accounts = append(gv.Accounts, row)
			if row.State == "trial" {
				gv.Summary.Trial++
			}
			if row.Vetoed {
				gv.Summary.Ejected++
			} else {
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

func qualityDecisionMetrics(m GroupQualityMetrics) GroupQualityDecisionMetrics {
	out := GroupQualityDecisionMetrics{Calls: m.Calls, SoftFailures: m.SoftFailures, WarmSamples: m.WarmSamples}
	if m.Calls >= 20 && m.SoftFailures >= 5 {
		rate := m.ErrorRate()
		out.ErrorRate = &rate
	}
	if m.WarmSamples >= 10 {
		out.WarmTTFTP90Ms = &m.WarmTTFTP90Ms
	}
	return out
}

func qualityHeadroom(p GroupQualityGatePolicy, m GroupQualityDecisionMetrics) *float64 {
	values := []float64{}
	if m.ErrorRate != nil {
		values = append(values, 1-*m.ErrorRate/p.MaxErrorRate)
	}
	if m.WarmTTFTP90Ms != nil {
		values = append(values, 1-float64(*m.WarmTTFTP90Ms)/float64(p.MaxWarmTTFTP90Ms))
	}
	if len(values) == 0 {
		return nil
	}
	out := values[0]
	for _, v := range values[1:] {
		if v < out {
			out = v
		}
	}
	return &out
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
	// Disabled OpenAI groups still need fast/other ordering tiers.
	groupIDs := make([]int64, 0, len(groups))
	for _, group := range groups {
		if group.Platform == PlatformOpenAI {
			groupIDs = append(groupIDs, group.ID)
		}
	}
	membersByGroup, err := s.policyRepo.LoadMembers(ctx, groupIDs)
	if err != nil {
		s.markFailed(now, err)
		return err
	}
	ids, seen := make([]int64, 0), map[int64]bool{}
	for _, accounts := range membersByGroup {
		for _, account := range accounts {
			if !seen[account.ID] {
				seen[account.ID] = true
				ids = append(ids, account.ID)
			}
		}
	}
	old := s.snapshot.Load()
	observations, observedAt, observationErr := s.policyRepo.LoadObservations(ctx, ids, now.Add(-125*time.Minute), now)
	stale := observationErr != nil || observedAt.Before(now.Add(-5*time.Minute))
	byAccount := make(map[int64][]GroupQualityObservation, len(ids))
	for _, observation := range observations {
		byAccount[observation.AccountID] = append(byAccount[observation.AccountID], observation)
	}
	fullMetrics := make(map[int64]GroupQualityMetrics, len(ids))
	for _, id := range ids {
		if stale {
			fullMetrics[id] = old.Metrics[id]
		} else {
			fullMetrics[id] = groupQualityMetricsFor(byAccount[id], now, now.Add(-15*time.Minute), now.Add(-30*time.Minute), now.Add(-2*time.Hour))
		}
	}
	next := &groupQualitySnapshot{GeneratedAt: now, Stale: stale, Policies: policies, Metrics: fullMetrics, Members: make(map[int64]map[int64]groupQualityMemberSnapshot, len(membersByGroup)), Resets: maps.Clone(old.Resets)}
	if next.Resets == nil {
		next.Resets = map[int64]time.Time{}
	}
	if stale {
		reason := observationErr
		if reason == nil {
			reason = errors.New("usage data timestamp is stale")
		}
		next.StaleSince = qualityFailureSince(old, now, observedAt)
		logGroupQualityFailure(old, now, next.StaleSince, reason)
	}
	for i := range groups {
		group := &groups[i]
		if group.Platform != PlatformOpenAI {
			continue
		}
		policy := normalizeGroupQualityGatePolicy(policies.Groups[strconv.FormatInt(group.ID, 10)])
		accounts := membersByGroup[group.ID]
		byID := make(map[int64]groupQualityMemberSnapshot, len(accounts))
		for i := range accounts {
			account := &accounts[i]
			resetAt := accountGroupQualityResetAt(account)
			previousMember, known := old.Members[group.ID][account.ID]
			// A rejoining member keeps its history unless a reset happened since.
			if departed, ok := old.Departed[group.ID][account.ID]; !known && ok && departed.ResetAt.Equal(resetAt) {
				previousMember.State, known = departed, true
			}
			previous := previousMember.State
			reset := resetAt.After(s.processStartedAt) && !resetAt.Equal(old.Resets[account.ID])
			if resetAt.After(s.processStartedAt) {
				next.Resets[account.ID] = resetAt
			}
			if reset {
				// A member without a previous record has no trial data from
				// before the previous round.
				since := resetAt
				if !known {
					since = maxTime(resetAt, old.GeneratedAt)
				}
				previous = groupQualityAccountState{State: "trial", TrialSince: since, Tier: GroupQualityGateTierTrial}
			}
			previous.ResetAt = resetAt
			pinned := policy.Enabled && containsInt64(policy.PinnedAccountIDs, account.ID)
			usable := groupMemberUsable(group, account, now)
			metric := fullMetrics[account.ID]
			state := previous
			passing := true
			if policy.Enabled {
				errorStart, ttftStart, cacheStart := qualityValidStarts(now, s.processStartedAt, resetAt, previous)
				metric = groupQualityMetricsFor(byAccount[account.ID], now, errorStart, ttftStart, cacheStart)
				if stale && !reset && previous.State != "ejected" {
					metric = previousMember.Metric
				}
				// Count each request and failure once, including late writes. Stale
				// observations cannot eject or promote until usage writes are fresh.
				if previous.State == "trial" && observationErr == nil {
					accumulateQualityTrial(&previous, byAccount[account.ID], now)
				}
				state = evaluateGroupQualityMember(now, policy, metric, previous, !stale)
				passing = state.State != "ejected" && !metric.EjectionReady(policy)
			} else {
				// Disabling the gate ends an ejection now, so its trigger data
				// cannot count again when the gate is re-enabled.
				end := previous.LastEjectionEnd
				if previous.State == "ejected" {
					end = previous.Until
					if now.Before(end) {
						end = now
					}
				}
				state = groupQualityAccountState{State: "eligible", TierAt: previous.TierAt, Strike: previous.Strike, LastEjectionEnd: end, ResetAt: resetAt}
			}
			tier := resolveGroupQualityTier(now, policy, metric, state, previous, policy.Enabled, passing)
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
			logGroupQualityTransition(previousMember.State, state, reset, group.ID, account.ID, policy, metric)
		}
		if policy.Enabled {
			applyQualityGuard(byID, old.Members[group.ID])
			for accountID, member := range byID {
				if member.Result.GuardHeld && !old.Members[group.ID][accountID].Result.GuardHeld {
					slog.Info("group_quality_gate.guard_hold", "group_id", group.ID, "account_id", accountID,
						"severity", member.State.Severity,
						"error_rate", member.Metric.ErrorRate(), "max_error_rate", policy.MaxErrorRate,
						"warm_ttft_p90_ms", member.Metric.WarmTTFTP90Ms, "max_warm_ttft_p90_ms", policy.MaxWarmTTFTP90Ms)
				}
			}
		}
		next.Members[group.ID] = byID
	}
	next.Departed = departedGroupQualityMembers(old, next.Members, now)
	s.snapshot.Store(next)
	return observationErr
}

// departedGroupQualityMembers carries absent members' states while their
// strikes and last ejection end still affect evaluation (two hours).
func departedGroupQualityMembers(old *groupQualitySnapshot, current map[int64]map[int64]groupQualityMemberSnapshot, now time.Time) map[int64]map[int64]groupQualityAccountState {
	out := map[int64]map[int64]groupQualityAccountState{}
	keep := func(groupID, accountID int64, state groupQualityAccountState) {
		if _, present := current[groupID][accountID]; present {
			return
		}
		end := state.LastEjectionEnd
		if state.State == "ejected" {
			end = state.Until
		}
		if end.IsZero() || now.Sub(end) >= 2*time.Hour {
			return
		}
		if out[groupID] == nil {
			out[groupID] = map[int64]groupQualityAccountState{}
		}
		out[groupID][accountID] = state
	}
	for groupID, members := range old.Members {
		for accountID, member := range members {
			keep(groupID, accountID, member.State)
		}
	}
	for groupID, members := range old.Departed {
		for accountID, state := range members {
			keep(groupID, accountID, state)
		}
	}
	return out
}

// accumulateQualityTrial rescans the bounded window to include late writes.
// Saved request and failure identities deduplicate repeated reads and retain
// already-counted observations after they leave the window.
func accumulateQualityTrial(state *groupQualityAccountState, observations []GroupQualityObservation, now time.Time) {
	from := maxTime(state.TrialSince, now.Add(-125*time.Minute))
	events := make([]GroupQualityObservation, 0)
	for _, o := range observations {
		if o.At.After(state.TrialSince) && !o.At.Before(from) && o.At.Before(now) && (o.Success || o.SoftFailure) {
			events = append(events, o)
		}
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].At.Equal(events[j].At) {
			return events[i].RequestID < events[j].RequestID
		}
		return events[i].At.Before(events[j].At)
	})
	state.TrialRequests = slices.Clone(state.TrialRequests)
	state.TrialFailures = slices.Clone(state.TrialFailures)
	for _, event := range events {
		id := event.RequestID
		if id == "" {
			id = "at:" + strconv.FormatInt(event.At.UnixMilli(), 10)
		}
		if !slices.Contains(state.TrialRequests, id) {
			if len(state.TrialRequests) == 20 {
				continue
			}
			state.TrialRequests = append(state.TrialRequests, id)
		}
		failureKey := groupQualityTrialFailure{RequestID: event.RequestID, AccountID: event.AccountID, AtUnixMs: event.At.UnixMilli()}
		if event.SoftFailure && !slices.Contains(state.TrialFailures, failureKey) {
			state.TrialFailures = append(state.TrialFailures, failureKey)
		}
	}
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

func groupQualityMetricsFor(observations []GroupQualityObservation, now, errorSince, ttftSince, cacheSince time.Time) GroupQualityMetrics {
	m := GroupQualityMetrics{}
	seenFailures := map[string]bool{}
	// A usage row of a request that also soft-failed on the same account (a
	// broken stream) is not a successful call. Empty IDs cannot be matched.
	type requestKey struct {
		accountID int64
		requestID string
	}
	failedRequests := map[requestKey]bool{}
	for _, o := range observations {
		if o.SoftFailure && o.RequestID != "" {
			failedRequests[requestKey{o.AccountID, o.RequestID}] = true
		}
	}
	ttftValues := []int{}
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
			} else if o.RequestID == "" || !failedRequests[requestKey{o.AccountID, o.RequestID}] {
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

// logGroupQualityTransition writes one Warn event per state transition, so
// account moves can be traced in the ops system log.
func logGroupQualityTransition(old, next groupQualityAccountState, reset bool, groupID, accountID int64, policy GroupQualityGatePolicy, metric GroupQualityMetrics) {
	from := old.State
	if from == "" {
		from = "eligible"
	}
	var reason string
	switch {
	case !policy.Enabled:
		if from == "eligible" {
			return
		}
		reason = "gate_disabled"
	case next.State == "ejected":
		if old.State == "ejected" && old.Until.Equal(next.Until) {
			return
		}
		reason = strings.Join(next.Reasons, ",")
	case reset:
		reason = "manual_reset"
	case from == next.State:
		return
	case from == "ejected":
		reason = "ejection_expired"
	default:
		reason = "trial_passed"
	}
	attrs := []any{"group_id", groupID, "account_id", accountID, "from", from, "to", next.State, "reason", reason, "strike", next.Strike,
		"error_rate", metric.ErrorRate(), "max_error_rate", policy.MaxErrorRate, "warm_ttft_p90_ms", metric.WarmTTFTP90Ms, "max_warm_ttft_p90_ms", policy.MaxWarmTTFTP90Ms}
	event := "group_quality_gate.recover"
	switch next.State {
	case "ejected":
		event = "group_quality_gate.eject"
		attrs = append(attrs, "until", next.Until)
	case "trial":
		event = "group_quality_gate.trial"
	}
	slog.Warn(event, attrs...)
}

func applyQualityGuard(members, previous map[int64]groupQualityMemberSnapshot) {
	usable, countable, passing := 0, 0, 0
	candidates := make([]int64, 0, len(members))
	for id, m := range members {
		if !m.Result.Usable {
			continue
		}
		usable++
		if m.Result.Pinned && !m.Result.Passing {
			continue
		}
		countable++
		if m.Result.Passing {
			passing++
		} else if m.Result.State == "ejected" {
			candidates = append(candidates, id)
		}
	}
	target := min(max(2, (usable+1)/2), countable)
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if members[a].State.Severity != members[b].State.Severity {
			return members[a].State.Severity < members[b].State.Severity
		}
		if previous[a].Result.GuardHeld != previous[b].Result.GuardHeld {
			return previous[a].Result.GuardHeld
		}
		return a < b
	})
	for _, id := range candidates {
		if passing >= target {
			break
		}
		m := members[id]
		m.Result.GuardHeld = true
		m.Result.Tier = GroupQualityGateTierOther
		members[id] = m
		passing++
	}
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

// Enabled reports whether the latest evaluation ran with the group's gate
// enabled. It reads only the in-process snapshot.
func (s *GroupQualityGateService) Enabled(groupID int64) bool {
	if s == nil {
		return false
	}
	v := s.snapshot.Load()
	return v != nil && v.Policies.Groups[strconv.FormatInt(groupID, 10)].Enabled
}

var _ GroupQualityGateView = (*GroupQualityGateService)(nil)
