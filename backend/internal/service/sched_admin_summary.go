package service

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
)

func schedAdminIDs(ids []int64) ([]int64, error) {
	if len(ids) > 1000 {
		return nil, infraerrors.BadRequest("TOO_MANY_IDS", "At most 1000 ids are allowed")
	}
	out := make([]int64, 0, len(ids))
	seen := map[int64]bool{}
	for _, id := range ids {
		if id <= 0 {
			return nil, infraerrors.BadRequest("INVALID_ID", "Invalid id")
		}
		if !seen[id] {
			out = append(out, id)
			seen[id] = true
		}
	}
	return out, nil
}
func (s *SchedAdminService) Summary(ctx context.Context, ids []int64) (SchedAdminSummary, error) {
	out := SchedAdminSummary{Accounts: []SchedAdminAccountSummary{}}
	ids, err := schedAdminIDs(ids)
	if err != nil {
		return out, err
	}
	p, now := s.snapshot(), s.d.Now()
	out.At, out.Stale, out.GlobalEnabled = schedAdminStamp(p.At), now.Sub(p.At) > schedquality.ViewMaxAge, p.GlobalEnabled
	if len(ids) == 0 {
		return out, nil
	}
	accounts, err := s.d.Accounts.GetByIDs(ctx, ids)
	if err != nil {
		return out, err
	}
	byID := map[int64]*Account{}
	for _, a := range accounts {
		if a != nil {
			byID[a.ID] = a
		}
	}
	for _, id := range ids {
		a := byID[id]
		if a == nil {
			continue
		}
		x, err := s.accountSummary(ctx, p, a, now)
		if err != nil {
			return out, err
		}
		out.Accounts = append(out.Accounts, x)
	}
	return out, nil
}
func (s *SchedAdminService) accountSummary(ctx context.Context, p *SchedQualitySnapshot, a *Account, now time.Time) (SchedAdminAccountSummary, error) {
	out := SchedAdminAccountSummary{AccountID: a.ID, Status: "normal", ResetAt: schedAdminTime(p.ResetAt[a.ID]), Substandard: []SchedAdminSubstandard{}, Exemptions: s.exemptions(ctx, p, a, now)}
	out.TTFO.Short, out.Error.Short, out.Cache.Short = true, true, true
	if a.Platform == PlatformOpenAI {
		v := p.Account(a.ID)
		out.TTFO = SchedAdminAccountSummaryTTFO{SchedAdminMark: SchedAdminMark{Short: v.FirstOutputP50Ms == nil}, P50Ms: v.FirstOutputP50Ms}
		out.Error.SchedAdminMark = SchedAdminMark{N: v.ErrorN, Short: v.ErrorN < schedquality.ErrorMinClear}
		if v.ErrorN > 0 {
			out.Error.Rate = schedAdminFloat(float64(v.ErrorBad) / float64(v.ErrorN))
		}
		out.Cache.SchedAdminMark = SchedAdminMark{N: v.CacheTurns, Short: v.CacheTurns < schedquality.CacheMinTurns}
		out.Cache.Ratio = v.CacheRatio
		for _, r := range p.RowsForAccount(a.ID) {
			if !schedAdminMember(a, r.GroupID) || len(schedRearLines(r.View)) == 0 {
				continue
			}
			exempt := schedAdminExemption(out.Exemptions, r.GroupID) != nil
			effective := schedAdminBelow(p, a.ID, r.GroupID, exempt, schedAdminEffective(p, now, p.Policies[r.GroupID].Policy.Enabled))
			out.Substandard = append(out.Substandard, SchedAdminSubstandard{GroupID: r.GroupID, Model: r.Model, Dims: append([]schedquality.Dimension{}, schedRearLines(r.View)...), Until: schedAdminStamp(schedStateUntil(r.View)), OtherGroup: len(r.SourceGroups) > 0, Exempt: exempt, Effective: effective})
			for _, d := range schedRearLines(r.View) {
				switch string(d) {
				case "ttfo":
					out.TTFO.Red = true
				case "error":
					out.Error.Red = true
				case "cache":
					out.Cache.Red = true
				}
			}
		}
		slices.SortFunc(out.Substandard, func(a, b SchedAdminSubstandard) int {
			if a.GroupID != b.GroupID {
				return cmp.Compare(a.GroupID, b.GroupID)
			}
			return cmp.Compare(a.Model, b.Model)
		})
		if out.TTFO.Short && len(out.Substandard) == 0 {
			out.Status = "observing"
		}
	}
	states, err := s.autoStates(ctx, a, now)
	if err != nil {
		return out, err
	}
	out.Stop = schedAdminStop(states)
	if out.Stop != nil {
		out.Status = "disabled"
	}
	return out, nil
}

// Single-model restrictions and untriggered counters do not stop the entire account.
func schedAdminStop(states []SchedAutoState) *SchedAdminStop {
	order := []string{"manual_off", "error", "temp_transient", "temp_post_output", "temp_balance", "temp_usage", "temp_rule", "temp_403", "temp_auth", "temp_health", "temp_stream_timeout", "rate_limit", "overload", "expired", "quota", "quota_auto_pause", "parent_blocked", "same_site", "post_output", "proxy_circuit"}
	for _, kind := range order {
		for _, v := range states {
			if v.Kind != kind || v.Scope == "model" || (v.Count != nil && v.Count[0] < v.Count[1]) {
				continue
			}
			out := &SchedAdminStop{Source: "auto", Reason: kind}
			if kind == "manual_off" {
				out.Source = "manual"
			}
			if v.Until != nil && kind != "expired" {
				out.Until = schedAdminTime(*v.Until)
			}
			return out
		}
	}
	return nil
}

type schedAdminAttentionResult struct {
	attention SchedAdminAttention
	normal    []int64
}

func (s *SchedAdminService) attention(ctx context.Context) (schedAdminAttentionResult, error) {
	out := schedAdminAttentionResult{attention: SchedAdminAttention{Substandard: []int64{}, Exempt: []int64{}, Observing: []int64{}}, normal: []int64{}}
	ids, err := s.d.Accounts.SchedAdminAccountIDs(ctx)
	if err != nil {
		return out, err
	}
	// Use one snapshot for all batches; no page can mix evaluation generations.
	p, now := s.snapshot(), s.d.Now()
	for start := 0; start < len(ids); start += 1000 {
		accounts, err := s.d.Accounts.GetByIDs(ctx, ids[start:min(start+1000, len(ids))])
		if err != nil {
			return out, err
		}
		for _, a := range accounts {
			if a == nil {
				continue
			}
			x, err := s.accountSummary(ctx, p, a, now)
			if err != nil {
				return out, err
			}
			if x.Status == "observing" {
				out.attention.Observing = append(out.attention.Observing, a.ID)
			}
			if len(x.Exemptions) > 0 {
				out.attention.Exempt = append(out.attention.Exempt, a.ID)
			}
			below := x.Stop == nil && schedAdminHasEffectiveSubstandard(x)
			if below {
				out.attention.Substandard = append(out.attention.Substandard, a.ID)
			}
			if a.Platform == PlatformOpenAI && x.Status == "normal" && len(x.Exemptions) == 0 && !below {
				out.normal = append(out.normal, a.ID)
			}
		}
	}
	slices.Sort(out.attention.Substandard)
	slices.Sort(out.attention.Exempt)
	slices.Sort(out.attention.Observing)
	slices.Sort(out.normal)
	out.attention.Substandard = slices.Compact(out.attention.Substandard)
	out.attention.Exempt = slices.Compact(out.attention.Exempt)
	out.attention.Observing = slices.Compact(out.attention.Observing)
	out.normal = slices.Compact(out.normal)
	return out, nil
}

func schedAdminHasEffectiveSubstandard(summary SchedAdminAccountSummary) bool {
	for _, r := range summary.Substandard {
		if r.Effective {
			return true
		}
	}
	return false
}

func (s *SchedAdminService) Attention(ctx context.Context) (SchedAdminAttention, error) {
	out, err := s.attention(ctx)
	return out.attention, err
}

func (s *SchedAdminService) AccountFilter(ctx context.Context, quality string) (SchedQualityFilter, error) {
	out, err := s.attention(ctx)
	if err != nil {
		return SchedQualityFilter{}, err
	}
	return out.attention.Filter(quality, out.normal), nil
}
func (s *SchedAdminService) Detail(ctx context.Context, id int64) (SchedAdminAccountDetail, error) {
	out := SchedAdminAccountDetail{Groups: []SchedAdminAccountDetailGroups{}, AutoStates: []SchedAdminAccountDetailAutoStates{}, Exemptions: []SchedAdminExemption{}}
	a, err := s.account(ctx, id)
	if err != nil {
		return out, err
	}
	p, now := s.snapshot(), s.d.Now()
	summary, err := s.accountSummary(ctx, p, a, now)
	if err != nil {
		return out, err
	}
	out.At, out.Stale, out.GlobalEnabled = schedAdminStamp(p.At), now.Sub(p.At) > schedquality.ViewMaxAge, p.GlobalEnabled
	out.Status, out.Stop, out.ResetAt, out.Exemptions = summary.Status, summary.Stop, summary.ResetAt, summary.Exemptions
	rate, _ := openAISchedulingRate(a, now, s.d.Gateway.openAIOAuthSchedulingRateMultiplier(ctx))
	out.Account = SchedAdminAccountDetailAccount{ID: id, Name: a.Name, Priority: a.Priority, Rate: rate, Concurrency: a.Concurrency}
	if s.d.InFlight != nil {
		out.Account.InFlight, err = s.d.InFlight(ctx, id)
		if err != nil {
			return out, err
		}
	}
	for _, g := range a.Groups {
		if g == nil || g.Platform != PlatformOpenAI {
			continue
		}
		x := SchedAdminAccountDetailGroups{GroupID: g.ID, Name: g.Name, Policy: schedAdminPolicyFields(schedAdminGroupPolicy(p, g.ID)), Exemption: schedAdminExemption(out.Exemptions, g.ID), ByModel: []SchedAdminAccountDetailGroupsByModel{}}
		for _, r := range p.RowsForAccount(id) {
			if r.GroupID == g.ID {
				x.ByModel = append(x.ByModel, SchedAdminGroupModel(r, x.Exemption != nil))
			}
		}
		slices.SortFunc(x.ByModel, func(a, b SchedAdminAccountDetailGroupsByModel) int { return cmp.Compare(a.Model, b.Model) })
		out.Groups = append(out.Groups, x)
	}
	slices.SortFunc(out.Groups, func(a, b SchedAdminAccountDetailGroups) int { return cmp.Compare(a.GroupID, b.GroupID) })
	states, err := s.autoStates(ctx, a, now)
	if err != nil {
		return out, err
	}
	for _, v := range states {
		x := SchedAdminAccountDetailAutoStates{Key: v.Key, Kind: v.Kind, Scope: v.Scope, Model: v.Model, Count: v.Count, Shared: v.Shared, Clearable: v.Clearable, Why: v.Why}
		if v.Until != nil {
			x.Until = schedAdminTime(*v.Until)
		}
		out.AutoStates = append(out.AutoStates, x)
	}
	return out, nil
}

// SchedAdminGroupModel temporarily projects the new evidence into the existing
// response. The admin package supplies the full per-line API at integration.
func SchedAdminGroupModel(r SchedGroupRow, exempt bool) SchedAdminAccountDetailGroupsByModel {
	out := SchedAdminAccountDetailGroupsByModel{Model: r.Model, Failing: len(schedRearLines(r.View)) > 0, Dims: schedRearLines(r.View), OtherGroupIDs: r.SourceGroups, Exempt: exempt, Level: max(r.View.TTFO.Level, max(r.View.Error.Level, r.View.Cache.Level))}
	out.Until = schedAdminTime(schedStateUntil(r.View))
	verdict := r.TTFTVerdict
	out.TTFO = SchedAdminCountDetail{SchedAdminDimDetail: SchedAdminDimDetail{Scope: "merged", WindowMin: 240, N: int(r.TTFT.N), Verdict: verdict.String()}, Bad: int(r.TTFT.K)}
	out.Error = SchedAdminCountDetail{SchedAdminDimDetail: SchedAdminDimDetail{Scope: "merged", WindowMin: 60, N: r.Error.Clear, LCB: schedAdminFloat(r.Error.LowerFail), Verdict: r.Error.Verdict.String()}, Bad: r.Error.Failed}
	out.Cache = SchedAdminCacheDetail{SchedAdminDimDetail: SchedAdminDimDetail{Scope: "merged", WindowMin: 30, N: r.Cache.Turns, Verdict: r.Cache.Verdict.String()}, Ratio: schedAdminFloat(r.Cache.Ratio)}
	return out
}
