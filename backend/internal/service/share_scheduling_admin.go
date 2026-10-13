package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	shareCounterMinuteSpan    = 2 * time.Hour
	shareAdminSummaryWorkers  = 8
	shareAdminPreviewMaxKeys  = 2000
	shareAdminPreviewMinKeys  = 200
	shareAdminPreviewBudget   = 1_000_000
	shareAdminCapacityWarning = 1.5
	shareStatsRebalanceField  = "rebalance_cleared"

	ShareRebalanceCold = "cold"
	ShareRebalanceAll  = "all"
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

// UpdateSettings validates, stores and broadcasts the global settings.
func (s *ShareSchedulingService) UpdateSettings(ctx context.Context, settings ShareSchedulingSettings) (ShareSchedulingSettings, error) {
	if err := settings.Validate(); err != nil {
		return ShareSchedulingSettings{}, infraerrors.BadRequest("SHARE_SCHEDULING_SETTINGS_INVALID", err.Error())
	}
	if s.settings == nil {
		return ShareSchedulingSettings{}, ErrShareSchedulingUnavailable
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return ShareSchedulingSettings{}, err
	}
	s.settingsWriteMu.Lock()
	defer s.settingsWriteMu.Unlock()
	if err := s.settings.Set(ctx, shareSchedulingSettingsKey, string(raw)); err != nil {
		return ShareSchedulingSettings{}, err
	}
	s.mu.Lock()
	s.settingsEpoch++
	current := s.state.Load()
	s.state.Store(&shareSchedulingState{settings: settings, groups: current.groups})
	s.mu.Unlock()
	s.publishChange(ctx, shareSchedulingSettingsChange)
	return s.Settings(), nil
}

// ---- members and shares ----

// ShareSchedulingMemberView is one group member with its resolved rule,
// current health and shares.
type ShareSchedulingMemberView struct {
	AccountID      int64      `json:"account_id"`
	Name           string     `json:"name"`
	Platform       string     `json:"platform"`
	Type           string     `json:"type"`
	Priority       int        `json:"priority"`
	Concurrency    int        `json:"concurrency"`
	Status         string     `json:"status"`
	Schedulable    bool       `json:"schedulable"`
	Healthy        bool       `json:"healthy"`
	Reason         string     `json:"reason,omitempty"`
	RecoverAt      *time.Time `json:"recover_at,omitempty"`
	Tier           int        `json:"tier"`
	Weight         int        `json:"weight"`
	Configured     bool       `json:"configured"`
	Drain          bool       `json:"drain"`
	DrainUntil     *time.Time `json:"drain_until,omitempty"`
	CacheDomain    string     `json:"cache_domain"`
	TargetShare    float64    `json:"target_share"`
	EffectiveShare float64    `json:"effective_share"`
	CapacityShare  float64    `json:"capacity_share"`
}

// shareMembers lists the group's accounts.
func (s *ShareSchedulingService) shareMembers(ctx context.Context, groupID int64) ([]Account, error) {
	if s.gateway == nil || s.gateway.accountRepo == nil {
		return nil, ErrShareSchedulingUnavailable
	}
	return s.gateway.accountRepo.ListByGroup(ctx, groupID)
}

func shareMemberMap(accounts []Account) map[int64]sharesched.Member {
	members := make(map[int64]sharesched.Member, len(accounts))
	for i := range accounts {
		members[accounts[i].ID] = sharesched.Member{Platform: accounts[i].Platform, Priority: accounts[i].Priority, Concurrency: accounts[i].Concurrency}
	}
	return members
}

// shareMemberViews resolves every member's rule and computes its shares.
// Target share is the weight share among the open members of its tier;
// effective share applies current health, so only the lowest tier with a
// healthy open member receives new sessions; capacity share is the
// concurrency share inside the tier.
func shareMemberViews(p *sharesched.Policy, accounts []Account, now time.Time) ([]ShareSchedulingMemberView, *int) {
	views := make([]ShareSchedulingMemberView, 0, len(accounts))
	type totals struct{ weight, healthyWeight, concurrency float64 }
	tiers := map[int]*totals{}
	resolve := p.Resolver()
	for i := range accounts {
		acc := &accounts[i]
		rule, configured := resolve(acc.ID, acc.Priority, acc.Concurrency)
		v := ShareSchedulingMemberView{AccountID: acc.ID, Name: acc.Name, Platform: acc.Platform, Type: acc.Type,
			Priority: acc.Priority, Concurrency: acc.Concurrency, Status: acc.Status, Schedulable: acc.Schedulable,
			Healthy: acc.IsSchedulable(), Tier: rule.Tier, Weight: rule.Weight, Configured: configured,
			Drain: rule.Drain, DrainUntil: rule.DrainUntil, CacheDomain: rule.CacheDomain}
		if !v.Healthy {
			reason, recoverAt := shareAccountStateReason(acc, now)
			v.Reason = string(reason)
			if !recoverAt.IsZero() {
				v.RecoverAt = &recoverAt
			}
		}
		views = append(views, v)
		if v.Drain {
			continue
		}
		t := tiers[v.Tier]
		if t == nil {
			t = &totals{}
			tiers[v.Tier] = t
		}
		t.weight += float64(v.Weight)
		t.concurrency += float64(max(v.Concurrency, 0))
		if v.Healthy {
			t.healthyWeight += float64(v.Weight)
		}
	}
	// Tiers may be negative, so the active tier needs its own presence flag.
	var active *int
	for tier, t := range tiers {
		if t.healthyWeight > 0 && (active == nil || tier < *active) {
			lowest := tier
			active = &lowest
		}
	}
	for i := range views {
		v := &views[i]
		t := tiers[v.Tier]
		if v.Drain || t == nil {
			continue
		}
		if t.weight > 0 {
			v.TargetShare = float64(v.Weight) / t.weight
		}
		if t.concurrency > 0 {
			v.CapacityShare = float64(max(v.Concurrency, 0)) / t.concurrency
		}
		if active != nil && v.Tier == *active && v.Healthy && t.healthyWeight > 0 {
			v.EffectiveShare = float64(v.Weight) / t.healthyWeight
		}
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Tier != views[j].Tier {
			return views[i].Tier < views[j].Tier
		}
		return views[i].AccountID < views[j].AccountID
	})
	return views, active
}

// ---- groups ----

// ShareSchedulingGroupView summarises one group for the list.
type ShareSchedulingGroupView struct {
	GroupID      int64      `json:"group_id"`
	Name         string     `json:"name"`
	Platform     string     `json:"platform"`
	Scheduler    string     `json:"scheduler"`
	Version      int64      `json:"version"`
	Configured   bool       `json:"configured"`
	Effective    bool       `json:"effective"`
	Invalid      bool       `json:"invalid,omitempty"`
	ImportedFrom string     `json:"imported_from,omitempty"`
	UpdatedAt    *time.Time `json:"updated_at,omitempty"`
	Requests1h   int64      `json:"requests_1h"`
	// CacheReadRatio1h is cache-read tokens over prompt tokens of the
	// attributed requests; SchedulingMissRatio1h is the token share of
	// misses caused by account switches.
	CacheReadRatio1h      float64  `json:"cache_read_ratio_1h"`
	SchedulingMissRatio1h float64  `json:"scheduling_miss_ratio_1h"`
	ShadowAgreeRate1h     *float64 `json:"shadow_agree_rate_1h,omitempty"`
}

// ListGroups returns every active group with its scheduler and a one-hour
// summary.
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
	views := make([]ShareSchedulingGroupView, len(groups))
	for i := range groups {
		g := &groups[i]
		v := ShareSchedulingGroupView{GroupID: g.ID, Name: g.Name, Platform: g.Platform, Scheduler: string(sharesched.SchedulerSub2API)}
		if row := byGroup[g.ID]; row != nil {
			v.Configured, v.Version, v.ImportedFrom = true, row.Version, row.ImportedFrom
			updated := row.UpdatedAt
			v.UpdatedAt = &updated
			// The request path skips an undecodable row, so it runs sub2api.
			if _, err := sharesched.DecodePolicy(row.Policy); err != nil {
				v.Invalid = true
			} else {
				v.Scheduler = row.Scheduler
			}
		}
		v.Effective = s.groupPolicy(g.ID) != nil
		views[i] = v
	}
	s.fillGroupSummaries(ctx, views)
	return views, nil
}

// fillGroupSummaries reads the last hour of counters for every group with
// bounded concurrency; a failed read leaves the summary empty.
func (s *ShareSchedulingService) fillGroupSummaries(ctx context.Context, views []ShareSchedulingGroupView) {
	if s.store == nil {
		return
	}
	now := time.Now()
	from := now.Add(-shareAdminDefaultWindow)
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(shareAdminSummaryWorkers, len(views)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				v := &views[i]
				if share, err := s.store.ReadCounters(ctx, shareStatsKindShare, v.GroupID, from, now); err == nil {
					v.Requests1h = share[shareStatsRequestsField]
					if total := share[shareStatsShadowField]; total > 0 {
						rate := float64(share[shareStatsShadowAgreeField]) / float64(total)
						v.ShadowAgreeRate1h = &rate
					}
				}
				if cache, err := s.store.ReadCounters(ctx, shareStatsKindCache, v.GroupID, from, now); err == nil {
					stats := newShareCacheStats(cache)
					v.CacheReadRatio1h, v.SchedulingMissRatio1h = stats.ReadRatio, stats.SchedulingMissRatio
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

// ---- policy ----

// ShareSchedulingPolicyView is a group's stored policy with its members.
type ShareSchedulingPolicyView struct {
	GroupID      int64                       `json:"group_id"`
	GroupName    string                      `json:"group_name"`
	Platform     string                      `json:"platform"`
	Version      int64                       `json:"version"`
	Scheduler    string                      `json:"scheduler"`
	Effective    bool                        `json:"effective"`
	Policy       json.RawMessage             `json:"policy"`
	Invalid      string                      `json:"invalid,omitempty"`
	ImportedFrom string                      `json:"imported_from,omitempty"`
	UpdatedBy    *int64                      `json:"updated_by,omitempty"`
	UpdatedAt    *time.Time                  `json:"updated_at,omitempty"`
	ActiveTier   *int                        `json:"active_tier"`
	Members      []ShareSchedulingMemberView `json:"members"`
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

// shareStoredPolicy is a group's stored policy row and its decoded form.
type shareStoredPolicy struct {
	row    *ShareSchedulingPolicyRow
	policy sharesched.Policy
	// invalid is why a stored row no longer decodes; policy then holds the
	// defaults, which is also what the request path uses.
	invalid error
}

// storedPolicy returns the group's stored policy, or the defaults (scheduler
// sub2api) when none is stored or the stored one no longer decodes.
func (s *ShareSchedulingService) storedPolicy(ctx context.Context, groupID int64) (shareStoredPolicy, error) {
	row, err := s.repo.GetPolicy(ctx, groupID)
	if err != nil {
		return shareStoredPolicy{}, err
	}
	stored := shareStoredPolicy{row: row, policy: sharesched.DefaultPolicy()}
	if row == nil {
		return stored, nil
	}
	if policy, decodeErr := sharesched.DecodePolicy(row.Policy); decodeErr != nil {
		stored.invalid = decodeErr
	} else {
		stored.policy = policy
	}
	return stored, nil
}

// GetPolicy returns the group's policy and members.
func (s *ShareSchedulingService) GetPolicy(ctx context.Context, groupID int64) (*ShareSchedulingPolicyView, error) {
	group, err := s.shareGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}
	stored, err := s.storedPolicy(ctx, groupID)
	if err != nil {
		return nil, err
	}
	accounts, err := s.shareMembers(ctx, groupID)
	if err != nil {
		return nil, err
	}
	return s.policyView(group, stored, accounts)
}

func (s *ShareSchedulingService) policyView(group *Group, stored shareStoredPolicy, accounts []Account) (*ShareSchedulingPolicyView, error) {
	row, policy := stored.row, stored.policy
	view := &ShareSchedulingPolicyView{GroupID: group.ID, GroupName: group.Name, Platform: group.Platform,
		Scheduler: string(policy.Scheduler), Effective: s.groupPolicy(group.ID) != nil}
	if row != nil {
		view.Version, view.ImportedFrom, view.UpdatedBy = row.Version, row.ImportedFrom, row.UpdatedBy
		updated := row.UpdatedAt
		view.UpdatedAt = &updated
		if stored.invalid == nil {
			view.Scheduler = row.Scheduler
		}
	}
	// An undecodable row is skipped by the request path, so the editor starts
	// from the defaults that actually run; saving replaces the row.
	if stored.invalid != nil {
		view.Invalid = stored.invalid.Error()
	}
	var err error
	if view.Policy, err = json.Marshal(policy); err != nil {
		return nil, err
	}
	view.Members, view.ActiveTier = shareMemberViews(&policy, accounts, time.Now())
	return view, nil
}

// ShareSchedulingAccountRule is an account's resolved rule in one group that
// stores a share scheduling policy.
type ShareSchedulingAccountRule struct {
	GroupID     int64      `json:"group_id"`
	GroupName   string     `json:"group_name"`
	Scheduler   string     `json:"scheduler"`
	Effective   bool       `json:"effective"`
	Configured  bool       `json:"configured"`
	Tier        int        `json:"tier"`
	Weight      int        `json:"weight"`
	Drain       bool       `json:"drain"`
	DrainUntil  *time.Time `json:"drain_until,omitempty"`
	CacheDomain string     `json:"cache_domain"`
}

// AccountRules lists the account's rules in its groups that store a share
// scheduling policy, for the read-only account details. Groups without a
// stored policy or with an undecodable one run the original scheduler and
// are left out.
func (s *ShareSchedulingService) AccountRules(ctx context.Context, accountID int64) ([]ShareSchedulingAccountRule, error) {
	if s.gateway == nil || s.gateway.accountRepo == nil || s.gateway.groupRepo == nil || s.repo == nil {
		return nil, ErrShareSchedulingUnavailable
	}
	account, err := s.gateway.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	groupIDs := account.GroupIDs
	if len(groupIDs) == 0 {
		for _, ag := range account.AccountGroups {
			groupIDs = append(groupIDs, ag.GroupID)
		}
	}
	out := []ShareSchedulingAccountRule{}
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
	for _, groupID := range groupIDs {
		row := byGroup[groupID]
		if row == nil {
			continue
		}
		policy, err := sharesched.DecodePolicy(row.Policy)
		if err != nil {
			continue
		}
		rule, configured := policy.ResolveRule(account.ID, account.Priority, account.Concurrency)
		v := ShareSchedulingAccountRule{GroupID: groupID, Scheduler: row.Scheduler, Effective: s.groupPolicy(groupID) != nil,
			Configured: configured, Tier: rule.Tier, Weight: rule.Weight, Drain: rule.Drain, DrainUntil: rule.DrainUntil,
			CacheDomain: rule.CacheDomain}
		if group, err := s.gateway.groupRepo.GetByIDLite(ctx, groupID); err == nil && group != nil {
			v.GroupName = group.Name
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GroupID < out[j].GroupID })
	return out, nil
}

// SavePolicy strictly validates and stores a policy when the stored version
// still equals expectedVersion, then broadcasts the change.
func (s *ShareSchedulingService) SavePolicy(ctx context.Context, groupID, expectedVersion int64, raw []byte, updatedBy int64) (*ShareSchedulingPolicyView, error) {
	group, err := s.shareGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}
	policy, err := sharesched.DecodePolicy(raw)
	if err != nil {
		return nil, shareSchedulingPolicyInvalid(err)
	}
	accounts, err := s.shareMembers(ctx, groupID)
	if err != nil {
		return nil, err
	}
	if err := policy.ValidateMembers(shareMemberMap(accounts)); err != nil {
		return nil, shareSchedulingPolicyInvalid(err)
	}
	normalized, err := json.Marshal(policy)
	if err != nil {
		return nil, err
	}
	row, err := s.repo.SavePolicy(ctx, groupID, expectedVersion, string(policy.Scheduler), normalized, updatedBy)
	if err != nil {
		return nil, err
	}
	// The committed row is authoritative: publish it without reading it back,
	// so a failing read cannot report a committed save as failed.
	s.installGroup(row)
	s.publishChange(ctx, groupID)
	return s.policyView(group, shareStoredPolicy{row: row, policy: policy}, accounts)
}

// ---- preview ----

// ShareSchedulingWarning is one risk found by a preview.
type ShareSchedulingWarning struct {
	Code      string `json:"code"`
	AccountID int64  `json:"account_id,omitempty"`
	Tier      *int   `json:"tier,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// ShareSchedulingPreview is the side-effect-free evaluation of a proposed
// policy against the group's current members.
type ShareSchedulingPreview struct {
	ActiveTier *int                        `json:"active_tier"`
	Members    []ShareSchedulingMemberView `json:"members"`
	// PlacementChange is the fraction of new sessions whose first choice
	// differs from the stored policy.
	PlacementChange     float64                  `json:"placement_change"`
	SampledKeys         int                      `json:"sampled_keys"`
	DeadlineAccounts    []int64                  `json:"deadline_accounts,omitempty"`
	EstimatedMigrations int                      `json:"estimated_migrations"`
	Warnings            []ShareSchedulingWarning `json:"warnings"`
}

// Preview evaluates a proposed policy without saving it.
func (s *ShareSchedulingService) Preview(ctx context.Context, groupID int64, raw []byte) (*ShareSchedulingPreview, error) {
	if _, err := s.shareGroup(ctx, groupID); err != nil {
		return nil, err
	}
	proposed, err := sharesched.DecodePolicy(raw)
	if err != nil {
		return nil, shareSchedulingPolicyInvalid(err)
	}
	accounts, err := s.shareMembers(ctx, groupID)
	if err != nil {
		return nil, err
	}
	if err := proposed.ValidateMembers(shareMemberMap(accounts)); err != nil {
		return nil, shareSchedulingPolicyInvalid(err)
	}
	stored, err := s.storedPolicy(ctx, groupID)
	if err != nil {
		return nil, err
	}
	current := stored.policy
	now := time.Now()
	out := &ShareSchedulingPreview{Warnings: []ShareSchedulingWarning{}}
	out.Members, out.ActiveTier = shareMemberViews(&proposed, accounts, now)
	out.PlacementChange, out.SampledKeys = sharePlacementChange(groupID, &current, &proposed, accounts, now)
	out.Warnings = sharePreviewWarnings(&proposed, out.Members, out.ActiveTier, s.Settings())
	for _, rule := range proposed.Accounts {
		if rule.DrainUntil != nil {
			out.DeadlineAccounts = append(out.DeadlineAccounts, rule.AccountID)
		}
	}
	if len(out.DeadlineAccounts) > 0 && s.store != nil {
		deadline := make(map[int64]bool, len(out.DeadlineAccounts))
		for _, id := range out.DeadlineAccounts {
			deadline[id] = true
		}
		// Counting walks the bindings without deleting any.
		if _, err := s.store.ClearBindings(ctx, groupID, func(b ShareSchedulingBinding) bool {
			if deadline[b.AccountID] {
				out.EstimatedMigrations++
			}
			return false
		}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// sharePlacementChange compares the first choice of sampled new sessions
// under two policies with the members' current health.
func sharePlacementChange(groupID int64, current, proposed *sharesched.Policy, accounts []Account, now time.Time) (float64, int) {
	if len(accounts) == 0 {
		return 0, 0
	}
	cands := make([]sharesched.Candidate, len(accounts))
	for i := range accounts {
		acc := &accounts[i]
		cands[i] = sharesched.Candidate{AccountID: acc.ID, Platform: acc.Platform, Class: shareAccountClass(acc),
			Priority: acc.Priority, Concurrency: acc.Concurrency, Eligible: acc.IsSchedulable()}
	}
	keys := min(shareAdminPreviewMaxKeys, max(shareAdminPreviewMinKeys, shareAdminPreviewBudget/len(accounts)))
	changed := 0
	for i := range keys {
		req := sharesched.Request{GroupID: groupID, Key: "preview:" + strconv.Itoa(i), Now: now}
		before := sharesched.Plan(current, req, cands)
		after := sharesched.Plan(proposed, req, cands)
		if firstStep(before) != firstStep(after) {
			changed++
		}
	}
	return float64(changed) / float64(keys), keys
}

func firstStep(d sharesched.Decision) int64 {
	if len(d.Steps) == 0 {
		return 0
	}
	return d.Steps[0].AccountID
}

func sharePreviewWarnings(p *sharesched.Policy, members []ShareSchedulingMemberView, active *int, settings ShareSchedulingSettings) []ShareSchedulingWarning {
	warnings := []ShareSchedulingWarning{}
	if p.Scheduler != sharesched.SchedulerSub2API && !settings.Enabled {
		warnings = append(warnings, ShareSchedulingWarning{Code: "global_switch_off"})
	}
	if p.Scheduler != sharesched.SchedulerSub2API && active == nil {
		warnings = append(warnings, ShareSchedulingWarning{Code: "no_healthy_open_account"})
	}
	tierMembers := map[int]int{}
	tierHealthy := map[int]int{}
	for _, m := range members {
		if m.Drain {
			continue
		}
		tierMembers[m.Tier]++
		if m.Healthy {
			tierHealthy[m.Tier]++
		}
	}
	tiers := make([]int, 0, len(tierMembers))
	for tier := range tierMembers {
		tiers = append(tiers, tier)
	}
	sort.Ints(tiers)
	for _, tier := range tiers {
		if tierHealthy[tier] == 0 {
			t := tier
			warnings = append(warnings, ShareSchedulingWarning{Code: "tier_unavailable", Tier: &t})
		}
	}
	for _, m := range members {
		if m.Drain || tierMembers[m.Tier] < 2 || m.CapacityShare <= 0 {
			continue
		}
		if m.TargetShare > shareAdminCapacityWarning*m.CapacityShare {
			warnings = append(warnings, ShareSchedulingWarning{Code: "capacity_mismatch", AccountID: m.AccountID,
				Detail: fmt.Sprintf("target %.1f%% vs capacity %.1f%%", m.TargetShare*100, m.CapacityShare*100)})
		}
	}
	return warnings
}

// ---- explain ----

// ShareSchedulingExplainInput describes a request to explain.
type ShareSchedulingExplainInput struct {
	Model string `json:"model"`
	// SessionHash is the session key as stored in the sticky binding.
	SessionHash     string          `json:"session_hash"`
	EstimatedTokens int             `json:"estimated_tokens"`
	CacheOneHour    bool            `json:"cache_one_hour"`
	Body            json.RawMessage `json:"body,omitempty"`
}

// ShareSchedulingExplanation is the full plan of one request. Explaining
// never takes a slot or writes a binding.
type ShareSchedulingExplanation struct {
	GroupID         int64              `json:"group_id"`
	Platform        string             `json:"platform"`
	Scheduler       string             `json:"scheduler"`
	Version         int64              `json:"version"`
	KeySource       string             `json:"key_source"`
	Session         string             `json:"session,omitempty"`
	BoundAccountID  int64              `json:"bound_account_id,omitempty"`
	IdleMs          int64              `json:"idle_ms"`
	CacheTTLSeconds int                `json:"cache_ttl_seconds"`
	EstimatedTokens int                `json:"estimated_tokens"`
	Cacheable       bool               `json:"cacheable"`
	Warm            bool               `json:"warm"`
	WaitBudgetMs    int64              `json:"wait_budget_ms"`
	Reason          string             `json:"reason"`
	Steps           []int64            `json:"steps"`
	Write           string             `json:"write"`
	SpillWrite      string             `json:"spill_write"`
	SpillReason     string             `json:"spill_reason,omitempty"`
	MaxWaiting      int                `json:"max_waiting,omitempty"`
	Target          int64              `json:"target,omitempty"`
	Ideal           int64              `json:"ideal,omitempty"`
	ActiveTier      int                `json:"active_tier"`
	Entries         []sharesched.Entry `json:"entries"`
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

// Explain runs the stored policy (share rules even while the group serves
// with sub2api) against the live candidates of the group.
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
	gp := &shareGroupPolicy{groupID: groupID, policy: policy}
	if stored.row != nil {
		gp.version = stored.row.Version
	}
	switch {
	case len(in.Body) > 0:
		ctx = withShareRequestProfile(ctx, in.Body)
	case in.EstimatedTokens > 0:
		ctx = context.WithValue(ctx, shareRequestProfileKey{}, shareRequestProfile{
			bodyBytes: in.EstimatedTokens * inflightInputBytesPerTokenEstimate, cacheOneHour: in.CacheOneHour})
	}
	key, _ := shareAffinityKey(&policy, in.SessionHash)
	gid := groupID
	var (
		platform string
		cands    []sharesched.Candidate
		binding  sharesched.Binding
	)
	if (&Account{Platform: group.Platform}).IsOpenAICompatible() && s.openai != nil {
		sel := openAIShareSelection{groupID: &gid, sessionHash: in.SessionHash, model: in.Model,
			transport: OpenAIUpstreamTransportAny, platform: NormalizeOpenAICompatiblePlatform(group.Platform)}
		cacheKey := ""
		if key != "" {
			cacheKey = s.openai.openAISessionCacheKey(in.SessionHash)
		}
		binding, _ = s.openai.readOpenAIShareBinding(ctx, gp, cacheKey, in.SessionHash)
		if cands, _, _, err = s.openai.shareOpenAICandidates(ctx, sel, binding.AccountID); err != nil {
			return nil, err
		}
		platform = sel.platform
	} else if s.gateway != nil {
		binding = s.readBinding(ctx, s.gateway.cache, gp, key)
		var in2 *shareGatewayInputs
		if ctx, in2, err = s.gateway.shareGatewayCandidates(ctx, group, &gid, in.Model, nil, binding.AccountID); err != nil {
			return nil, err
		}
		cands, platform = in2.cands, in2.platform
	} else {
		return nil, ErrShareSchedulingUnavailable
	}
	req, source := s.newRequest(ctx, gp, platform, in.Model, in.SessionHash, binding)
	d := sharesched.Plan(&policy, req, cands)
	out := &ShareSchedulingExplanation{GroupID: groupID, Platform: platform, Scheduler: string(policy.Scheduler), Version: gp.version,
		KeySource: string(source), Session: shareSessionFingerprint(key), BoundAccountID: req.Binding.AccountID,
		IdleMs: req.Binding.Idle.Milliseconds(), CacheTTLSeconds: int(req.Cache.TTL / time.Second),
		EstimatedTokens: req.Cache.EstimatedTokens, Cacheable: req.Cache.Cacheable, Warm: req.Cache.Warm(req.Binding.Idle),
		WaitBudgetMs: d.Wait.Milliseconds(), Reason: string(d.Reason), Steps: make([]int64, len(d.Steps)),
		Write: shareWriteName(d.Write), SpillWrite: shareWriteName(d.Spill), SpillReason: string(d.SpillReason),
		MaxWaiting: d.MaxWaiting, Target: d.Target, Ideal: d.Ideal, ActiveTier: d.ActiveTier, Entries: d.ExplainedEntries()}
	for i, step := range d.Steps {
		out.Steps[i] = step.AccountID
	}
	return out, nil
}

// ---- report ----

// ShareSchedulingAccountActual is an account's actual traffic in a window.
type ShareSchedulingAccountActual struct {
	AccountID    int64   `json:"account_id"`
	Placed       int64   `json:"placed"`
	PlacedShare  float64 `json:"placed_share"`
	Requests     int64   `json:"requests"`
	RequestShare float64 `json:"request_share"`
	Tokens       int64   `json:"tokens"`
	TokenShare   float64 `json:"token_share"`
	StandardCost float64 `json:"standard_cost"`
	CostShare    float64 `json:"cost_share"`
	// Skipped counts the times the account was tried but passed over, by
	// reason (saturated, session limit, ...). Frequent saturation means the
	// weight asks for more than the account's concurrency can take.
	Skipped map[string]int64 `json:"skipped,omitempty"`
	// TTFT is the sampled first-token latency of requests the account served.
	TTFT *ShareSchedulingTTFT `json:"ttft,omitempty"`
}

// ShareSchedulingDecisionStats are the selection counters of a window.
type ShareSchedulingDecisionStats struct {
	Requests        int64            `json:"requests"`
	Reasons         map[string]int64 `json:"reasons"`
	KeySources      map[string]int64 `json:"key_sources"`
	IdealSkips      map[string]int64 `json:"ideal_skips"`
	HotMigrations   int64            `json:"hot_migrations"`
	HotReasons      map[string]int64 `json:"hot_reasons"`
	WaitCount       int64            `json:"wait_count"`
	WaitHit         int64            `json:"wait_hit"`
	AvgWaitMs       float64          `json:"avg_wait_ms"`
	Attempts        int64            `json:"attempts"`
	Shadow          int64            `json:"shadow"`
	ShadowAgree     int64            `json:"shadow_agree"`
	ShadowAgreeRate float64          `json:"shadow_agree_rate"`
	ShadowReasons   map[string]int64 `json:"shadow_reasons"`
	ShadowSkipped   int64            `json:"shadow_skipped"`
	RebalanceClears int64            `json:"rebalance_cleared"`
	Dropped         int64            `json:"dropped"`
}

// ShareSchedulingTTFT summarises first-token latency of one decision type.
// Percentiles are histogram bucket upper bounds; nil means above the last
// bucket.
type ShareSchedulingTTFT struct {
	Count int64 `json:"count"`
	P50Ms *int  `json:"p50_ms"`
	P95Ms *int  `json:"p95_ms"`
	P99Ms *int  `json:"p99_ms"`
}

// ShareSchedulingCacheStats are the cache attribution counters of a window.
type ShareSchedulingCacheStats struct {
	Requests            int64                          `json:"requests"`
	Classes             map[string]int64               `json:"classes"`
	ClassTokens         map[string]int64               `json:"class_tokens"`
	PromptTokens        int64                          `json:"prompt_tokens"`
	CacheReadTokens     int64                          `json:"cache_read_tokens"`
	CacheCreationTokens int64                          `json:"cache_creation_tokens"`
	ReadRatio           float64                        `json:"read_ratio"`
	SchedulingMissRatio float64                        `json:"scheduling_miss_ratio"`
	TTFT                map[string]ShareSchedulingTTFT `json:"ttft"`
	// Selection is the sampled selection cost by scheduler (share, shadow,
	// original).
	Selection map[string]ShareSchedulingSelectionCost `json:"selection"`
	Dropped   int64                                   `json:"dropped"`
}

// ShareSchedulingSelectionCost averages the sampled selections of one
// scheduler, from the start of selection to its result. Redis and database
// figures average the IOSamples taken on nodes with server timing enabled
// and are nil without any.
type ShareSchedulingSelectionCost struct {
	Samples       int64    `json:"samples"`
	AvgMs         float64  `json:"avg_ms"`
	IOSamples     int64    `json:"io_samples"`
	AvgRedisTrips *float64 `json:"avg_redis_round_trips"`
	AvgRedisMs    *float64 `json:"avg_redis_ms"`
	AvgDBQueries  *float64 `json:"avg_db_queries"`
}

// ShareSchedulingReport is target against actual shares plus the decision,
// cache and latency statistics of a window.
type ShareSchedulingReport struct {
	GroupID       int64                          `json:"group_id"`
	Scheduler     string                         `json:"scheduler"`
	Version       int64                          `json:"version"`
	Unit          string                         `json:"unit"`
	From          time.Time                      `json:"from"`
	To            time.Time                      `json:"to"`
	ActiveTier    *int                           `json:"active_tier"`
	Members       []ShareSchedulingMemberView    `json:"members"`
	Actual        []ShareSchedulingAccountActual `json:"actual"`
	Decisions     ShareSchedulingDecisionStats   `json:"decisions"`
	Cache         ShareSchedulingCacheStats      `json:"cache"`
	Corrections   map[int64]float64              `json:"corrections,omitempty"`
	CountersError string                         `json:"counters_error,omitempty"`
	// CounterPrecision is "minute", or "hour_rounded" when the window is read
	// from hourly buckets starting at the nearest hour.
	CounterPrecision string `json:"counter_precision"`
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
	row, policy := stored.row, stored.policy
	accounts, err := s.shareMembers(ctx, groupID)
	if err != nil {
		return nil, err
	}
	to := time.Now()
	from := to.Add(-window)
	out := &ShareSchedulingReport{GroupID: groupID, Scheduler: string(policy.Scheduler), Unit: policy.Balance.Unit,
		From: from, To: to, Corrections: s.correctionsFor(groupID), CounterPrecision: "minute"}
	if window > shareCounterMinuteSpan {
		out.CounterPrecision = "hour_rounded"
	}
	if row != nil {
		out.Version, out.Scheduler = row.Version, row.Scheduler
	}
	out.Members, out.ActiveTier = shareMemberViews(&policy, accounts, to)
	usage, err := s.repo.AccountUsage(ctx, groupID, from)
	if err != nil {
		return nil, err
	}
	var share, cache map[string]int64
	if s.store != nil {
		var shareErr, cacheErr error
		share, shareErr = s.store.ReadCounters(ctx, shareStatsKindShare, groupID, from, to)
		cache, cacheErr = s.store.ReadCounters(ctx, shareStatsKindCache, groupID, from, to)
		if joined := errors.Join(shareErr, cacheErr); joined != nil {
			out.CountersError = joined.Error()
		}
	}
	out.Decisions = newShareDecisionStats(share)
	out.Cache = newShareCacheStats(cache)
	out.Actual = shareActuals(accounts, usage, share, cache)
	return out, nil
}

func shareActuals(accounts []Account, usage map[int64]ShareSchedulingUsage, counters, cache map[string]int64) []ShareSchedulingAccountActual {
	ids := map[int64]struct{}{}
	for i := range accounts {
		ids[accounts[i].ID] = struct{}{}
	}
	for id := range usage {
		ids[id] = struct{}{}
	}
	placed := map[int64]int64{}
	for field, n := range counters {
		if raw, ok := strings.CutPrefix(field, shareStatsServedPrefix); ok {
			if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
				placed[id] += n
				ids[id] = struct{}{}
			}
		}
	}
	skipped := perAccountCounters(counters, shareStatsSkipPrefix, ids)
	ttft := perAccountCounters(cache, shareStatsAccountTTFTPrefix, ids)
	var totalPlaced, totalRequests, totalTokens int64
	var totalCost float64
	for id := range ids {
		totalPlaced += placed[id]
		totalRequests += usage[id].Requests
		totalTokens += usage[id].Tokens
		totalCost += usage[id].StandardCost
	}
	ratio := func(part, total float64) float64 {
		if total <= 0 {
			return 0
		}
		return part / total
	}
	out := make([]ShareSchedulingAccountActual, 0, len(ids))
	for id := range ids {
		u := usage[id]
		row := ShareSchedulingAccountActual{AccountID: id,
			Placed: placed[id], PlacedShare: ratio(float64(placed[id]), float64(totalPlaced)),
			Requests: u.Requests, RequestShare: ratio(float64(u.Requests), float64(totalRequests)),
			Tokens: u.Tokens, TokenShare: ratio(float64(u.Tokens), float64(totalTokens)),
			StandardCost: u.StandardCost, CostShare: ratio(u.StandardCost, totalCost), Skipped: skipped[id]}
		if buckets := ttft[id]; len(buckets) > 0 {
			summary := shareTTFTSummary(buckets)
			row.TTFT = &summary
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out
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

func newShareDecisionStats(c map[string]int64) ShareSchedulingDecisionStats {
	stats := ShareSchedulingDecisionStats{
		Requests: c[shareStatsRequestsField], Reasons: collectPrefixed(c, shareStatsReasonPrefix),
		KeySources: collectPrefixed(c, shareStatsKeySourcePrefix), IdealSkips: collectPrefixed(c, shareStatsIdealSkipPrefix),
		HotMigrations: c[shareStatsHotMigrationField], HotReasons: collectPrefixed(c, shareStatsHotPrefix),
		WaitCount: c[shareStatsWaitCountField], WaitHit: c[shareStatsWaitHitField], Attempts: c[shareStatsAttemptsField],
		Shadow: c[shareStatsShadowField], ShadowAgree: c[shareStatsShadowAgreeField],
		ShadowReasons: collectPrefixed(c, shareStatsShadowPrefix), ShadowSkipped: c[shareStatsShadowSkippedField],
		RebalanceClears: c[shareStatsRebalanceField],
		Dropped:         c[shareStatsDroppedField],
	}
	if stats.WaitCount > 0 {
		stats.AvgWaitMs = float64(c[shareStatsWaitMsField]) / float64(stats.WaitCount)
	}
	if stats.Shadow > 0 {
		stats.ShadowAgreeRate = float64(stats.ShadowAgree) / float64(stats.Shadow)
	}
	return stats
}

func newShareCacheStats(c map[string]int64) ShareSchedulingCacheStats {
	stats := ShareSchedulingCacheStats{
		Requests: c[shareStatsRequestsField], Classes: collectPrefixed(c, shareStatsClassPrefix),
		ClassTokens: collectPrefixed(c, shareStatsClassTokensPrefix), PromptTokens: c[shareStatsPromptTokensField],
		CacheReadTokens: c[shareStatsCacheReadField], CacheCreationTokens: c[shareStatsCacheCreationField],
		TTFT: map[string]ShareSchedulingTTFT{}, Selection: shareSelectionCosts(c), Dropped: c[shareStatsDroppedField],
	}
	if stats.PromptTokens > 0 {
		stats.ReadRatio = float64(stats.CacheReadTokens) / float64(stats.PromptTokens)
		var switchTokens int64
		for class, tokens := range stats.ClassTokens {
			if strings.HasPrefix(class, "miss_switch_") {
				switchTokens += tokens
			}
		}
		stats.SchedulingMissRatio = float64(switchTokens) / float64(stats.PromptTokens)
	}
	histograms := map[string]map[string]int64{}
	for field, n := range collectPrefixed(c, shareStatsTTFTPrefix) {
		reason, bucket, ok := strings.Cut(field, ":")
		if !ok {
			continue
		}
		if histograms[reason] == nil {
			histograms[reason] = map[string]int64{}
		}
		histograms[reason][bucket] += n
	}
	for reason, buckets := range histograms {
		stats.TTFT[reason] = shareTTFTSummary(buckets)
	}
	return stats
}

func shareSelectionCosts(c map[string]int64) map[string]ShareSchedulingSelectionCost {
	sums := map[string]map[string]int64{}
	for field, n := range collectPrefixed(c, shareStatsSelectionPrefix) {
		mode, name, ok := strings.Cut(field, ":")
		if !ok {
			continue
		}
		if sums[mode] == nil {
			sums[mode] = map[string]int64{}
		}
		sums[mode][name] += n
	}
	out := map[string]ShareSchedulingSelectionCost{}
	for mode, sum := range sums {
		samples := sum[shareSelectionSamples]
		if samples <= 0 {
			continue
		}
		cost := ShareSchedulingSelectionCost{Samples: samples, AvgMs: float64(sum[shareSelectionMicros]) / 1000 / float64(samples)}
		if io := sum[shareSelectionIOSamples]; io > 0 {
			cost.IOSamples = io
			avg := func(name string, scale float64) *float64 {
				v := float64(sum[name]) / scale / float64(io)
				return &v
			}
			cost.AvgRedisTrips, cost.AvgRedisMs, cost.AvgDBQueries = avg(shareSelectionRedisTrips, 1), avg(shareSelectionRedisMicros, 1000), avg(shareSelectionDBQueries, 1)
		}
		out[mode] = cost
	}
	return out
}

// shareTTFTSummary reads percentiles off a bucketed histogram.
func shareTTFTSummary(buckets map[string]int64) ShareSchedulingTTFT {
	var total int64
	for _, n := range buckets {
		total += n
	}
	out := ShareSchedulingTTFT{Count: total}
	if total == 0 {
		return out
	}
	percentile := func(p float64) *int {
		need := int64(p*float64(total) + 0.999999)
		var seen int64
		for _, bound := range shareTTFTBucketsMs {
			seen += buckets[strconv.Itoa(bound)]
			if seen >= need {
				b := bound
				return &b
			}
		}
		return nil
	}
	out.P50Ms, out.P95Ms, out.P99Ms = percentile(0.50), percentile(0.95), percentile(0.99)
	return out
}

// ---- decisions and rebalance ----

// RecentDecisions returns the group's latest decision samples, newest first.
func (s *ShareSchedulingService) RecentDecisions(ctx context.Context, groupID int64, limit int) ([]json.RawMessage, error) {
	if s.store == nil {
		return nil, ErrShareSchedulingUnavailable
	}
	raw, err := s.store.RecentDecisions(ctx, groupID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]json.RawMessage, 0, len(raw))
	for _, sample := range raw {
		if json.Valid(sample) {
			out = append(out, json.RawMessage(sample))
		}
	}
	return out, nil
}

// ShareSchedulingRebalanceResult reports an explicit rebalance.
type ShareSchedulingRebalanceResult struct {
	Mode    string `json:"mode"`
	Cleared int    `json:"cleared"`
}

// Rebalance clears session bindings so the next requests are placed anew.
// The default clears only bindings whose cache is already cold; protocol
// continuation and ownership keys are never touched.
func (s *ShareSchedulingService) Rebalance(ctx context.Context, groupID int64, mode string) (*ShareSchedulingRebalanceResult, error) {
	group, err := s.shareGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}
	if mode == "" {
		mode = ShareRebalanceCold
	}
	if mode != ShareRebalanceCold && mode != ShareRebalanceAll {
		return nil, infraerrors.BadRequest("SHARE_SCHEDULING_REBALANCE_MODE_INVALID", "mode must be cold or all")
	}
	if s.store == nil {
		return nil, ErrShareSchedulingUnavailable
	}
	stored, err := s.storedPolicy(ctx, groupID)
	if err != nil {
		return nil, err
	}
	policy := stored.policy
	bindingTTL := time.Duration(policy.Affinity.TTLSeconds) * time.Second
	// The longest cache the group's protocol can hold: clearing late never
	// rebuilds a warm cache.
	cacheTTL := sharesched.CacheTTL(shareCacheFamily(NormalizeGroupCachePlatform(group.Platform)), true, policy.Cache.TTLSeconds)
	cleared, err := s.store.ClearBindings(ctx, groupID, func(b ShareSchedulingBinding) bool {
		if mode == ShareRebalanceAll {
			return true
		}
		if b.Remaining <= 0 || b.Remaining >= bindingTTL {
			return false
		}
		return bindingTTL-b.Remaining >= cacheTTL
	})
	if cleared > 0 {
		s.stats.add(shareStatsKindShare, groupID, time.Now(), shareStatsRebalanceField, int64(cleared))
	}
	if err != nil {
		return nil, err
	}
	return &ShareSchedulingRebalanceResult{Mode: mode, Cleared: cleared}, nil
}

// NormalizeGroupCachePlatform maps a group platform to the account platform
// whose cache semantics its requests follow.
func NormalizeGroupCachePlatform(platform string) string {
	if (&Account{Platform: platform}).IsOpenAICompatible() {
		return NormalizeOpenAICompatiblePlatform(platform)
	}
	return platform
}
