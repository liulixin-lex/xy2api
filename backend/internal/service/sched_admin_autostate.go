package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

func schedAdminTempSource(reason string) string {
	var v struct {
		Source         string `json:"source"`
		MatchedKeyword string `json:"matched_keyword"`
	}
	_ = json.Unmarshal([]byte(reason), &v)
	switch {
	case IsAccountSchedulingThresholdReason(reason):
		return "usage"
	case resilience.IsBalancePause(reason):
		return "balance"
	case strings.Contains(reason, "POST_OUTPUT"):
		return "post_output"
	case strings.Contains(reason, resilience.Reason403Cooldown) || strings.Contains(reason, "403_cooldown"):
		return "403"
	case v.MatchedKeyword == "stream_timeout":
		return "stream_timeout"
	case v.MatchedKeyword == openAIAPIKeyHealthBreakerReason:
		return "health"
	case strings.HasPrefix(reason, resilience.ReasonPrefixTransient):
		return "transient"
	case v.Source != "":
		return v.Source
	case v.MatchedKeyword != "" || strings.HasPrefix(strings.TrimSpace(reason), "{"):
		return "rule"
	default:
		return "auth"
	}
}
func schedAdminLimitUntil(v any) *time.Time {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	raw, _ := m["rate_limit_reset_at"].(string)
	t, e := time.Parse(time.RFC3339Nano, raw)
	if e != nil {
		return nil
	}
	return &t
}
func (s *SchedAdminService) AutoStates(ctx context.Context, id int64) ([]SchedAutoState, error) {
	a, e := s.account(ctx, id)
	if e != nil {
		return nil, e
	}
	return s.autoStates(ctx, a, s.d.Now())
}
func (s *SchedAdminService) autoStates(ctx context.Context, a *Account, now time.Time) ([]SchedAutoState, error) {
	out := []SchedAutoState{}
	add := func(key, kind string, until *time.Time, clear bool, why string) {
		out = append(out, SchedAutoState{Key: key, Kind: kind, Scope: "account", Until: until, Clearable: clear, Why: why})
	}
	if a.Status == StatusError {
		add("error", "error", nil, true, "")
	}
	if a.RateLimitResetAt != nil && now.Before(*a.RateLimitResetAt) {
		add("rate_limit", "rate_limit", a.RateLimitResetAt, true, "")
	}
	if a.OverloadUntil != nil && now.Before(*a.OverloadUntil) {
		add("overload", "overload", a.OverloadUntil, true, "")
	}
	if a.TempUnschedulableUntil != nil && now.Before(*a.TempUnschedulableUntil) {
		source := schedAdminTempSource(a.TempUnschedulableReason)
		add("temp", "temp_"+source, a.TempUnschedulableUntil, true, "")
	}
	if limits, ok := a.Extra["model_rate_limits"].(map[string]any); ok {
		for key, v := range limits {
			until := schedAdminLimitUntil(v)
			iq := strings.HasPrefix(key, "iq:")
			if !iq && (until == nil || !now.Before(*until)) {
				continue
			}
			kind, why, stateKey := "model_limit", "", "model_limit:"+key
			if iq {
				kind, why, stateKey = "iq_gate", "iq_gate", key
			}
			out = append(out, SchedAutoState{Key: stateKey, Kind: kind, Scope: "model", Model: strings.TrimPrefix(key, "iq:"), Until: until, Clearable: !iq, Why: why})
		}
	}
	if s.d.Transient != nil {
		for _, v := range s.d.Transient.List(a.ID) {
			out = append(out, SchedAutoState{Key: v.Key, Kind: v.Kind, Scope: v.Scope, Model: v.Model, Until: v.Until, Clearable: true})
		}
	}
	if s.d.Resilience != nil {
		out = append(out, s.d.Resilience.schedAdminLocalStates(a, now)...)
	}
	if a.ProxyID != nil {
		if v := s.d.Gateway.schedAdminProxyState(*a.ProxyID, now); v != nil {
			n, e := s.d.Accounts.SchedAdminProxyPeers(ctx, a.ID, *a.ProxyID)
			if e != nil {
				return nil, e
			}
			v.Shared = n
			out = append(out, *v)
		}
	}
	learned := a.CapabilityLearned(now)
	if len(learned) > 0 {
		until := learned[0].ExpiresAt
		for _, v := range learned {
			if v.ExpiresAt.After(until) {
				until = v.ExpiresAt
			}
		}
		add("capability_learned", "capability_learned", &until, true, "")
	}
	if !a.Schedulable || a.Status == StatusDisabled {
		add("manual_off", "manual_off", nil, false, "manual_off")
	}
	if a.AutoPauseOnExpired && a.ExpiresAt != nil && !now.Before(*a.ExpiresAt) {
		add("expired", "expired", a.ExpiresAt, false, "expired")
	}
	if a.IsAPIKeyOrBedrock() && a.IsQuotaExceeded() {
		add("quota", "quota", nil, false, "quota")
	}
	var thresholds map[string]int
	if s.d.Thresholds != nil {
		thresholds = s.d.Thresholds(ctx)
	}
	if d := EvaluateAccountSchedulingThreshold(a, thresholds, now); d.ShouldPause {
		add("quota_auto_pause", "quota_auto_pause", d.Until, false, "quota_auto_pause")
	}
	if g := s.d.Gateway; g != nil && g.openAICodexTicketEnabledContext(ctx) {
		cfg := g.openAICodexTicketConfig()
		cfg.Enabled = true
		for _, st := range OpenAICodexTicketStatuses(a, cfg, now) {
			if st.Blocked || g.codexQualityBlocks(a, st.Model) {
				out = append(out, SchedAutoState{Key: "codex_ticket:" + st.Model, Kind: "codex_ticket", Scope: "model", Model: st.Model, Until: st.ExpiresAt, Why: "codex_ticket"})
			}
		}
	}
	if a.ParentAccountID != nil {
		parent, e := s.d.Accounts.GetByID(ctx, *a.ParentAccountID)
		if e != nil && !errors.Is(e, ErrAccountNotFound) {
			return nil, e
		}
		if !parentHealthyForShadow(a, func(int64) *Account { return parent }) {
			add(fmt.Sprintf("parent_blocked:%d", *a.ParentAccountID), "parent_blocked", nil, false, "parent")
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
func (s *SchedAdminService) ClearAutoState(ctx context.Context, id int64, key string, op int64) (bool, error) {
	a, e := s.account(ctx, id)
	if e != nil {
		return false, e
	}
	states, e := s.autoStates(ctx, a, s.d.Now())
	if e != nil {
		return false, e
	}
	for _, v := range states {
		if v.Key == key {
			if !v.Clearable {
				return false, infraerrors.BadRequest("STATE_READ_ONLY", v.Why)
			}
			ok, e := s.clearState(ctx, a, key)
			if e != nil {
				return false, e
			}
			if !ok {
				return false, infraerrors.Conflict("STATE_CHANGED", "State has changed")
			}
			return true, nil
		}
	}
	return false, infraerrors.NotFound("AUTO_STATE_NOT_FOUND", "Automatic state not found")
}
func (s *SchedAdminService) clearState(ctx context.Context, a *Account, key string) (bool, error) {
	switch key {
	case "error":
		ok, e := s.d.Accounts.SchedAdminClearError(ctx, a.ID, a.ErrorMessage)
		if e != nil || !ok {
			return ok, e
		}
		if strings.Contains(strings.ToLower(a.ErrorMessage), "403") {
			e = s.schedAdminCounters(ctx, a.ID, "403")
		}
		if strings.Contains(strings.ToLower(a.ErrorMessage), "timeout") {
			e = errors.Join(e, s.schedAdminCounters(ctx, a.ID, "stream_timeout"))
		}
		return true, e
	case "rate_limit":
		return s.d.Accounts.SchedAdminClear429(ctx, a.ID, a.RateLimitedAt, a.RateLimitResetAt)
	case "overload":
		return s.d.Accounts.SchedAdminClearOverload(ctx, a.ID, a.OverloadUntil)
	case "temp":
		ok, e := s.d.Accounts.SchedAdminClearTemp(ctx, a.ID, a.TempUnschedulableReason, a.TempUnschedulableUntil)
		if e != nil || !ok {
			return ok, e
		}
		cacheErr := s.clearTempCache(ctx, a)
		counterErr := s.schedAdminCounters(ctx, a.ID, schedAdminTempSource(a.TempUnschedulableReason))
		return cacheErr == nil && counterErr == nil, errors.Join(cacheErr, counterErr)
	case "same_site":
		return s.d.Resilience.schedAdminClearSameSite(a.ID), nil
	case "post_output":
		return s.d.Resilience.schedAdminClearPostOutput(a.ID), nil
	case "proxy_circuit":
		if a.ProxyID == nil {
			return false, nil
		}
		return s.d.Gateway.schedAdminClearProxy(*a.ProxyID), nil
	case "capability_learned":
		if s.d.Capability == nil {
			return false, infraerrors.ServiceUnavailable("CAPABILITY_UNAVAILABLE", "Capability service is unavailable")
		}
		return true, s.d.Capability.ClearAccountCapabilityLearning(ctx, a.ID)
	}
	if strings.HasPrefix(key, "model_limit:") {
		raw := strings.TrimPrefix(key, "model_limit:")
		if strings.HasPrefix(raw, "iq:") {
			return false, infraerrors.BadRequest("STATE_READ_ONLY", "iq_gate")
		}
		limits, _ := a.Extra["model_rate_limits"].(map[string]any)
		original, ok := limits[raw]
		if !ok {
			return false, nil
		}
		return s.d.Accounts.SchedAdminClearModelLimit(ctx, a.ID, raw, original)
	}
	if strings.HasPrefix(key, "model_transient:") && s.d.Transient != nil {
		return s.d.Transient.Clear(a.ID, strings.TrimPrefix(key, "model_transient:")), nil
	}
	return false, nil
}
func (s *SchedAdminService) clearTempCache(ctx context.Context, a *Account) error {
	if s.d.RateLimit == nil || s.d.RateLimit.tempUnschedCache == nil {
		return nil
	}
	cache := s.d.RateLimit.tempUnschedCache
	conditional, ok := cache.(interface {
		SchedAdminDeleteTempIf(context.Context, int64, string, *time.Time) (bool, error)
	})
	if !ok {
		return infraerrors.ServiceUnavailable("CONDITIONAL_CACHE_REQUIRED", "Conditional temporary state cache is required")
	}
	cleared, err := conditional.SchedAdminDeleteTempIf(ctx, a.ID, a.TempUnschedulableReason, a.TempUnschedulableUntil)
	if err != nil || cleared {
		return err
	}
	current, err := s.account(ctx, a.ID)
	if err != nil {
		return err
	}
	if current.TempUnschedulableUntil != nil || current.TempUnschedulableReason != "" {
		return infraerrors.Conflict("STATE_CHANGED", "State has changed")
	}
	return cache.DeleteTempUnsched(ctx, a.ID)
}

func (s *SchedAdminService) Reset(ctx context.Context, id, op int64) (SchedResetResult, error) {
	out := SchedResetResult{Cleared: []SchedResetItem{}, Preserved: []SchedResetItem{}, Failed: []SchedResetItem{}}
	a, e := s.account(ctx, id)
	if e != nil {
		return out, e
	}
	finish := func(key, kind string, ok bool, e error) {
		item := SchedResetItem{Key: key, Kind: kind}
		if e != nil {
			item.Error = e.Error()
			out.Failed = append(out.Failed, item)
		} else if ok {
			out.Cleared = append(out.Cleared, item)
		}
	}
	// Capture local entries before Reset invokes this instance's ClearLocal callback.
	states := []SchedAutoState{}
	if a.Status == StatusError {
		states = append(states, SchedAutoState{Key: "error", Kind: "error"})
	}
	if a.RateLimitedAt != nil || a.RateLimitResetAt != nil {
		states = append(states, SchedAutoState{Key: "rate_limit", Kind: "rate_limit"})
	}
	if a.OverloadUntil != nil {
		states = append(states, SchedAutoState{Key: "overload", Kind: "overload"})
	}
	if a.TempUnschedulableUntil != nil || a.TempUnschedulableReason != "" {
		states = append(states, SchedAutoState{Key: "temp", Kind: "temp_" + schedAdminTempSource(a.TempUnschedulableReason)})
	}
	if limits, ok := a.Extra["model_rate_limits"].(map[string]any); ok {
		for raw := range limits {
			if !strings.HasPrefix(raw, "iq:") {
				states = append(states, SchedAutoState{Key: "model_limit:" + raw, Kind: "model_limit"})
			}
		}
	}
	if s.d.Transient != nil {
		for _, v := range s.d.Transient.List(id) {
			states = append(states, SchedAutoState{Key: v.Key, Kind: v.Kind})
		}
	}
	states = append(states, s.d.Resilience.schedAdminResetLocalStates(id)...)
	if a.Extra[capabilityLearnedResponsesKey] != nil || a.Extra[capabilityLearnedChatKey] != nil {
		states = append(states, SchedAutoState{Key: "capability_learned", Kind: "capability_learned"})
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Key < states[j].Key })
	out.ResetAt, e = s.d.Engine.Reset(ctx, id)
	if errors.Is(e, sql.ErrNoRows) || errors.Is(e, ErrAccountNotFound) {
		return out, ErrAccountNotFound
	}
	finish("quality", "quality", e == nil, e)
	for _, v := range states {
		ok, e := s.clearState(ctx, a, v.Key)
		if v.Kind == "model_transient" || v.Kind == "same_site" || v.Kind == "post_output" {
			ok = e == nil
		}
		if e == nil && !ok {
			e = infraerrors.Conflict("STATE_CHANGED", "State has changed")
		}
		finish(v.Key, v.Kind, ok, e)
	}
	for _, source := range []string{"403", "stream_timeout", "health"} {
		if err := s.schedAdminCounters(ctx, id, source); err != nil {
			key, kind := "temp", "temp_"+source
			if a.Status == StatusError && ((source == "403" && strings.Contains(a.ErrorMessage, "403")) || (source == "stream_timeout" && strings.Contains(strings.ToLower(a.ErrorMessage), "timeout"))) {
				key, kind = "error", "error"
			}
			finish(key, kind, false, err)
		}
	}
	s.ClearLocal(id, out.ResetAt)
	states, e = s.AutoStates(ctx, id)
	if e != nil {
		finish("auto_states", "auto_states", false, e)
	} else {
		for _, v := range states {
			if !v.Clearable || v.Kind == "proxy_circuit" {
				why := v.Why
				if v.Kind == "proxy_circuit" {
					why = "shared_proxy"
				}
				out.Preserved = append(out.Preserved, SchedResetItem{Key: v.Key, Kind: v.Kind, Why: why})
			}
		}
	}
	out.Preserved = append(out.Preserved, SchedResetItem{Key: "session_avoid", Kind: "session_avoid", Why: "expires_within_10m"})
	return out, nil
}
