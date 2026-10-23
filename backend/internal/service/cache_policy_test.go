//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

type policyTestStore struct {
	SettingRepository
	mu       sync.Mutex
	rows     map[string]string
	readErr  error
	writeErr error
}

func newPolicyTestStore() *policyTestStore { return &policyTestStore{rows: map[string]string{}} }
func (r *policyTestStore) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.readErr != nil {
		return nil, r.readErr
	}
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := r.rows[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}
func (r *policyTestStore) UpdateCachePolicy(_ context.Context, key string, fn func(string) (string, error)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, err := fn(r.rows[key])
	if err != nil {
		return err
	}
	if r.writeErr != nil {
		return r.writeErr
	}
	r.rows[key] = value
	return nil
}
func policyTestConfig() *config.Config {
	c := &config.Config{}
	c.Gateway.OpenAICache.DiagnosticsEnabled = true
	return c
}
func policyWrite(t *testing.T, raw string) CachePolicyWrite {
	t.Helper()
	w, e := DecodeCachePolicyWrite(strings.NewReader(raw))
	require.Empty(t, e)
	return w
}
func TestCachePolicyLifecycleAndImmutableSnapshot(t *testing.T) {
	ctx := context.Background()
	repo := newPolicyTestStore()
	a := NewCachePolicyService(repo, policyTestConfig())
	b := NewCachePolicyService(repo, policyTestConfig())
	old := a.Snapshot()
	require.False(t, old.TraceEnabled())
	v, _, err := a.Update(ctx, "observability", policyWrite(t, `{"expected_rev":0,"spec":{"enabled":true}}`), nil, CachePolicyActor{1, "admin@example.com"})
	require.NoError(t, err)
	require.EqualValues(t, 1, v.Stored.Rev)
	require.True(t, a.Snapshot().TraceEnabled())
	require.False(t, old.TraceEnabled())
	require.False(t, b.Snapshot().TraceEnabled())
	stale, err := b.Get(ctx, "observability")
	require.NoError(t, err)
	require.Equal(t, "pending_reload", stale.Effective.State)
	require.False(t, *stale.Effective.Active)
	require.NoError(t, b.Refresh(ctx))
	require.True(t, b.Snapshot().TraceEnabled())
	frozen := a.Snapshot()
	_, _, err = a.Update(ctx, "observability", policyWrite(t, `{"expected_rev":1,"spec":{"enabled":false}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	require.True(t, frozen.TraceEnabled())
	require.False(t, a.Snapshot().TraceEnabled())
	repo.readErr = errors.New("offline")
	require.Error(t, a.Refresh(ctx))
	require.False(t, a.Snapshot().TraceEnabled())
	repo.readErr = nil
	repo.writeErr = errors.New("offline")
	_, _, err = a.Update(ctx, "observability", policyWrite(t, `{"expected_rev":2,"spec":{"enabled":true}}`), nil, CachePolicyActor{})
	require.Error(t, err)
	require.False(t, a.Snapshot().TraceEnabled())
	repo.writeErr = nil
	repo.rows[cachePolicySettings[1]] = `{"rev":8,"spec":{"enabled":true,"future":1}}`
	require.NoError(t, a.Refresh(ctx))
	require.False(t, a.Snapshot().TraceEnabled())
	bad, err := a.Get(ctx, "observability")
	require.NoError(t, err)
	require.Equal(t, "parse_error", bad.Effective.State)
	require.EqualValues(t, 8, bad.Stored.Rev)
	_, _, err = a.Update(ctx, "observability", policyWrite(t, `{"expected_rev":8,"spec":{"enabled":true}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	require.True(t, a.Snapshot().TraceEnabled())
}
func TestCachePolicyConfirmConflictAndValidation(t *testing.T) {
	ctx := context.Background()
	s := NewCachePolicyService(newPolicyTestStore(), policyTestConfig())
	_, _, err := s.Update(ctx, "master", policyWrite(t, `{"expected_rev":0,"spec":{"enabled":true}}`), nil, CachePolicyActor{})
	var failure *CachePolicyError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, 422, failure.Status)
	require.Equal(t, "reason,checkbox", failure.Metadata["missing"])
	w := policyWrite(t, `{"expected_rev":0,"spec":{"enabled":true},"reason":" 开启 ","confirm":{"checkbox":true}}`)
	_, _, err = s.Update(ctx, "master", w, nil, CachePolicyActor{})
	require.NoError(t, err)
	_, _, err = s.Update(ctx, "master", w, nil, CachePolicyActor{})
	require.ErrorAs(t, err, &failure)
	require.Equal(t, 409, failure.Status)
	require.Equal(t, "1", failure.Metadata["current_rev"])
	preview, err := s.Validate(ctx, "master", w, nil)
	require.NoError(t, err)
	require.True(t, preview.RevStale)
	_, _, err = s.Update(ctx, "master", policyWrite(t, `{"expected_rev":1,"spec":{"enabled":false}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	for _, tc := range []struct{ spec, code string }{
		{`{"sample_percent":101}`, "out_of_range"},
		{`{"sample_percent":1.5}`, "malformed_body"},
		{`{"enabled":null}`, "malformed_body"},
		{`{"enabled":"true"}`, "malformed_body"},
		{`{"normalize_client_pck":true}`, "forbidden_field"},
		{`{"byte_budget_mib":65}`, "exceeds_static_cap"},
		{`{"unknown":1}`, "unknown_field"},
		{`{"platforms":["anthropic"]}`, "unsupported_in_build"},
		{`{"persist_trace_hourly":false}`, "unsupported_in_build"},
	} {
		t.Run(tc.spec, func(t *testing.T) {
			w := policyWrite(t, `{"expected_rev":0,"spec":`+tc.spec+`}`)
			v, err := s.Validate(ctx, "observability", w, nil)
			require.NoError(t, err)
			require.False(t, v.Valid)
			require.Equal(t, tc.code, v.Errors[0].Code)
		})
	}
	w = policyWrite(t, `{"expected_rev":0,"spec":{},"reason":"`+strings.Repeat("中", 200)+`"}`)
	v, err := s.Validate(ctx, "observability", w, nil)
	require.NoError(t, err)
	require.True(t, v.Valid)
	w.Reason += "中"
	v, err = s.Validate(ctx, "observability", w, nil)
	require.NoError(t, err)
	require.False(t, v.Valid)
	require.Equal(t, "too_long", v.Errors[0].Code)
}
func TestCachePolicyLayersCapsAndFutureKeys(t *testing.T) {
	ctx := context.Background()
	repo := newPolicyTestStore()
	cfg := policyTestConfig()
	cfg.Gateway.CacheFeatures.KillSwitch = true
	s := NewCachePolicyService(repo, cfg)
	v, _, err := s.Update(ctx, "observability", policyWrite(t, `{"expected_rev":0,"spec":{"enabled":true,"rate_mib_per_s":30}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	require.Equal(t, "killed", v.Effective.State)
	require.False(t, s.Snapshot().TraceEnabled())
	cfg.Gateway.CacheFeatures.KillSwitch = false
	cfg.Gateway.CacheTrace.MaxRateMiBPerS = 16
	s = NewCachePolicyService(repo, cfg)
	require.NoError(t, s.Refresh(ctx))
	require.True(t, s.Snapshot().TraceEnabled())
	require.EqualValues(t, 16<<20, s.Snapshot().RateBytesPerSecond())
	v, err = s.Get(ctx, "observability")
	require.NoError(t, err)
	require.Contains(t, v.Effective.ClippedFields, "rate_mib_per_s")
	require.Equal(t, 30, v.Stored.Spec["rate_mib_per_s"])
	require.False(t, s.Snapshot().MasterEnabled()) // Master never masks observability.
	cfg.Gateway.OpenAICache.DiagnosticsEnabled = false
	s = NewCachePolicyService(repo, cfg)
	require.NoError(t, s.Refresh(ctx))
	require.False(t, s.Snapshot().TraceEnabled())
	list, err := s.List(ctx, "test")
	require.NoError(t, err)
	require.Len(t, list.Policies, 6)
	for _, p := range list.Policies[2:] {
		require.False(t, p.Available)
		require.Nil(t, p.Stored)
		require.Nil(t, p.Loaded)
		require.Nil(t, p.Defaults)
		require.Empty(t, p.Schema)
		require.Equal(t, "not_in_build", p.Effective.State)
		_, err = s.Get(ctx, p.Key)
		require.Error(t, err)
		_, err = s.Validate(ctx, p.Key, CachePolicyWrite{}, nil)
		require.Error(t, err)
		_, _, err = s.Update(ctx, p.Key, CachePolicyWrite{}, nil, CachePolicyActor{})
		require.Error(t, err)
	}
	require.Equal(t, "/admin/settings?tab=gateway", list.Existing.Claude.SettingsLink)
}
func TestCachePolicyConcurrentWritersAndSnapshots(t *testing.T) {
	repo := newPolicyTestStore()
	a := NewCachePolicyService(repo, policyTestConfig())
	b := NewCachePolicyService(repo, policyTestConfig())
	w := policyWrite(t, `{"expected_rev":0,"spec":{"enabled":true}}`)
	var wins atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := a
			if i%2 == 1 {
				s = b
			}
			_, _, err := s.Update(context.Background(), "observability", w, nil, CachePolicyActor{})
			if err == nil {
				wins.Add(1)
			} else {
				var p *CachePolicyError
				if !errors.As(err, &p) || p.Status != 409 {
					t.Errorf("unexpected: %v", err)
				}
			}
			for j := 0; j < 100; j++ {
				p := s.Snapshot()
				_ = p.TraceEnabled()
				_ = p.Revision()
			}
		}(i)
	}
	wg.Wait()
	require.EqualValues(t, 1, wins.Load())
	require.NoError(t, a.Refresh(context.Background()))
	require.NoError(t, b.Refresh(context.Background()))
	require.Equal(t, a.Snapshot().Revision(), b.Snapshot().Revision())
}
func TestCachePolicySnapshotZeroAllocations(t *testing.T) {
	s := NewCachePolicyService(newPolicyTestStore(), policyTestConfig())
	require.Equal(t, 0.0, testing.AllocsPerRun(1000, func() {
		p := s.Snapshot()
		if p.TraceEnabled() {
			panic("default trace on")
		}
	}))
}
func TestCachePolicyINV18UserDTOs(t *testing.T) {
	for _, dto := range []any{PublicSettings{}, PublicSettingsInjectionPayload{}, SystemSettings{}} {
		b, err := json.Marshal(dto)
		require.NoError(t, err)
		for _, key := range cachePolicySettings {
			require.NotContains(t, string(b), key)
		}
	}
}

func TestCachePolicyRequestAndTurnSnapshot(t *testing.T) {
	s := NewCachePolicyService(newPolicyTestStore(), policyTestConfig())
	c := &gin.Context{Keys: map[string]any{"api_key": int64(1)}}
	old := CaptureCachePolicy(c, s)
	_, _, err := s.Update(context.Background(), "observability", policyWrite(t, `{"expected_rev":0,"spec":{"enabled":true}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	require.Same(t, old, CaptureCachePolicy(c, s))
	require.False(t, CachePolicyFromGin(c).TraceEnabled())
	require.True(t, CaptureCachePolicyTurn(c, s).TraceEnabled())
	require.False(t, old.TraceEnabled())
	require.Equal(t, 0.0, testing.AllocsPerRun(1000, func() { delete(c.Keys, cachePolicySnapshotGinKey); CaptureCachePolicy(c, s) }))
}

func TestCachePolicySimultaneousBlockers(t *testing.T) {
	repo := newPolicyTestStore()
	repo.rows[cachePolicySettings[1]] = `{"rev":9,"spec":{"enabled":true,"future":true}}`
	cfg := policyTestConfig()
	cfg.Gateway.CacheFeatures.KillSwitch = true
	cfg.Gateway.OpenAICache.DiagnosticsEnabled = false
	s := NewCachePolicyService(repo, cfg)
	require.NoError(t, s.Refresh(context.Background()))
	view, err := s.Get(context.Background(), "observability")
	require.NoError(t, err)
	require.Equal(t, "killed", view.Effective.State)
	require.False(t, *view.Effective.Active)
	require.ElementsMatch(t, []string{"kill_switch", "parse_error", "diagnostics_disabled"}, view.Effective.Blockers)
}

func TestCachePolicyAdminViewCannotMutateSnapshot(t *testing.T) {
	s := NewCachePolicyService(newPolicyTestStore(), policyTestConfig())
	view, _, err := s.Update(context.Background(), "observability",
		policyWrite(t, `{"expected_rev":0,"spec":{"enabled":true},"reason":"initial"}`), nil,
		CachePolicyActor{UserID: 1, Email: "admin@example.com"})
	require.NoError(t, err)
	view.Stored.Spec["enabled"] = false
	view.Stored.UpdatedBy.Email = "changed@example.com"
	*view.Stored.Reason = "changed"
	snapshot := s.Snapshot()
	require.True(t, snapshot.TraceEnabled())
	require.Equal(t, "admin@example.com", snapshot.entries[1].envelope.UpdatedBy.Email)
	require.Equal(t, "initial", *snapshot.entries[1].envelope.Reason)
}

func TestCachePolicyMalformedStoredEnvelopeFailsClosed(t *testing.T) {
	for _, raw := range []string{
		`{"spec":{"enabled":true}}`,
		`{"rev":null,"spec":{"enabled":true}}`,
		`{"rev":-1,"spec":{"enabled":true}}`,
		`{"rev":1,"spec":null}`,
	} {
		t.Run(raw, func(t *testing.T) {
			repo := newPolicyTestStore()
			repo.rows[cachePolicySettings[1]] = raw
			s := NewCachePolicyService(repo, policyTestConfig())
			require.NoError(t, s.Refresh(context.Background()))
			require.False(t, s.Snapshot().TraceEnabled())
			v, err := s.Get(context.Background(), "observability")
			require.NoError(t, err)
			require.Equal(t, "parse_error", v.Effective.State)
		})
	}
}

func TestCachePolicyHistoryCompletenessRules(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	envelope := func(rev int64, at *time.Time) string {
		b, err := json.Marshal(map[string]any{"rev": rev, "updated_at": at})
		require.NoError(t, err)
		return string(b)
	}
	recent, edge, old := now.Add(-time.Second), now.Add(-5*time.Second), now.Add(-6*time.Second)
	for _, tc := range []struct {
		name         string
		coverage     CachePolicyRevisionCoverage
		stored, want string
	}{
		{"never_written", CachePolicyRevisionCoverage{}, "", ""},
		{"zero_rev", CachePolicyRevisionCoverage{}, envelope(0, &old), ""},
		{"first_put_pending", CachePolicyRevisionCoverage{}, envelope(1, &recent), ""},
		{"later_put_pending", CachePolicyRevisionCoverage{}, envelope(9, &recent), ""},
		{"grace_boundary", CachePolicyRevisionCoverage{}, envelope(9, &edge), ""},
		{"cleared_after_grace", CachePolicyRevisionCoverage{}, envelope(1, &old), "earlier_missing"},
		{"no_timestamp", CachePolicyRevisionCoverage{}, envelope(1, nil), "earlier_missing"},
		{"retention", CachePolicyRevisionCoverage{Count: 2, MinRev: 5, MaxRev: 6}, envelope(6, &old), "earlier_missing"},
		{"middle_gap", CachePolicyRevisionCoverage{Count: 2, MinRev: 1, MaxRev: 3}, envelope(3, &old), "gap"},
		{"latest_pending", CachePolicyRevisionCoverage{Count: 2, MinRev: 1, MaxRev: 2}, envelope(3, &recent), ""},
		{"stored_rev_not_coverage", CachePolicyRevisionCoverage{Count: 2, MinRev: 1, MaxRev: 2}, envelope(100, &old), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, cachePolicyHistoryIncompleteReason(tc.coverage, tc.stored, now))
		})
	}
}
