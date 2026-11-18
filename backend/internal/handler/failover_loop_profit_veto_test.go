package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/service"

	"github.com/stretchr/testify/require"
)

// profitVetoLoopResult 记录一次模拟选号循环的终止方式与步数。
type profitVetoLoopResult struct {
	outcome        string // "forwarded" | "exhausted" | "budget_exceeded"
	forwardedID    int64
	iterations     int
	backoffRetries int
}

// runProfitVetoLoop 模拟 handler 的「选号 → 利润终检 → 重选」循环，只保留与
// 活锁相关的状态机（FailoverState + 排除列表 + HandleSelectionExhausted），
// 不涉及真实调度器与上游转发。
//
// pool 按顺序给出候选账号；vetoed 中的账号每次终检都被利润门否决——这对应
// 「候选池快照与 per-account 快照短暂不一致」时门的确定性判定。
// maxIterations 是测试自身的预算：循环超过它即视为活锁。
func runProfitVetoLoop(t *testing.T, fs *FailoverState, pool []int64, vetoed map[int64]bool, maxIterations int) profitVetoLoopResult {
	t.Helper()
	res := profitVetoLoopResult{}
	for res.iterations = 1; res.iterations <= maxIterations; res.iterations++ {
		// 选号：返回第一个不在排除列表中的账号。
		var picked int64
		for _, id := range pool {
			if _, excluded := fs.FailedAccountIDs[id]; !excluded {
				picked = id
				break
			}
		}
		if picked == 0 {
			// 选号耗尽，交给退避决策。
			switch fs.HandleSelectionExhausted(context.Background()) {
			case FailoverContinue:
				res.backoffRetries++
				continue
			default:
				res.outcome = "exhausted"
				return res
			}
		}
		if vetoed[picked] {
			if fs.RecordProfitVeto(picked) == FailoverExhausted {
				res.outcome = "exhausted"
				return res
			}
			continue
		}
		res.outcome = "forwarded"
		res.forwardedID = picked
		return res
	}
	res.outcome = "budget_exceeded"
	return res
}

// TestProfitVetoAfter503DoesNotLivelock 钉死 #4925 引入的活锁回归：
// 一次真实 503 之后，若调度器持续返回会被利润门否决的账号，
// 「选号 → 否决 → 排除 → 选号耗尽 → 清空排除 → 睡 2s → 选号」会无限循环，
// 因为利润否决不推进 SwitchCount，退避条件永远成立。
func TestProfitVetoAfter503DoesNotLivelock(t *testing.T) {
	fs := NewFailoverState(10, false)
	// 已经历一次真实 503（Antigravity 单账号分组 MODEL_CAPACITY_EXHAUSTED 是设计内路径）。
	fs.LastFailoverErr = newTestFailoverErr(503, false, false)
	fs.SwitchCount = 1
	fs.FailedAccountIDs[1] = struct{}{}

	start := time.Now()
	res := runProfitVetoLoop(t, fs, []int64{1}, map[int64]bool{1: true}, 50)
	elapsed := time.Since(start)

	require.Equal(t, "exhausted", res.outcome, "整池被利润门否决时必须有限步终止")
	require.LessOrEqual(t, res.backoffRetries, 1, "被否决的账号不得被退避分支复活并反复重选")
	require.Less(t, elapsed, 10*time.Second, "不得每 2s 空转一轮")
}

// TestProfitVetoKeepsBackoffUsefulForHealthyAccount 钉死修复没有削弱既有的
// 503 退避语义：排除列表里仍有非利润否决的账号时，退避照常清空并重试，
// 只是被利润门否决的账号不再复活。
func TestProfitVetoKeepsBackoffUsefulForHealthyAccount(t *testing.T) {
	fs := NewFailoverState(10, false)
	fs.LastFailoverErr = newTestFailoverErr(503, false, false)
	fs.SwitchCount = 1
	// 账号 1 因真实 503 被排除；账号 2 会被利润门否决。
	fs.FailedAccountIDs[1] = struct{}{}

	res := runProfitVetoLoop(t, fs, []int64{2, 1}, map[int64]bool{2: true}, 50)

	require.Equal(t, "forwarded", res.outcome)
	require.Equal(t, int64(1), res.forwardedID, "退避清空后应重新可选账号 1")
	require.Equal(t, 1, res.backoffRetries, "应发生且只发生一次退避重试")
	require.Contains(t, fs.FailedAccountIDs, int64(2), "利润否决的账号在退避清空后必须被放回排除集")
}

// TestProfitVetoAttemptsCapped 钉死没有 503 参与时，大分组整池越线也会在
// 常数步内终止，而不是把整池逐个选一遍。
func TestProfitVetoAttemptsCapped(t *testing.T) {
	fs := NewFailoverState(10, false)
	pool := make([]int64, 0, 64)
	vetoed := make(map[int64]bool, 64)
	for id := int64(1); id <= 64; id++ {
		pool = append(pool, id)
		vetoed[id] = true
	}

	res := runProfitVetoLoop(t, fs, pool, vetoed, 200)

	require.Equal(t, "exhausted", res.outcome)
	require.Equal(t, maxProfitVetoAttempts, fs.ProfitVetoCount())
	require.Equal(t, maxProfitVetoAttempts, res.iterations, "达到上限即终止，不应继续遍历候选池")
}

// TestRecordProfitVetoExcludesAccount 钉死 RecordProfitVeto 仍然把账号加入
// 调度排除列表（选号入参用的就是 FailedAccountIDs）。
func TestRecordProfitVetoExcludesAccount(t *testing.T) {
	fs := NewFailoverState(10, false)
	require.Equal(t, FailoverContinue, fs.RecordProfitVeto(42))
	require.Contains(t, fs.FailedAccountIDs, int64(42))
	require.Equal(t, 1, fs.ProfitVetoCount())
}

// TestHandleSelectionExhaustedUnaffectedWithoutProfitVeto 钉死未启用利润控制的
// 请求（无任何利润否决）走的仍是原有退避语义：清空排除列表并重试。
func TestHandleSelectionExhaustedUnaffectedWithoutProfitVeto(t *testing.T) {
	fs := NewFailoverState(3, false)
	fs.LastFailoverErr = newTestFailoverErr(503, false, false)
	fs.SwitchCount = 1
	fs.FailedAccountIDs[100] = struct{}{}

	require.Equal(t, FailoverContinue, fs.HandleSelectionExhausted(context.Background()))
	require.Empty(t, fs.FailedAccountIDs, "无利润否决时排除列表应被完全清空")
}

type waitedAccountRepo struct {
	service.AccountRepository
	pool    []service.Account
	fresh   service.Account
	readErr error
	reads   int
}

func (r *waitedAccountRepo) ListSchedulableByGroupIDAndPlatforms(context.Context, int64, []string) ([]service.Account, error) {
	return r.pool, nil
}
func (r *waitedAccountRepo) GetByID(context.Context, int64) (*service.Account, error) {
	r.reads++
	return &r.fresh, r.readErr
}

type waitedSlots struct {
	service.ConcurrencyCache
	busy     bool
	released []int64
}

func (s *waitedSlots) AcquireAccountSlot(_ context.Context, id int64, _ int, _ string) (bool, error) {
	return id != 1 || !s.busy, nil
}
func (s *waitedSlots) ReleaseAccountSlot(_ context.Context, id int64, _ string) error {
	s.released = append(s.released, id)
	return nil
}

type waitedSessions struct {
	service.SessionLimitCache
	added, removed []int64
}

func (s *waitedSessions) RegisterSession(_ context.Context, id int64, _ string, _ int, _ time.Duration) (bool, error) {
	s.added = append(s.added, id)
	return true, nil
}
func (s *waitedSessions) UnregisterSession(_ context.Context, id int64, _ string) error {
	s.removed = append(s.removed, id)
	return nil
}

func TestGatewayWaitRechecksBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wait    bool
		change  func(*service.Account)
		readErr error
		want    int64
	}{
		{"disabled", true, func(a *service.Account) { a.Status = service.StatusDisabled }, nil, 2},
		{"left_group", true, func(a *service.Account) { a.AccountGroups = nil }, nil, 2},
		{"cooldown", true, func(a *service.Account) { end := time.Now().Add(time.Minute); a.RateLimitResetAt = &end }, nil, 2},
		{"deleted", true, nil, service.ErrAccountNotFound, 2},
		{"unchanged", true, nil, nil, 1},
		{"read_error", true, nil, errors.New("read failed"), 1},
		{"direct", false, nil, nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			group := &service.Group{ID: 9, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive}
			ctx := context.WithValue(context.Background(), ctxkey.Group, group)
			a := service.Account{ID: 1, Platform: group.Platform, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Concurrency: 1,
				Extra: map[string]any{"max_sessions": 3}, AccountGroups: []service.AccountGroup{{GroupID: group.ID}}}
			b := a
			b.ID = 2
			b.Priority = 5
			repo := &waitedAccountRepo{pool: []service.Account{a, b}, fresh: a}
			slots := &waitedSlots{busy: tc.wait}
			sessions := &waitedSessions{}
			concurrency := service.NewConcurrencyService(slots)
			gateway := service.NewGatewayService(repo, nil, nil, nil, nil, nil, nil, nil, &config.Config{}, nil, concurrency, nil, nil, nil, nil, nil, nil, nil, sessions, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			fs := NewFailoverState(2, false)
			deferred := map[int64]*service.Account{}
			var dispatched []int64
			for i := 0; i < 2; i++ {
				pick, err := gateway.SelectAccountWithLoadAwareness(ctx, &group.ID, "fixture", "", fs.FailedAccountIDs, "", 0)
				require.NoError(t, err)
				release := pick.ReleaseFunc
				if !pick.Acquired {
					require.NotNil(t, pick.WaitPlan)
					require.Zero(t, repo.reads)
					if tc.change != nil {
						tc.change(&repo.fresh)
					}
					repo.readErr = tc.readErr
					slots.busy = false
					acquired, err := concurrency.AcquireAccountSlot(ctx, pick.Account.ID, 1)
					require.NoError(t, err)
					require.True(t, acquired.Acquired)
					release = acquired.ReleaseFunc
					deferred[pick.Account.ID] = pick.Account
				}
				latest, veto, _ := fs.admitGatewaySlot(ctx, gateway, pick, "fixture", release, deferred)
				require.False(t, veto)
				if latest == nil {
					continue
				}
				dispatched = append(dispatched, latest.ID)
				require.Len(t, slots.released, len(sessions.removed), "the admitted slot stays held through dispatch")
				release()
				break
			}
			require.Equal(t, []int64{tc.want}, dispatched)
			require.Zero(t, fs.SwitchCount)
			require.Nil(t, fs.LastFailoverErr)
			require.False(t, fs.ForceCacheBilling)
			require.Zero(t, fs.ProfitVetoCount())
			if tc.want == 2 {
				require.Equal(t, []int64{1, 2}, sessions.added)
				require.Equal(t, []int64{1}, sessions.removed)
				require.NotContains(t, deferred, int64(1))
				require.Equal(t, []int64{1, 2}, slots.released)
			} else {
				require.Empty(t, sessions.removed)
				require.Equal(t, []int64{1}, slots.released)
			}
			if tc.wait {
				require.Equal(t, 1, repo.reads)
			} else {
				require.Zero(t, repo.reads)
			}
		})
	}
}
