package admin

import "context"

// fillCurrentGroupConcurrency 给按具体分组筛选的账号列表填上容量列的第三个数：
// 本分组的请求在该账号上占用的槽位（xy2api 分组并发）。不筛分组或筛“未分配分组”时不读，
// 字段不出现在响应里。读取失败时按 0 显示，与 current_concurrency 一致；
// 分组数在总数之后读取，按 current_concurrency 截断，保证第三个数不超过第一个数。
func (h *AccountHandler) fillCurrentGroupConcurrency(ctx context.Context, items []AccountWithConcurrency, groupID int64) {
	if groupID <= 0 || len(items) == 0 {
		return
	}
	accountIDs := make([]int64, 0, len(items))
	for i := range items {
		if items[i].Account != nil {
			accountIDs = append(accountIDs, items[i].ID)
		}
	}
	counts := h.concurrencyService.GetAccountGroupConcurrencyBatch(ctx, accountIDs)
	for i := range items {
		if items[i].Account == nil {
			continue
		}
		count := max(0, min(counts[items[i].ID][groupID], items[i].CurrentConcurrency))
		items[i].CurrentGroupConcurrency = &count
	}
}
