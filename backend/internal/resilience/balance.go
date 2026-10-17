package resilience

import (
	"encoding/json"
	"strings"
	"time"
)

// BalanceState is the recoverable balance state of an API-key account
// (design §3.2).
type BalanceState string

// Balance states.
const (
	BalanceOK         BalanceState = "OK"
	BalanceSuspect    BalanceState = "SUSPECT"
	BalanceExhausted  BalanceState = "EXHAUSTED"
	BalanceRecovering BalanceState = "RECOVERING"
)

// BalanceExtraKey is the accounts.extra key holding the BalanceRecord.
const BalanceExtraKey = "resilience_balance"

// Temp-unschedulable reason prefixes written by the resilience layer. The
// prefix decides the penalty tier and lets each component clear only what it
// wrote.
const (
	ReasonPrefixBalanceExhausted = "balance:exhausted"
	ReasonPrefixBalanceSuspect   = "balance:suspect"
	ReasonPrefixTransient        = "transient:"
)

// exhaustedGrace keeps an EXHAUSTED account excluded a little past its next
// probe, so a stopped probe loop eventually lets real traffic re-check it
// instead of excluding it forever.
const exhaustedGrace = 10 * time.Minute

// BalanceRecord is persisted in accounts.extra[BalanceExtraKey].
type BalanceRecord struct {
	State               BalanceState `json:"state"`
	Since               time.Time    `json:"since"`
	NextCheckAt         time.Time    `json:"next_check_at,omitempty"`
	Level               int          `json:"level"`
	LastEvidence        string       `json:"last_evidence,omitempty"`
	LastProbeAt         time.Time    `json:"last_probe_at,omitempty"`
	ProbeSource         string       `json:"probe_source,omitempty"`
	RecoveringUntil     time.Time    `json:"recovering_until,omitempty"`
	RecoveringSuccesses int          `json:"recovering_successes,omitempty"`
	Notified            bool         `json:"notified,omitempty"`
}

// BalanceRecordFrom decodes the record from an account's extra map. Missing
// or malformed records are OK.
func BalanceRecordFrom(extra map[string]any) BalanceRecord {
	raw, ok := extra[BalanceExtraKey]
	if !ok || raw == nil {
		return BalanceRecord{State: BalanceOK}
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return BalanceRecord{State: BalanceOK}
	}
	var r BalanceRecord
	if err := json.Unmarshal(data, &r); err != nil || r.State == "" {
		return BalanceRecord{State: BalanceOK}
	}
	return r
}

// BalanceStateOf returns the state of the record in an account's extra map
// without decoding the rest of it; selection reads it for every account.
// Only EXHAUSTED needs the full record (BalanceRecordFrom).
func BalanceStateOf(extra map[string]any) BalanceState {
	raw, ok := extra[BalanceExtraKey]
	if !ok || raw == nil {
		return BalanceOK
	}
	if m, ok := raw.(map[string]any); ok {
		if st, _ := m["state"].(string); st != "" {
			return BalanceState(st)
		}
		return BalanceOK
	}
	return BalanceRecordFrom(extra).State
}

// Extra encodes the record for accounts.extra.
func (r BalanceRecord) Extra() map[string]any {
	data, _ := json.Marshal(r)
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	return map[string]any{BalanceExtraKey: m}
}

// TempUnschedReason is the reason written with the record's exclusion.
func (r BalanceRecord) TempUnschedReason() string {
	switch r.State {
	case BalanceExhausted:
		return ReasonPrefixBalanceExhausted + ": " + truncate(r.LastEvidence, 200)
	case BalanceSuspect:
		return ReasonPrefixBalanceSuspect + ": " + truncate(r.LastEvidence, 200)
	}
	return ""
}

// ExclusionUntil returns until when the record excludes the account; zero
// when it does not.
func (r BalanceRecord) ExclusionUntil(s *Settings) time.Time {
	switch r.State {
	case BalanceExhausted:
		if r.NextCheckAt.IsZero() {
			return r.Since.Add(exhaustedGrace)
		}
		return r.NextCheckAt.Add(exhaustedGrace)
	case BalanceSuspect:
		return r.Since.Add(time.Duration(s.Balance.SuspectTTLSeconds) * time.Second)
	}
	return time.Time{}
}

// RecheckDelay returns the jittered delay before the probe of a level.
func RecheckDelay(s *Settings, level int) time.Duration {
	sched := s.Balance.RecheckScheduleSeconds
	if len(sched) == 0 {
		sched = Defaults().Balance.RecheckScheduleSeconds
	}
	if level < 0 {
		level = 0
	}
	if level >= len(sched) {
		level = len(sched) - 1
	}
	return Jitter(time.Duration(sched[level])*time.Second, s.Balance.JitterRatio)
}

// OnStrongSignal applies a balance failure from a real request: the account
// becomes EXHAUSTED. A failure while RECOVERING moves one back-off level up.
func OnStrongSignal(r BalanceRecord, s *Settings, evidence string, now time.Time) BalanceRecord {
	level := 0
	switch r.State {
	case BalanceExhausted:
		// Already exhausted: keep the schedule, refresh the evidence.
		r.LastEvidence = truncate(evidence, 500)
		return r
	case BalanceRecovering:
		level = r.Level + 1
	}
	return BalanceRecord{
		State:        BalanceExhausted,
		Since:        now,
		Level:        level,
		NextCheckAt:  now.Add(RecheckDelay(s, level)),
		LastEvidence: truncate(evidence, 500),
		LastProbeAt:  r.LastProbeAt,
		ProbeSource:  r.ProbeSource,
	}
}

// OnWeakSignal applies balance wording on an otherwise healthy account: it
// becomes SUSPECT for the configured TTL unless already worse.
func OnWeakSignal(r BalanceRecord, s *Settings, evidence string, now time.Time) BalanceRecord {
	if r.State == BalanceExhausted || r.State == BalanceSuspect {
		return r
	}
	return BalanceRecord{
		State:        BalanceSuspect,
		Since:        now,
		Level:        r.Level,
		NextCheckAt:  now,
		LastEvidence: truncate(evidence, 500),
		LastProbeAt:  r.LastProbeAt,
		ProbeSource:  r.ProbeSource,
	}
}

// ProbeVerdict is the outcome of one balance probe.
type ProbeVerdict int

// Probe verdicts.
const (
	ProbeUnknown ProbeVerdict = iota
	ProbePositive
	ProbeNegative
)

// OnProbe applies a probe verdict.
func OnProbe(r BalanceRecord, s *Settings, v ProbeVerdict, source, evidence string, now time.Time) BalanceRecord {
	r.LastProbeAt = now
	r.ProbeSource = source
	switch v {
	case ProbePositive:
		switch r.State {
		case BalanceSuspect:
			return BalanceRecord{State: BalanceOK, Since: now, LastProbeAt: now, ProbeSource: source}
		case BalanceExhausted:
			return BalanceRecord{
				State:           BalanceRecovering,
				Since:           now,
				Level:           r.Level,
				LastEvidence:    truncate(evidence, 500),
				LastProbeAt:     now,
				ProbeSource:     source,
				RecoveringUntil: now.Add(time.Duration(s.Balance.RecoveringWindowSeconds) * time.Second),
			}
		}
		return r
	case ProbeNegative:
		if r.State == BalanceExhausted {
			r.Level++
			r.LastEvidence = truncate(evidence, 500)
			r.NextCheckAt = now.Add(RecheckDelay(s, r.Level))
			return r
		}
		return BalanceRecord{
			State:        BalanceExhausted,
			Since:        now,
			Level:        0,
			NextCheckAt:  now.Add(RecheckDelay(s, 0)),
			LastEvidence: truncate(evidence, 500),
			LastProbeAt:  now,
			ProbeSource:  source,
		}
	default:
		// Inconclusive: keep the state, check again at the same level.
		if r.State == BalanceExhausted || r.State == BalanceSuspect {
			r.NextCheckAt = now.Add(RecheckDelay(s, r.Level))
		}
		return r
	}
}

// OnSuccess applies a successful real request. It returns the new record and
// whether it changed.
func OnSuccess(r BalanceRecord, s *Settings, now time.Time) (BalanceRecord, bool) {
	switch r.State {
	case BalanceRecovering:
		r.RecoveringSuccesses++
		if r.RecoveringSuccesses >= s.Balance.RecoveringSuccessTarget || !now.Before(r.RecoveringUntil) {
			return BalanceRecord{State: BalanceOK, Since: now, LastProbeAt: r.LastProbeAt, ProbeSource: r.ProbeSource}, true
		}
		return r, true
	case BalanceSuspect:
		if !now.Before(r.Since.Add(time.Duration(s.Balance.SuspectTTLSeconds) * time.Second)) {
			return BalanceRecord{State: BalanceOK, Since: now, LastProbeAt: r.LastProbeAt, ProbeSource: r.ProbeSource}, true
		}
	}
	return r, false
}

// Due reports whether the record needs a probe at now.
func (r BalanceRecord) Due(now time.Time) bool {
	switch r.State {
	case BalanceExhausted, BalanceSuspect:
		return r.NextCheckAt.IsZero() || !now.Before(r.NextCheckAt)
	}
	return false
}

// PenaltyTier is the scheduling tier of an account restriction.
type PenaltyTier int

// Penalty tiers (design §3.1).
const (
	TierNone PenaltyTier = iota
	TierSoft
	TierHard
)

// TempUnschedTier classifies a temp_unschedulable reason. Reasons written by
// the resilience layer are explicit; the counted 403 cooldown and overload
// style pauses are soft; every other reason (credentials, balance of CN
// providers, administrator rules) stays hard.
func TempUnschedTier(reason string) PenaltyTier {
	r := strings.TrimSpace(reason)
	switch {
	case r == "":
		return TierHard
	case strings.HasPrefix(r, ReasonPrefixBalanceSuspect), strings.HasPrefix(r, ReasonPrefixTransient):
		return TierSoft
	case strings.HasPrefix(r, Reason403Cooldown), strings.HasPrefix(r, legacyOpenAI403CooldownPrefix):
		return TierSoft
	}
	return TierHard
}

// Reason403Cooldown prefixes the counted non-balance 403 cooldown of
// non-OpenAI API-key platforms.
const Reason403Cooldown = "auth:403_cooldown"

// legacyOpenAI403CooldownPrefix is the reason the existing OpenAI 403
// counter writes; it is the same soft cooldown.
const legacyOpenAI403CooldownPrefix = "OpenAI 403 temporary cooldown"

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	// Cut on a rune boundary.
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut]
}
