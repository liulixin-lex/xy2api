//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

func TestOpenAISelectionRuntimeFrontPriorityBeforeRateAndLoad(t *testing.T) {
	a, b := selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2)
	a.RateMultiplier, b.RateMultiplier = floatPtr(3), floatPtr(.1)
	f := newSelectionRuntimeFixture(t, a, b)
	f.loads.loadMap = map[int64]*AccountLoadInfo{1: {LoadRate: 90}, 2: {LoadRate: 0}}
	require.Equal(t, int64(1), f.selectNow(t, context.Background(), "", nil).Account.ID)
	require.Equal(t, []int64{1}, f.attempts)
}

func TestOpenAISelectionRuntimeCoolingRecoveryRestoresPriority(t *testing.T) {
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2))
	key := fakeSchedViewKey{7, 1, "m"}
	f.view.States[key] = schedquality.StateView{State: schedquality.StateCooling}
	require.Equal(t, int64(2), f.selectNow(t, context.Background(), "", nil).Account.ID)
	f.view.States[key] = schedquality.StateView{State: schedquality.StateNormal}
	require.Equal(t, int64(1), f.selectNow(t, context.Background(), "", nil).Account.ID)
}

func TestOpenAISelectionRuntimeManualBackWithoutAutomaticGate(t *testing.T) {
	for _, mode := range []string{"gate_disabled", "global_disabled"} {
		t.Run(mode, func(t *testing.T) {
			f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2))
			// With no enabled group policy the published automatic state is normal.
			f.view.Takeovers = nil
			f.view.Enabled = mode != "global_disabled"
			key := fakeSchedViewKey{7, 1, "m"}
			f.view.States[key] = schedquality.StateView{GroupOverride: schedquality.OverrideRef{Mode: schedquality.OverrideDemote}}
			require.Equal(t, int64(2), f.selectNow(t, context.Background(), "", nil).Account.ID)
			require.Equal(t, int64(1), f.selectNow(t, context.Background(), "", map[int64]struct{}{2: {}}).Account.ID, "manual back remains selectable when alone")
			delete(f.view.States, key)
			require.Equal(t, int64(1), f.selectNow(t, context.Background(), "", nil).Account.ID, "cancel restores original priority")
		})
	}
}

func TestOpenAISelectionRuntimeCheapTakeoverPreflight(t *testing.T) {
	for _, reason := range []string{"unsupported_model", "wrong_platform"} {
		t.Run(reason, func(t *testing.T) {
			owner, candidate := selectionFixtureAccount(10, 5), selectionFixtureAccount(1, 1)
			if reason == "unsupported_model" {
				candidate.Credentials = map[string]any{"model_mapping": map[string]any{"other": "other"}}
			} else {
				candidate.Platform = PlatformAnthropic
			}
			f := newSelectionRuntimeFixture(t, owner, candidate)
			f.cache.sessionBindings["openai:session"] = owner.ID
			// The stale member snapshot deliberately includes an incompatible row.
			f.svc.schedulerSnapshot = NewSchedulerSnapshotService(&snapshotHydrationCache{
				snapshot: []*Account{&owner, &candidate}, accounts: map[int64]*Account{owner.ID: &owner, candidate.ID: &candidate},
			}, nil, f.repo, nil, nil)
			require.Equal(t, owner.ID, f.selectNow(t, context.Background(), "session", nil).Account.ID)
			require.Zero(t, f.loads.batches, "incompatible higher priority members must not build a load pool")
			require.Empty(t, f.cache.casKeys)
		})
	}
}

func TestOpenAISelectionBorrowProbeLimit(t *testing.T) {
	for _, mode := range []string{"slots_full", "db_rejects", "limit_changes", "retry_counts"} {
		t.Run(mode, func(t *testing.T) {
			accounts := make([]*Account, 0, 70)
			for i := int64(1); i <= 70; i++ {
				a := selectionFixtureAccount(i, int(i))
				a.GroupIDs = nil
				accounts = append(accounts, &a)
			}
			cache := &upstreamCostTrackingConcurrencyCache{rejectAcquire: mode == "slots_full"}
			svc, repo := newUpstreamCostSelectionFixture(accounts, cache)
			for _, a := range accounts {
				fresh := *a
				if mode == "db_rejects" || mode == "limit_changes" && a.ID < 64 {
					fresh.Schedulable = false
				} else if mode == "limit_changes" {
					fresh.Concurrency = 1
				} else if mode == "retry_counts" {
					fresh.Concurrency = 1
					fresh.Type = AccountTypeOAuth
				}
				repo.accounts[a.ID] = &fresh
			}
			owner := selectionFixtureAccount(100, 0)
			req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI}
			if mode == "retry_counts" {
				req.RequiredImageCapability = OpenAIImagesCapabilityAPIKey
			}
			selection, err := svc.selectOpenAIStickyCapacityBorrow(context.Background(), &owner, req)
			require.NoError(t, err)
			require.Nil(t, selection)
			require.Equal(t, 64, cache.totalAcquires())
			if mode == "slots_full" {
				require.Zero(t, repo.calls())
				require.Empty(t, cache.releases)
			} else if mode == "retry_counts" {
				require.Equal(t, 32, repo.calls())
				for i := int64(1); i <= 32; i++ {
					require.Equal(t, []int{10, 1}, cache.limits(i))
					require.Equal(t, 2, cache.releaseCount(i))
				}
				require.Empty(t, cache.limits(33), "two slot attempts consume two probes")
			} else {
				require.Equal(t, 64, repo.calls())
				for i := int64(1); i <= 64; i++ {
					require.Equal(t, 1, cache.releaseCount(i), "every acquired slot must be released")
				}
			}
			require.Empty(t, cache.limits(65), "never acquire candidate 65")
			if mode == "limit_changes" {
				require.Equal(t, []int{10}, cache.limits(64), "stale-limit retry shares the acquire budget")
			}
		})
	}
}
