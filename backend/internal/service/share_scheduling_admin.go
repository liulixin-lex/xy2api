package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
)

const (
	shareAdminDefaultWindow = time.Hour
	shareAdminMaxWindow     = 24 * time.Hour
	// shareCounterMinuteSpan mirrors the store: longer windows are read from
	// hourly buckets.
	shareCounterMinuteSpan = 2 * time.Hour
	// shareAdminSummaryWindow and shareAdminSummaryTTL: the one-hour
	// summaries of the admin views are reused for 30 seconds, so frequent
	// refreshes of the account and share pages do not rescan the log tables.
	shareAdminSummaryWindow  = time.Hour
	shareAdminSummaryTTL     = 30 * time.Second
	shareAdminSummaryWorkers = 8
	// Preview warnings (design §7.5).
	shareDominantShare  = 0.8
	shareHighLoadFactor = 999

	shareCounterPrecisionMinute = "minute"
	shareCounterPrecisionHour   = "hour_rounded"
)

// Preview warning codes (design §7.5).
const (
	ShareWarningGlobalSwitchOff       = "global_switch_off"
	ShareWarningNoHealthyAccount      = "no_healthy_account"
	ShareWarningTopPriorityUnhealthy  = "top_priority_unhealthy"
	ShareWarningSingleAccountDominant = "single_account_dominant"
	ShareWarningHighLoadFactor        = "high_load_factor"
)

// Report anchor sources and baseline gaps (design §7.7).
const (
	ShareAnchorSwitchRecord    = "switch_record"
	ShareAnchorPolicyUpdatedAt = "policy_updated_at"

	ShareBaselineNotShare       = "not_share"
	ShareBaselineNeverSwitched  = "never_switched"
	ShareBaselineOutOfRetention = "out_of_retention"
)

var (
	ErrShareSchedulingUnavailable   = infraerrors.ServiceUnavailable("SHARE_SCHEDULING_UNAVAILABLE", "share scheduling storage is not available")
	ErrShareSchedulingGroupNotFound = infraerrors.NotFound("SHARE_SCHEDULING_GROUP_NOT_FOUND", "group not found")
)

// shareSchedulingPolicyInvalid reports every policy problem to the editor.
func shareSchedulingPolicyInvalid(err error) error {
	problems := []string{err.Error()}
	var validation *sharesched.ValidationError
	if errors.As(err, &validation) {
		problems = validation.Problems
	}
	return infraerrors.BadRequest("SHARE_SCHEDULING_POLICY_INVALID", err.Error()).
		WithMetadata(map[string]string{"problems": strings.Join(problems, "\n")})
}

// attachGateways gives the admin operations access to the live candidate
// gates of both gateway services.
func (s *ShareSchedulingService) attachGateways(gateway *GatewayService, openai *OpenAIGatewayService) {
	s.gateway, s.openai = gateway, openai
}

// ---- settings ----

// UpdateSettings stores and broadcasts the master switch. The stored
// document keeps the fields of earlier versions, so a rollback reads them
// unchanged.
func (s *ShareSchedulingService) UpdateSettings(ctx context.Context, enabled bool) (ShareSchedulingSettings, error) {
	if s.settings == nil {
		return ShareSchedulingSettings{}, ErrShareSchedulingUnavailable
	}
	s.settingsWriteMu.Lock()
	defer s.settingsWriteMu.Unlock()
	stored, err := s.settings.GetValue(ctx, shareSchedulingSettingsKey)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return ShareSchedulingSettings{}, err
	}
	if err := s.settings.Set(ctx, shareSchedulingSettingsKey, patchShareSettingsDocument(stored, enabled)); err != nil {
		return ShareSchedulingSettings{}, err
	}
	settings := ShareSchedulingSettings{Enabled: enabled}
	s.mu.Lock()
	s.settingsEpoch++
	current := s.state.Load()
	s.state.Store(&shareSchedulingState{settings: settings, groups: current.groups})
	s.mu.Unlock()
	s.publishChange(ctx, shareSchedulingSettingsChange)
	return s.Settings(), nil
}

// patchShareSettingsDocument sets enabled in a stored settings document and
// keeps every other field; a document that no longer decodes is replaced.
func patchShareSettingsDocument(stored string, enabled bool) string {
	doc := map[string]json.RawMessage{}
	if stored != "" {
		if err := json.Unmarshal([]byte(stored), &doc); err != nil || doc == nil {
			doc = map[string]json.RawMessage{}
		}
	}
	doc["enabled"] = json.RawMessage(`false`)
	if enabled {
		doc["enabled"] = json.RawMessage(`true`)
	}
	raw, _ := json.Marshal(doc)
	return string(raw)
}

// ---- summaries cache ----

type shareCached[T any] struct {
	at    time.Time
	value T
}

func (c shareCached[T]) fresh(now time.Time) bool {
	return !c.at.IsZero() && now.Sub(c.at) < shareAdminSummaryTTL
}

// shareAdminCache holds the one-hour summaries of the admin views: the
// usage of every group, the switch loss of each group and the per-account
// requests of each group. Only counts are cached; members, settings, health
// and shares are always read fresh.
type shareAdminCache struct {
	mu        sync.Mutex
	summaries shareCached[map[int64]ShareSchedulingGroupSummary]
	switches  map[int64]shareCached[*float64]
	requests  map[int64]shareCached[map[int64]int64]
}

// groupSummaries returns the one-hour usage of every group.
func (s *ShareSchedulingService) groupSummaries(ctx context.Context, now time.Time) (map[int64]ShareSchedulingGroupSummary, error) {
	s.admin.mu.Lock()
	cached := s.admin.summaries
	s.admin.mu.Unlock()
	if cached.fresh(now) {
		return cached.value, nil
	}
	summaries, err := s.repo.GroupSummaries(ctx, now.Add(-shareAdminSummaryWindow))
	if err != nil {
		return nil, err
	}
	s.admin.mu.Lock()
	s.admin.summaries = shareCached[map[int64]ShareSchedulingGroupSummary]{at: now, value: summaries}
	s.admin.mu.Unlock()
	return summaries, nil
}

// groupSwitchLoss returns the share of attributed prompt tokens lost to
// account switches in the last hour; nil without samples.
func (s *ShareSchedulingService) groupSwitchLoss(ctx context.Context, groupID int64, now time.Time) (*float64, error) {
	s.admin.mu.Lock()
	cached, ok := s.admin.switches[groupID]
	s.admin.mu.Unlock()
	if ok && cached.fresh(now) {
		return cached.value, nil
	}
	counters, err := s.store.ReadCounters(ctx, shareStatsKindCache, groupID, now.Add(-shareAdminSummaryWindow), now)
	if err != nil {
		return nil, err
	}
	loss := newShareAttribution(counters).switchLossRatio()
	s.admin.mu.Lock()
	if s.admin.switches == nil {
		s.admin.switches = map[int64]shareCached[*float64]{}
	}
	s.admin.switches[groupID] = shareCached[*float64]{at: now, value: loss}
	s.admin.mu.Unlock()
	return loss, nil
}

// groupAccountRequests returns the successful requests of each account in
// the group over the last hour.
func (s *ShareSchedulingService) groupAccountRequests(ctx context.Context, groupID int64, now time.Time) (map[int64]int64, error) {
	s.admin.mu.Lock()
	cached, ok := s.admin.requests[groupID]
	s.admin.mu.Unlock()
	if ok && cached.fresh(now) {
		return cached.value, nil
	}
	usage, err := s.repo.AccountUsage(ctx, groupID, now.Add(-shareAdminSummaryWindow), now)
	if err != nil {
		return nil, err
	}
	requests := make(map[int64]int64, len(usage))
	for id, u := range usage {
		requests[id] = u.Requests
	}
	s.admin.mu.Lock()
	if s.admin.requests == nil {
		s.admin.requests = map[int64]shareCached[map[int64]int64]{}
	}
	s.admin.requests[groupID] = shareCached[map[int64]int64]{at: now, value: requests}
	s.admin.mu.Unlock()
	return requests, nil
}

// ---- members and shares ----

// ShareSchedulingMemberView is one group member with its settings, current
// health and shares (design §7.3).
type ShareSchedulingMemberView struct {
	AccountID           int64      `json:"account_id"`
	Name                string     `json:"name"`
	Platform            string     `json:"platform"`
	Type                string     `json:"type"`
	Status              string     `json:"status"`
	Schedulable         bool       `json:"schedulable"`
	Healthy             bool       `json:"healthy"`
	Reason              *string    `json:"reason"`
	RecoverAt           *time.Time `json:"recover_at"`
	Priority            int        `json:"priority"`
	Concurrency         int        `json:"concurrency"`
	LoadFactor          *int       `json:"load_factor"`
	EffectiveLoadFactor int        `json:"effective_load_factor"`
	OtherGroupIDs       []int64    `json:"other_group_ids"`
	ExpectedShare       float64    `json:"expected_share"`
	ShareIfHealthy      float64    `json:"share_if_healthy"`
	ActualRequests1h    int64      `json:"actual_requests_1h"`
	ActualShare1h       *float64   `json:"actual_share_1h"`
}

// shareMembers lists every member of the group that is not deleted,
// whatever its status, read from the database so that a changed load factor
// shows at once.
func (s *ShareSchedulingService) shareMembers(ctx context.Context, groupID int64) ([]Account, error) {
	if s.gateway == nil || s.gateway.accountRepo == nil {
		return nil, ErrShareSchedulingUnavailable
	}
	return s.gateway.accountRepo.ListAllWithFilters(ctx, "", "", "", "", groupID, "")
}

// shareMember is the input of the share computation.
type shareMember struct {
	priority int
	weight   float64
	healthy  bool
}

// shareShares computes each member's expected share of new sessions and its
// share if it alone recovered (design §7.3): the healthy members of the
// lowest priority with a healthy member split new sessions by weight; every
// other member gets none. active is that priority, nil when no member is
// healthy.
func shareShares(members []shareMember) (expected, ifHealthy []float64, active *int) {
	totals := map[int]float64{}
	for _, m := range members {
		if !m.healthy {
			continue
		}
		totals[m.priority] += m.weight
		if active == nil || m.priority < *active {
			p := m.priority
			active = &p
		}
	}
	expected = make([]float64, len(members))
	ifHealthy = make([]float64, len(members))
	for i, m := range members {
		switch {
		case m.healthy:
			if m.priority == *active {
				expected[i] = m.weight / totals[m.priority]
			}
			ifHealthy[i] = expected[i]
		case active == nil || m.priority < *active:
			ifHealthy[i] = 1
		case m.priority == *active:
			ifHealthy[i] = m.weight / (totals[m.priority] + m.weight)
		}
	}
	return expected, ifHealthy, active
}

// shareMemberViews builds the member views of a group, sorted by priority
// and account id. requests are the members' successful requests in the
// group over the last hour (nil when unknown).
func shareMemberViews(groupID int64, accounts []Account, requests map[int64]int64, now time.Time) ([]ShareSchedulingMemberView, *int) {
	sorted := slices.Clone(accounts)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Priority != sorted[j].Priority {
			return sorted[i].Priority < sorted[j].Priority
		}
		return sorted[i].ID < sorted[j].ID
	})
	members := make([]shareMember, len(sorted))
	views := make([]ShareSchedulingMemberView, len(sorted))
	var total int64
	for i := range sorted {
		acc := &sorted[i]
		v := ShareSchedulingMemberView{AccountID: acc.ID, Name: acc.Name, Platform: acc.Platform, Type: acc.Type,
			Status: acc.Status, Schedulable: acc.Schedulable, Healthy: acc.IsSchedulable(), Priority: acc.Priority,
			Concurrency: acc.Concurrency, EffectiveLoadFactor: shareWeight(acc), OtherGroupIDs: shareOtherGroups(acc, groupID),
			ActualRequests1h: requests[acc.ID]}
		if acc.LoadFactor != nil {
			lf := *acc.LoadFactor
			v.LoadFactor = &lf
		}
		if !v.Healthy {
			reason, recoverAt := shareAccountStateReason(acc, now)
			r := string(reason)
			v.Reason = &r
			if !recoverAt.IsZero() {
				v.RecoverAt = &recoverAt
			}
		}
		total += v.ActualRequests1h
		views[i] = v
		members[i] = shareMember{priority: acc.Priority, weight: float64(v.EffectiveLoadFactor), healthy: v.Healthy}
	}
	expected, ifHealthy, active := shareShares(members)
	for i := range views {
		views[i].ExpectedShare, views[i].ShareIfHealthy = expected[i], ifHealthy[i]
		if total > 0 {
			share := float64(views[i].ActualRequests1h) / float64(total)
			views[i].ActualShare1h = &share
		}
	}
	return views, active
}

// shareOtherGroups lists the account's other groups, sorted.
func shareOtherGroups(acc *Account, groupID int64) []int64 {
	ids := acc.GroupIDs
	if len(ids) == 0 {
		for _, ag := range acc.AccountGroups {
			ids = append(ids, ag.GroupID)
		}
	}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id != groupID && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// groupMemberViews reads the members of a group with their one-hour
// requests; a failed requests read leaves them at zero.
func (s *ShareSchedulingService) groupMemberViews(ctx context.Context, groupID int64) ([]ShareSchedulingMemberView, *int, error) {
	accounts, err := s.shareMembers(ctx, groupID)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	requests, err := s.groupAccountRequests(ctx, groupID, now)
	if err != nil {
		slog.Warn("share_scheduling.member_requests_failed", "group_id", groupID, "error", err)
	}
	views, active := shareMemberViews(groupID, accounts, requests, now)
	return views, active, nil
}

// ---- stored policies ----

// shareStoredPolicy is a group's stored policy row and its decoded form.
type shareStoredPolicy struct {
	row    *ShareSchedulingPolicyRow
	policy sharesched.Policy
	// invalid is why a stored row no longer decodes; policy then holds the
	// defaults, which is also what the request path uses.
	invalid error
}

func newShareStoredPolicy(row *ShareSchedulingPolicyRow) shareStoredPolicy {
	stored := shareStoredPolicy{row: row, policy: sharesched.DefaultPolicy()}
	if row == nil {
		return stored
	}
	if policy, err := sharesched.DecodePolicy(row.Policy); err != nil {
		stored.invalid = err
	} else {
		stored.policy = policy
	}
	return stored
}

// scheduler is the scheduler the group runs when the master switch is on.
func (p shareStoredPolicy) scheduler() sharesched.Scheduler { return p.policy.Effective() }

// legacyShadow reports a stored shadow scheduler of 0.3.0–0.4.3.
func (p shareStoredPolicy) legacyShadow() bool {
	return p.invalid == nil && p.policy.Scheduler == sharesched.SchedulerShadow
}

func (p shareStoredPolicy) version() int64 {
	if p.row == nil {
		return 0
	}
	return p.row.Version
}

func (p shareStoredPolicy) updatedAt() *time.Time {
	if p.row == nil {
		return nil
	}
	at := p.row.UpdatedAt
	return &at
}

// storedPolicy returns the group's stored policy, or the defaults (scheduler
// sub2api) when none is stored or the stored one no longer decodes.
func (s *ShareSchedulingService) storedPolicy(ctx context.Context, groupID int64) (shareStoredPolicy, error) {
	row, err := s.repo.GetPolicy(ctx, groupID)
	if err != nil {
		return shareStoredPolicy{}, err
	}
	return newShareStoredPolicy(row), nil
}

func (s *ShareSchedulingService) shareGroup(ctx context.Context, groupID int64) (*Group, error) {
	if s.gateway == nil || s.gateway.groupRepo == nil || s.repo == nil {
		return nil, ErrShareSchedulingUnavailable
	}
	group, err := s.gateway.groupRepo.GetByIDLite(ctx, groupID)
	if err != nil || group == nil {
		return nil, ErrShareSchedulingGroupNotFound
	}
	return group, nil
}

// ---- groups ----

// ShareSchedulingGroupView summarises one group for the list (design §7.2).
type ShareSchedulingGroupView struct {
	GroupID           int64      `json:"group_id"`
	Name              string     `json:"name"`
	Platform          string     `json:"platform"`
	Scheduler         string     `json:"scheduler"`
	SessionKeep       bool       `json:"session_keep"`
	LegacyShadow      bool       `json:"legacy_shadow"`
	Configured        bool       `json:"configured"`
	Invalid           bool       `json:"invalid"`
	Effective         bool       `json:"effective"`
	Version           int64      `json:"version"`
	UpdatedAt         *time.Time `json:"updated_at"`
	MemberCount       int64      `json:"member_count"`
	AvailableCount    int64      `json:"available_count"`
	Requests1h        int64      `json:"requests_1h"`
	CacheHitRatio1h   *float64   `json:"cache_hit_ratio_1h"`
	SwitchLossRatio1h *float64   `json:"switch_loss_ratio_1h"`
}

// ListGroups returns every active group with its scheduler and a one-hour
// summary. The summaries are best effort: a failed read leaves them empty.
func (s *ShareSchedulingService) ListGroups(ctx context.Context) ([]ShareSchedulingGroupView, error) {
	if s.gateway == nil || s.gateway.groupRepo == nil || s.repo == nil {
		return nil, ErrShareSchedulingUnavailable
	}
	groups, err := s.gateway.groupRepo.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.ListPolicies(ctx)
	if err != nil {
		return nil, err
	}
	byGroup := make(map[int64]*ShareSchedulingPolicyRow, len(rows))
	for i := range rows {
		byGroup[rows[i].GroupID] = &rows[i]
	}
	now := time.Now()
	summaries, err := s.groupSummaries(ctx, now)
	if err != nil {
		slog.Warn("share_scheduling.group_summaries_failed", "error", err)
	}
	views := make([]ShareSchedulingGroupView, len(groups))
	for i := range groups {
		g := &groups[i]
		stored := newShareStoredPolicy(byGroup[g.ID])
		summary := summaries[g.ID]
		views[i] = ShareSchedulingGroupView{GroupID: g.ID, Name: g.Name, Platform: g.Platform,
			Scheduler: string(stored.scheduler()), SessionKeep: stored.policy.SessionKeep(), LegacyShadow: stored.legacyShadow(),
			Configured: stored.row != nil, Invalid: stored.invalid != nil, Effective: s.groupPolicy(g.ID) != nil,
			Version: stored.version(), UpdatedAt: stored.updatedAt(), MemberCount: g.AccountCount, AvailableCount: g.ActiveAccountCount,
			Requests1h: summary.Requests, CacheHitRatio1h: shareRatio(summary.CacheReadTokens, summary.PromptTokens)}
	}
	s.fillSwitchLoss(ctx, views, now)
	return views, nil
}

// fillSwitchLoss reads the switch loss of every group with bounded
// concurrency; a failed read leaves it empty.
func (s *ShareSchedulingService) fillSwitchLoss(ctx context.Context, views []ShareSchedulingGroupView, now time.Time) {
	if s.store == nil {
		return
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(shareAdminSummaryWorkers, len(views)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if loss, err := s.groupSwitchLoss(ctx, views[i].GroupID, now); err == nil {
					views[i].SwitchLossRatio1h = loss
				}
			}
		}()
	}
	for i := range views {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
}

// shareRatio is part over total, nil when total is not positive.
func shareRatio(part, total int64) *float64 {
	if total <= 0 {
		return nil
	}
	r := float64(part) / float64(total)
	return &r
}

// ---- policy ----

// ShareSchedulingPolicyView is a group's settings with its members (design
// §7.3).
type ShareSchedulingPolicyView struct {
	GroupID        int64                       `json:"group_id"`
	GroupName      string                      `json:"group_name"`
	Platform       string                      `json:"platform"`
	Scheduler      string                      `json:"scheduler"`
	SessionKeep    bool                        `json:"session_keep"`
	LegacyShadow   bool                        `json:"legacy_shadow"`
	Effective      bool                        `json:"effective"`
	Version        int64                       `json:"version"`
	UpdatedBy      *int64                      `json:"updated_by"`
	UpdatedAt      *time.Time                  `json:"updated_at"`
	Enabled        bool                        `json:"enabled"`
	Invalid        *string                     `json:"invalid"`
	ActivePriority *int                        `json:"active_priority"`
	Members        []ShareSchedulingMemberView `json:"members"`
}

// GetPolicy returns the group's settings and members.
func (s *ShareSchedulingService) GetPolicy(ctx context.Context, groupID int64) (*ShareSchedulingPolicyView, error) {
	group, err := s.shareGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}
	stored, err := s.storedPolicy(ctx, groupID)
	if err != nil {
		return nil, err
	}
	return s.policyView(ctx, group, stored)
}

func (s *ShareSchedulingService) policyView(ctx context.Context, group *Group, stored shareStoredPolicy) (*ShareSchedulingPolicyView, error) {
	members, active, err := s.groupMemberViews(ctx, group.ID)
	if err != nil {
		return nil, err
	}
	view := &ShareSchedulingPolicyView{GroupID: group.ID, GroupName: group.Name, Platform: group.Platform,
		Scheduler: string(stored.scheduler()), SessionKeep: stored.policy.SessionKeep(), LegacyShadow: stored.legacyShadow(),
		Effective: s.groupPolicy(group.ID) != nil, Version: stored.version(), UpdatedAt: stored.updatedAt(),
		Enabled: s.Settings().Enabled, ActivePriority: active, Members: members}
	if stored.row != nil {
		view.UpdatedBy = stored.row.UpdatedBy
	}
	// An undecodable row is skipped by the request path, so the group runs
	// the original scheduler; saving replaces the row.
	if stored.invalid != nil {
		reason := stored.invalid.Error()
		view.Invalid = &reason
	}
	return view, nil
}

// ShareSchedulingPolicyUpdate is a validated save or preview request.
type ShareSchedulingPolicyUpdate struct {
	Scheduler   sharesched.Scheduler
	SessionKeep bool
}

// ParseSharePolicyUpdate validates the scheduler and session keeping of a
// save or preview request (design §7.4). legacy is the policy document an
// admin page of 0.3.0–0.4.3 submits instead; only its scheduler and
// affinity are read, and a shadow scheduler is taken as the original one.
func ParseSharePolicyUpdate(scheduler *string, sessionKeep *bool, legacy json.RawMessage) (ShareSchedulingPolicyUpdate, error) {
	if len(legacy) > 0 && string(legacy) != "null" {
		if scheduler != nil || sessionKeep != nil {
			return ShareSchedulingPolicyUpdate{}, infraerrors.BadRequest("SHARE_SCHEDULING_POLICY_INVALID",
				"send either scheduler and session_keep or policy")
		}
		policy, err := sharesched.DecodePolicy(legacy)
		if err != nil {
			return ShareSchedulingPolicyUpdate{}, shareSchedulingPolicyInvalid(err)
		}
		return ShareSchedulingPolicyUpdate{Scheduler: policy.Effective(), SessionKeep: policy.SessionKeep()}, nil
	}
	if scheduler == nil || sessionKeep == nil {
		return ShareSchedulingPolicyUpdate{}, infraerrors.BadRequest("SHARE_SCHEDULING_POLICY_INVALID",
			"scheduler and session_keep are required")
	}
	switch sharesched.Scheduler(*scheduler) {
	case sharesched.SchedulerSub2API, sharesched.SchedulerShare:
	default:
		return ShareSchedulingPolicyUpdate{}, infraerrors.BadRequest("SHARE_SCHEDULING_POLICY_INVALID",
			"scheduler must be sub2api or share").WithMetadata(map[string]string{"problems": "scheduler must be sub2api or share"})
	}
	return ShareSchedulingPolicyUpdate{Scheduler: sharesched.Scheduler(*scheduler), SessionKeep: *sessionKeep}, nil
}

// SavePolicy stores the scheduler and session keeping when the stored
// version still equals expectedVersion, then broadcasts the change. Every
// other stored field is kept as it is (design §3.2). A scheduler change
// also records the switch anchor of the report (design §7.7).
func (s *ShareSchedulingService) SavePolicy(ctx context.Context, groupID, expectedVersion int64, update ShareSchedulingPolicyUpdate, updatedBy int64) (*ShareSchedulingPolicyView, error) {
	group, err := s.shareGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}
	previous, err := s.storedPolicy(ctx, groupID)
	if err != nil {
		return nil, err
	}
	var current []byte
	if previous.row != nil {
		current = previous.row.Policy
	}
	document, err := sharesched.PatchPolicyDocument(current, update.Scheduler, update.SessionKeep)
	if err != nil {
		return nil, shareSchedulingPolicyInvalid(err)
	}
	policy, err := sharesched.DecodePolicy(document)
	if err != nil {
		return nil, err
	}
	row, err := s.repo.SavePolicy(ctx, groupID, expectedVersion, string(policy.Effective()), document, updatedBy)
	if err != nil {
		return nil, err
	}
	if policy.Effective() != previous.scheduler() && s.store != nil {
		// Best effort: without the record the report falls back to the
		// policy's update time.
		if err := s.store.RecordSwitch(ctx, groupID, row.UpdatedAt); err != nil {
			slog.Warn("share_scheduling.switch_record_failed", "group_id", groupID, "error", err)
		}
	}
	// The committed row is authoritative: publish it without reading it back,
	// so a failing read cannot report a committed save as failed.
	s.installGroup(row)
	s.publishChange(ctx, groupID)
	return s.policyView(ctx, group, shareStoredPolicy{row: row, policy: policy})
}

// ---- account rules ----

// ShareSchedulingAccountRule is an account's place in one group that stores
// the share scheduler (design §7.8).
type ShareSchedulingAccountRule struct {
	GroupID             int64   `json:"group_id"`
	GroupName           string  `json:"group_name"`
	Scheduler           string  `json:"scheduler"`
	Effective           bool    `json:"effective"`
	Priority            int     `json:"priority"`
	LoadFactor          *int    `json:"load_factor"`
	EffectiveLoadFactor int     `json:"effective_load_factor"`
	Healthy             bool    `json:"healthy"`
	ExpectedShare       float64 `json:"expected_share"`
	ShareIfHealthy      float64 `json:"share_if_healthy"`
}

// AccountRules lists the account's expected shares in its groups that store
// the share scheduler, sorted by group id. Groups on the original scheduler
// (including stored shadow and undecodable policies) are left out.
func (s *ShareSchedulingService) AccountRules(ctx context.Context, accountID int64) ([]ShareSchedulingAccountRule, error) {
	if s.gateway == nil || s.gateway.accountRepo == nil || s.gateway.groupRepo == nil || s.repo == nil {
		return nil, ErrShareSchedulingUnavailable
	}
	account, err := s.gateway.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	out := []ShareSchedulingAccountRule{}
	groupIDs := shareOtherGroups(account, 0)
	if len(groupIDs) == 0 {
		return out, nil
	}
	rows, err := s.repo.ListPolicies(ctx)
	if err != nil {
		return nil, err
	}
	byGroup := make(map[int64]*ShareSchedulingPolicyRow, len(rows))
	for i := range rows {
		byGroup[rows[i].GroupID] = &rows[i]
	}
	now := time.Now()
	for _, groupID := range groupIDs {
		stored := newShareStoredPolicy(byGroup[groupID])
		if stored.row == nil || stored.scheduler() != sharesched.SchedulerShare {
			continue
		}
		accounts, err := s.shareMembers(ctx, groupID)
		if err != nil {
			return nil, err
		}
		views, _ := shareMemberViews(groupID, accounts, nil, now)
		for _, v := range views {
			if v.AccountID != accountID {
				continue
			}
			rule := ShareSchedulingAccountRule{GroupID: groupID, Scheduler: string(sharesched.SchedulerShare),
				Effective: s.groupPolicy(groupID) != nil, Priority: v.Priority, LoadFactor: v.LoadFactor,
				EffectiveLoadFactor: v.EffectiveLoadFactor, Healthy: v.Healthy, ExpectedShare: v.ExpectedShare,
				ShareIfHealthy: v.ShareIfHealthy}
			if group, err := s.gateway.groupRepo.GetByIDLite(ctx, groupID); err == nil && group != nil {
				rule.GroupName = group.Name
			}
			out = append(out, rule)
		}
	}
	return out, nil
}

// ---- preview ----

// ShareSchedulingWarning is one risk found by a preview (design §7.5).
type ShareSchedulingWarning struct {
	Code      string   `json:"code"`
	AccountID *int64   `json:"account_id"`
	Priority  *int     `json:"priority"`
	Value     *float64 `json:"value"`
}

// ShareSchedulingPreview is the side-effect-free evaluation of a proposed
// scheduler and session keeping against the group's current members.
type ShareSchedulingPreview struct {
	Scheduler      string                      `json:"scheduler"`
	SessionKeep    bool                        `json:"session_keep"`
	Enabled        bool                        `json:"enabled"`
	EffectiveAfter bool                        `json:"effective_after"`
	ActivePriority *int                        `json:"active_priority"`
	Members        []ShareSchedulingMemberView `json:"members"`
	Warnings       []ShareSchedulingWarning    `json:"warnings"`
}

// Preview evaluates a proposed scheduler and session keeping without
// saving them.
func (s *ShareSchedulingService) Preview(ctx context.Context, groupID int64, update ShareSchedulingPolicyUpdate) (*ShareSchedulingPreview, error) {
	if _, err := s.shareGroup(ctx, groupID); err != nil {
		return nil, err
	}
	members, active, err := s.groupMemberViews(ctx, groupID)
	if err != nil {
		return nil, err
	}
	enabled := s.Settings().Enabled
	out := &ShareSchedulingPreview{Scheduler: string(update.Scheduler), SessionKeep: update.SessionKeep, Enabled: enabled,
		EffectiveAfter: enabled && update.Scheduler == sharesched.SchedulerShare, ActivePriority: active, Members: members,
		Warnings: []ShareSchedulingWarning{}}
	if update.Scheduler == sharesched.SchedulerShare {
		out.Warnings = sharePreviewWarnings(members, active, enabled)
	}
	return out, nil
}

func sharePreviewWarnings(members []ShareSchedulingMemberView, active *int, enabled bool) []ShareSchedulingWarning {
	warnings := []ShareSchedulingWarning{}
	if !enabled {
		warnings = append(warnings, ShareSchedulingWarning{Code: ShareWarningGlobalSwitchOff})
	}
	if active == nil {
		warnings = append(warnings, ShareSchedulingWarning{Code: ShareWarningNoHealthyAccount})
	} else if len(members) > 0 && members[0].Priority < *active {
		// Members are sorted by priority: the first is the top priority.
		top, current := members[0].Priority, float64(*active)
		warnings = append(warnings, ShareSchedulingWarning{Code: ShareWarningTopPriorityUnhealthy, Priority: &top, Value: &current})
	}
	healthyActive := 0
	for _, m := range members {
		if active != nil && m.Healthy && m.Priority == *active {
			healthyActive++
		}
	}
	for _, m := range members {
		if healthyActive >= 2 && m.ExpectedShare > shareDominantShare {
			id, share := m.AccountID, m.ExpectedShare
			warnings = append(warnings, ShareSchedulingWarning{Code: ShareWarningSingleAccountDominant, AccountID: &id, Value: &share})
		}
	}
	for _, m := range members {
		if m.EffectiveLoadFactor >= shareHighLoadFactor {
			id, lf := m.AccountID, float64(m.EffectiveLoadFactor)
			warnings = append(warnings, ShareSchedulingWarning{Code: ShareWarningHighLoadFactor, AccountID: &id, Value: &lf})
		}
	}
	return warnings
}

// ---- explain ----

// ShareSchedulingExplainInput describes a request to explain.
type ShareSchedulingExplainInput struct {
	// Model may be empty: no model filter.
	Model string `json:"model"`
	// SessionHash is the session key as stored in the sticky binding; empty
	// simulates a new session.
	SessionHash     string          `json:"session_hash"`
	EstimatedTokens int             `json:"estimated_tokens"`
	CacheOneHour    bool            `json:"cache_one_hour"`
	Body            json.RawMessage `json:"body,omitempty"`
}

// ShareSchedulingExplainEntry is one member's evaluation with its load.
type ShareSchedulingExplainEntry struct {
	sharesched.Entry
	// LoadRate is (in flight + waiting) × 100 ÷ load factor; nil when it
	// could not be read.
	LoadRate  *int `json:"load_rate"`
	Saturated bool `json:"saturated"`
}

// ShareSchedulingExplanation is the full plan of one request (design §7.6).
// Explaining never takes a slot or writes a binding.
type ShareSchedulingExplanation struct {
	GroupID             int64                         `json:"group_id"`
	Platform            string                        `json:"platform"`
	Scheduler           string                        `json:"scheduler"`
	Version             int64                         `json:"version"`
	SimulatedNewSession bool                          `json:"simulated_new_session"`
	KeySource           string                        `json:"key_source"`
	Session             string                        `json:"session,omitempty"`
	BoundAccountID      int64                         `json:"bound_account_id,omitempty"`
	IdleMs              int64                         `json:"idle_ms"`
	CacheTTLSeconds     int                           `json:"cache_ttl_seconds"`
	EstimatedTokens     int                           `json:"estimated_tokens"`
	Cacheable           bool                          `json:"cacheable"`
	Warm                bool                          `json:"warm"`
	WaitBudgetMs        int64                         `json:"wait_budget_ms"`
	MaxWaiting          int                           `json:"max_waiting,omitempty"`
	Reason              string                        `json:"reason"`
	Steps               []int64                       `json:"steps"`
	Write               string                        `json:"write"`
	SpillWrite          string                        `json:"spill_write"`
	SpillReason         string                        `json:"spill_reason,omitempty"`
	Target              int64                         `json:"target,omitempty"`
	Ideal               int64                         `json:"ideal,omitempty"`
	ActivePriority      *int                          `json:"active_priority"`
	Entries             []ShareSchedulingExplainEntry `json:"entries"`
	// Resilience is the gateway-resilience view of the group and session
	// (penalties, failures, avoid set, last resort, fallback).
	Resilience *ResilienceExplanation `json:"resilience,omitempty"`
}

func shareWriteName(w sharesched.Write) string {
	switch w {
	case sharesched.WriteRefresh:
		return "refresh"
	case sharesched.WriteIfAbsent:
		return "if_absent"
	case sharesched.WriteReplace:
		return "replace"
	default:
		return "none"
	}
}

// shareSimulatedSession is a random session key: explaining it plans a new
// session.
func shareSimulatedSession() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "explain-" + hex.EncodeToString(b[:])
}

// Explain plans one request with the share rules (also while the group runs
// the original scheduler) against the live candidates of the group.
func (s *ShareSchedulingService) Explain(ctx context.Context, groupID int64, in ShareSchedulingExplainInput) (*ShareSchedulingExplanation, error) {
	group, err := s.shareGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}
	stored, err := s.storedPolicy(ctx, groupID)
	if err != nil {
		return nil, err
	}
	policy := stored.policy
	gp := &shareGroupPolicy{groupID: groupID, version: stored.version(), policy: policy}
	switch {
	case len(in.Body) > 0:
		ctx = withShareRequestProfile(ctx, in.Body)
	case in.EstimatedTokens > 0:
		ctx = context.WithValue(ctx, shareRequestProfileKey{}, shareRequestProfile{
			bodyBytes: in.EstimatedTokens * inflightInputBytesPerTokenEstimate, cacheOneHour: in.CacheOneHour})
	}
	sessionHash, simulated := in.SessionHash, false
	if sessionHash == "" {
		sessionHash, simulated = shareSimulatedSession(), true
	}
	key, _ := shareAffinityKey(&policy, sessionHash)
	gid := groupID
	var (
		platform    string
		cands       []sharesched.Candidate
		accounts    map[int64]*Account
		binding     sharesched.Binding
		concurrency *ConcurrencyService
	)
	if (&Account{Platform: group.Platform}).IsOpenAICompatible() && s.openai != nil {
		sel := openAIShareSelection{groupID: &gid, sessionHash: sessionHash, model: in.Model,
			transport: OpenAIUpstreamTransportAny, platform: NormalizeOpenAICompatiblePlatform(group.Platform)}
		cacheKey := ""
		if key != "" {
			cacheKey = s.openai.openAISessionCacheKey(sessionHash)
		}
		binding, _ = s.openai.readOpenAIShareBinding(ctx, gp, cacheKey, sessionHash)
		if cands, accounts, _, err = s.openai.shareOpenAICandidates(ctx, sel, binding.AccountID); err != nil {
			return nil, err
		}
		platform, concurrency = sel.platform, s.openai.concurrencyService
	} else if s.gateway != nil {
		binding = s.readBinding(ctx, s.gateway.cache, gp, key)
		var gin *shareGatewayInputs
		if ctx, gin, err = s.gateway.shareGatewayCandidates(ctx, group, &gid, in.Model, nil, binding.AccountID); err != nil {
			return nil, err
		}
		cands, accounts, platform, concurrency = gin.cands, gin.accounts, gin.platform, s.gateway.concurrencyService
	} else {
		return nil, ErrShareSchedulingUnavailable
	}
	req, source := s.newRequest(ctx, gp, platform, in.Model, sessionHash, binding)
	d := sharesched.Plan(req, cands)
	out := &ShareSchedulingExplanation{GroupID: groupID, Platform: platform, Scheduler: string(stored.scheduler()), Version: gp.version,
		SimulatedNewSession: simulated, KeySource: string(source), Session: shareSessionFingerprint(key), BoundAccountID: req.Binding.AccountID,
		IdleMs: req.Binding.Idle.Milliseconds(), CacheTTLSeconds: int(req.Cache.TTL / time.Second),
		EstimatedTokens: req.Cache.EstimatedTokens, Cacheable: req.Cache.Cacheable, Warm: req.Cache.Warm(req.Binding.Idle),
		WaitBudgetMs: d.Wait.Milliseconds(), Reason: string(d.Reason), Steps: make([]int64, len(d.Steps)),
		Write: shareWriteName(d.Write), SpillWrite: shareWriteName(d.Spill), SpillReason: string(d.SpillReason),
		MaxWaiting: d.MaxWaiting, Target: d.Target, Ideal: d.Ideal, Entries: shareExplainEntries(ctx, concurrency, d.Entries, accounts)}
	if d.Target > 0 {
		active := d.ActivePriority
		out.ActivePriority = &active
	}
	for i, step := range d.Steps {
		out.Steps[i] = step.AccountID
	}
	if r := Resilience(); r != nil {
		out.Resilience, _ = r.Explain(ctx, groupID, in.SessionHash, in.Model)
	}
	return out, nil
}

// shareExplainEntries adds the load rate of design §5 to the entries; a
// failed load read leaves it nil.
func shareExplainEntries(ctx context.Context, concurrency *ConcurrencyService, entries []sharesched.Entry, accounts map[int64]*Account) []ShareSchedulingExplainEntry {
	out := make([]ShareSchedulingExplainEntry, len(entries))
	batch := make([]AccountWithConcurrency, 0, len(entries))
	for i, e := range entries {
		out[i].Entry = e
		if acc := accounts[e.AccountID]; acc != nil {
			batch = append(batch, AccountWithConcurrency{ID: acc.ID, MaxConcurrency: acc.EffectiveLoadFactor()})
		}
	}
	if concurrency == nil || len(batch) == 0 {
		return out
	}
	loads, err := concurrency.GetAccountsLoadBatch(ctx, batch)
	if err != nil {
		return out
	}
	for i := range out {
		if info := loads[out[i].AccountID]; info != nil {
			rate := info.LoadRate
			out[i].LoadRate, out[i].Saturated = &rate, rate >= 100
		}
	}
	return out
}

// ---- report ----

// ShareSchedulingMetrics are the health figures of one window (design §7.7).
type ShareSchedulingMetrics struct {
	Requests           int64    `json:"requests"`
	CacheHitRatio      *float64 `json:"cache_hit_ratio"`
	TTFTP50Ms          *int     `json:"ttft_p50_ms"`
	TTFTP95Ms          *int     `json:"ttft_p95_ms"`
	TTFTSamples        int64    `json:"ttft_samples"`
	Errors             int64    `json:"errors"`
	ErrorRate          *float64 `json:"error_rate"`
	AttributionSamples int64    `json:"attribution_samples"`
	SwitchRate         *float64 `json:"switch_rate"`
	SwitchLossRatio    *float64 `json:"switch_loss_ratio"`
}

// ShareSchedulingReportAccount is an account's traffic in the current
// window.
type ShareSchedulingReportAccount struct {
	AccountID    int64   `json:"account_id"`
	Requests     int64   `json:"requests"`
	RequestShare float64 `json:"request_share"`
	Tokens       int64   `json:"tokens"`
	TokenShare   float64 `json:"token_share"`
	StandardCost float64 `json:"standard_cost"`
	CostShare    float64 `json:"cost_share"`
	// Skipped counts the times the account was tried but passed over, by
	// reason (saturated, session limit, ...); share mode only.
	Skipped map[string]int64 `json:"skipped,omitempty"`
}

// ShareSchedulingCacheClass is one cache attribution class.
type ShareSchedulingCacheClass struct {
	Requests int64 `json:"requests"`
	Tokens   int64 `json:"tokens"`
}

// ShareSchedulingWaits are the bounded waits for bound accounts.
type ShareSchedulingWaits struct {
	Count int64    `json:"count"`
	Hit   int64    `json:"hit"`
	AvgMs *float64 `json:"avg_ms"`
}

// ShareSchedulingHotMigrations counts sessions that share scheduling moved
// off an account whose cache was still warm, by decision reason.
type ShareSchedulingHotMigrations struct {
	Total    int64            `json:"total"`
	ByReason map[string]int64 `json:"by_reason"`
}

// ShareSchedulingReportDetails are the counters of the current window.
// CacheClasses exist in both modes; the rest only in share mode.
type ShareSchedulingReportDetails struct {
	CacheClasses  map[string]ShareSchedulingCacheClass `json:"cache_classes"`
	Reasons       map[string]int64                     `json:"reasons,omitempty"`
	Waits         *ShareSchedulingWaits                `json:"waits,omitempty"`
	Attempts      *int64                               `json:"attempts,omitempty"`
	ShareRequests *int64                               `json:"share_requests,omitempty"`
	HotMigrations *ShareSchedulingHotMigrations        `json:"hot_migrations,omitempty"`
}

// ShareSchedulingReport compares a window with the window before the
// switch to share scheduling (design §7.7).
type ShareSchedulingReport struct {
	GroupID             int64                          `json:"group_id"`
	Scheduler           string                         `json:"scheduler"`
	SessionKeep         bool                           `json:"session_keep"`
	Version             int64                          `json:"version"`
	WindowSeconds       int64                          `json:"window_seconds"`
	CurrentFrom         time.Time                      `json:"current_from"`
	CurrentTo           time.Time                      `json:"current_to"`
	Current             ShareSchedulingMetrics         `json:"current"`
	SwitchedAt          *time.Time                     `json:"switched_at"`
	AnchorSource        *string                        `json:"anchor_source"`
	BaselineFrom        *time.Time                     `json:"baseline_from"`
	BaselineTo          *time.Time                     `json:"baseline_to"`
	Baseline            *ShareSchedulingMetrics        `json:"baseline"`
	BaselineUnavailable *string                        `json:"baseline_unavailable"`
	Accounts            []ShareSchedulingReportAccount `json:"accounts"`
	Details             ShareSchedulingReportDetails   `json:"details"`
	CountersError       *string                        `json:"counters_error"`
	CounterPrecision    string                         `json:"counter_precision"`
}

// shareAnchor is the time of the group's last scheduler switch.
func (s *ShareSchedulingService) shareAnchor(ctx context.Context, groupID int64, stored shareStoredPolicy) (time.Time, string) {
	if s.store != nil {
		at, err := s.store.SwitchedAt(ctx, groupID)
		if err != nil {
			slog.Warn("share_scheduling.switch_read_failed", "group_id", groupID, "error", err)
		} else if !at.IsZero() {
			return at, ShareAnchorSwitchRecord
		}
	}
	if stored.row != nil {
		return stored.row.UpdatedAt, ShareAnchorPolicyUpdatedAt
	}
	return time.Time{}, ""
}

// Report builds the group's report over the window (default one hour, at
// most a day).
func (s *ShareSchedulingService) Report(ctx context.Context, groupID int64, window time.Duration) (*ShareSchedulingReport, error) {
	if _, err := s.shareGroup(ctx, groupID); err != nil {
		return nil, err
	}
	if window <= 0 {
		window = shareAdminDefaultWindow
	}
	window = min(window, shareAdminMaxWindow)
	stored, err := s.storedPolicy(ctx, groupID)
	if err != nil {
		return nil, err
	}
	share := stored.scheduler() == sharesched.SchedulerShare
	now := time.Now()
	out := &ShareSchedulingReport{GroupID: groupID, Scheduler: string(stored.scheduler()), SessionKeep: stored.policy.SessionKeep(),
		Version: stored.version(), WindowSeconds: int64(window / time.Second), CurrentTo: now,
		CurrentFrom: now.Add(-window), CounterPrecision: shareCounterPrecisionMinute}

	anchor, source := s.shareAnchor(ctx, groupID, stored)
	if !anchor.IsZero() && (share || source == ShareAnchorSwitchRecord) {
		out.SwitchedAt, out.AnchorSource = &anchor, &source
		// Data from before the switch never mixes into the current window.
		if anchor.After(out.CurrentFrom) && anchor.Before(now) {
			out.CurrentFrom = anchor
		}
	}
	if out.CurrentTo.Sub(out.CurrentFrom) > shareCounterMinuteSpan {
		out.CounterPrecision = shareCounterPrecisionHour
	}

	var counterErrs []error
	cacheCounters, err := s.readCounters(ctx, shareStatsKindCache, groupID, out.CurrentFrom, now)
	counterErrs = append(counterErrs, err)
	if out.Current, err = s.reportMetrics(ctx, groupID, out.CurrentFrom, now, cacheCounters); err != nil {
		return nil, err
	}
	usage, err := s.repo.AccountUsage(ctx, groupID, out.CurrentFrom, now)
	if err != nil {
		return nil, err
	}
	var shareCounters map[string]int64
	if share {
		shareCounters, err = s.readCounters(ctx, shareStatsKindShare, groupID, out.CurrentFrom, now)
		counterErrs = append(counterErrs, err)
	}
	out.Accounts = shareReportAccounts(usage, shareCounters, share)
	out.Details = shareReportDetails(cacheCounters, shareCounters, share)

	switch {
	case !share:
		out.BaselineUnavailable = shareStringPtr(ShareBaselineNotShare)
	case anchor.IsZero():
		out.BaselineUnavailable = shareStringPtr(ShareBaselineNeverSwitched)
	default:
		baseline, unavailable, err := s.reportBaseline(ctx, groupID, anchor, window, now)
		if err != nil {
			counterErrs = append(counterErrs, err)
		}
		from, to := anchor.Add(-window), anchor
		out.BaselineFrom, out.BaselineTo, out.Baseline, out.BaselineUnavailable = &from, &to, baseline, unavailable
		out.CounterPrecision = shareCounterPrecisionHour
	}
	if joined := errors.Join(counterErrs...); joined != nil {
		out.CountersError = shareStringPtr(joined.Error())
	}
	return out, nil
}

func shareStringPtr(s string) *string { return &s }

func (s *ShareSchedulingService) readCounters(ctx context.Context, kind string, groupID int64, from, to time.Time) (map[string]int64, error) {
	if s.store == nil {
		return nil, nil
	}
	return s.store.ReadCounters(ctx, kind, groupID, from, to)
}

// reportMetrics combines the database metrics of a window with its cache
// attribution counters (nil when they could not be read).
func (s *ShareSchedulingService) reportMetrics(ctx context.Context, groupID int64, from, to time.Time, cache map[string]int64) (ShareSchedulingMetrics, error) {
	db, err := s.repo.GroupMetrics(ctx, groupID, from, to)
	if err != nil {
		return ShareSchedulingMetrics{}, err
	}
	m := ShareSchedulingMetrics{Requests: db.Requests, CacheHitRatio: shareRatio(db.CacheReadTokens, db.PromptTokens),
		TTFTP50Ms: db.TTFTP50Ms, TTFTP95Ms: db.TTFTP95Ms, TTFTSamples: db.TTFTSamples, Errors: db.Errors,
		ErrorRate: shareRatio(db.Errors, db.Requests+db.Errors)}
	if cache != nil {
		attribution := newShareAttribution(cache)
		m.AttributionSamples = attribution.requests
		m.SwitchRate, m.SwitchLossRatio = attribution.switchRate(), attribution.switchLossRatio()
	}
	return m, nil
}

// reportBaseline reads the window of the same length before the anchor.
// Its attribution comes from whole hourly buckets; beyond their retention
// only the database metrics are given.
func (s *ShareSchedulingService) reportBaseline(ctx context.Context, groupID int64, anchor time.Time, window time.Duration, now time.Time) (*ShareSchedulingMetrics, *string, error) {
	from := anchor.Add(-window)
	toHour := anchor.Unix() / 3600
	hours := int64((window + 30*time.Minute) / time.Hour)
	if hours < 1 {
		hours = 1
	}
	fromHour := toHour - hours
	var cache map[string]int64
	var unavailable *string
	var counterErr error
	if time.Unix(fromHour*3600, 0).Before(now.Add(-ShareCacheCounterRetention)) {
		unavailable = shareStringPtr(ShareBaselineOutOfRetention)
	} else if s.store != nil {
		cache, counterErr = s.store.ReadHourCounters(ctx, shareStatsKindCache, groupID, fromHour, toHour)
	}
	m, err := s.reportMetrics(ctx, groupID, from, anchor, cache)
	if err != nil {
		return nil, unavailable, errors.Join(counterErr, err)
	}
	return &m, unavailable, counterErr
}

func shareReportAccounts(usage map[int64]ShareSchedulingUsage, counters map[string]int64, share bool) []ShareSchedulingReportAccount {
	ids := map[int64]struct{}{}
	for id := range usage {
		ids[id] = struct{}{}
	}
	var skipped map[int64]map[string]int64
	if share {
		skipped = perAccountCounters(counters, shareStatsSkipPrefix, ids)
	}
	var totalRequests, totalTokens int64
	var totalCost float64
	for _, u := range usage {
		totalRequests += u.Requests
		totalTokens += u.Tokens
		totalCost += u.StandardCost
	}
	ratio := func(part, total float64) float64 {
		if total <= 0 {
			return 0
		}
		return part / total
	}
	out := make([]ShareSchedulingReportAccount, 0, len(ids))
	for id := range ids {
		u := usage[id]
		out = append(out, ShareSchedulingReportAccount{AccountID: id,
			Requests: u.Requests, RequestShare: ratio(float64(u.Requests), float64(totalRequests)),
			Tokens: u.Tokens, TokenShare: ratio(float64(u.Tokens), float64(totalTokens)),
			StandardCost: u.StandardCost, CostShare: ratio(u.StandardCost, totalCost), Skipped: skipped[id]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out
}

func shareReportDetails(cache, counters map[string]int64, share bool) ShareSchedulingReportDetails {
	attribution := newShareAttribution(cache)
	details := ShareSchedulingReportDetails{CacheClasses: map[string]ShareSchedulingCacheClass{}}
	for class, n := range attribution.classes {
		details.CacheClasses[class] = ShareSchedulingCacheClass{Requests: n, Tokens: attribution.classTokens[class]}
	}
	if !share {
		return details
	}
	waits := &ShareSchedulingWaits{Count: counters[shareStatsWaitCountField], Hit: counters[shareStatsWaitHitField]}
	if waits.Count > 0 {
		avg := float64(counters[shareStatsWaitMsField]) / float64(waits.Count)
		waits.AvgMs = &avg
	}
	attempts, requests := counters[shareStatsAttemptsField], counters[shareStatsRequestsField]
	details.Reasons, details.Waits, details.Attempts, details.ShareRequests = collectPrefixed(counters, shareStatsReasonPrefix), waits, &attempts, &requests
	details.HotMigrations = &ShareSchedulingHotMigrations{Total: counters[shareStatsHotMigrationField],
		ByReason: collectPrefixed(counters, shareStatsHotPrefix)}
	return details
}

// shareAttribution reads the cache attribution counters of a window.
type shareAttribution struct {
	requests     int64
	promptTokens int64
	classes      map[string]int64
	classTokens  map[string]int64
}

func newShareAttribution(c map[string]int64) shareAttribution {
	return shareAttribution{requests: c[shareStatsRequestsField], promptTokens: c[shareStatsPromptTokensField],
		classes: collectPrefixed(c, shareStatsClassPrefix), classTokens: collectPrefixed(c, shareStatsClassTokensPrefix)}
}

// switchRate is the share of attributed requests that missed the cache
// because they changed account while it was warm.
func (a shareAttribution) switchRate() *float64 {
	var switched int64
	for class, n := range a.classes {
		if strings.HasPrefix(class, shareCacheMissSwitchPrefix) {
			switched += n
		}
	}
	return shareRatio(switched, a.requests)
}

// switchLossRatio is the share of attributed prompt tokens lost that way.
func (a shareAttribution) switchLossRatio() *float64 {
	var lost int64
	for class, tokens := range a.classTokens {
		if strings.HasPrefix(class, shareCacheMissSwitchPrefix) {
			lost += tokens
		}
	}
	return shareRatio(lost, a.promptTokens)
}

// perAccountCounters splits prefix + account + ":" + rest fields by account
// and adds the accounts to ids.
func perAccountCounters(counters map[string]int64, prefix string, ids map[int64]struct{}) map[int64]map[string]int64 {
	out := map[int64]map[string]int64{}
	for field, n := range collectPrefixed(counters, prefix) {
		raw, rest, ok := strings.Cut(field, ":")
		if !ok {
			continue
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			continue
		}
		if out[id] == nil {
			out[id] = map[string]int64{}
		}
		out[id][rest] += n
		ids[id] = struct{}{}
	}
	return out
}

func collectPrefixed(counters map[string]int64, prefix string) map[string]int64 {
	out := map[string]int64{}
	for field, n := range counters {
		if rest, ok := strings.CutPrefix(field, prefix); ok {
			out[rest] += n
		}
	}
	return out
}
