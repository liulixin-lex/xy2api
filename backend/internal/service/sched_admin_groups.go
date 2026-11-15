package service

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
)

func (s *SchedAdminService) groupAccounts(ctx context.Context, ids []int64) ([]*Account, error) {
	memberIDs, err := s.d.Groups.GetAccountIDsByGroupIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	if len(memberIDs) == 0 {
		return []*Account{}, nil
	}
	return s.d.Accounts.GetByIDs(ctx, memberIDs)
}
func schedAdminEffective(p *SchedQualitySnapshot, now time.Time, enabled bool) bool {
	return p.GlobalEnabled && now.Sub(p.At) <= schedquality.ViewMaxAge && enabled
}
func schedAdminBelow(p *SchedQualitySnapshot, id, g int64, exempt bool, enabled bool) bool {
	if exempt || !enabled {
		return false
	}
	for _, r := range p.RowsForAccount(id) {
		if r.GroupID == g && r.View.Failing {
			return true
		}
	}
	return false
}
func (s *SchedAdminService) GroupsSummary(ctx context.Context, ids []int64) (SchedAdminGroupsSummary, error) {
	out := SchedAdminGroupsSummary{Groups: []SchedAdminGroupSummary{}}
	ids, err := schedAdminIDs(ids)
	if err != nil {
		return out, err
	}
	p, now := s.snapshot(), s.d.Now()
	out.At, out.Stale = schedAdminStamp(p.At), now.Sub(p.At) > schedquality.ViewMaxAge
	if len(ids) == 0 {
		return out, nil
	}
	groups := []int64{}
	for _, id := range ids {
		_, err := s.group(ctx, id)
		if errors.Is(err, ErrGroupNotFound) {
			continue
		}
		if err != nil {
			return out, err
		}
		groups = append(groups, id)
	}
	if len(groups) == 0 {
		return out, nil
	}
	accounts, err := s.groupAccounts(ctx, groups)
	if err != nil {
		return out, err
	}
	stats, err := s.d.Usage.GroupLast60(ctx, groups, now)
	if err != nil {
		return out, err
	}
	summary := map[int64]SchedAdminAccountSummary{}
	for _, a := range accounts {
		x, err := s.accountSummary(ctx, p, a, now)
		if err != nil {
			return out, err
		}
		summary[a.ID] = x
	}
	for _, g := range groups {
		policy := schedAdminGroupPolicy(p, g)
		x := SchedAdminGroupSummary{GroupID: g, SchedAdminPolicyPolicy: schedAdminPolicyFields(policy)}
		v := stats[g]
		x.Last60 = SchedAdminGroupLast60{TTFOP50Ms: v.TTFOP50Ms, FailedRate: v.FailedRate, CacheRatio: v.CacheRatio, Requests: v.Requests}
		for _, a := range accounts {
			if !schedAdminMember(a, g) {
				continue
			}
			v := summary[a.ID]
			switch {
			case v.Stop != nil:
				x.Accounts.Stopped++
			case schedAdminBelow(p, a.ID, g, schedAdminExemption(v.Exemptions, g) != nil, schedAdminEffective(p, now, policy.Enabled)):
				x.Accounts.Below++
			default:
				x.Accounts.Normal++
			}
		}
		out.Groups = append(out.Groups, x)
	}
	return out, nil
}
func (s *SchedAdminService) PreviewPolicy(ctx context.Context, g, version int64, policy schedquality.Policy) (SchedAdminPolicyPreview, error) {
	out := SchedAdminPolicyPreview{Changed: []SchedAdminPolicyChange{}}
	if version < 0 {
		return out, infraerrors.BadRequest("INVALID_SCHED_POLICY", "Invalid expected version")
	}
	if err := ValidateSchedQualityPolicy(policy); err != nil {
		return out, err
	}
	group, err := s.group(ctx, g)
	if err != nil {
		return out, err
	}
	if group.Platform != PlatformOpenAI {
		return out, infraerrors.BadRequest("INVALID_GROUP_PLATFORM", "Only OpenAI groups support scheduling policies")
	}
	current, err := s.d.Engine.Policy(ctx, g)
	if err != nil {
		return out, err
	}
	if current.Version != version {
		return out, ErrSchedPolicyConflict
	}
	changes, err := s.d.Engine.PreviewPolicy(ctx, g, policy)
	if err != nil {
		return out, err
	}
	p, now := s.snapshot(), s.d.Now()
	accounts, err := s.groupAccounts(ctx, []int64{g})
	if err != nil {
		return out, err
	}
	eligible := map[int64]*Account{}
	changed := map[int64]bool{}
	beforeEnabled := schedAdminEffective(p, now, current.Policy.Enabled)
	afterEnabled := schedAdminEffective(p, now, policy.Enabled)
	for _, a := range accounts {
		summary, err := s.accountSummary(ctx, p, a, now)
		if err != nil {
			return out, err
		}
		if summary.Stop != nil {
			continue
		}
		exempt := schedAdminExemption(summary.Exemptions, g) != nil
		if schedAdminBelow(p, a.ID, g, exempt, beforeEnabled) {
			out.Now.Below++
		} else {
			out.Now.Normal++
		}
		if schedAdminBelow(p, a.ID, g, exempt, afterEnabled) {
			out.After.Below++
			changed[a.ID] = true
		} else {
			out.After.Normal++
		}
		if !exempt && afterEnabled {
			eligible[a.ID] = a
		}
	}
	for _, c := range changes {
		a := eligible[c.AccountID]
		if a == nil {
			continue
		}
		out.Changed = append(out.Changed, SchedAdminPolicyChange{AccountID: a.ID, Name: a.Name, Model: c.Model, Dims: append([]schedquality.Dimension{}, c.Dims...)})
		if !changed[a.ID] {
			out.After.Normal--
			out.After.Below++
			changed[a.ID] = true
		}
	}
	slices.SortFunc(out.Changed, func(a, b SchedAdminPolicyChange) int {
		if a.AccountID != b.AccountID {
			return cmp.Compare(a.AccountID, b.AccountID)
		}
		return cmp.Compare(a.Model, b.Model)
	})
	return out, nil
}
func (s *SchedAdminService) Order(ctx context.Context, g int64, model string) (SchedAdminOrder, error) {
	out := SchedAdminOrder{GroupID: g, Models: []string{}, Rows: []SchedAdminOrderRow{}}
	if _, err := s.group(ctx, g); err != nil {
		return out, err
	}
	p, now := s.snapshot(), s.d.Now()
	out.At, out.Stale, out.GlobalEnabled = schedAdminStamp(p.At), now.Sub(p.At) > schedquality.ViewMaxAge, p.GlobalEnabled
	for _, m := range p.GroupModels[g] {
		out.Models = append(out.Models, m.Model)
	}
	out.Model = strings.TrimSpace(model)
	if out.Model == "" && len(out.Models) > 0 {
		out.Model = out.Models[0]
	}
	if out.Model == "" {
		return out, nil
	}
	rows, err := s.d.Board.Board(ctx, g, out.Model)
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		x := SchedAdminOrderRow{Rank: r.Rank, AccountID: r.AccountID, Name: r.Name, Layer: r.Layer, Dims: append([]schedquality.Dimension{}, r.Dims...), Exempt: r.Exempt, Priority: r.Priority, Rate: r.Rate, LoadRate: r.LoadRate, InFlight: r.InFlight, Concurrency: r.Concurrency}
		if r.Until != nil {
			x.Until = schedAdminTime(*r.Until)
		}
		if r.LastUsedAt != nil {
			x.LastUsedAt = schedAdminTime(*r.LastUsedAt)
		}
		out.Rows = append(out.Rows, x)
	}
	return out, nil
}
