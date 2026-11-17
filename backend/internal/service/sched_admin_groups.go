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
	stats, err := s.groupLast60(ctx, groups, now)
	if err != nil {
		return out, err
	}
	for _, g := range groups {
		x := SchedAdminGroupSummary{GroupID: g, Last60: stats[g]}
		effective := schedAdminEffective(p, now, schedAdminGroupPolicy(p, g).Enabled)
		for _, a := range accounts {
			if a == nil || a.Platform != PlatformOpenAI || !schedAdminMember(a, g) {
				continue
			}
			exempt := schedAdminExemption(s.exemptions(ctx, p, a, now), g) != nil
			for _, r := range p.RowsForAccount(a.ID) {
				if r.GroupID != g {
					continue
				}
				switch schedAdminGroupStatus(r.View, exempt, effective) {
				case "rear":
					x.Accounts.Rear++
				case "trial":
					x.Accounts.Trial++
				case "pass":
					x.Accounts.Pass++
				default:
					x.Accounts.Unknown++
				}
			}
		}
		out.Groups = append(out.Groups, x)
	}
	return out, nil
}

// Serialize cache misses as well as writes, so concurrent boards share one query.
func (s *SchedAdminService) groupLast60(ctx context.Context, ids []int64, now time.Time) (map[int64]SchedAdminGroupLast60, error) {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	out := map[int64]SchedAdminGroupLast60{}
	missing := []int64{}
	for id, e := range s.stats {
		if now.Sub(e.at) >= 45*time.Second || now.Before(e.at) {
			delete(s.stats, id)
		}
	}
	for _, id := range ids {
		if e, ok := s.stats[id]; ok {
			out[id] = e.value
		} else {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		stats, err := s.d.Accounts.SchedAdminGroupLast60(ctx, missing, now)
		if err != nil {
			return nil, err
		}
		for _, id := range missing {
			value := stats[id]
			s.stats[id] = schedAdminStatsEntry{at: now, value: value}
			out[id] = value
		}
	}
	return out, nil
}
func (s *SchedAdminService) PreviewPolicy(ctx context.Context, g, version int64, policy schedquality.Policy) (SchedAdminPolicyPreview, error) {
	out := SchedAdminPolicyPreview{RelaxedLines: []schedquality.Dimension{}, Lines: map[schedquality.Dimension]SchedAdminPreviewLine{}, Changed: []SchedAdminPolicyChange{}}
	dims := []schedquality.Dimension{schedquality.DimensionTTFO, schedquality.DimensionError, schedquality.DimensionCache}
	for _, d := range dims {
		out.Lines[d] = SchedAdminPreviewLine{}
	}
	if version < 0 {
		return out, infraerrors.BadRequest("INVALID_SCHED_POLICY", "expected_version must be nonnegative")
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
	out.RelaxedLines = append(out.RelaxedLines, relaxedPolicyLines(current.Policy, policy)...)
	currentAssessments, err := s.d.Engine.PreviewPolicy(ctx, g, current.Policy)
	if err != nil {
		return out, err
	}
	changes, err := s.d.Engine.PreviewPolicy(ctx, g, policy)
	if err != nil {
		return out, err
	}
	accounts, err := s.groupAccounts(ctx, []int64{g})
	if err != nil {
		return out, err
	}
	names := map[int64]string{}
	for _, a := range accounts {
		if a != nil && schedAdminMember(a, g) {
			names[a.ID] = a.Name
		}
	}
	// Compare assessments made consecutively for this request, rather than a published snapshot.
	type accountModel struct {
		account int64
		model   string
	}
	beforeByKey := make(map[accountModel]map[schedquality.Dimension]schedquality.Verdict, len(currentAssessments))
	for _, c := range currentAssessments {
		beforeByKey[accountModel{c.AccountID, c.Model}] = c.Lines
	}
	for _, c := range changes {
		name, ok := names[c.AccountID]
		if !ok {
			continue
		}
		before := beforeByKey[accountModel{c.AccountID, c.Model}]
		for _, d := range dims {
			from, to := before[d], c.Lines[d]
			counts := out.Lines[d]
			schedAdminCountVerdict(&counts.Now, from)
			schedAdminCountVerdict(&counts.After, to)
			out.Lines[d] = counts
			if from != to {
				out.Changed = append(out.Changed, SchedAdminPolicyChange{AccountID: c.AccountID, Name: name, Model: c.Model, Line: d, From: from.String(), To: to.String()})
			}
		}
	}
	slices.SortFunc(out.Changed, func(a, b SchedAdminPolicyChange) int {
		if a.AccountID != b.AccountID {
			return cmp.Compare(a.AccountID, b.AccountID)
		}
		if a.Model != b.Model {
			return cmp.Compare(a.Model, b.Model)
		}
		return cmp.Compare(a.Line, b.Line)
	})
	if len(out.Changed) > 50 {
		out.Changed = out.Changed[:50]
	}
	return out, nil
}
func schedAdminCountVerdict(c *SchedAdminVerdictCounts, v schedquality.Verdict) {
	switch v {
	case schedquality.VerdictPass:
		c.Pass++
	case schedquality.VerdictFail:
		c.Fail++
	default:
		c.Unknown++
	}
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
		x := SchedAdminOrderRow{Rank: r.Rank, AccountID: r.AccountID, Name: r.Name, Layer: r.Layer, Lines: r.Lines, Pinned: r.Pinned, Exempt: r.Exempt, Priority: r.Priority, Rate: r.Rate, LoadRate: r.LoadRate, InFlight: r.InFlight, Concurrency: r.Concurrency}
		if r.Excluded != "" {
			excluded := r.Excluded
			x.Excluded = &excluded
		}
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
