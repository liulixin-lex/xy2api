//go:build unit

package service

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

type selectionBoardRepo struct {
	AccountRepository
	accounts []Account
}

func (r *selectionBoardRepo) ListAllWithFilters(_ context.Context, platform, kind, status, search string, group int64, privacy string) ([]Account, error) {
	if platform != "" || kind != "" || status != "" || search != "" || group != 7 || privacy != "" {
		return nil, errors.New("unexpected board scope")
	}
	return append([]Account(nil), r.accounts...), nil
}

type selectionBoardLoads struct {
	ConcurrencyCache
	loads map[int64]*AccountLoadInfo
	err   error
}

func (c *selectionBoardLoads) GetAccountsLoadBatch(context.Context, []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error) {
	return c.loads, c.err
}

type selectionBoardBindings struct {
	GatewayCache
	*selectionBindingFake
}

func TestSchedBoardOrderAdmissionAndNoMutation(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)
	a, b, c, d, e := selectionTestAccount(1, 1), selectionTestAccount(2, 2), selectionTestAccount(3, 3), selectionTestAccount(4, 4), selectionTestAccount(5, 4)
	a.Name, b.Name = "account-one", "account-two"
	a.LastUsedAt = &now
	c.Status = "disabled"
	d.RateLimitResetAt = &future
	e.Type = AccountTypeOAuth
	repo := &selectionBoardRepo{accounts: []Account{*e, *d, *c, *b, *a}}
	view := selectionTestView(now)
	view.States[fakeSchedViewKey{7, 1, "m"}] = schedquality.StateView{Failing: true, Dims: []schedquality.Dimension{schedquality.DimensionError}, Until: future}
	loads := map[int64]*AccountLoadInfo{1: {LoadRate: 10, CurrentConcurrency: 1}, 2: {LoadRate: 100, CurrentConcurrency: 5}}
	events := []string{}
	binding := &selectionBindingFake{owner: 10, events: &events}
	s := &OpenAIGatewayService{cache: &selectionBoardBindings{selectionBindingFake: binding}, accountRepo: repo, concurrencyService: &ConcurrencyService{cache: &selectionBoardLoads{loads: loads}}}
	board := NewOpenAISelectionBoard(s, func() schedquality.View { return view })
	board.now = func() time.Time { return now }
	s.selectionFullMarks.Mark(5, now.Add(-30*time.Second))
	before := map[int64]time.Time{}
	for k, v := range s.selectionFullMarks.at {
		before[k] = v
	}
	rows, err := board.Board(context.Background(), 7, "m")
	require.NoError(t, err)
	require.Len(t, rows, 5)
	var ids []int64
	for _, row := range rows {
		ids = append(ids, row.AccountID)
	}
	require.Equal(t, []int64{2, 5, 1, 3, 4}, ids)
	require.Equal(t, 5, rows[0].Concurrency)
	require.Equal(t, 5, rows[0].InFlight)
	require.Equal(t, "account-two", rows[0].Name)
	require.Equal(t, "normal", rows[0].Layer)
	require.Equal(t, 100, rows[0].LoadRate)
	require.Equal(t, "below", rows[2].Layer)
	require.Equal(t, []schedquality.Dimension{schedquality.DimensionError}, rows[2].Dims)
	require.Equal(t, &future, rows[2].Until)
	require.Equal(t, &now, rows[2].LastUsedAt)
	require.Equal(t, "stopped", rows[3].Layer)
	require.Equal(t, &future, rows[4].Until)
	require.Nil(t, rows[3].Until)
	require.Equal(t, 5, rows[4].Rank)
	require.Equal(t, before, s.selectionFullMarks.at, "a full load snapshot must not mark an account from Board")
	// W1 exercises exactly the admission/comparator seam; W2 connects the same
	// attempt order to the public selection entry (D11 end-to-end).
	checker := s
	req := OpenAIAccountScheduleRequest{GroupID: ptrInt64Selection(7), Platform: PlatformOpenAI, RequestedModel: "m"}
	var candidates []*Account
	for i := range repo.accounts {
		account := &repo.accounts[i]
		if reason := openAICompatibleAccountEligibilityFailureReason(context.Background(), account, req.Platform, req.RequestedModel, false, "", s); reason != "" {
			continue
		}
		if ok, _ := checker.isAccountRequestCompatibleReason(context.Background(), account, req); ok {
			candidates = append(candidates, account)
		}
	}
	selectionReq := selectionRequest{TokenBilled: true, Loads: loads, OAuthRateMultiplier: s.openAIOAuthSchedulingRateMultiplier(context.Background())}
	sort.Slice(candidates, func(i, j int) bool {
		return selectionLess(buildSelectionKey(view, 7, "m", candidates[i], selectionReq, now), buildSelectionKey(view, 7, "m", candidates[j], selectionReq, now))
	})
	for i, a := range candidates {
		require.Equal(t, a.ID, rows[i].AccountID)
		require.Equal(t, i+1, rows[i].Rank)
		require.Equal(t, buildSelectionKey(view, 7, "m", a, selectionReq, now).Rate, rows[i].Rate)
	}
	require.Equal(t, 5, loads[2].CurrentConcurrency)
	require.Equal(t, int64(10), binding.owner)
	require.Zero(t, binding.reads)
	require.Empty(t, events)
}

func TestSchedBoardRejectUntilAndProfit(t *testing.T) {
	now := time.Now()
	until := now.Add(time.Hour)
	for _, kind := range []string{"temp", "overload", "model", "transient", "iq"} {
		t.Run(kind, func(t *testing.T) {
			a := selectionTestAccount(1, 1)
			s := &OpenAIGatewayService{}
			switch kind {
			case "temp":
				a.TempUnschedulableUntil = &until
			case "overload":
				a.OverloadUntil = &until
			case "model":
				setAccountModelRateLimitSnapshot(a, "m", until, "limited", now)
			case "transient":
				s.getOpenAIAccountModelTransientState().recordFailure(1, "m", now)
				s.getOpenAIAccountModelTransientState().recordFailure(1, "m", now)
			case "iq":
				a.Extra = map[string]any{"model_rate_limits": map[string]any{"iq:m": map[string]any{}}}
			}
			s.accountRepo = &selectionBoardRepo{accounts: []Account{*a}}
			b := NewOpenAISelectionBoard(s, func() schedquality.View { return selectionTestView(now) })
			rows, err := b.Board(context.Background(), 7, "m")
			require.NoError(t, err)
			require.Equal(t, "stopped", rows[0].Layer)
			if kind == "iq" {
				require.Nil(t, rows[0].Until)
			} else {
				require.NotNil(t, rows[0].Until)
				require.True(t, rows[0].Until.After(now))
			}
		})
	}
	t.Run("group_profit_applies", func(t *testing.T) {
		s := &OpenAIGatewayService{accountRepo: &selectionBoardRepo{accounts: []Account{*selectionTestAccount(1, 1)}}}
		b := NewOpenAISelectionBoard(s, func() schedquality.View { return selectionTestView(now) })
		rows, err := b.Board(profitControlTestCtx(profitControlTestGroup(7, .9, 0)), 7, "m")
		require.NoError(t, err)
		require.Equal(t, "stopped", rows[0].Layer)
	})
}

func TestSchedBoardSchedulingThresholdReadOnly(t *testing.T) {
	for _, tt := range []struct {
		name   string
		setup  func(*Account, time.Time)
		paused bool
	}{
		{"global_threshold", func(*Account, time.Time) {}, true},
		{"below_threshold", func(a *Account, _ time.Time) { a.Extra["codex_7d_used_percent"] = 79.0 }, false},
		{"account_override", func(a *Account, _ time.Time) { a.Credentials = map[string]any{"account_scheduling_threshold": 90} }, false},
		{"expired_window", func(a *Account, now time.Time) {
			a.Extra["codex_7d_reset_at"] = now.Add(-time.Second).Format(time.RFC3339)
		}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, a, writes := selectionThresholdFixture(t)
			now := time.Now()
			tt.setup(a, now)
			before := *a
			repo := &selectionBoardRepo{AccountRepository: writes, accounts: []Account{*a}}
			s.accountRepo = repo
			board := NewOpenAISelectionBoard(s, func() schedquality.View { return selectionTestView(now) })
			board.now = func() time.Time { return now }
			rows, err := board.Board(context.Background(), 7, "m")
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, tt.paused, rows[0].Layer == "stopped")
			if tt.paused {
				require.Equal(t, 1, rows[0].Rank)
				decision := EvaluateAccountSchedulingThreshold(a, s.settingService.GetAccountSchedulingThresholds(context.Background()), now)
				require.Equal(t, decision.Until, rows[0].Until)
			} else {
				require.Nil(t, rows[0].Until)
			}
			require.Zero(t, writes.tempCalls)
			require.Equal(t, before, *a)
			require.Equal(t, before, repo.accounts[0])
			require.Empty(t, s.selectionFullMarks.at)
		})
	}
}

func TestSchedBoardUpstreamChannelRestriction(t *testing.T) {
	channel := newTestChannelService(makeStandardRepo(Channel{
		ID: 1, Status: StatusActive, GroupIDs: []int64{7}, RestrictModels: true,
		BillingModelSource: BillingModelSourceUpstream,
		ModelPricing:       []ChannelModelPricing{{Platform: PlatformOpenAI, Models: []string{"allowed"}}},
	}, map[int64]string{7: PlatformOpenAI}))
	blocked, allowed := selectionTestAccount(1, 1), selectionTestAccount(2, 2)
	blocked.Credentials = map[string]any{"model_mapping": map[string]any{"m": "blocked"}}
	allowed.Credentials = map[string]any{"model_mapping": map[string]any{"m": "allowed"}}
	s := &OpenAIGatewayService{channelService: channel, accountRepo: &selectionBoardRepo{accounts: []Account{*blocked, *allowed}}}
	ctx := context.Background()
	require.True(t, s.needsUpstreamChannelRestrictionCheck(ctx, ptrInt64Selection(7)))
	require.False(t, s.checkChannelPricingRestriction(ctx, ptrInt64Selection(7), "m"), "the group-level check cannot resolve account mapping")
	board := NewOpenAISelectionBoard(s, func() schedquality.View { return selectionTestView(time.Now()) })
	rows, err := board.Board(ctx, 7, "m")
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, allowed.ID, rows[0].AccountID)
	require.Equal(t, 1, rows[0].Rank)
	require.Equal(t, "normal", rows[0].Layer)
	require.Equal(t, blocked.ID, rows[1].AccountID)
	require.Equal(t, "stopped", rows[1].Layer)
	require.Equal(t, 2, rows[1].Rank)
}

func TestSchedBoardRejectUntilInjectedClock(t *testing.T) {
	ctx := context.Background()
	a := selectionTestAccount(1, 1)
	wallNow := time.Now()
	setAccountModelRateLimitSnapshot(a, "m", wallNow.Add(time.Hour), "limited", wallNow)
	now := wallNow.Add(-24 * time.Hour)
	remaining := a.GetRateLimitRemainingTimeWithContext(ctx, "m")
	until := selectionRejectUntil(ctx, &OpenAIGatewayService{}, a, "m", "model_rate_limited", now)
	require.NotNil(t, until)
	require.WithinDuration(t, now.Add(remaining), *until, time.Second)
}
