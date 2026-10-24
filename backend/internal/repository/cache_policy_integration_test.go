//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCachePolicyPostgresConcurrentFirstWriteAndRollback(t *testing.T) {
	ctx := context.Background()
	repo := NewSettingRepository(testEntClient(t))
	key := "cache_observability_policy"
	require.NoError(t, repo.Delete(ctx, key))
	t.Cleanup(func() { require.NoError(t, repo.Delete(ctx, key)) })
	cfg := &config.Config{}
	cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
	a := service.NewCachePolicyService(repo, cfg)
	b := service.NewCachePolicyService(repo, cfg)
	// Failed validation must not leave a dummy row from the first-write lock.
	w, errs := service.DecodeCachePolicyWrite(strings.NewReader(`{"expected_rev":0,"spec":{"sample_percent":200}}`))
	_, _, err := a.Update(ctx, "observability", w, errs, service.CachePolicyActor{})
	require.Error(t, err)
	_, err = repo.GetValue(ctx, key)
	require.ErrorIs(t, err, service.ErrSettingNotFound)
	w, errs = service.DecodeCachePolicyWrite(strings.NewReader(`{"expected_rev":0,"spec":{"enabled":true}}`))
	require.Empty(t, errs)
	var winners atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := a
			if i%2 == 1 {
				s = b
			}
			_, _, err := s.Update(ctx, "observability", w, nil, service.CachePolicyActor{UserID: 1})
			if err == nil {
				winners.Add(1)
				return
			}
			var failure *service.CachePolicyError
			if !errors.As(err, &failure) || failure.Status != 409 {
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	require.EqualValues(t, 1, winners.Load())
	raw, err := repo.GetValue(ctx, key)
	require.NoError(t, err)
	var env service.CachePolicyEnvelope
	require.NoError(t, json.Unmarshal([]byte(raw), &env))
	require.EqualValues(t, 1, env.Rev)
	// Two real service instances share only the database. The second instance
	// discovers a write using its normal refresh loop within the public interval.
	c := service.NewCachePolicyService(repo, cfg)
	c.Start()
	defer c.Stop()
	require.True(t, c.Snapshot().TraceEnabled())
	w, errs = service.DecodeCachePolicyWrite(strings.NewReader(`{"expected_rev":1,"spec":{"enabled":false}}`))
	require.Empty(t, errs)
	_, _, err = a.Update(ctx, "observability", w, nil, service.CachePolicyActor{})
	require.NoError(t, err)
	require.False(t, a.Snapshot().TraceEnabled())
	require.Eventually(t, func() bool { return !c.Snapshot().TraceEnabled() }, 60*time.Second, 100*time.Millisecond)
}

func TestCachePolicyPostgresAuditFilters(t *testing.T) {
	ctx := context.Background()
	db := integrationDB
	repo := NewAuditLogRepository(db)
	now := time.Now().UTC().Truncate(time.Microsecond)
	uid := int64(1)
	action := "admin.cache.policies.master.update"
	t.Cleanup(func() {
		_, err := db.ExecContext(ctx, "DELETE FROM audit_logs WHERE action=$1 AND created_at >= $2", action, now)
		require.NoError(t, err)
	})
	for i, status := range []int{200, 422, 200} {
		extra := map[string]any{"policy_key": "master", "changed_fields": "enabled"}
		if status == 200 {
			rev := int64(1)
			if i == 2 {
				rev = 2
			}
			extra["rev_before"] = rev - 1
			extra["rev_after"] = rev
		}
		require.NoError(t, repo.Insert(ctx, &service.AuditLog{CreatedAt: now.Add(time.Duration(i) * time.Second), Action: action, Method: "PUT", StatusCode: status, ActorUserID: &uid, ActorEmail: "admin@example.com", RequestBody: `{"reason":"test"}`, Extra: extra}))
	}
	after := int64(1)
	got, err := repo.List(ctx, &service.AuditLogFilter{PageSize: 20, StartTime: &now, Action: action, Method: "PUT", CachePolicyOnly: true, CachePolicyAfterRev: &after, IncludeRequestBody: true})
	require.NoError(t, err)
	require.Equal(t, 1, got.Total)
	require.Len(t, got.Logs, 1)
	require.Contains(t, got.Logs[0].RequestBody, "test")
	before := now.Add(time.Second)
	got, err = repo.List(ctx, &service.AuditLogFilter{PageSize: 20, StartTime: &now, Action: action, Method: "PUT", CachePolicyOnly: true, CachePolicyBefore: &before})
	require.NoError(t, err)
	require.Equal(t, 1, got.Total)
	require.Empty(t, got.Logs[0].RequestBody)
	coverageFilter := &service.AuditLogFilter{Action: action, Method: "PUT", StartTime: &now,
		CachePolicyOnly: true, CachePolicyRevisionOnly: true, CachePolicyCoverage: true}
	got, err = repo.List(ctx, coverageFilter)
	require.NoError(t, err)
	require.Equal(t, &service.CachePolicyRevisionCoverage{Count: 2, MinRev: 1, MaxRev: 2}, got.PolicyRevisions)
	_, err = db.ExecContext(ctx, "DELETE FROM audit_logs WHERE action=$1 AND created_at=$2", action, now)
	require.NoError(t, err)
	got, err = repo.List(ctx, coverageFilter)
	require.NoError(t, err)
	require.Equal(t, &service.CachePolicyRevisionCoverage{Count: 1, MinRev: 2, MaxRev: 2}, got.PolicyRevisions)

}
