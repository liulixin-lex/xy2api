package sharesched

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"time"
)

// Scheduler selects which algorithm serves a group.
type Scheduler string

const (
	// SchedulerSub2API keeps the original Sub2API selection untouched.
	SchedulerSub2API Scheduler = "sub2api"
	// SchedulerShadow serves with Sub2API and records what share would do.
	SchedulerShadow Scheduler = "shadow"
	// SchedulerShare serves with share scheduling.
	SchedulerShare Scheduler = "share"
)

const (
	PolicySchema = 1

	MaxPolicyAccounts = 5000
	MinWeight         = 1
	MaxWeight         = 1_000_000
	MaxCacheDomainLen = 64

	MinAffinityTTLSeconds  = 300
	MaxAffinityTTLSeconds  = 86400
	MaxWaitMs              = 15000
	MaxWaitMsPer1kTokens   = 5000
	MaxWaitingLimit        = 100
	MaxSoftSpillSeconds    = 1800
	MinCacheTTLSeconds     = 60
	MaxCacheTTLSeconds     = 86400
	MaxStableSeconds       = 3600
	MinReturnDeadlineSecs  = 600
	MaxReturnDeadlineSecs  = 86400
	MinBalanceWindowMins   = 5
	MaxBalanceWindowMins   = 1440
	MaxBalanceMinVolume    = 1_000_000
	DeadlineStaggerSeconds = 600
)

// Policy is one group's share-scheduling configuration. It is stored as JSON
// and versioned by the repository; the kernel only reads it.
type Policy struct {
	Schema    int           `json:"schema"`
	Scheduler Scheduler     `json:"scheduler"`
	Accounts  []AccountRule `json:"accounts"`
	Defaults  Defaults      `json:"defaults"`
	Affinity  Affinity      `json:"affinity"`
	Cache     Cache         `json:"cache"`
	Tiers     Tiers         `json:"tiers"`
	Balance   Balance       `json:"balance"`
}

// AccountRule is the administrator's intent for one group member. Members
// without a rule use Defaults.
type AccountRule struct {
	AccountID   int64      `json:"account_id"`
	Tier        int        `json:"tier"`
	Weight      int        `json:"weight"`
	Drain       bool       `json:"drain,omitempty"`
	DrainUntil  *time.Time `json:"drain_until,omitempty"`
	CacheDomain string     `json:"cache_domain,omitempty"`
}

// UnmarshalJSON requires account_id, tier and weight so an omitted tier can
// never silently become the highest-priority tier 0.
func (r *AccountRule) UnmarshalJSON(data []byte) error {
	var raw struct {
		AccountID   *int64     `json:"account_id"`
		Tier        *int       `json:"tier"`
		Weight      *int       `json:"weight"`
		Drain       bool       `json:"drain"`
		DrainUntil  *time.Time `json:"drain_until"`
		CacheDomain string     `json:"cache_domain"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	switch {
	case raw.AccountID == nil:
		return errors.New("account rule requires account_id")
	case raw.Tier == nil:
		return fmt.Errorf("account %d requires tier", *raw.AccountID)
	case raw.Weight == nil:
		return fmt.Errorf("account %d requires weight", *raw.AccountID)
	}
	*r = AccountRule{AccountID: *raw.AccountID, Tier: *raw.Tier, Weight: *raw.Weight, Drain: raw.Drain, DrainUntil: raw.DrainUntil, CacheDomain: raw.CacheDomain}
	return nil
}

// Defaults decides tier and weight for members without an explicit rule.
type Defaults struct {
	Tier   string `json:"tier"`
	Weight string `json:"weight"`
}

const (
	DefaultTierAccountPriority = "account_priority"
	DefaultWeightConcurrency   = "concurrency"
	DefaultWeightEqual         = "equal"
)

// Affinity configures session binding.
type Affinity struct {
	Enabled             bool   `json:"enabled"`
	Key                 string `json:"key"`
	TTLSeconds          int    `json:"ttl_seconds"`
	Wait                Wait   `json:"wait"`
	OnSpill             string `json:"on_spill"`
	SoftSpillMaxSeconds int    `json:"soft_spill_max_seconds"`
}

const (
	AffinityKeyAuto    = "auto"
	AffinityKeySession = "session"
	AffinityKeyPrefix  = "prefix"
	AffinityKeyNone    = "none"

	OnSpillRebind = "rebind"
	OnSpillKeep   = "keep"
)

// Wait bounds how long a request waits for its saturated bound account.
type Wait struct {
	Mode          string `json:"mode"`
	MsPer1kTokens int    `json:"ms_per_1k_tokens"`
	MaxMs         int    `json:"max_ms"`
	MaxWaiting    int    `json:"max_waiting"`
}

const (
	WaitAuto  = "auto"
	WaitFixed = "fixed"
	WaitOff   = "off"
)

// Cache configures cold-window detection. TTLSeconds 0 means the protocol
// default from CacheTTL.
type Cache struct {
	TTLSeconds int    `json:"ttl_seconds"`
	Realign    string `json:"realign"`
}

const (
	RealignCold = "cold"
	RealignOff  = "off"
)

// Tiers configures overflow and return between tiers.
type Tiers struct {
	OnSaturated           string `json:"on_saturated"`
	OnFailed              string `json:"on_failed"`
	Return                string `json:"return"`
	StableSeconds         int    `json:"stable_seconds"`
	ReturnDeadlineSeconds int    `json:"return_deadline_seconds"`
}

const (
	OnSaturatedSpill = "spill"
	OnSaturatedWait  = "wait"

	OnFailedNextTier = "next_tier"
	OnFailedStop     = "stop"

	ReturnCold       = "cold"
	ReturnImmediate  = "immediate"
	ReturnSessionEnd = "session_end"
)

// Balance configures share reporting and the optional bounded correction.
type Balance struct {
	Unit          string `json:"unit"`
	Correction    string `json:"correction"`
	WindowMinutes int    `json:"window_minutes"`
	MinVolume     int    `json:"min_volume"`
}

const (
	UnitSessions     = "sessions"
	UnitRequests     = "requests"
	UnitTokens       = "tokens"
	UnitStandardCost = "standard_cost"

	CorrectionOff    = "off"
	CorrectionGentle = "gentle"
	CorrectionStrong = "strong"
)

// DefaultPolicy returns the documented defaults with scheduler sub2api, so a
// freshly created policy never changes routing until an administrator opts in.
func DefaultPolicy() Policy {
	return Policy{
		Schema:    PolicySchema,
		Scheduler: SchedulerSub2API,
		Accounts:  []AccountRule{},
		Defaults:  Defaults{Tier: DefaultTierAccountPriority, Weight: DefaultWeightConcurrency},
		Affinity: Affinity{
			Enabled:             true,
			Key:                 AffinityKeyAuto,
			TTLSeconds:          3600,
			Wait:                Wait{Mode: WaitAuto, MsPer1kTokens: 100, MaxMs: MaxWaitMs, MaxWaiting: 3},
			OnSpill:             OnSpillRebind,
			SoftSpillMaxSeconds: 300,
		},
		Cache:   Cache{TTLSeconds: 0, Realign: RealignCold},
		Tiers:   Tiers{OnSaturated: OnSaturatedSpill, OnFailed: OnFailedNextTier, Return: ReturnCold, StableSeconds: 120},
		Balance: Balance{Unit: UnitSessions, Correction: CorrectionOff, WindowMinutes: 60, MinVolume: 50},
	}
}

// DecodePolicy strictly decodes a policy document over the defaults: unknown
// fields, trailing data and out-of-range values are rejected.
func DecodePolicy(data []byte) (Policy, error) {
	policy := DefaultPolicy()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&policy); err != nil {
		return Policy{}, fmt.Errorf("invalid share scheduling policy: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Policy{}, errors.New("invalid share scheduling policy: trailing data")
	}
	if policy.Accounts == nil {
		policy.Accounts = []AccountRule{}
	}
	if err := policy.Validate(); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

var cacheDomainPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

// ValidationError lists every problem in a policy so the editor can show them
// together.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	if len(e.Problems) == 1 {
		return "invalid share scheduling policy: " + e.Problems[0]
	}
	return fmt.Sprintf("invalid share scheduling policy: %d problems, first: %s", len(e.Problems), e.Problems[0])
}

// Validate checks the group-independent rules.
func (p Policy) Validate() error {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	inSet := func(value string, allowed ...string) bool {
		for _, candidate := range allowed {
			if value == candidate {
				return true
			}
		}
		return false
	}

	if p.Schema != PolicySchema {
		add("schema must be %d", PolicySchema)
	}
	if !inSet(string(p.Scheduler), string(SchedulerSub2API), string(SchedulerShadow), string(SchedulerShare)) {
		add("scheduler must be sub2api, shadow or share")
	}
	if len(p.Accounts) > MaxPolicyAccounts {
		add("at most %d accounts", MaxPolicyAccounts)
	}
	seen := make(map[int64]struct{}, len(p.Accounts))
	for _, rule := range p.Accounts {
		if rule.AccountID <= 0 {
			add("account_id must be positive")
			continue
		}
		if _, dup := seen[rule.AccountID]; dup {
			add("account %d is listed twice", rule.AccountID)
		}
		seen[rule.AccountID] = struct{}{}
		if rule.Tier < math.MinInt32 || rule.Tier > math.MaxInt32 {
			add("account %d tier out of range", rule.AccountID)
		}
		if rule.Weight < MinWeight || rule.Weight > MaxWeight {
			add("account %d weight must be %d-%d", rule.AccountID, MinWeight, MaxWeight)
		}
		if rule.DrainUntil != nil && !rule.Drain {
			add("account %d drain_until requires drain", rule.AccountID)
		}
		if rule.CacheDomain != "" && (len(rule.CacheDomain) > MaxCacheDomainLen || !cacheDomainPattern.MatchString(rule.CacheDomain)) {
			add("account %d cache_domain must be 1-%d characters of letters, digits, '.', '_', ':' or '-'", rule.AccountID, MaxCacheDomainLen)
		} else if strings.HasPrefix(rule.CacheDomain, ownCacheDomainPrefix) {
			add("account %d cache_domain must not start with the reserved prefix %q", rule.AccountID, ownCacheDomainPrefix)
		}
	}
	if p.Defaults.Tier != DefaultTierAccountPriority {
		add("defaults.tier must be account_priority")
	}
	if !inSet(p.Defaults.Weight, DefaultWeightConcurrency, DefaultWeightEqual) {
		add("defaults.weight must be concurrency or equal")
	}

	a := p.Affinity
	if !inSet(a.Key, AffinityKeyAuto, AffinityKeySession, AffinityKeyPrefix, AffinityKeyNone) {
		add("affinity.key must be auto, session, prefix or none")
	}
	if a.TTLSeconds < MinAffinityTTLSeconds || a.TTLSeconds > MaxAffinityTTLSeconds {
		add("affinity.ttl_seconds must be %d-%d", MinAffinityTTLSeconds, MaxAffinityTTLSeconds)
	}
	if !inSet(a.Wait.Mode, WaitAuto, WaitFixed, WaitOff) {
		add("affinity.wait.mode must be auto, fixed or off")
	}
	if a.Wait.MsPer1kTokens < 0 || a.Wait.MsPer1kTokens > MaxWaitMsPer1kTokens {
		add("affinity.wait.ms_per_1k_tokens must be 0-%d", MaxWaitMsPer1kTokens)
	}
	if a.Wait.MaxMs < 0 || a.Wait.MaxMs > MaxWaitMs {
		add("affinity.wait.max_ms must be 0-%d", MaxWaitMs)
	}
	if a.Wait.MaxWaiting < 0 || a.Wait.MaxWaiting > MaxWaitingLimit {
		add("affinity.wait.max_waiting must be 0-%d", MaxWaitingLimit)
	}
	if !inSet(a.OnSpill, OnSpillRebind, OnSpillKeep) {
		add("affinity.on_spill must be rebind or keep")
	}
	if a.SoftSpillMaxSeconds < 0 || a.SoftSpillMaxSeconds > MaxSoftSpillSeconds {
		add("affinity.soft_spill_max_seconds must be 0-%d", MaxSoftSpillSeconds)
	}

	if p.Cache.TTLSeconds != 0 && (p.Cache.TTLSeconds < MinCacheTTLSeconds || p.Cache.TTLSeconds > MaxCacheTTLSeconds) {
		add("cache.ttl_seconds must be 0 (auto) or %d-%d", MinCacheTTLSeconds, MaxCacheTTLSeconds)
	}
	if !inSet(p.Cache.Realign, RealignCold, RealignOff) {
		add("cache.realign must be cold or off")
	}

	t := p.Tiers
	if !inSet(t.OnSaturated, OnSaturatedSpill, OnSaturatedWait) {
		add("tiers.on_saturated must be spill or wait")
	}
	if !inSet(t.OnFailed, OnFailedNextTier, OnFailedStop) {
		add("tiers.on_failed must be next_tier or stop")
	}
	if !inSet(t.Return, ReturnCold, ReturnImmediate, ReturnSessionEnd) {
		add("tiers.return must be cold, immediate or session_end")
	}
	if t.StableSeconds < 0 || t.StableSeconds > MaxStableSeconds {
		add("tiers.stable_seconds must be 0-%d", MaxStableSeconds)
	}
	if t.ReturnDeadlineSeconds != 0 && (t.ReturnDeadlineSeconds < MinReturnDeadlineSecs || t.ReturnDeadlineSeconds > MaxReturnDeadlineSecs) {
		add("tiers.return_deadline_seconds must be 0 or %d-%d", MinReturnDeadlineSecs, MaxReturnDeadlineSecs)
	}

	b := p.Balance
	if !inSet(b.Unit, UnitSessions, UnitRequests, UnitTokens, UnitStandardCost) {
		add("balance.unit must be sessions, requests, tokens or standard_cost")
	}
	if !inSet(b.Correction, CorrectionOff, CorrectionGentle, CorrectionStrong) {
		add("balance.correction must be off, gentle or strong")
	}
	if b.Unit == UnitSessions && b.Correction != CorrectionOff {
		add("balance.correction must be off when balance.unit is sessions")
	}
	if b.WindowMinutes < MinBalanceWindowMins || b.WindowMinutes > MaxBalanceWindowMins {
		add("balance.window_minutes must be %d-%d", MinBalanceWindowMins, MaxBalanceWindowMins)
	}
	if b.MinVolume < 0 || b.MinVolume > MaxBalanceMinVolume {
		add("balance.min_volume must be 0-%d", MaxBalanceMinVolume)
	}

	if len(problems) > 0 {
		return &ValidationError{Problems: problems}
	}
	return nil
}

// Member describes a group member for group-dependent validation.
type Member struct {
	Platform    string
	Priority    int
	Concurrency int
}

// ValidateMembers checks the rules that need the group's member list: every
// rule names a member, a cache domain never spans platforms, and an active
// policy keeps at least one member able to take new sessions.
func (p Policy) ValidateMembers(members map[int64]Member) error {
	var problems []string
	domainPlatform := make(map[string]string)
	explicit := make(map[int64]AccountRule, len(p.Accounts))
	for _, rule := range p.Accounts {
		explicit[rule.AccountID] = rule
		member, ok := members[rule.AccountID]
		if !ok {
			problems = append(problems, fmt.Sprintf("account %d is not a member of this group", rule.AccountID))
			continue
		}
		if rule.CacheDomain == "" {
			continue
		}
		if platform, seen := domainPlatform[rule.CacheDomain]; seen && platform != member.Platform {
			problems = append(problems, fmt.Sprintf("cache_domain %q mixes platforms %s and %s", rule.CacheDomain, platform, member.Platform))
		} else if !seen {
			domainPlatform[rule.CacheDomain] = member.Platform
		}
	}
	if p.Scheduler != SchedulerSub2API {
		open := false
		for id := range members {
			if rule, ok := explicit[id]; !ok || !rule.Drain {
				open = true
				break
			}
		}
		if !open {
			problems = append(problems, "at least one account must accept new sessions (not drained)")
		}
	}
	if len(problems) > 0 {
		return &ValidationError{Problems: problems}
	}
	return nil
}
