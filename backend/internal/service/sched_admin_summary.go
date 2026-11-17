package service

import (
	"cmp"
	"context"
	"slices"
	"strconv"
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
	out := SchedAdminAccountSummary{AccountID: a.ID, GroupStatus: map[string]string{}, ResetAt: schedAdminTime(p.ResetAt[a.ID]), Rear: []SchedAdminRear{}, Trial: []SchedAdminTrial{}, Exemptions: s.exemptions(ctx, p, a, now)}
	out.Error.Short = true
	if a.Platform == PlatformOpenAI {
		known := map[int64]bool{}
		for _, g := range a.Groups {
			if g != nil {
				known[g.ID] = true
				if g.Platform == PlatformOpenAI {
					out.GroupStatus[strconv.FormatInt(g.ID, 10)] = "unknown"
				}
			}
		}
		for _, id := range a.GroupIDs {
			if known[id] {
				continue
			}
			if s.d.Groups == nil {
				continue
			}
			g, err := s.group(ctx, id)
			if err != nil {
				return out, err
			}
			if g.Platform == PlatformOpenAI {
				out.GroupStatus[strconv.FormatInt(id, 10)] = "unknown"
			}
		}
		v := p.Account(a.ID)
		out.TTFO = SchedAdminAccountSummaryTTFO{FirstEventP50Ms: v.FirstEventP50Ms, FirstOutputP50Ms: v.FirstOutputP50Ms, GapP50Ms: v.GapP50Ms, N: v.FirstOutputN}
		out.Error.SchedAdminMark = SchedAdminMark{N: v.ErrorN, Short: v.ErrorN < schedquality.ErrorMinClear}
		if v.ErrorN > 0 {
			out.Error.Rate = schedAdminFloat(float64(v.ErrorBad) / float64(v.ErrorN))
		}
		out.Cache = SchedAdminAccountSummaryCache{Ratio: v.CacheRatio, Turns: v.CacheTurns, AllRatio: v.CacheAllRatio, AllRequests: v.CacheAllRequests}
		for _, r := range p.RowsForAccount(a.ID) {
			key := strconv.FormatInt(r.GroupID, 10)
			if _, ok := out.GroupStatus[key]; !ok {
				continue
			}
			exempt := schedAdminExemption(out.Exemptions, r.GroupID) != nil
			effective := schedAdminEffective(p, now, schedAdminGroupPolicy(p, r.GroupID).Enabled)
			status := schedAdminGroupStatus(r.View, exempt, effective)
			if schedAdminStatusRank(status) > schedAdminStatusRank(out.GroupStatus[key]) {
				out.GroupStatus[key] = status
			}
			rear := schedRearLines(r.View)
			if len(rear) > 0 {
				out.Rear = append(out.Rear, SchedAdminRear{GroupID: r.GroupID, Model: r.Model, Lines: append([]schedquality.Dimension{}, rear...), Until: schedAdminStamp(schedAdminRearUntil(r.View)), Exempt: exempt, Effective: !exempt && effective})
			}
			if r.View.Error.Identity == schedquality.IdentityRear {
				out.Error.Red = true
			}
			if len(rear) == 0 && !exempt && effective {
				var until time.Time
				for _, l := range []schedquality.LineView{r.View.TTFO, r.View.Error, r.View.Cache} {
					if l.Trial && (l.TrialUntil.After(until)) {
						until = l.TrialUntil
					}
				}
				if !until.IsZero() {
					out.Trial = append(out.Trial, SchedAdminTrial{GroupID: r.GroupID, Model: r.Model, Until: schedAdminStamp(until)})
				}
			}
		}
		slices.SortFunc(out.Rear, func(a, b SchedAdminRear) int {
			if a.GroupID != b.GroupID {
				return cmp.Compare(a.GroupID, b.GroupID)
			}
			return cmp.Compare(a.Model, b.Model)
		})
		slices.SortFunc(out.Trial, func(a, b SchedAdminTrial) int {
			if a.GroupID != b.GroupID {
				return cmp.Compare(a.GroupID, b.GroupID)
			}
			return cmp.Compare(a.Model, b.Model)
		})
	}
	states, err := s.autoStates(ctx, a, now)
	if err != nil {
		return out, err
	}
	out.Stop = schedAdminStop(states)
	return out, nil
}

func schedAdminStatusRank(s string) int {
	switch s {
	case "rear":
		return 3
	case "trial":
		return 2
	case "pass":
		return 1
	}
	return 0
}

// Dots and filters share the effective state, while line details retain raw evidence.
func schedAdminGroupStatus(v schedquality.StateView, exempt, effective bool) string {
	if !exempt && effective {
		for _, l := range []schedquality.LineView{v.TTFO, v.Error, v.Cache} {
			if l.Identity == schedquality.IdentityRear {
				return "rear"
			}
		}
		for _, l := range []schedquality.LineView{v.TTFO, v.Error, v.Cache} {
			if l.Trial {
				return "trial"
			}
		}
	}
	for _, l := range []schedquality.LineView{v.TTFO, v.Error, v.Cache} {
		if l.Identity == schedquality.IdentityPass {
			return "pass"
		}
	}
	return "unknown"
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
	out := schedAdminAttentionResult{attention: SchedAdminAttention{Rear: []int64{}, Trial: []int64{}, Unknown: []int64{}, Exempt: []int64{}}, normal: []int64{}}
	ids, err := s.d.Accounts.SchedAdminAccountIDs(ctx)
	if err != nil {
		return out, err
	}
	p, now := s.snapshot(), s.d.Now()
	for start := 0; start < len(ids); start += 1000 {
		accounts, err := s.d.Accounts.GetByIDs(ctx, ids[start:min(start+1000, len(ids))])
		if err != nil {
			return out, err
		}
		for _, a := range accounts {
			if a == nil || a.Platform != PlatformOpenAI {
				continue
			}
			x, err := s.accountSummary(ctx, p, a, now)
			if err != nil {
				return out, err
			}
			if len(x.Exemptions) > 0 {
				out.attention.Exempt = append(out.attention.Exempt, a.ID)
			}
			has := map[string]bool{}
			allPass := len(x.GroupStatus) > 0
			for _, status := range x.GroupStatus {
				has[status] = true
				allPass = allPass && status == "pass"
			}
			if x.Stop == nil && has["rear"] {
				out.attention.Rear = append(out.attention.Rear, a.ID)
			}
			if has["trial"] {
				out.attention.Trial = append(out.attention.Trial, a.ID)
			}
			if has["unknown"] {
				out.attention.Unknown = append(out.attention.Unknown, a.ID)
			}
			if x.Stop == nil && allPass {
				out.normal = append(out.normal, a.ID)
			}
		}
	}
	for _, ids := range []*[]int64{&out.attention.Rear, &out.attention.Trial, &out.attention.Unknown, &out.attention.Exempt, &out.normal} {
		slices.Sort(*ids)
		*ids = slices.Compact(*ids)
	}
	return out, nil
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
	out.Stop, out.ResetAt, out.Exemptions = summary.Stop, summary.ResetAt, summary.Exemptions
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

func schedAdminIdentity(v schedquality.LineView) string {
	if v.Trial {
		return "trial"
	}
	switch v.Identity {
	case schedquality.IdentityRear:
		return "rear"
	case schedquality.IdentityPass:
		return "pass"
	}
	return "unknown"
}
func schedAdminLine[E any](v schedquality.LineView, verdict schedquality.Verdict, e E, groups []int64) SchedAdminLine[E] {
	return SchedAdminLine[E]{Identity: schedAdminIdentity(v), Level: v.Level, Until: schedAdminTime(v.Until), TrialUntil: schedAdminTime(v.TrialUntil), Verdict: verdict.String(), Evidence: e, SourceGroups: append([]int64{}, groups...)}
}
func schedAdminErrorEvidence(e schedquality.ErrorAssessment) SchedAdminErrorEvidence {
	return SchedAdminErrorEvidence{Limit: e.Limit, Clear: e.Clear, Failed: e.Failed, Unknown: e.Unknown, Padded: e.Padded, LowerFail: e.LowerFail, LowerPass: e.LowerPass}
}
func schedAdminCacheEvidence(e schedquality.CacheAssessment) SchedAdminCacheEvidence {
	return SchedAdminCacheEvidence{MinRatio: e.MinRatio, Turns: e.Turns, Ratio: e.Ratio, Sessions: e.Sessions, LowSessions: e.LowSessions}
}

// SchedAdminGroupModel copies each line; it never mutates the published snapshot.
func SchedAdminGroupModel(r SchedGroupRow, exempt bool) SchedAdminAccountDetailGroupsByModel {
	placement := "front"
	if !exempt {
		for _, l := range []schedquality.LineView{r.View.TTFO, r.View.Error, r.View.Cache} {
			if l.Trial {
				placement = "trial"
			}
		}
		if len(schedRearLines(r.View)) > 0 {
			placement = "rear"
		}
	}
	ttft := r.TTFT
	ttft.Causes = append([]schedquality.RuleBUnknownReason{}, ttft.Causes...)
	return SchedAdminAccountDetailGroupsByModel{Model: r.Model, Placement: placement, Exempt: exempt, Lines: SchedAdminLines{
		TTFO:  schedAdminLine(r.View.TTFO, r.TTFTVerdict, ttft, r.TTFTSourceGroups),
		Error: schedAdminLine(r.View.Error, r.Error.Verdict, schedAdminErrorEvidence(r.Error), r.Error.SourceGroups),
		Cache: schedAdminLine(r.View.Cache, r.Cache.Verdict, schedAdminCacheEvidence(r.Cache), r.Cache.SourceGroups)}}
}

// Composite rear ends when the last rear line expires; a concurrent trial does not extend it.
func schedAdminRearUntil(v schedquality.StateView) time.Time {
	var until time.Time
	for _, l := range []schedquality.LineView{v.TTFO, v.Error, v.Cache} {
		if l.Identity == schedquality.IdentityRear && l.Until.After(until) {
			until = l.Until
		}
	}
	return until
}
