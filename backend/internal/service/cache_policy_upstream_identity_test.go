//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type upstreamPolicyAccounts struct {
	mu      sync.Mutex
	rows    map[int64]*Account
	err     error
	lookups int
}

func (r *upstreamPolicyAccounts) GetByIDs(_ context.Context, ids []int64) ([]*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lookups++
	if r.err != nil {
		return nil, r.err
	}
	out := []*Account{}
	for _, id := range ids {
		if a := r.rows[id]; a != nil {
			copy := *a
			out = append(out, &copy)
		}
	}
	return out, nil
}
func upstreamPolicyAccountsFixture() *upstreamPolicyAccounts {
	return &upstreamPolicyAccounts{rows: map[int64]*Account{
		101: {ID: 101, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		102: {ID: 102, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		103: {ID: 103, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		104: {ID: 104, Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
	}}
}
func upstreamPolicyWrite(t *testing.T, revision int, spec string) CachePolicyWrite {
	t.Helper()
	return policyWrite(t, fmt.Sprintf(`{"expected_rev":%d,"spec":%s,"reason":"synthetic policy validation","confirm":{"checkbox":true}}`, revision, spec))
}

func TestCachePolicyUpstreamDefaultsAndExplicitSupport(t *testing.T) {
	s := NewCachePolicyService(newPolicyTestStore(), policyTestConfig())
	var nilPolicy *CachePolicySnapshot
	for _, p := range []CacheUpstreamIdentityPolicy{nilPolicy.UpstreamIdentity(), s.Snapshot().UpstreamIdentity()} {
		require.False(t, p.Enabled())
		require.Equal(t, "off", p.Mode)
		require.Equal(t, "whitelist", p.ForwardSessionHeaders)
		require.Zero(t, p.RolloutPercent)
		require.False(t, p.ChatCompatUUID)
		require.False(t, p.HeaderOnlyUUID)
		require.True(t, p.AllowsAccount(101))
	}
	list, err := s.List(context.Background(), "policy-preparation")
	require.NoError(t, err)
	for _, p := range list.Policies {
		want := p.Key == "master" || p.Key == "observability" || p.Key == "upstream_identity"
		require.Equal(t, want, p.Available, p.Key)
		if !want {
			require.Equal(t, "not_in_build", p.Effective.State)
			require.Nil(t, p.Stored)
			require.Nil(t, p.Loaded)
			require.Nil(t, p.Defaults)
			require.Empty(t, p.Schema)
			_, err := s.Get(context.Background(), p.Key)
			require.Error(t, err)
			_, err = s.Validate(context.Background(), p.Key, upstreamPolicyWrite(t, 0, `{}`), nil)
			require.Error(t, err)
			_, _, err = s.Update(context.Background(), p.Key, upstreamPolicyWrite(t, 0, `{}`), nil, CachePolicyActor{})
			require.Error(t, err)
		}
	}
	upstream, err := s.Get(context.Background(), "upstream_identity")
	require.NoError(t, err)
	require.Equal(t, policyDefaults(3), upstream.Stored.Spec)
	require.Len(t, upstream.Schema, 6)
	require.True(t, upstream.GatedByMaster)
	require.Equal(t, "high", upstream.Risk)
	require.Equal(t, map[string]any{"field": "account_ids", "type": "id_list", "ref": "account", "options": []cachePolicyAccountOption{}}, upstream.Schema[1])
	require.Equal(t, 0.0, testing.AllocsPerRun(1000, func() {
		p := s.Snapshot().UpstreamIdentity()
		if p.Enabled() || !p.AllowsAccount(101) {
			panic("default upstream policy changed")
		}
	}))
}

func TestCachePolicyUpstreamStrictValidation(t *testing.T) {
	accounts := upstreamPolicyAccountsFixture()
	s := NewCachePolicyService(newPolicyTestStore(), policyTestConfig(), WithCachePolicyAccounts(accounts))
	for _, tc := range []struct{ name, spec, field, code string }{
		{"mode_unknown", `{"mode":"replace"}`, "mode", "malformed_body"},
		{"mode_type", `{"mode":1}`, "mode", "malformed_body"},
		{"mode_null", `{"mode":null}`, "mode", "malformed_body"},
		{"percentage_low", `{"rollout_percent":-1}`, "rollout_percent", "out_of_range"},
		{"percentage_high", `{"rollout_percent":101}`, "rollout_percent", "out_of_range"},
		{"percentage_fraction", `{"rollout_percent":1.5}`, "rollout_percent", "malformed_body"},
		{"percentage_null", `{"rollout_percent":null}`, "rollout_percent", "malformed_body"},
		{"chat_requires_mode", `{"chat_compat_uuid":true}`, "chat_compat_uuid", "requires_mode_uuid_fill"},
		{"chat_type", `{"chat_compat_uuid":1}`, "chat_compat_uuid", "malformed_body"},
		{"header_only_pending", `{"mode":"uuid_fill","header_only_uuid":true}`, "header_only_uuid", "unsupported_in_build"},
		{"normalization_pending", `{"forward_session_headers":"normalize"}`, "forward_session_headers", "unsupported_in_build"},
		{"header_unknown", `{"forward_session_headers":"all"}`, "forward_session_headers", "malformed_body"},
		{"duplicate_id", `{"account_ids":[101,101]}`, "account_ids", "duplicate"},
		{"invalid_id", `{"account_ids":[0]}`, "account_ids", "unknown_account"},
		{"negative_id", `{"account_ids":[-1]}`, "account_ids", "unknown_account"},
		{"id_fraction", `{"account_ids":[101.5]}`, "account_ids", "malformed_body"},
		{"id_string", `{"account_ids":["101"]}`, "account_ids", "malformed_body"},
		{"ids_null", `{"account_ids":null}`, "account_ids", "malformed_body"},
		{"ids_object", `{"account_ids":{}}`, "account_ids", "malformed_body"},
		{"unknown_account", `{"mode":"uuid_fill","account_ids":[999]}`, "account_ids", "unknown_account"},
		{"oauth_account", `{"mode":"uuid_fill","account_ids":[103]}`, "account_ids", "wrong_platform"},
		{"other_platform", `{"mode":"uuid_fill","account_ids":[104]}`, "account_ids", "wrong_platform"},
		{"normalize_forbidden", `{"normalize_client_pck":false}`, "normalize_client_pck", "forbidden_field"},
		{"unknown_field", `{"future":false}`, "future", "unknown_field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := s.Validate(context.Background(), "upstream_identity", upstreamPolicyWrite(t, 0, tc.spec), nil)
			require.NoError(t, err)
			require.False(t, v.Valid)
			found := false
			for _, failure := range v.Errors {
				if failure.Field == tc.field && failure.Code == tc.code {
					found = true
					if tc.code == "out_of_range" {
						require.Equal(t, map[string]int{"min": 0, "max": 100}, failure.Limit)
					}
				}
			}
			require.True(t, found, "%s: %v", tc.name, v.Errors)
		})
	}
}

func TestCachePolicyUpstreamValidDefaultsAndFutureValueErrors(t *testing.T) {
	s := NewCachePolicyService(newPolicyTestStore(), policyTestConfig())
	for _, spec := range []string{`{}`, `{"mode":"off","account_ids":[],"header_only_uuid":false,"forward_session_headers":"whitelist"}`, `{"mode":"uuid_fill","chat_compat_uuid":true,"rollout_percent":100}`} {
		v, err := s.Validate(context.Background(), "upstream_identity", upstreamPolicyWrite(t, 0, spec), nil)
		require.NoError(t, err)
		require.True(t, v.Valid, spec)
	}
	for _, spec := range []string{`{"header_only_uuid":true}`, `{"forward_session_headers":"normalize"}`} {
		_, _, err := s.Update(context.Background(), "upstream_identity", upstreamPolicyWrite(t, 0, spec), nil, CachePolicyActor{})
		var failure *CachePolicyError
		require.ErrorAs(t, err, &failure)
		require.Equal(t, 400, failure.Status)
		require.Equal(t, "CACHE_POLICY_UNSUPPORTED", failure.Reason)
		require.EqualValues(t, 0, s.Snapshot().UpstreamIdentity().Revision())
	}
}

func TestCachePolicyUpstreamMasksPreviewAndFrozenRequests(t *testing.T) {
	ctx := context.Background()
	s := NewCachePolicyService(newPolicyTestStore(), policyTestConfig())
	_, _, err := s.Update(ctx, "observability", upstreamPolicyWrite(t, 0, `{"enabled":true}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	v, _, err := s.Update(ctx, "upstream_identity", upstreamPolicyWrite(t, 0, `{"mode":"uuid_fill","rollout_percent":50,"chat_compat_uuid":true}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	require.Equal(t, "masked_by_master", v.Effective.State)
	require.False(t, *v.Effective.Active)
	require.True(t, s.Snapshot().TraceEnabled(), "observability is never masked by master")
	preview, err := s.Validate(ctx, "master", upstreamPolicyWrite(t, 0, `{"enabled":true}`), nil)
	require.NoError(t, err)
	require.Equal(t, CachePolicyConfirm{ReasonRequired: true, CheckboxRequired: true}, preview.Confirm)
	require.Len(t, preview.Affected, 3)
	require.Equal(t, "masked_by_master", preview.Affected[0].StateBefore)
	require.Equal(t, "on", preview.Affected[0].StateAfter)
	require.True(t, *preview.Affected[0].ActiveAfter)
	for _, child := range preview.Affected[1:] {
		require.Equal(t, "not_in_build", child.StateAfter)
		require.Nil(t, child.ActiveAfter)
	}
	_, _, err = s.Update(ctx, "master", upstreamPolicyWrite(t, 0, `{"enabled":true}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	c := &gin.Context{Keys: map[string]any{"api_key": int64(1)}}
	frozen := CaptureCachePolicy(c, s)
	require.True(t, frozen.UpstreamIdentity().Enabled())
	require.Equal(t, "m1.o1.u1.a0.r0", frozen.Revision())
	_, _, err = s.Update(ctx, "master", policyWrite(t, `{"expected_rev":1,"spec":{"enabled":false}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	require.Same(t, frozen, CaptureCachePolicy(c, s))
	require.True(t, CachePolicyFromGin(c).UpstreamIdentity().Enabled())
	require.False(t, s.Snapshot().UpstreamIdentity().Enabled())
	require.True(t, s.Snapshot().TraceEnabled())
	require.False(t, CaptureCachePolicyTurn(c, s).UpstreamIdentity().Enabled())
}

func TestCachePolicyUpstreamConfirmationScopeAndRollback(t *testing.T) {
	for _, tc := range []struct {
		before, after []int64
		direction     string
	}{
		{[]int64{}, []int64{101}, "disable"},
		{[]int64{101}, []int64{}, "enable"},
		{[]int64{101}, []int64{101, 102}, "enable"},
		{[]int64{101, 102}, []int64{101}, "disable"},
		{[]int64{101}, []int64{102}, "mixed"},
		{[]int64{101, 102}, []int64{102, 101}, "disable"},
	} {
		t.Run(fmt.Sprint(tc.before, "-", tc.after), func(t *testing.T) {
			require.Equal(t, tc.direction, policyDirection([]CachePolicyDiff{{Field: "account_ids", Before: tc.before, After: tc.after}}))
		})
	}
	s := NewCachePolicyService(newPolicyTestStore(), policyTestConfig())
	_, _, err := s.Update(context.Background(), "upstream_identity", policyWrite(t, `{"expected_rev":0,"spec":{"mode":"uuid_fill","rollout_percent":50}}`), nil, CachePolicyActor{})
	var failure *CachePolicyError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, 422, failure.Status)
	require.Equal(t, "reason,checkbox", failure.Metadata["missing"])
	_, _, err = s.Update(context.Background(), "upstream_identity", upstreamPolicyWrite(t, 0, `{"mode":"uuid_fill","rollout_percent":50}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	rollback := policyWrite(t, `{"expected_rev":1,"spec":{"mode":"off","rollout_percent":0}}`)
	v, err := s.Validate(context.Background(), "upstream_identity", rollback, nil)
	require.NoError(t, err)
	require.Equal(t, "disable", v.Direction)
	require.Equal(t, CachePolicyConfirm{}, v.Confirm)
	_, _, err = s.Update(context.Background(), "upstream_identity", rollback, nil, CachePolicyActor{})
	require.NoError(t, err)
}

func TestCachePolicyUpstreamAccountRecheckAndFailures(t *testing.T) {
	ctx := context.Background()
	accounts := upstreamPolicyAccountsFixture()
	store := newPolicyTestStore()
	s := NewCachePolicyService(store, policyTestConfig(), WithCachePolicyAccounts(accounts))
	w := upstreamPolicyWrite(t, 0, `{"mode":"uuid_fill","account_ids":[101],"rollout_percent":50}`)
	v, err := s.Validate(ctx, "upstream_identity", w, nil)
	require.NoError(t, err)
	require.True(t, v.Valid)
	accounts.rows[101].Type = AccountTypeOAuth
	_, _, err = s.Update(ctx, "upstream_identity", w, nil, CachePolicyActor{})
	var failure *CachePolicyError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, "CACHE_POLICY_INVALID", failure.Reason)
	require.Empty(t, store.rows[cachePolicySettings[3]])
	accounts.rows[101].Type = AccountTypeAPIKey
	accounts.err = errors.New("synthetic database unavailable")
	previous := s.Snapshot()
	_, _, err = s.Update(ctx, "upstream_identity", w, nil, CachePolicyActor{})
	require.ErrorIs(t, err, accounts.err)
	require.Same(t, previous, s.Snapshot())
	accounts.err = nil
	_, _, err = s.Update(ctx, "upstream_identity", w, nil, CachePolicyActor{})
	require.NoError(t, err)
	_, _, err = s.Update(ctx, "upstream_identity", w, nil, CachePolicyActor{})
	require.ErrorAs(t, err, &failure)
	require.Equal(t, 409, failure.Status)
	missing := NewCachePolicyService(newPolicyTestStore(), policyTestConfig())
	_, err = missing.Validate(ctx, "upstream_identity", w, nil)
	require.Error(t, err, "missing repository must fail closed for a nonempty scope")
}

func TestCachePolicyUpstreamImmutabilityAndZeroAllocations(t *testing.T) {
	s := NewCachePolicyService(newPolicyTestStore(), policyTestConfig(), WithCachePolicyAccounts(upstreamPolicyAccountsFixture()))
	ctx := context.Background()
	_, _, err := s.Update(ctx, "master", upstreamPolicyWrite(t, 0, `{"enabled":true}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	v, _, err := s.Update(ctx, "upstream_identity", upstreamPolicyWrite(t, 0, `{"mode":"uuid_fill","account_ids":[101],"rollout_percent":50}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	frozen := s.Snapshot()
	v.Stored.Spec["account_ids"].([]int64)[0] = 102
	require.True(t, frozen.UpstreamIdentity().AllowsAccount(101))
	require.False(t, frozen.UpstreamIdentity().AllowsAccount(102))
	v, err = s.Get(ctx, "upstream_identity")
	require.NoError(t, err)
	require.Equal(t, []int64{101}, v.Stored.Spec["account_ids"])
	v.Stored.Spec["account_ids"].([]int64)[0] = 102
	copy := frozen.UpstreamIdentity()
	copy.Mode = "off"
	require.Equal(t, "uuid_fill", frozen.UpstreamIdentity().Mode)
	require.Equal(t, 0.0, testing.AllocsPerRun(1000, func() {
		p := s.Snapshot().UpstreamIdentity()
		if !p.Enabled() || !p.AllowsAccount(101) || p.AllowsAccount(102) {
			panic("snapshot mutated")
		}
	}))
	_, _, err = s.Update(ctx, "upstream_identity", upstreamPolicyWrite(t, 1, `{"account_ids":[102]}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	require.True(t, frozen.UpstreamIdentity().AllowsAccount(101))
	require.False(t, frozen.UpstreamIdentity().AllowsAccount(102))
	require.True(t, s.Snapshot().UpstreamIdentity().AllowsAccount(102))
}

func TestCachePolicyUpstreamKillAndParseFailClosed(t *testing.T) {
	ctx := context.Background()
	for _, bad := range []string{`{"rev":4,"spec":{"mode":"uuid_fill","rollout_percent":50,"future":true}}`, `{"rev":4,"spec":{"mode":"uuid_fill","header_only_uuid":true}}`, `{"rev":4,"spec":{"forward_session_headers":"normalize"}}`, `{"rev":4,"spec":{"mode":"uuid_fill","chat_compat_uuid":true,"account_ids":[101,101]}}`} {
		repo := newPolicyTestStore()
		repo.rows[cachePolicySettings[0]] = `{"rev":1,"spec":{"enabled":true}}`
		repo.rows[cachePolicySettings[3]] = bad
		s := NewCachePolicyService(repo, policyTestConfig())
		require.NoError(t, s.Refresh(ctx))
		require.False(t, s.Snapshot().UpstreamIdentity().Enabled())
		v, err := s.Get(ctx, "upstream_identity")
		require.NoError(t, err)
		require.Equal(t, "parse_error", v.Effective.State)
		require.Contains(t, v.Effective.Blockers, "parse_error")
		require.EqualValues(t, 4, v.Stored.Rev)
	}
	cfg := policyTestConfig()
	cfg.Gateway.CacheFeatures.KillSwitch = true
	s := NewCachePolicyService(newPolicyTestStore(), cfg)
	_, _, err := s.Update(ctx, "master", upstreamPolicyWrite(t, 0, `{"enabled":true}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	v, _, err := s.Update(ctx, "upstream_identity", upstreamPolicyWrite(t, 0, `{"mode":"uuid_fill","rollout_percent":50}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	require.Equal(t, "killed", v.Effective.State)
	require.Equal(t, "uuid_fill", v.Stored.Spec["mode"])
	require.False(t, s.Snapshot().UpstreamIdentity().Enabled())
}

func TestCachePolicyUpstreamPrivacyAndNoSchedulingState(t *testing.T) {
	s := NewCachePolicyService(newPolicyTestStore(), policyTestConfig(), WithCachePolicyAccounts(upstreamPolicyAccountsFixture()))
	v, _, err := s.Update(context.Background(), "upstream_identity", upstreamPolicyWrite(t, 0, `{"mode":"uuid_fill","account_ids":[101],"rollout_percent":50}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	b, err := json.Marshal(v)
	require.NoError(t, err)
	for _, secretField := range []string{"credentials", "api_key", "proxy", "session_digest", "origin_group_id", "account_stats"} {
		require.NotContains(t, string(b), secretField)
	}
}

func TestCachePolicyUpstreamParseLogContainsNoPlaintext(t *testing.T) {
	var log bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&log, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	store := newPolicyTestStore()
	store.rows[cachePolicySettings[3]] = `{"rev":7,"reason":"POLICY_REASON_MARKER","spec":{"mode":"uuid_fill","unknown_body_marker":"POLICY_BODY_MARKER"}}`
	s := NewCachePolicyService(store, policyTestConfig())
	require.NoError(t, s.Refresh(context.Background()))
	require.Contains(t, log.String(), "upstream_identity")
	require.NotContains(t, log.String(), "POLICY_REASON_MARKER")
	require.NotContains(t, log.String(), "POLICY_BODY_MARKER")
	require.NotContains(t, log.String(), "unknown_body_marker")
}

func TestCachePolicyUpstreamInvalidAccountsClearEffectiveWarnings(t *testing.T) {
	store := newPolicyTestStore()
	store.rows["cache_feature_master"] = `{"rev":1,"spec":{"enabled":true}}`
	store.rows["cache_observability_policy"] = `{"rev":1,"spec":{"enabled":true}}`
	s := NewCachePolicyService(store, policyTestConfig(), WithCachePolicyAccounts(upstreamPolicyAccountsFixture()))
	require.NoError(t, s.Refresh(context.Background()))
	for _, tc := range []struct {
		name  string
		ids   string
		codes []string
	}{
		{"missing", "[101,999]", []string{"unknown_account"}},
		{"wrong_type", "[101,103]", []string{"wrong_platform"}},
		{"both", "[101,999,103]", []string{"unknown_account", "wrong_platform"}},
		{"valid", "[101,102]", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := s.Validate(context.Background(), "upstream_identity", upstreamPolicyWrite(t, 0,
				fmt.Sprintf(`{"mode":"uuid_fill","rollout_percent":0,"account_ids":%s}`, tc.ids)), nil)
			require.NoError(t, err)
			if len(tc.codes) == 0 {
				require.True(t, v.Valid)
				require.Empty(t, v.Errors)
				require.Equal(t, []string{"no_effect_rollout_zero"}, v.Warnings)
				require.NotNil(t, v.EffectivePreview)
				return
			}
			require.False(t, v.Valid)
			want := make([]CachePolicyFieldError, len(tc.codes))
			for i, code := range tc.codes {
				want[i] = CachePolicyFieldError{Field: "account_ids", Code: code}
			}
			require.Equal(t, want, v.Errors)
			require.Equal(t, []string{}, v.Warnings)
			require.Nil(t, v.EffectivePreview)
			require.Equal(t, []CachePolicyAffected{}, v.Affected)
		})
	}
}

func TestCachePolicyM3AndNorthstarPublishedTogether(t *testing.T) {
	store := newPolicyTestStore()
	store.rows["cache_feature_master"] = `{"rev":1,"spec":{"enabled":true}}`
	store.rows["cache_northstar_policy"] = `{"rev":2,"spec":{"rollup_enabled":true}}`
	store.rows["cache_upstream_identity_policy"] = `{"rev":3,"spec":{"mode":"uuid_fill","rollout_percent":100}}`
	policies := NewCachePolicyService(store, policyTestConfig(), WithCacheNorthstar())
	require.NoError(t, policies.Refresh(context.Background()))
	before := policies.Snapshot()
	require.True(t, before.NorthstarEnabled())
	require.EqualValues(t, 2, before.NorthstarRevision())
	require.True(t, before.UpstreamIdentity().Enabled())
	require.EqualValues(t, 3, before.UpstreamIdentity().Revision())
	_, _, err := policies.Update(context.Background(), "northstar", policyWrite(t, `{"expected_rev":2,"spec":{"rollup_enabled":false}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	after := policies.Snapshot()
	require.False(t, after.NorthstarEnabled())
	require.True(t, after.UpstreamIdentity().Enabled())
	require.EqualValues(t, 3, after.UpstreamIdentity().Revision())
	require.True(t, before.NorthstarEnabled())
}
