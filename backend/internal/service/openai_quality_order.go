package service

import (
	"context"
	"sort"
)

// orderOpenAIPredictedPositions refines a stable load/LRU base order. Unknown
// predictions stay in their base positions, so missing values cannot create a
// non-transitive pairwise comparison. Only comparable known positions move.
func orderOpenAIPredictedPositions[T any](items []T, itemOf func(T) accountWithLoad, order openAICandidateOrder, held map[int64]openAIAccountLatencyHoldSnapshot, compact bool) {
	if !order.rates.enabled || len(order.expectedP90) < 2 {
		return
	}
	type cohort struct {
		tier, compact, priority       int
		held, nearCapacity, rateKnown bool
		rate                          float64
	}
	positions := make(map[cohort][]int)
	for i, value := range items {
		item := itemOf(value)
		account := item.account
		if _, known := order.expectedP90[account.ID]; !known {
			continue
		}
		rate, known := order.rates.rates[account.ID]
		key := cohort{tier: order.tiers[account.ID], held: held[account.ID].Held, priority: account.Priority, nearCapacity: openAIAccountNearCapacity(item), rateKnown: known, rate: rate}
		if compact {
			key.compact = openAICompactSupportTier(account)
		}
		positions[key] = append(positions[key], i)
	}
	for _, indices := range positions {
		if len(indices) < 2 {
			continue
		}
		known := make([]T, len(indices))
		for i, index := range indices {
			known[i] = items[index]
		}
		sort.SliceStable(known, func(i, j int) bool {
			return order.expectedP90[itemOf(known[i]).account.ID] < order.expectedP90[itemOf(known[j]).account.ID]
		})
		for i, index := range indices {
			items[index] = known[i]
		}
	}
}

func (s *OpenAIGatewayService) orderOpenAIPredictedAccounts(accounts []*Account, order openAICandidateOrder, model string, compact bool) {
	if !order.rates.enabled || len(order.expectedP90) < 2 {
		return
	}
	orderOpenAIPredictedPositions(accounts, func(a *Account) accountWithLoad { return accountWithLoad{account: a} }, order, s.openAIAccountLatencyHoldSnapshotForAccounts(accounts, model), compact)
}

func (s *OpenAIGatewayService) orderOpenAIPredictedLoad(items []accountWithLoad, order openAICandidateOrder, model string, compact bool) {
	if !order.rates.enabled || len(order.expectedP90) < 2 {
		return
	}
	accounts := make([]*Account, len(items))
	for i, item := range items {
		accounts[i] = item.account
	}
	orderOpenAIPredictedPositions(items, func(item accountWithLoad) accountWithLoad { return item }, order, s.openAIAccountLatencyHoldSnapshotForAccounts(accounts, model), compact)
}

// openAIQualitySegmentKey keeps latency partitions inside quality and protocol
// capability segments. Disabled groups retain the original partition behavior.
func (s *OpenAIGatewayService) openAIQualitySegmentKey(ctx context.Context, compact bool, groupIDs ...*int64) func(*Account) int {
	groupID := int64(0)
	if len(groupIDs) > 0 {
		groupID = derefGroupID(groupIDs[0])
	} else if gate, _ := ctx.Value(openAISelectionContextKey{}).(*openAISelectionContext); gate != nil {
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
