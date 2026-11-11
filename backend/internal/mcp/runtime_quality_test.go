//go:build unit

package mcp

import (
	"context"
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
	snapshot := &service.SchedQualitySnapshot{At: now, GlobalEnabled: true, GroupRows: map[int64][]service.SchedGroupRow{1: {{GroupID: 2, Model: "a", View: schedquality.StateView{State: schedquality.StateCooling, Reason: schedquality.ReasonSpeed}}, {GroupID: 2, Model: "b"}}}, Policies: map[int64]service.SchedPolicyRecord{2: {Version: 7, Policy: schedquality.Policy{TTFTSeconds: 15}}}}
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
	require.Equal(t, "cooling", q.result.Items[0]["state"])
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
	require.Equal(t, schedquality.StateCooling, snapshot.GroupRows[1][0].View.State)
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
	snapshot := &service.SchedQualitySnapshot{At: now, Evidence: map[service.SchedAccountModel]schedquality.Summary{
		{AccountID: 1, Model: "small"}: {Samples: 1},
		{AccountID: 1, Model: "main"}:  {Samples: 50, Speed: schedquality.SpeedSummary{N: 40, Sessions: 4, P90Ms: 120}, Error: schedquality.ErrorSummary{N: 50, Sessions: 4, Rate: 0.1}, Cache: schedquality.CacheSummary{Turns: 40, Sessions: 4, Ratio: 0.9}},
	}}
	engine := &qualitySnapshotStub{snapshot: snapshot}
	balance := &service.UpstreamBalanceEntries{Records: map[int64]*service.UpstreamBalanceRecord{1: {AmountUSD: &amount, ObservedAt: now, Fingerprint: "must-not-be-returned"}}}
	r := &runtimeReader{summaries: service.NewSchedAdminService(service.SchedAdminDependencies{Engine: engine}), balances: qualityBalanceStub{data: balance}}
	q, err := newReaderQuery(QueryRequest{Tool: "xy2api.accounts.get_health", Actor: testReaderActor(), Input: QueryInput{AccountIDs: []int64{1}}}, now)
	require.NoError(t, err)
	require.NoError(t, r.health(context.Background(), q, []map[string]any{{"account_id": int64(1)}}))
	require.Equal(t, 1, engine.reads)
	row := q.result.Items[0]
	require.Equal(t, "main", *row["model"].(*string))
	require.Equal(t, 120, *row["first_output"].(service.SchedAdminAccountSummaryTTFO).P90Ms)
	require.Equal(t, 0.1, *row["failure"].(service.SchedAdminAccountSummaryError).Rate)
	require.Equal(t, 0.9, *row["cache"].(service.SchedAdminAccountSummaryCache).Ratio)
	require.Equal(t, &amount, row["balance"].(map[string]any)["amount_usd"])
	require.NotContains(t, row["balance"], "fingerprint")
	require.Equal(t, []string{"process.sched_quality_snapshot", "redis.upstream_balance_snapshot"}, q.result.Source)
	require.Equal(t, 40, snapshot.Evidence[service.SchedAccountModel{AccountID: 1, Model: "main"}].Speed.N)
}
