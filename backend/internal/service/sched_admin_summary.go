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
		out.TTFO = SchedAdminAccountSummaryTTFO{SchedAdminMark: SchedAdminMark{N: v.Speed.N, Short: v.Speed.N < schedquality.SpeedMinSamples}, P50Ms: v.Speed.P50Ms}
		out.Error.SchedAdminMark = SchedAdminMark{N: v.Error.N, Short: v.Error.N < schedquality.ErrorMinSamples}
		if v.Error.N > 0 {
			out.Error.Rate = schedAdminFloat(v.Error.Rate)
		}
		out.Cache.SchedAdminMark = SchedAdminMark{N: v.Cache.Turns, Short: v.Cache.Turns < schedquality.CacheMinSamples}
		if v.Cache.InputTokens > 0 {
			out.Cache.Ratio = schedAdminFloat(v.Cache.Ratio)
		}
		for _, r := range p.RowsForAccount(a.ID) {
			if !schedAdminMember(a, r.GroupID) || !r.View.Failing {
				continue
			}
			out.Substandard = append(out.Substandard, SchedAdminSubstandard{GroupID: r.GroupID, Model: r.Model, Dims: append([]schedquality.Dimension{}, r.View.Dims...), Until: schedAdminStamp(r.View.Until), OtherGroup: len(r.OtherGroupIDs) > 0, Exempt: schedAdminExemption(out.Exemptions, r.GroupID) != nil})
			for _, d := range r.View.Dims {
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
func (s *SchedAdminService) Attention(ctx context.Context) (SchedAdminAttention, error) {
	out := SchedAdminAttention{Substandard: []int64{}, Exempt: []int64{}, Observing: []int64{}}
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
			x, err := s.accountSummary(ctx, p, a, now)
			if err != nil {
				return out, err
			}
			if x.Status == "observing" {
				out.Observing = append(out.Observing, a.ID)
			}
			if len(x.Exemptions) > 0 {
				out.Exempt = append(out.Exempt, a.ID)
			}
			for _, r := range x.Substandard {
				enabled := schedAdminEffective(p, now, p.Policies[r.GroupID].Policy.Enabled)
				if x.Stop == nil && schedAdminBelow(p, a.ID, r.GroupID, r.Exempt, enabled) {
					out.Substandard = append(out.Substandard, a.ID)
					break
				}
			}
		}
	}
	slices.Sort(out.Substandard)
	slices.Sort(out.Exempt)
	slices.Sort(out.Observing)
	out.Substandard = slices.Compact(out.Substandard)
	out.Exempt = slices.Compact(out.Exempt)
	out.Observing = slices.Compact(out.Observing)
	return out, nil
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
func schedAdminDim(w schedquality.EvidenceWindow, n int, lcb *float64, v schedquality.Verdict) SchedAdminDimDetail {
	verdict := "short"
	if v == schedquality.VerdictPass {
		verdict = "pass"
	}
	if v == schedquality.VerdictFail {
		verdict = "fail"
	}
	scope := "own"
	if w.Source != schedquality.EvidenceLocal {
		scope = "merged"
	}
	return SchedAdminDimDetail{Scope: scope, WindowMin: int(w.Window / time.Minute), N: n, LCB: lcb, Verdict: verdict}
}

// SchedAdminGroupModel is shared with MCP so both readers expose the same evidence.
func SchedAdminGroupModel(r SchedGroupRow, exempt bool) SchedAdminAccountDetailGroupsByModel {
	v, w := r.Evaluation, r.View
	out := SchedAdminAccountDetailGroupsByModel{Model: r.Model, Failing: w.Failing, Dims: append([]schedquality.Dimension{}, w.Dims...), Level: w.Level, OtherGroupIDs: append([]int64{}, r.OtherGroupIDs...), Exempt: exempt}
	if w.Failing {
		out.Since, out.Until = schedAdminTime(w.Since), schedAdminTime(w.Until)
	} else {
		out.Dims = []schedquality.Dimension{}
		out.OtherGroupIDs = []int64{}
	}
	var speedLCB, errorLCB, ratio *float64
	if v.Speed.N > 0 {
		speedLCB = schedAdminFloat(v.Speed.LCB)
	}
	if v.Error.N > 0 {
		errorLCB = schedAdminFloat(v.Error.LCB)
	}
	if v.Cache.InputTokens > 0 {
		ratio = schedAdminFloat(v.Cache.Ratio)
	}
	out.TTFO = SchedAdminCountDetail{SchedAdminDimDetail: schedAdminDim(v.Speed.EvidenceWindow, v.Speed.N, speedLCB, v.Verdicts.Speed), Bad: v.Speed.Slow}
	out.Error = SchedAdminCountDetail{SchedAdminDimDetail: schedAdminDim(v.Error.EvidenceWindow, v.Error.N, errorLCB, v.Verdicts.Error), Bad: v.Error.Bad}
	out.Cache = SchedAdminCacheDetail{SchedAdminDimDetail: schedAdminDim(v.Cache.EvidenceWindow, v.Cache.Turns, nil, v.Verdicts.Cache), Ratio: ratio}
	return out
}
