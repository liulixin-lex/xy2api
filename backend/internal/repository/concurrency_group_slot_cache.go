package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
)

var _ service.AccountGroupSlotReader = (*concurrencyCache)(nil)

// GetAccountGroupConcurrencyBatch 按槽位成员的分组后缀统计 (账号, 分组) 的在用槽位（xy2api 分组并发）。
// 只读：先取一次 Redis TIME，再用一个 pipeline 按分数读成员，截止与 PeekAccountsLoadBatch 相同
// （普通槽位 now-TTL，Live 租约 now-60，都不含边界），不修剪过期成员。
// 不带后缀的成员（无分组请求、旧进程）只计入账号总数，这里不计。
func (c *concurrencyCache) GetAccountGroupConcurrencyBatch(ctx context.Context, accountIDs []int64) (map[int64]map[int64]int, error) {
	result := make(map[int64]map[int64]int, len(accountIDs))
	if len(accountIDs) == 0 {
		return result, nil
	}
	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("redis TIME: %w", err)
	}
	slotCutoff := "(" + strconv.FormatInt(now.Unix()-int64(c.slotTTLSeconds), 10)
	liveCutoff := "(" + strconv.FormatInt(now.Unix()-liveLeaseTTLSeconds, 10)

	pipe := c.rdb.Pipeline()
	type accountCmds struct {
		accountID   int64
		slots, live *redis.StringSliceCmd
	}
	cmds := make([]accountCmds, 0, len(accountIDs))
	seen := make(map[int64]struct{}, len(accountIDs))
	for _, accountID := range accountIDs {
		if _, ok := seen[accountID]; ok {
			continue
		}
		seen[accountID] = struct{}{}
		id := strconv.FormatInt(accountID, 10)
		cmds = append(cmds, accountCmds{
			accountID: accountID,
			slots:     pipe.ZRangeByScore(ctx, accountSlotKeyPrefix+id, &redis.ZRangeBy{Min: slotCutoff, Max: "+inf"}),
			live:      pipe.ZRangeByScore(ctx, liveAccountSlotKeyPrefix+id, &redis.ZRangeBy{Min: liveCutoff, Max: "+inf"}),
		})
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("pipeline exec: %w", err)
	}

	for _, cmd := range cmds {
		for _, members := range [][]string{cmd.slots.Val(), cmd.live.Val()} {
			for _, member := range members {
				groupID := service.SlotMemberGroupID(member)
				if groupID <= 0 {
					continue
				}
				if result[cmd.accountID] == nil {
					result[cmd.accountID] = make(map[int64]int)
				}
				result[cmd.accountID][groupID]++
			}
		}
	}
	return result, nil
}
