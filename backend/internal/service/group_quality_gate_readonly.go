package service

import (
	"sort"
	"strconv"
	"time"
)

// GroupQualityReadSnapshot is a bounded copy of published process memory. It
// neither loads policies nor queries the business repositories.
type GroupQualityReadSnapshot struct {
	GeneratedAt   time.Time              `json:"generated_at"`
	Stale         bool                   `json:"stale"`
	StaleSince    time.Time              `json:"stale_since,omitempty"`
	PolicyVersion int                    `json:"policy_version"`
	Items         []GroupQualityReadItem `json:"items"`
	Truncated     bool                   `json:"truncated"`
}
type GroupQualityReadItem struct {
	GroupID int64                     `json:"group_id"`
	Account GroupQualityAccountView   `json:"account"`
	Policy  *GroupQualityGateV3Policy `json:"policy,omitempty"`
}

// PureSnapshot reads only the immutable atomic snapshot. The existing Status
// method hydrates account entities, so diagnostic callers must use this method.
// Snapshot pointers and policy members never escape without a deep copy.
func (s *GroupQualityGateService) PureSnapshot(groupID, accountID, afterGroupID, afterAccountID int64, limit int, includePolicies bool) *GroupQualityReadSnapshot {
	if s == nil || limit < 1 || limit > 100 {
		return nil
	}
	snap := s.snapshot.Load()
	if snap == nil {
		return nil
	}
	now := time.Now().UTC()
	out := &GroupQualityReadSnapshot{GeneratedAt: snap.GeneratedAt, Stale: snap.Stale || snap.GeneratedAt.IsZero() || now.Sub(snap.GeneratedAt) > 2*groupQualityEvaluationInterval, StaleSince: snap.StaleSince, PolicyVersion: snap.Policies.Version, Items: []GroupQualityReadItem{}}
	groups := make([]int64, 0, len(snap.Members))
	for id := range snap.Members {
		if groupID == 0 || id == groupID {
			groups = append(groups, id)
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i] < groups[j] })
	copyFloat := func(p *float64) *float64 {
		if p == nil {
			return nil
		}
		v := *p
		return &v
	}
	for _, gid := range groups {
		members := snap.Members[gid]
		accounts := make([]int64, 0, len(members))
		for id := range members {
			if accountID == 0 || id == accountID {
				accounts = append(accounts, id)
			}
		}
		sort.Slice(accounts, func(i, j int) bool { return accounts[i] < accounts[j] })
		for _, aid := range accounts {
			if afterGroupID > 0 && (gid < afterGroupID || (gid == afterGroupID && aid <= afterAccountID)) {
				continue
			}
			if len(out.Items) == limit {
				out.Truncated = true
				return out
			}
			view := qualityAccountView(members[aid], now)
			view.TTFT.Value = copyFloat(view.TTFT.Value)
			view.TTFT.Factor = copyFloat(view.TTFT.Factor)
			view.Error.Value = copyFloat(view.Error.Value)
			view.Error.WilsonLow = copyFloat(view.Error.WilsonLow)
			view.Error.WilsonHigh = copyFloat(view.Error.WilsonHigh)
			view.Cache.Value = copyFloat(view.Cache.Value)
			view.Cache.WilsonLow = copyFloat(view.Cache.WilsonLow)
			view.Cache.WilsonHigh = copyFloat(view.Cache.WilsonHigh)
			item := GroupQualityReadItem{GroupID: gid, Account: view}
			if includePolicies {
				if policy := snap.Policies.Groups[strconv.FormatInt(gid, 10)].V3; policy != nil {
					v := *policy
					item.Policy = &v
				}
			}
			out.Items = append(out.Items, item)
		}
	}
	return out
}
