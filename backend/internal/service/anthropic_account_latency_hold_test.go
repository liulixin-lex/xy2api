//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type anthropicTestCache struct {
	*mockGatewayCacheForPlatform
	failDelete   bool
	deletes      int
	ownerChanged bool
	newOwner     int64
}

func (c *anthropicTestCache) DeleteSessionAccountIDIfOwnerAndReport(_ context.Context, _ int64, session string, owner int64) (bool, error) {
	c.deletes++
	if c.failDelete {
		return false, errors.New("delete failed")
	}
	if c.ownerChanged {
		delete(c.sessionBindings, session)
		if c.newOwner > 0 {
			c.sessionBindings[session] = c.newOwner
		}
		return false, nil
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
	svc.noteAnthropicStickyMigration(probeTestGroupID, "sess", now)
	off := on
	off.AnthropicLatencyHold.Enabled = false
	resilience.Publish(off)
	resilience.Publish(on)
	require.False(t, svc.anthropicLatencyHoldSnapshot(a, "model", now).Held)
	require.False(t, svc.anthropicStickyMigrationCoolingDown(probeTestGroupID, "sess", now), "disable/re-enable clears migration cooldowns")
	anthropicTestSamples(svc, a, "model", 11*time.Second, 5, now)
	require.False(t, svc.anthropicLatencyHoldSnapshot(a, "model", now).Held)
	anthropicTestSamples(svc, a, "model", 11*time.Second, 1, now)
	require.True(t, svc.anthropicLatencyHoldSnapshot(a, "model", now).Held)
	resilience.Publish(off)
	anthropicTestSamples(svc, a, "model", time.Second, 7, now)
	resilience.Publish(on)
	anthropicTestSamples(svc, a, "model", 11*time.Second, 5, now)
	require.False(t, svc.anthropicLatencyHoldSnapshot(a, "model", now).Held)
	anthropicTestSamples(svc, a, "model", 11*time.Second, 1, now)
	require.True(t, svc.anthropicLatencyHoldSnapshot(a, "model", now).Held)
}

func TestAnthropicLatencyStaleReaderPreservesNewGeneration(t *testing.T) {
	old := *resilience.Current()
	t.Cleanup(func() { resilience.Publish(old) })
	on := resilience.Defaults()
	resilience.Publish(on)
	svc := &GatewayService{}
	account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	now := time.Now()
	anthropicTestSamples(svc, account, "model", 11*time.Second, 6, now)
	staleGeneration := resilience.AnthropicLatencyHoldGeneration()
	off := on
	off.AnthropicLatencyHold.Enabled = false
	resilience.Publish(off)
	resilience.Publish(on)
	anthropicTestSamples(svc, account, "model", 11*time.Second, 5, now)
	svc.noteAnthropicStickyMigration(probeTestGroupID, "sess", now)
	currentGeneration := resilience.AnthropicLatencyHoldGeneration()
	require.Greater(t, currentGeneration, staleGeneration)

	// The reader observed N before waiting for a writer that reached N+1.
	state, active := svc.anthropicLatencyStateForGeneration(staleGeneration)
	require.True(t, active)
	require.NotNil(t, state)
	require.Equal(t, currentGeneration, svc.anthropicLatencyHoldReset.Load())
	require.True(t, svc.anthropicStickyMigrationCoolingDown(probeTestGroupID, "sess", now))
	require.False(t, svc.anthropicLatencyHoldSnapshot(account, "model", now).Held)
	anthropicTestSamples(svc, account, "model", 11*time.Second, 1, now)
	require.True(t, svc.anthropicLatencyHoldSnapshot(account, "model", now).Held, "new-generation samples must survive the stale reader")
}

func TestAnthropicLatencyMigrationCooldownCapacity(t *testing.T) {
	old := *resilience.Current()
	t.Cleanup(func() { resilience.Publish(old) })
	resilience.Publish(resilience.Defaults())
	svc := &GatewayService{}
	now := time.Now()
	anthropicTestSamples(svc, &Account{ID: 1}, "model", time.Second, 1, now)
	for i := 0; i < anthropicStickyMigrationMax; i++ {
		svc.noteAnthropicStickyMigration(probeTestGroupID, strconv.Itoa(i), now.Add(time.Duration(i)*time.Millisecond))
	}
	svc.noteAnthropicStickyMigration(probeTestGroupID, "new", now.Add(time.Minute))
	require.Len(t, svc.anthropicStickyMigration, anthropicStickyMigrationMax)
	require.False(t, svc.anthropicStickyMigrationCoolingDown(probeTestGroupID, "0", now.Add(time.Minute)), "the oldest entry must be evicted")
	require.True(t, svc.anthropicStickyMigrationCoolingDown(probeTestGroupID, "1", now.Add(time.Minute)))
	require.True(t, svc.anthropicStickyMigrationCoolingDown(probeTestGroupID, "new", now.Add(time.Minute)))
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
		{"non-streaming", &AccountSelectionResult{stickySessionHit: true}, false, false, false, true, false},
		{"no semantic output", &AccountSelectionResult{stickySessionHit: true}, false, false, true, false, false},
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
			anthropicTestSamples(svc, account, "outbound", 11*time.Second, 5, start.Add(20*time.Second))
			require.Equal(t, tc.want, svc.anthropicLatencyHoldSnapshot(account, "alias", start.Add(20*time.Second)).Held)
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
	for _, name := range []string{"account segment partition", "load segment partition"} {
		t.Run(name, func(t *testing.T) {
			if name == "load segment partition" {
				items := []accountWithLoad{{account: accounts[0]}, {account: accounts[1]}, {account: accounts[2]}}
				got := svc.partitionAnthropicLatencyHeldLoad(items, "model")
				require.Equal(t, []int64{2, 1, 3}, []int64{got[0].account.ID, got[1].account.ID, got[2].account.ID})
				return
			}
			got := svc.partitionAnthropicLatencyHeldAccounts(accounts, "model")
			require.Equal(t, []int64{2, 1, 3}, []int64{got[0].ID, got[1].ID, got[2].ID})
		})
	}
}

func TestAnthropicLatencyMigration(t *testing.T) {
	old := *resilience.Current()
	t.Cleanup(func() { resilience.Publish(old) })
	for _, tc := range []struct {
		name                                                                                                     string
		targetHeld, full, priority, deleteFail, sessionOff, rejectEligible, routeMiss, cooldown, expiredCooldown bool
		migrated                                                                                                 bool
		reason                                                                                                   string
		ownerChanged                                                                                             bool
		newOwner                                                                                                 int64
	}{
		{name: "healthy", migrated: true, reason: "latency_hold"},
		{name: "held target", targetHeld: true, reason: "no_target"},
		{name: "no free slot", full: true, reason: "no_free_slot"},
		{name: "different priority", priority: true, reason: "no_target"},
		{name: "delete failure", deleteFail: true, reason: "owner_changed"},
		{name: "owner changed", ownerChanged: true, newOwner: 2, reason: "owner_changed"},
		{name: "owner missing", ownerChanged: true, reason: "owner_changed"},
		{name: "session off", sessionOff: true, reason: "session_off"},
		{name: "admission rejected", rejectEligible: true, reason: "no_target"},
		{name: "outside routed set", routeMiss: true, reason: "no_target"},
		{name: "migration cooldown", cooldown: true, reason: "cooldown"},
		{name: "expired migration cooldown", expiredCooldown: true, migrated: true, reason: "latency_hold"},
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
			cache := &anthropicTestCache{mockGatewayCacheForPlatform: base, failDelete: tc.deleteFail, ownerChanged: tc.ownerChanged, newOwner: tc.newOwner}
			svc.cache = cache
			now := time.Now()
			anthropicTestSamples(svc, &accounts[0], "model", 11*time.Second, 6, now)
			duration := time.Second
			if tc.targetHeld {
				duration = 11 * time.Second
			}
			anthropicTestSamples(svc, &accounts[1], "model", duration, 7, now)
			held := svc.anthropicLatencyHoldSnapshot(&accounts[0], "model", now)
			if tc.cooldown {
				svc.noteAnthropicStickyMigration(probeTestGroupID, "sess", now)
			}
			if tc.expiredCooldown {
				svc.noteAnthropicStickyMigration(probeTestGroupID, "sess", now.Add(-anthropicStickyMigrationCooldown-time.Second))
			}
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			routingIDs := []int64(nil)
			if tc.routeMiss {
				routingIDs = []int64{1}
			}
			eligible := func(*Account, bool) bool { return !tc.rejectEligible }
			groupID := probeTestGroupID
			id, migrated := svc.migrateAnthropicLatencyHeldOwner(context.Background(), &groupID, "sess", "model", 1, []*Account{&accounts[0], &accounts[1]}, routingIDs, nil, eligible, held)
			require.Equal(t, tc.migrated, migrated)
			require.Contains(t, logs.String(), `"reason":"`+tc.reason+`"`)
			if tc.migrated {
				require.Zero(t, id)
				require.Equal(t, 1, cache.deletes)
				// Normal selection binds a new owner. If it also becomes held,
				// the successful migration must suppress another deletion.
				cache.sessionBindings["sess"] = 2
				anthropicTestSamples(svc, &accounts[1], "model", 11*time.Second, 6, now)
				newHeld := svc.anthropicLatencyHoldSnapshot(&accounts[1], "model", now)
				require.True(t, newHeld.Held)
				logs.Reset()
				id, migrated = svc.migrateAnthropicLatencyHeldOwner(context.Background(), &groupID, "sess", "model", 2,
					[]*Account{&accounts[0], &accounts[1]}, nil, nil, eligible, newHeld)
				require.Equal(t, int64(2), id)
				require.False(t, migrated)
				require.Contains(t, logs.String(), `"reason":"cooldown"`)
				require.Equal(t, 1, cache.deletes)
			} else if tc.ownerChanged {
				require.Equal(t, tc.newOwner, id)
				require.Equal(t, tc.newOwner, cache.sessionBindings["sess"])
				require.Equal(t, 1, cache.deletes)
			} else {
				require.Equal(t, int64(1), id)
				require.Equal(t, int64(1), cache.sessionBindings["sess"])
			}
			if tc.deleteFail || tc.ownerChanged {
				require.False(t, svc.anthropicStickyMigrationCoolingDown(groupID, "sess", now), "failed deletions must not start a cooldown")
			}
		})
	}
}

func TestAnthropicLatencySelectionMigratesAndRebinds(t *testing.T) {
	old := *resilience.Current()
	t.Cleanup(func() { resilience.Publish(old) })
	resilience.Publish(resilience.Defaults())
	for _, tc := range []struct {
		name       string
		bound      int64
		route      []int64
		batchError bool
		full       bool
		want       int64
		wantSticky bool
		channel    bool
	}{
		{name: "held sticky owner yields despite adverse LRU", bound: 1, want: 2},
		{name: "healthy sticky owner is marked hot", bound: 2, want: 2, wantSticky: true},
		{name: "routed migration and ordering", bound: 1, route: []int64{1, 3}, want: 3},
		{name: "channel mapped migration", bound: 1, channel: true, want: 2},
		{name: "batch load error retains deferral", batchError: true, want: 2},
		{name: "all full waits for healthy same-tier account", full: true, want: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			accounts := probeTestAccounts(PlatformAnthropic)
			accounts = append(accounts, Account{ID: 3, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
				Priority: 2, Concurrency: 4, LoadFactor: intPtr(1), GroupIDs: []int64{probeTestGroupID}, AccountGroups: []AccountGroup{{GroupID: probeTestGroupID}}})
			accounts[1].Priority = accounts[0].Priority
			if len(tc.route) > 0 {
				accounts[2].Priority = accounts[0].Priority
			}
			older, newer := time.Now().Add(-time.Minute), time.Now()
			accounts[0].LastUsedAt, accounts[1].LastUsedAt = &older, &newer
			accounts[2].LastUsedAt = &newer
			var group *Group
			if len(tc.route) > 0 {
				group = &Group{ID: probeTestGroupID, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true,
					ModelRoutingEnabled: true, ModelRouting: map[string][]int64{"model": tc.route}}
			}
			inFlight := map[int64]int{}
			if tc.full {
				inFlight[1], inFlight[2], inFlight[3] = 2, 4, 4
			}
			bindings := map[string]int64{}
			if tc.bound > 0 {
				bindings["sess"] = tc.bound
			}
			svc, slots, base := newGatewayProbeTestService(accounts, inFlight, bindings, group)
			cache := &anthropicTestCache{mockGatewayCacheForPlatform: base}
			svc.cache = cache
			if tc.batchError {
				slots.loadErr = errors.New("load batch failed")
			}
			now := time.Now()
			ctx := context.Background()
			latencyModel := "model"
			if tc.channel {
				latencyModel = "mapped-model"
				ctx = WithAnthropicLatencyModel(ctx, latencyModel)
			}
			anthropicTestSamples(svc, &accounts[0], latencyModel, 11*time.Second, 6, now)
			result := selectGatewayForProbeTest(t, ctx, svc, "sess", "model")
			require.Equal(t, tc.want, result.Account.ID)
			require.Equal(t, tc.wantSticky, result.stickySessionHit)
			if tc.bound == 1 && !tc.batchError && !tc.full {
				require.Equal(t, int64(tc.want), cache.sessionBindings["sess"])
				require.Equal(t, 1, cache.deletes)
				require.False(t, result.stickySessionHit)
			}
			if tc.full {
				require.NotNil(t, result.WaitPlan)
				require.Equal(t, int64(2), result.WaitPlan.AccountID)
				require.NotEqual(t, int64(3), result.WaitPlan.AccountID, "must not pass a lower priority candidate")
			}
			if tc.batchError {
				require.True(t, result.Acquired)
				require.NotEqual(t, int64(1), result.Account.ID)
			}
			if result.ReleaseFunc != nil {
				result.ReleaseFunc()
			}
		})
	}
}
