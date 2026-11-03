//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type anthropicTestCache struct {
	*mockGatewayCacheForPlatform
	failDelete bool
	deletes    int
}

func (c *anthropicTestCache) DeleteSessionAccountIDIfOwnerAndReport(_ context.Context, _ int64, session string, owner int64) (bool, error) {
	c.deletes++
	if c.failDelete {
		return false, errors.New("delete failed")
	}
	if c.sessionBindings[session] != owner {
		return false, nil
	}
	delete(c.sessionBindings, session)
	return true, nil
}

func anthropicTestSamples(s *GatewayService, a *Account, model string, d time.Duration, n int, at time.Time) {
	for i := 0; i < n; i++ {
		s.recordAnthropicAccountLatencySample(a, model, d, at)
	}
}

func TestAnthropicLatencySwitchReset(t *testing.T) {
	old := *resilience.Current()
	t.Cleanup(func() { resilience.Publish(old) })
	on := resilience.Defaults()
	resilience.Publish(on)
	svc := &GatewayService{}
	a := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	now := time.Now()
	anthropicTestSamples(svc, a, "model", 11*time.Second, 6, now)
	require.True(t, svc.anthropicLatencyHoldSnapshot(a, "model", now).Held)
	off := on
	off.AnthropicLatencyHold.Enabled = false
	resilience.Publish(off)
	resilience.Publish(on)
	require.False(t, svc.anthropicLatencyHoldSnapshot(a, "model", now).Held)
	require.Zero(t, svc.anthropicLatencyFreshSampleCount(a, "model", now))
	resilience.Publish(off)
	anthropicTestSamples(svc, a, "model", time.Second, 7, now)
	resilience.Publish(on)
	require.Zero(t, svc.anthropicLatencyFreshSampleCount(a, "model", now))
	anthropicTestSamples(svc, a, "model", time.Second, 1, now)
	require.Equal(t, float64(1), svc.anthropicLatencyFreshSampleCount(a, "model", now))
}

func TestAnthropicLatencyAttemptGates(t *testing.T) {
	old := *resilience.Current()
	t.Cleanup(func() { resilience.Publish(old) })
	resilience.Publish(resilience.Defaults())
	for _, tc := range []struct {
		name                                    string
		selection                               *AccountSelectionResult
		failure, accountFailure, stream, output bool
		want                                    bool
	}{
		{"cold", &AccountSelectionResult{}, false, false, true, true, false},
		{"sticky", &AccountSelectionResult{stickySessionHit: true}, false, false, true, true, true},
		{"spillover", &AccountSelectionResult{stickySessionHit: true, StickyCapacitySpillover: true}, false, false, true, true, false},
		{"prior failure", &AccountSelectionResult{stickySessionHit: true}, true, false, true, true, false},
		{"same account failure", &AccountSelectionResult{stickySessionHit: true}, false, true, true, true, false},
		{"conversion", &AccountSelectionResult{stickySessionHit: true}, false, false, false, true, false},
		{"no semantic output", &AccountSelectionResult{stickySessionHit: true}, false, false, true, false, false},
		{"after migration", &AccountSelectionResult{}, false, false, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &GatewayService{}
			account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"alias": "outbound"}}}
			model, _ := resolveAnthropicOutboundModel(account, "alias")
			require.Equal(t, "outbound", model)
			st := resilience.NewRequestState()
			ctx := resilience.WithRequestState(context.Background(), st)
			ctx = WithAnthropicAttemptWarm(ctx, tc.selection, tc.failure)
			start := time.Now().Add(-20 * time.Second)
			st.SetAttemptProfile(resilience.AttemptProfile{AccountID: 1, Model: model, AttemptStart: start, Warm: anthropicAttemptWarmFromContext(ctx), Stream: tc.stream})
			st.NoteFirstOutput(start.Add(time.Second))
			if tc.output {
				st.NoteSemanticOutput(start.Add(12 * time.Second))
			}
			if tc.accountFailure {
				st.MarkAccountFailed(1)
			}
			svc.ReportAnthropicAttemptSuccess(ctx, account, &ForwardResult{})
			state := svc.anthropicLatencyHold.Load()
			if state == nil {
				require.False(t, tc.want)
				return
			}
			key, _ := openAIAccountModelTransientKey(1, "outbound")
			state.mu.Lock()
			entry := state.entries[key]
			state.mu.Unlock()
			if tc.want {
				require.Equal(t, float64(1), entry.weightedSamples)
				require.Equal(t, float64(1), entry.weightedSlow)
			} else {
				require.Zero(t, entry.weightedSamples)
			}
		})
	}
}

func TestAnthropicLatencyOrdering(t *testing.T) {
	old := *resilience.Current()
	t.Cleanup(func() { resilience.Publish(old) })
	resilience.Publish(resilience.Defaults())
	svc := &GatewayService{}
	now := time.Now()
	accounts := []*Account{{ID: 1, Priority: 1, Type: AccountTypeAPIKey}, {ID: 2, Priority: 1, Type: AccountTypeAPIKey}, {ID: 3, Priority: 2, Type: AccountTypeAPIKey}}
	anthropicTestSamples(svc, accounts[0], "model", 11*time.Second, 6, now)
	for _, name := range []string{"model routing", "layer 2", "layer 3"} {
		t.Run(name, func(t *testing.T) {
			if name == "layer 2" {
				items := []accountWithLoad{{account: accounts[0]}, {account: accounts[1]}, {account: accounts[2]}}
				got := svc.partitionAnthropicLatencyHeldLoad(items, "model")
				require.Equal(t, []int64{2, 1, 3}, []int64{got[0].account.ID, got[1].account.ID, got[2].account.ID})
				return
			}
			got := svc.partitionAnthropicLatencyHeldAccounts(accounts, "model")
			require.Equal(t, []int64{2, 1, 3}, []int64{got[0].ID, got[1].ID, got[2].ID})
		})
	}
	t.Run("last resort", func(t *testing.T) {
		got := pickLastResort([]lastResortCandidate{{account: accounts[0], held: true}, {account: accounts[1]}, {account: accounts[2]}})
		require.Equal(t, []int64{2, 1, 3}, []int64{got[0].account.ID, got[1].account.ID, got[2].account.ID})
	})
}

func TestAnthropicLatencyMigration(t *testing.T) {
	old := *resilience.Current()
	t.Cleanup(func() { resilience.Publish(old) })
	for _, tc := range []struct {
		name                                                             string
		targetHeld, insufficient, full, priority, deleteFail, sessionOff bool
		migrated                                                         bool
		reason                                                           string
	}{
		{name: "healthy", migrated: true, reason: "latency_hold"}, {name: "held", targetHeld: true, reason: "no_target"}, {name: "insufficient", insufficient: true, reason: "insufficient_samples"}, {name: "full", full: true, reason: "no_free_slot"}, {name: "priority", priority: true, reason: "no_target"}, {name: "delete failure", deleteFail: true, reason: "owner_changed"}, {name: "session off", sessionOff: true, reason: "session_off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := resilience.Defaults()
			settings.Session.Enabled = !tc.sessionOff
			resilience.Publish(settings)
			accounts := probeTestAccounts(PlatformAnthropic)
			accounts[1].Priority = accounts[0].Priority
			if tc.priority {
				accounts[1].Priority++
			}
			inFlight := map[int64]int{}
			if tc.full {
				inFlight[2] = accounts[1].Concurrency
			}
			svc, _, base := newGatewayProbeTestService(accounts, inFlight, map[string]int64{"sess": 1}, nil)
			cache := &anthropicTestCache{mockGatewayCacheForPlatform: base, failDelete: tc.deleteFail}
			svc.cache = cache
			now := time.Now()
			anthropicTestSamples(svc, &accounts[0], "model", 11*time.Second, 6, now)
			if !tc.insufficient {
				duration := time.Second
				if tc.targetHeld {
					duration = 11 * time.Second
				}
				anthropicTestSamples(svc, &accounts[1], "model", duration, 7, now)
			}
			held := svc.anthropicLatencyHoldSnapshot(&accounts[0], "model", now)
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			id, migrated := svc.migrateAnthropicLatencyHeldOwner(context.Background(), nil, "sess", "model", 1, []*Account{&accounts[0], &accounts[1]}, nil, nil, func(*Account, bool) bool { return true }, held)
			require.Equal(t, tc.migrated, migrated)
			require.Contains(t, logs.String(), `"reason":"`+tc.reason+`"`)
			if tc.migrated {
				require.Zero(t, id)
				require.Equal(t, 1, cache.deletes)
			} else {
				require.Equal(t, int64(1), id)
				require.Equal(t, int64(1), cache.sessionBindings["sess"])
			}
		})
	}
}

func TestAnthropicLatencySelectionMigratesAndRebinds(t *testing.T) {
	old := *resilience.Current()
	t.Cleanup(func() { resilience.Publish(old) })
	resilience.Publish(resilience.Defaults())
	accounts := probeTestAccounts(PlatformAnthropic)
	accounts[1].Priority = accounts[0].Priority
	svc, _, base := newGatewayProbeTestService(accounts, map[int64]int{}, map[string]int64{"sess": 1}, nil)
	cache := &anthropicTestCache{mockGatewayCacheForPlatform: base}
	svc.cache = cache
	now := time.Now()
	anthropicTestSamples(svc, &accounts[0], "mapped-model", 11*time.Second, 6, now)
	anthropicTestSamples(svc, &accounts[1], "mapped-model", time.Second, 7, now)
	ctx := WithAnthropicLatencyModel(context.Background(), "mapped-model")
	result := selectGatewayForProbeTest(t, ctx, svc, "sess", "model")
	require.Equal(t, int64(2), result.Account.ID)
	require.Equal(t, int64(2), cache.sessionBindings["sess"])
	require.Equal(t, 1, cache.deletes)
	require.False(t, result.stickySessionHit)
	if result.ReleaseFunc != nil {
		result.ReleaseFunc()
	}
}
