package service

import "context"

// openAIQualitySegmentKey keeps latency partitions inside quality and protocol
// capability segments. Disabled groups retain the original partition behavior.
func (s *OpenAIGatewayService) openAIQualitySegmentKey(ctx context.Context, compact bool, groupIDs ...*int64) func(*Account) int {
	groupID := int64(0)
	if len(groupIDs) > 0 {
		groupID = derefGroupID(groupIDs[0])
	} else if gate, _ := ctx.Value(groupQualityGateContextKey{}).(*openAIGroupQualityGate); gate != nil {
		groupID = gate.groupID
	}
	view := s.GroupQualityGateView()
	enabled := view.Enabled(groupID)
	if !enabled && !compact {
		return nil
	}
	return func(a *Account) int {
		if a == nil {
			return 0
		}
		key := 0
		if enabled {
			key = view.OrderTier(groupID, a.ID)
		}
		if compact {
			key += 3 * openAICompactSupportTier(a)
		}
		return key
	}
}

func shuffleOpenAIWithinQualityGroups(items []accountWithLoad, tiers map[int64]int, held map[int64]openAIAccountLatencyHoldSnapshot) {
	if tiers == nil {
		shuffleWithinSortGroups(items)
		return
	}
	for start := 0; start < len(items); {
		end := start + 1
		for end < len(items) && tiers[items[end].account.ID] == tiers[items[start].account.ID] && held[items[end].account.ID].Held == held[items[start].account.ID].Held {
			end++
		}
		shuffleWithinSortGroups(items[start:end])
		start = end
	}
}

func (s *OpenAIGatewayService) preferOpenAIQualityCacheCandidates(ctx context.Context, items []accountWithLoad, model string, tiers map[int64]int) {
	if tiers == nil {
		s.preferCacheCandidates(ctx, items, model)
		return
	}
	accounts := make([]*Account, len(items))
	for i, item := range items {
		accounts[i] = item.account
	}
	held := s.openAIAccountLatencyHoldSnapshotForAccounts(accounts, model)
	for start := 0; start < len(items); {
		end := start + 1
		first := items[start].account.ID
		for end < len(items) && tiers[items[end].account.ID] == tiers[first] && held[items[end].account.ID].Held == held[first].Held {
			end++
		}
		s.preferCacheCandidates(ctx, items[start:end], model)
		start = end
	}
}

// openAIQualityPools returns nil when the group does not use quality tiers.
func (s *OpenAIGatewayService) openAIQualityPools(groupID *int64, accounts []*Account, model string) [][]*Account {
	view := s.GroupQualityGateView()
	if !view.Enabled(derefGroupID(groupID)) {
		return nil
	}
	pools := make([][]*Account, 6)
	held := s.openAIAccountLatencyHoldSnapshotForAccounts(accounts, model)
	for _, a := range accounts {
		pool := 2 * view.OrderTier(derefGroupID(groupID), a.ID)
		if held[a.ID].Held {
			pool++
		}
		pools[pool] = append(pools[pool], a)
	}
	return pools
}
