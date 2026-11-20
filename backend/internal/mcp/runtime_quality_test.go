//go:build unit

package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type qualitySnapshotStub struct {
	service.SchedQualityEngine
	snapshot *service.SchedQualitySnapshot
	reads    int
}

func (s *qualitySnapshotStub) Snapshot() *service.SchedQualitySnapshot { s.reads++; return s.snapshot }

func TestRuntimeQualityPoliciesKeepAccountsViewPermission(t *testing.T) {
	now := time.Now().UTC()
	actor := Actor{UserID: 1, Role: service.RoleOperator, Scope: &operatoraccess.Scope{Perms: operatoraccess.Permissions{Modules: map[operatoraccess.Module]operatoraccess.Level{operatoraccess.ModuleAccounts: operatoraccess.LevelView}}}}
	policy := corePolicy(t, "xy2api.routing.get_quality_state")
	require.True(t, policyAllowed(actor, policy))
	r := &runtimeReader{quality: &qualitySnapshotStub{snapshot: &service.SchedQualitySnapshot{At: now}}}
	q, err := newReaderQuery(QueryRequest{Tool: policy.Name, Actor: actor, Input: QueryInput{IncludePolicies: true}, RequireSource: func(_ context.Context, key string) error {
		return additionalSource(actor, policy, key)
	}}, now)
	require.NoError(t, err)
	require.NoError(t, r.qualityState(context.Background(), q))
	delete(actor.Scope.Perms.Modules, operatoraccess.ModuleAccounts)
	require.ErrorIs(t, r.qualityState(context.Background(), q), ErrForbidden)
}

func TestRuntimeQualityUsesSnapshotAndPolicy(t *testing.T) {
	now := time.Now().UTC()
	snapshot := &service.SchedQualitySnapshot{At: now, GlobalEnabled: true, Members: map[int64][]int64{1: {2}}, GroupRows: map[int64][]service.SchedGroupRow{1: {{GroupID: 2, Model: "a", View: schedquality.StateView{TTFO: schedquality.LineView{Identity: schedquality.IdentityRear}}}, {GroupID: 2, Model: "b"}}}, Policies: map[int64]service.SchedPolicyRecord{2: {Version: 7, Policy: schedquality.Policy{TTFTSeconds: 15}}}}
	engine := &qualitySnapshotStub{snapshot: snapshot}
	r := &runtimeReader{quality: engine}
	req := QueryRequest{RequireSource: func(_ context.Context, key string) error {
		require.Equal(t, "GET /sched-quality/accounts/:id", key)
		return nil
	}, Tool: "xy2api.routing.get_quality_state", Actor: testReaderActor(), Input: QueryInput{GroupID: 2, IncludePolicies: true, Limit: 1}, Cursor: testReaderCursor()}
	q, err := newReaderQuery(req, now)
	require.NoError(t, err)
	require.NoError(t, r.qualityState(context.Background(), q))
	require.Equal(t, 1, engine.reads)
	require.Len(t, q.result.Items, 1)
	require.Equal(t, "a", q.result.Items[0]["model"])
	require.Equal(t, "rear", q.result.Items[0]["placement"])
	require.Equal(t, "rear", q.result.Items[0]["lines"].(service.SchedAdminLines).TTFO.Identity)
	require.NotContains(t, q.result.Items[0], "state")
	raw, err := json.Marshal(q.result.Items[0])
	require.NoError(t, err)
	require.Contains(t, string(raw), `"placement":"rear"`)
	require.Contains(t, string(raw), `"identity":"rear"`)
	require.NotContains(t, string(raw), `"dims"`)
	require.NotContains(t, string(raw), `"failing"`)
	require.Equal(t, int64(7), q.result.Items[0]["policy_version"])
	require.Equal(t, service.SchedAdminPolicyPolicy{TTFTSeconds: 15}, q.result.Items[0]["policy"])
	require.Equal(t, []string{"process.sched_quality_snapshot"}, q.result.Source)
	require.NotEmpty(t, q.result.NextCursor)
	req.Input.Cursor = q.result.NextCursor
	q, err = newReaderQuery(req, now)
	require.NoError(t, err)
	require.NoError(t, r.qualityState(context.Background(), q))
	require.Len(t, q.result.Items, 1)
	require.Equal(t, "b", q.result.Items[0]["model"])
	require.Empty(t, q.result.NextCursor)
	// A cursor cannot silently continue on a different published snapshot.
	snapshot.At = now.Add(time.Second)
	q, err = newReaderQuery(req, now)
	require.NoError(t, err)
	require.ErrorIs(t, r.qualityState(context.Background(), q), ErrInvalidInput)
	require.Equal(t, schedquality.IdentityRear, snapshot.GroupRows[1][0].View.TTFO.Identity)
}

type qualityBalanceStub struct {
	service.UpstreamBalanceStore
	data *service.UpstreamBalanceEntries
}

func (s qualityBalanceStub) Load(context.Context, []int64) (*service.UpstreamBalanceEntries, error) {
	return s.data, nil
}
func TestRuntimeHealthUsesQualitySummaryAndKeepsBalance(t *testing.T) {
	now := time.Now().UTC()
	amount := 12.5
	median := 120
	ratio := .9
	reset := now.Add(-time.Minute)
	snapshot := &service.SchedQualitySnapshot{
		At: now, ResetAt: map[int64]time.Time{1: reset},
		Accounts: map[int64]schedquality.AccountSummary{1: {
			From: reset, To: now,
			FirstOutput: schedquality.FirstOutputSummary{P50Ms: &median, N: 7, Timeouts: 2},
			Slow10s:     schedquality.Slow10sSummary{N: 6, Slow: 2, Timeouts: 1},
			ErrorN:      50, ErrorBad: 5, CacheTurns: 40, CacheRatio: &ratio,
		}},
	}

	engine := &qualitySnapshotStub{snapshot: snapshot}
	balance := &service.UpstreamBalanceEntries{Records: map[int64]*service.UpstreamBalanceRecord{1: {AmountUSD: &amount, ObservedAt: now, Fingerprint: "must-not-be-returned"}}}
	r := &runtimeReader{summaries: service.NewSchedAdminService(service.SchedAdminDependencies{Engine: engine, Accounts: qualityHealthAccounts{}}), balances: qualityBalanceStub{data: balance}}
	q, err := newReaderQuery(QueryRequest{Tool: "xy2api.accounts.get_health", Actor: testReaderActor(), Input: QueryInput{AccountIDs: []int64{1}}}, now)
	require.NoError(t, err)
	require.NoError(t, r.health(context.Background(), q, []map[string]any{{"account_id": int64(1)}}))
	require.Equal(t, 1, engine.reads)
	row := q.result.Items[0]
	require.NotContains(t, row, "model")
	require.Equal(t, snapshot.Account(1).FirstOutput, row["first_output"])
	require.Equal(t, snapshot.Account(1).Slow10s, row["slow_10s"])
	require.Equal(t, reset.Format(time.RFC3339Nano), *row["recent_since"].(*string))
	require.Equal(t, 5, row["failure"].(service.SchedAdminAccountSummaryError).Failed)
	require.Equal(t, 0.1, *row["failure"].(service.SchedAdminAccountSummaryError).Rate)
	require.Equal(t, 0.9, *row["cache"].(service.SchedAdminAccountSummaryCache).Ratio)
	require.Equal(t, &amount, row["balance"].(map[string]any)["amount_usd"])
	require.NotContains(t, row["balance"], "fingerprint")
	require.Equal(t, []string{"process.sched_quality_snapshot", "redis.upstream_balance_snapshot"}, q.result.Source)
	require.Equal(t, 40, snapshot.Account(1).CacheTurns)
}

type qualityHealthAccounts struct {
	service.SchedAdminAccountRepository
}

func (qualityHealthAccounts) GetByIDs(_ context.Context, ids []int64) ([]*service.Account, error) {
	out := []*service.Account{}
	for _, id := range ids {
		out = append(out, &service.Account{ID: id, Platform: service.PlatformOpenAI, Status: service.StatusActive, Schedulable: true})
	}
	return out, nil
}

func TestRuntimeQualityKeepsTrialDeadlineAndEvidence(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	key := schedquality.LineKey{AccountID: 1, GroupID: 2, Model: "model-a", Line: schedquality.DimensionTTFO}
	entered, action := schedquality.Step(schedquality.Relaxed(key), schedquality.EventRegular, schedquality.VerdictFail, schedquality.Basis{}, now)
	require.Equal(t, schedquality.ActionEnter, action)
	trial, action := schedquality.Step(entered, schedquality.EventReview, schedquality.VerdictUnknown, schedquality.Basis{}, entered.Until)
	require.Equal(t, schedquality.ActionTrial, action)
	snapshot := &service.SchedQualitySnapshot{At: trial.EvaluatedAt, GlobalEnabled: true, Members: map[int64][]int64{1: {2}}, GroupRows: map[int64][]service.SchedGroupRow{1: {{GroupID: 2, Model: "model-a", View: schedquality.StateView{TTFO: schedquality.LineView{Identity: trial.Identity, Trial: trial.Trial, Level: trial.Level, Until: trial.Until, TrialUntil: trial.TrialUntil}}, TTFTSourceGroups: []int64{3}, TTFT: schedquality.TTFTEvidence{Causes: []schedquality.RuleBUnknownReason{schedquality.RuleBNoPeers}}}}}}
	r := &runtimeReader{quality: &qualitySnapshotStub{snapshot: snapshot}}
	q, err := newReaderQuery(QueryRequest{Tool: "xy2api.routing.get_quality_state", Actor: testReaderActor(), Input: QueryInput{AccountID: 1}}, trial.EvaluatedAt)
	require.NoError(t, err)
	require.NoError(t, r.qualityState(context.Background(), q))
	raw, err := json.Marshal(q.result.Items[0])
	require.NoError(t, err)
	var row map[string]any
	require.NoError(t, json.Unmarshal(raw, &row))
	require.Equal(t, "trial", row["placement"])
	ttfo := row["lines"].(map[string]any)["ttfo"].(map[string]any)
	require.Equal(t, "trial", ttfo["identity"])
	require.Nil(t, ttfo["until"])
	require.Equal(t, trial.TrialUntil.Format(time.RFC3339Nano), ttfo["trial_until"])
	require.Equal(t, []any{float64(3)}, ttfo["source_groups"])
	evidence := ttfo["evidence"].(map[string]any)
	require.Equal(t, []any{"no_peers"}, evidence["causes"])
	for _, key := range []string{"p_rel", "u", "z"} {
		require.Nil(t, evidence[key])
	}
	require.NotContains(t, row, "dims")
	require.NotContains(t, row, "failing")
	require.NotContains(t, row, "status")
}
