package service

import (
	"cmp"
	"context"
	"errors"
	"slices"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
)

type SchedAdminPerformance struct {
	At            string              `json:"at"`
	WindowFrom    string              `json:"window_from"`
	WindowHours   int                 `json:"window_hours"`
	Complete      bool                `json:"complete"`
	Stale         bool                `json:"stale"`
	GlobalEnabled bool                `json:"global_enabled"`
	Groups        []schedPerfGroup    `json:"groups"`
	Rows          []schedAdminPerfRow `json:"rows"`
}
type schedPerfGroup struct {
	GroupID       int64   `json:"group_id"`
	Name          string  `json:"name"`
	GateEnabled   bool    `json:"gate_enabled"`
	TTFTSeconds   float64 `json:"ttft_seconds"`
	MinCacheRatio float64 `json:"min_cache_ratio"`
}
type schedAdminPerfRow struct {
	schedPerfRow
	GroupID   int64          `json:"group_id"`
	AccountID int64          `json:"account_id"`
	Name      string         `json:"name"`
	Model     string         `json:"model"`
	Priority  int            `json:"priority"`
	Member    bool           `json:"member"`
	Score     schedPerfScore `json:"score"`
	Lines     schedPerfLines `json:"lines"`
}
type schedPerfLines struct {
	TTFO  schedPerfLine `json:"ttfo"`
	Error schedPerfLine `json:"error"`
	Cache schedPerfLine `json:"cache"`
}
type schedPerfLine struct {
	q.SampleCounts
	Identity string              `json:"identity"`
	Until    *string             `json:"until"`
	LastEval *SchedAdminLastEval `json:"last_eval"`
}

func perfLine(v q.LineView, counts q.SampleCounts, last *SchedAdminLastEval) schedPerfLine {
	until := v.Until
	if v.Trial {
		until = v.TrialUntil
	}
	return schedPerfLine{Identity: schedAdminIdentity(v), Until: schedAdminTime(until), SampleCounts: counts, LastEval: last}
}

func (s *SchedQualitySnapshot) performanceLines(key schedQualityKey) schedPerfLines {
	// The short-window publisher orders each account's rows by group and model.
	rows := s.RowsForAccount(key.account)
	i, found := slices.BinarySearchFunc(rows, key, func(row SchedGroupRow, key schedQualityKey) int {
		return cmp.Or(cmp.Compare(row.GroupID, key.group), cmp.Compare(row.Model, key.model))
	})
	var row SchedGroupRow
	if found {
		row = rows[i]
	}
	return schedPerfLines{
		TTFO:  perfLine(row.View.TTFO, row.TTFTSamples, row.TTFTLastEval),
		Error: perfLine(row.View.Error, row.ErrorSamples, row.ErrorLastEval),
		Cache: perfLine(row.View.Cache, row.CacheSamples, row.CacheLastEval),
	}
}

func (s *SchedAdminService) GroupPerformance(ctx context.Context, id int64) (SchedAdminPerformance, error) {
	g, err := s.group(ctx, id)
	if err != nil {
		return SchedAdminPerformance{}, err
	}
	return s.performance(ctx, g, nil)
}
func (s *SchedAdminService) AccountPerformance(ctx context.Context, id int64) (SchedAdminPerformance, error) {
	a, err := s.account(ctx, id)
	if err != nil {
		return SchedAdminPerformance{}, err
	}
	return s.performance(ctx, nil, a)
}

func (s *SchedAdminService) performance(ctx context.Context, group *Group, account *Account) (SchedAdminPerformance, error) {
	p, quality, now := s.d.Engine.PerformanceSnapshot(), s.snapshot(), s.d.Now()
	out := SchedAdminPerformance{WindowHours: 12, Groups: []schedPerfGroup{}, Rows: []schedAdminPerfRow{}, Stale: true, GlobalEnabled: quality.GlobalEnabled}
	if p == nil {
		return out, nil
	}
	out.At, out.WindowFrom, out.Complete = schedAdminStamp(p.at), schedAdminStamp(p.from), p.complete
	out.Stale = now.Sub(p.at) > 2*schedPerfTick
	keys := make([]schedQualityKey, 0)
	accounts := make(map[int64]*Account)
	groups := make(map[int64]*Group)
	if group != nil {
		groups[group.ID] = group
	}
	if account != nil {
		accounts[account.ID] = account
		if p.complete {
			// A member group can have a publication policy even when it has no counted rows.
			for _, g := range account.Groups {
				if g != nil && g.Platform == PlatformOpenAI {
					groups[g.ID] = g
				}
			}
		}
	}
	for k := range p.rows {
		if group != nil && group.ID != k.group || account != nil && account.ID != k.account {
			continue
		}
		keys = append(keys, k)
	}
	slices.SortFunc(keys, perfKeyOrder)
	if group != nil {
		ids := make([]int64, 0, len(keys))
		for _, k := range keys {
			ids = append(ids, k.account)
		}
		rows, err := s.d.Accounts.GetByIDs(ctx, ids)
		if err != nil {
			return out, err
		}
		for _, a := range rows {
			if a != nil {
				accounts[a.ID] = a
			}
		}
	}
	for _, k := range keys {
		if _, ok := groups[k.group]; !ok {
			g, err := s.group(ctx, k.group)
			if err != nil && !errors.Is(err, ErrGroupNotFound) {
				return out, err
			}
			groups[k.group] = g
		}
		r := schedAdminPerfRow{schedPerfRow: *p.rows[k], GroupID: k.group, AccountID: k.account, Model: k.model, Score: p.scores[SchedAccountModel{k.account, k.model}]}
		if a := accounts[k.account]; a != nil {
			r.Name, r.Priority, r.Member = a.Name, a.Priority, schedAdminMember(a, k.group)
		}
		r.Lines = quality.performanceLines(k)
		out.Rows = append(out.Rows, r)
	}
	for id, g := range groups {
		policy := schedAdminGroupPolicy(quality, id)
		if p.complete {
			policy = q.DefaultPolicy()
			if record, ok := p.policies[id]; ok {
				policy = record.Policy
			}
		}
		row := schedPerfGroup{GroupID: id, TTFTSeconds: policy.TTFTSeconds, GateEnabled: policy.Enabled, MinCacheRatio: policy.MinCacheRatio}
		if g != nil {
			row.Name = g.Name
		}
		out.Groups = append(out.Groups, row)
	}
	slices.SortFunc(out.Groups, func(a, b schedPerfGroup) int { return cmp.Compare(a.GroupID, b.GroupID) })
	return out, nil
}
