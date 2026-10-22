package service

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// 分组并发（xy2api）：账号槽位成员带上“账号是从哪个分组选出来的”分组 ID，
// 成员从 r<进程前缀>-<序号> 变成 r<进程前缀>-<序号>|g<分组ID>。
// 进程前缀仍在开头，释放（闭包保存完整成员）、过期修剪、周期清理和启动时按前缀清理都照旧；
// 不加新 key，也不多一次 Redis 写。读取时按后缀计数，只用于管理页展示，不参与选号。

const slotMemberGroupSeparator = "|g"

// accountGroupConcurrencyReadTimeout 与 GetAccountConcurrencyBatch 的独立超时一致。
const accountGroupConcurrencyReadTimeout = 3 * time.Second

// AccountGroupSlotReader 是 ConcurrencyCache 的可选能力，用类型断言调用，不扩充主接口，
// 免得测试桩跟着改。返回 账号 -> 分组 -> 在用槽位数（普通槽位 + Live 租约，只读，不修剪）。
type AccountGroupSlotReader interface {
	GetAccountGroupConcurrencyBatch(ctx context.Context, accountIDs []int64) (map[int64]map[int64]int, error)
}

// withSlotGroup 用上下文里的分组给槽位成员加后缀：调度器内是实际选号的分组
// （回退、组合路由解析后的分组），处理器等待路径是 API Key 的分组。没有分组时原样返回。
func withSlotGroup(ctx context.Context, id string) string {
	if ctx == nil {
		return id
	}
	group, ok := ctx.Value(ctxkey.Group).(*Group)
	if !ok || group == nil || group.ID <= 0 {
		return id
	}
	return id + slotMemberGroupSeparator + strconv.FormatInt(group.ID, 10)
}

// withSlotGroupID 给 Live 租约 ID 加同样的后缀；租约 ID 只作为不透明字符串保存和回传。
func withSlotGroupID(groupID *int64, id string) string {
	if groupID == nil || *groupID <= 0 {
		return id
	}
	return id + slotMemberGroupSeparator + strconv.FormatInt(*groupID, 10)
}

// SlotMemberGroupID 解析槽位成员的分组后缀；没有后缀（无分组请求、旧进程）或后缀非法时返回 0。
// 导出给仓储按成员计数。
func SlotMemberGroupID(member string) int64 {
	return slotMemberGroupID(member)
}

func slotMemberGroupID(member string) int64 {
	i := strings.LastIndex(member, slotMemberGroupSeparator)
	if i < 0 {
		return 0
	}
	groupID, err := strconv.ParseInt(member[i+len(slotMemberGroupSeparator):], 10, 64)
	if err != nil || groupID <= 0 {
		return 0
	}
	return groupID
}

// GetAccountGroupConcurrencyBatch 返回 账号 -> 分组 -> 在用槽位数。只用于管理页展示：
// 缓存不支持或读取失败时返回空结果并记 warn，不影响调用方。用独立的 3 秒超时，
// 请求取消不会让读取失败。
func (s *ConcurrencyService) GetAccountGroupConcurrencyBatch(ctx context.Context, accountIDs []int64) map[int64]map[int64]int {
	result := map[int64]map[int64]int{}
	if s == nil || s.cache == nil || len(accountIDs) == 0 {
		return result
	}
	reader, ok := s.cache.(AccountGroupSlotReader)
	if !ok {
		return result
	}
	baseCtx := context.Background()
	if ctx != nil {
		baseCtx = context.WithoutCancel(ctx)
	}
	readCtx, cancel := context.WithTimeout(baseCtx, accountGroupConcurrencyReadTimeout)
	defer cancel()
	counts, err := reader.GetAccountGroupConcurrencyBatch(readCtx, accountIDs)
	if err != nil {
		logger.L().Warn("account_group_concurrency_read_failed", zap.Int("accounts", len(accountIDs)), zap.Error(err))
		return result
	}
	if counts == nil {
		return result
	}
	return counts
}

// groupSlotsWithin 返回 groupID 在账号上占用的槽位，但不超过账号当前并发 current：
// 总数先读、分组后读，中间新占的槽位只出现在后一次读取里，截断后绿色数不会超过第一个数。
func groupSlotsWithin(counts map[int64]map[int64]int, accountID, groupID int64, current int) int {
	return max(0, min(counts[accountID][groupID], current))
}

// groupSlotsUsed 累加 groupID 在这些账号上占用的槽位（逐个账号按 current 截断）。
func groupSlotsUsed(counts map[int64]map[int64]int, groupID int64, accountIDs []int64, current map[int64]int) int {
	used := 0
	for _, accountID := range accountIDs {
		used += groupSlotsWithin(counts, accountID, groupID, current[accountID])
	}
	return used
}
